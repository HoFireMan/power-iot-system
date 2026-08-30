import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../domain/models/admin_overview.dart';
import '../../domain/models/device_assignment.dart';
import '../../domain/models/device_inventory.dart';
import '../../domain/models/device_ref.dart';
import '../../domain/models/measurement_point.dart';
import '../../domain/repositories/admin_overview_repository.dart';
import '../../data/repositories/admin_overview_repository_impl.dart';
import '../providers/admin_overview_provider.dart';

class ReplaceDeviceScreen extends ConsumerStatefulWidget {
  const ReplaceDeviceScreen({required this.assignmentId, super.key});

  final String assignmentId;

  @override
  ConsumerState<ReplaceDeviceScreen> createState() =>
      _ReplaceDeviceScreenState();
}

class _ReplaceDeviceScreenState extends ConsumerState<ReplaceDeviceScreen> {
  static const _failureMessage =
      'Unable to replace Device. Please check the replacement selection and try again.';

  String? _selectedSerialNumber;
  String? _submissionError;
  bool _isSubmitting = false;
  bool _isCommitted = false;
  final _formKey = GlobalKey<FormState>();

  @override
  void initState() {
    super.initState();
    final pending =
        ref.read(replaceDeviceRequestIdentitySourceProvider).pending;
    if (pending?.currentAssignmentId == widget.assignmentId) {
      _selectedSerialNumber = pending?.serialNumber;
    }
  }

  Future<void> _submit(DeviceAssignment current) async {
    if (_isSubmitting || _isCommitted || !_formKey.currentState!.validate()) {
      return;
    }

    setState(() {
      _isSubmitting = true;
      _submissionError = null;
    });
    final requestIdentity =
        ref.read(replaceDeviceRequestIdentitySourceProvider).identityFor(
              currentAssignmentId: current.id,
              serialNumber: _selectedSerialNumber!,
            );

    try {
      await ref.read(adminOverviewRepositoryProvider).replaceDevice(
            ReplaceDeviceInput(
              requestIdentity: requestIdentity,
              currentAssignmentId: current.id,
              replacementDeviceRef: DeviceRef(
                serialNumber: _selectedSerialNumber,
              ),
            ),
          );
    } catch (error) {
      if (!mounted) {
        return;
      }
      setState(() {
        _isSubmitting = false;
        _submissionError = adminErrorMessage(error, _failureMessage);
      });
      return;
    }

    ref
        .read(replaceDeviceRequestIdentitySourceProvider)
        .complete(requestIdentity);
    _isCommitted = true;
    if (!mounted) return;
    await _reconcileCommitted();
  }

  Future<void> _reconcileCommitted() async {
    try {
      await retryAdminOverview(ref);
    } catch (_) {
      if (mounted) {
        setState(() {
          _isSubmitting = false;
          _submissionError =
              'Device replaced, but the latest view could not be loaded.';
        });
      }
      return;
    }
    if (mounted) context.pop(true);
  }

  Future<void> _retryReconciliation() async {
    if (!_isCommitted || _isSubmitting) return;
    setState(() {
      _isSubmitting = true;
      _submissionError = null;
    });
    await _reconcileCommitted();
  }

  @override
  Widget build(BuildContext context) {
    final overview = ref.watch(adminOverviewProvider);

    return PopScope(
      canPop: !_isSubmitting && !_isCommitted,
      child: Scaffold(
        appBar: AppBar(title: const Text('Replace Device')),
        body: SafeArea(
          child: overview.when(
            loading: () => const Center(child: Text('Loading admin overview…')),
            error: (error, stackTrace) => _isCommitted
                ? _reconciliationError(context)
                : Center(
                    child: Column(
                      mainAxisSize: MainAxisSize.min,
                      children: [
                        Text(adminErrorMessage(
                          error,
                          'Unable to load admin overview. Please retry.',
                        )),
                        OutlinedButton(
                          onPressed: () => retryAdminOverview(ref),
                          child: const Text('Retry'),
                        ),
                      ],
                    ),
                  ),
            data: (data) => _buildData(context, data),
          ),
        ),
      ),
    );
  }

  Widget _buildData(BuildContext context, AdminOverview overview) {
    DeviceAssignment? current;
    for (final assignment in overview.activeAssignments) {
      if (assignment.id == widget.assignmentId) {
        current = assignment;
        break;
      }
    }
    if (current == null) {
      return const Center(
        child: Text('Unable to load the current Device relationship.'),
      );
    }

    final device = _findDevice(overview, current.deviceId);
    final point = _findMeasurementPoint(overview, current.measurementPointId);
    if (device == null || point == null) {
      return const Center(
        child: Text('Unable to load the current Device relationship.'),
      );
    }

    final assignedDeviceIds = overview.activeAssignments
        .map((assignment) => assignment.deviceId)
        .toSet();
    final replacementDevices = overview.devices
        .where(
          (candidate) =>
              candidate.id != current!.deviceId &&
              candidate.id != null &&
              candidate.serialNumber.trim().isNotEmpty &&
              !assignedDeviceIds.contains(candidate.id),
        )
        .toList();

    return _ReplaceForm(
      current: current,
      currentDevice: device,
      currentPointName: point.name,
      replacementDevices: replacementDevices,
      formKey: _formKey,
      selectedSerialNumber: _selectedSerialNumber,
      submissionError: _submissionError,
      isSubmitting: _isSubmitting,
      onDeviceChanged: (value) => setState(() => _selectedSerialNumber = value),
      onSubmit: () => _submit(current!),
      isCommitted: _isCommitted,
      onRetryReconciliation: _retryReconciliation,
    );
  }

  Widget _reconciliationError(BuildContext context) => Center(
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            Text(
              _submissionError ??
                  'Device replaced, but the latest view could not be loaded.',
              textAlign: TextAlign.center,
              style: TextStyle(color: Theme.of(context).colorScheme.error),
            ),
            OutlinedButton(
              key: const Key('replace-refresh-retry-button'),
              onPressed: _isSubmitting ? null : _retryReconciliation,
              child: const Text('Retry refresh'),
            ),
          ],
        ),
      );

  DeviceInventory? _findDevice(AdminOverview overview, String deviceId) {
    for (final device in overview.devices) {
      if (device.id == deviceId) {
        return device;
      }
    }
    return null;
  }

  MeasurementPoint? _findMeasurementPoint(
    AdminOverview overview,
    String measurementPointId,
  ) {
    for (final point in overview.measurementPoints) {
      if (point.id == measurementPointId) {
        return point;
      }
    }
    return null;
  }
}

class _ReplaceForm extends StatelessWidget {
  const _ReplaceForm({
    required this.current,
    required this.currentDevice,
    required this.currentPointName,
    required this.replacementDevices,
    required this.formKey,
    required this.selectedSerialNumber,
    required this.submissionError,
    required this.isSubmitting,
    required this.onDeviceChanged,
    required this.onSubmit,
    required this.isCommitted,
    required this.onRetryReconciliation,
  });

  final DeviceAssignment current;
  final DeviceInventory currentDevice;
  final String currentPointName;
  final List<DeviceInventory> replacementDevices;
  final GlobalKey<FormState> formKey;
  final String? selectedSerialNumber;
  final String? submissionError;
  final bool isSubmitting;
  final ValueChanged<String?> onDeviceChanged;
  final VoidCallback onSubmit;
  final bool isCommitted;
  final VoidCallback onRetryReconciliation;

  @override
  Widget build(BuildContext context) {
    return Form(
      key: formKey,
      child: ListView(
        padding: const EdgeInsets.all(20),
        children: [
          Text(
            'Current Device: ${currentDevice.serialNumber} (${currentDevice.id})',
          ),
          const SizedBox(height: 8),
          Text(
            'Measurement Point: $currentPointName (${current.measurementPointId})',
          ),
          const SizedBox(height: 8),
          const Text('Effective time is controlled by the mock server.'),
          const SizedBox(height: 16),
          _ReplacementSelectionField(
            key: const Key('replace-device-field'),
            value: selectedSerialNumber,
            options: replacementDevices,
            enabled: !isSubmitting && !isCommitted,
            onChanged: onDeviceChanged,
          ),
          const SizedBox(height: 16),
          if (submissionError case final error?) ...[
            Text(
              error,
              style: TextStyle(color: Theme.of(context).colorScheme.error),
            ),
            const SizedBox(height: 16),
          ],
          if (isCommitted && submissionError != null)
            OutlinedButton(
              key: const Key('replace-refresh-retry-button'),
              onPressed: isSubmitting ? null : onRetryReconciliation,
              child: const Text('Retry refresh'),
            ),
          FilledButton(
            key: const Key('replace-submit-button'),
            onPressed: isSubmitting || isCommitted ? null : onSubmit,
            child: isSubmitting
                ? const SizedBox.square(
                    dimension: 20,
                    child: CircularProgressIndicator(strokeWidth: 2),
                  )
                : const Text('Replace Device'),
          ),
        ],
      ),
    );
  }
}

class _ReplacementSelectionField extends FormField<String> {
  _ReplacementSelectionField({
    required Key super.key,
    required String? value,
    required List<DeviceInventory> options,
    required bool enabled,
    required ValueChanged<String?> onChanged,
  }) : super(
          initialValue: value,
          validator: (candidate) =>
              candidate == null ? 'Replacement Device is required.' : null,
          builder: (state) => InputDecorator(
            decoration: InputDecoration(
              labelText: 'Replacement Device serial number',
              border: const OutlineInputBorder(),
              errorText: state.errorText,
            ),
            child: Column(
              children: options
                  .map(
                    (device) => ListTile(
                      key: Key(
                        'replace-device-option-${device.serialNumber}',
                      ),
                      selected: state.value == device.serialNumber,
                      leading: Icon(
                        state.value == device.serialNumber
                            ? Icons.radio_button_checked
                            : Icons.radio_button_unchecked,
                      ),
                      title: Text(device.serialNumber),
                      subtitle: Text(device.id!),
                      contentPadding: EdgeInsets.zero,
                      onTap: enabled
                          ? () {
                              state.didChange(device.serialNumber);
                              onChanged(device.serialNumber);
                            }
                          : null,
                    ),
                  )
                  .toList(),
            ),
          ),
        );
}
