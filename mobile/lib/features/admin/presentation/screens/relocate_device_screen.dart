import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../domain/models/admin_overview.dart';
import '../../domain/models/device_assignment.dart';
import '../../domain/models/device_inventory.dart';
import '../../domain/models/measurement_point.dart';
import '../../domain/repositories/admin_overview_repository.dart';
import '../../data/repositories/admin_overview_repository_impl.dart';
import '../providers/admin_overview_provider.dart';

class RelocateDeviceScreen extends ConsumerStatefulWidget {
  const RelocateDeviceScreen({required this.assignmentId, super.key});

  final String assignmentId;

  @override
  ConsumerState<RelocateDeviceScreen> createState() =>
      _RelocateDeviceScreenState();
}

class _RelocateDeviceScreenState extends ConsumerState<RelocateDeviceScreen> {
  static const _failureMessage =
      'Unable to relocate Device. Please check the target Measurement Point and try again.';

  String? _selectedTargetMeasurementPointId;
  String? _submissionError;
  bool _isSubmitting = false;
  bool _isCommitted = false;
  final _formKey = GlobalKey<FormState>();

  @override
  void initState() {
    super.initState();
    final pending =
        ref.read(relocateDeviceRequestIdentitySourceProvider).pending;
    if (pending?.currentAssignmentId == widget.assignmentId) {
      _selectedTargetMeasurementPointId = pending?.targetMeasurementPointId;
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
        ref.read(relocateDeviceRequestIdentitySourceProvider).identityFor(
              currentAssignmentId: current.id,
              targetMeasurementPointId: _selectedTargetMeasurementPointId!,
            );

    try {
      await ref.read(adminOverviewRepositoryProvider).relocateDevice(
            RelocateDeviceInput(
              requestIdentity: requestIdentity,
              currentAssignmentId: current.id,
              targetMeasurementPointId: _selectedTargetMeasurementPointId!,
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
        .read(relocateDeviceRequestIdentitySourceProvider)
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
              'Device relocated, but the latest view could not be loaded.';
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
        appBar: AppBar(title: const Text('Relocate Device')),
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
    final source = _findMeasurementPoint(overview, current.measurementPointId);
    if (device == null || source == null) {
      return const Center(
        child: Text('Unable to load the current Device relationship.'),
      );
    }

    final occupiedPointIds = overview.activeAssignments
        .map((assignment) => assignment.measurementPointId)
        .toSet();
    final targetPoints = overview.measurementPoints
        .where(
          (point) =>
              point.id != source.id && !occupiedPointIds.contains(point.id),
        )
        .toList();

    return _RelocateForm(
      currentDevice: device,
      sourcePoint: source,
      targetPoints: targetPoints,
      formKey: _formKey,
      selectedTargetMeasurementPointId: _selectedTargetMeasurementPointId,
      submissionError: _submissionError,
      isSubmitting: _isSubmitting,
      onTargetChanged: (value) =>
          setState(() => _selectedTargetMeasurementPointId = value),
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
                  'Device relocated, but the latest view could not be loaded.',
              textAlign: TextAlign.center,
              style: TextStyle(color: Theme.of(context).colorScheme.error),
            ),
            OutlinedButton(
              key: const Key('relocate-refresh-retry-button'),
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

class _RelocateForm extends StatelessWidget {
  const _RelocateForm({
    required this.currentDevice,
    required this.sourcePoint,
    required this.targetPoints,
    required this.formKey,
    required this.selectedTargetMeasurementPointId,
    required this.submissionError,
    required this.isSubmitting,
    required this.onTargetChanged,
    required this.onSubmit,
    required this.isCommitted,
    required this.onRetryReconciliation,
  });

  final DeviceInventory currentDevice;
  final MeasurementPoint sourcePoint;
  final List<MeasurementPoint> targetPoints;
  final GlobalKey<FormState> formKey;
  final String? selectedTargetMeasurementPointId;
  final String? submissionError;
  final bool isSubmitting;
  final ValueChanged<String?> onTargetChanged;
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
            'Source Measurement Point: ${sourcePoint.name} (${sourcePoint.id})',
          ),
          const SizedBox(height: 8),
          const Text('Effective time is controlled by the mock server.'),
          const SizedBox(height: 16),
          _RelocationTargetField(
            key: const Key('relocate-target-field'),
            value: selectedTargetMeasurementPointId,
            options: targetPoints,
            enabled: !isSubmitting && !isCommitted,
            onChanged: onTargetChanged,
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
              key: const Key('relocate-refresh-retry-button'),
              onPressed: isSubmitting ? null : onRetryReconciliation,
              child: const Text('Retry refresh'),
            ),
          FilledButton(
            key: const Key('relocate-submit-button'),
            onPressed: isSubmitting || isCommitted ? null : onSubmit,
            child: isSubmitting
                ? const SizedBox.square(
                    dimension: 20,
                    child: CircularProgressIndicator(strokeWidth: 2),
                  )
                : const Text('Relocate Device'),
          ),
        ],
      ),
    );
  }
}

class _RelocationTargetField extends FormField<String> {
  _RelocationTargetField({
    required Key super.key,
    required String? value,
    required List<MeasurementPoint> options,
    required bool enabled,
    required ValueChanged<String?> onChanged,
  }) : super(
          initialValue: value,
          validator: (candidate) => candidate == null
              ? 'Target Measurement Point is required.'
              : null,
          builder: (state) => InputDecorator(
            decoration: InputDecoration(
              labelText: 'Target Measurement Point',
              border: const OutlineInputBorder(),
              errorText: state.errorText,
            ),
            child: Column(
              children: options
                  .map(
                    (point) => ListTile(
                      key: Key('relocate-target-option-${point.id}'),
                      selected: state.value == point.id,
                      leading: Icon(
                        state.value == point.id
                            ? Icons.radio_button_checked
                            : Icons.radio_button_unchecked,
                      ),
                      title: Text(point.name),
                      subtitle: Text(point.id),
                      contentPadding: EdgeInsets.zero,
                      onTap: enabled
                          ? () {
                              state.didChange(point.id);
                              onChanged(point.id);
                            }
                          : null,
                    ),
                  )
                  .toList(),
            ),
          ),
        );
}
