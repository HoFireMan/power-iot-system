import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../domain/models/admin_overview.dart';
import '../../domain/models/device_ref.dart';
import '../../domain/repositories/admin_overview_repository.dart';
import '../../data/repositories/admin_overview_repository_impl.dart';
import '../providers/admin_overview_provider.dart';
import '../../../shops/providers/remote_shop_provider.dart';
import '../../../shops/providers/shop_provider.dart';
import '../../../auth/auth_controller.dart';

class BindDeviceScreen extends ConsumerStatefulWidget {
  const BindDeviceScreen({super.key});

  @override
  ConsumerState<BindDeviceScreen> createState() => _BindDeviceScreenState();
}

class _BindDeviceScreenState extends ConsumerState<BindDeviceScreen> {
  static const _failureMessage =
      'Unable to bind Device. Please check the selections and try again.';

  final _formKey = GlobalKey<FormState>();
  String? _selectedSerialNumber;
  String? _selectedMeasurementPointId;
  String? _submissionError;
  bool _isSubmitting = false;
  bool _isCommitted = false;

  @override
  void initState() {
    super.initState();
    final pending = ref.read(bindDeviceRequestIdentitySourceProvider).pending;
    _selectedSerialNumber = pending?.serialNumber;
    _selectedMeasurementPointId = pending?.measurementPointId;
  }

  Future<void> _submit() async {
    if (_isSubmitting || _isCommitted || !_formKey.currentState!.validate()) {
      return;
    }

    setState(() {
      _isSubmitting = true;
      _submissionError = null;
    });
    final requestIdentity =
        ref.read(bindDeviceRequestIdentitySourceProvider).identityFor(
              serialNumber: _selectedSerialNumber!,
              measurementPointId: _selectedMeasurementPointId!,
            );

    try {
      await ref.read(adminOverviewRepositoryProvider).bindDevice(
            BindDeviceInput(
              requestIdentity: requestIdentity,
              deviceRef: DeviceRef(serialNumber: _selectedSerialNumber),
              measurementPointId: _selectedMeasurementPointId!,
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

    ref.read(bindDeviceRequestIdentitySourceProvider).complete(requestIdentity);
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
              'Device bound, but the latest view could not be loaded.';
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
    final authenticated = ref.watch(authControllerProvider).isAuthenticated;
    final shops = authenticated ? ref.watch(shopsProvider) : null;
    final siteName = authenticated
        ? (selectedAdminShop(shops!)?.name ?? 'No authorized shop')
        : ref.watch(shopProvider).currentShop.name;

    return PopScope(
      canPop: !_isSubmitting && !_isCommitted,
      child: Scaffold(
        appBar: AppBar(title: const Text('Bind Device')),
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
            data: (data) => _BindForm(
              overview: data,
              siteName: siteName,
              formKey: _formKey,
              selectedSerialNumber: _selectedSerialNumber,
              selectedMeasurementPointId: _selectedMeasurementPointId,
              submissionError: _submissionError,
              isSubmitting: _isSubmitting,
              onDeviceChanged: (value) =>
                  setState(() => _selectedSerialNumber = value),
              onMeasurementPointChanged: (value) => setState(
                () => _selectedMeasurementPointId = value,
              ),
              onSubmit: _submit,
              isCommitted: _isCommitted,
              onRetryReconciliation: _retryReconciliation,
            ),
          ),
        ),
      ),
    );
  }

  Widget _reconciliationError(BuildContext context) => Center(
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            Text(
              _submissionError ??
                  'Device bound, but the latest view could not be loaded.',
              textAlign: TextAlign.center,
              style: TextStyle(color: Theme.of(context).colorScheme.error),
            ),
            OutlinedButton(
              key: const Key('bind-refresh-retry-button'),
              onPressed: _isSubmitting ? null : _retryReconciliation,
              child: const Text('Retry refresh'),
            ),
          ],
        ),
      );
}

class _BindForm extends StatelessWidget {
  const _BindForm({
    required this.overview,
    required this.siteName,
    required this.formKey,
    required this.selectedSerialNumber,
    required this.selectedMeasurementPointId,
    required this.submissionError,
    required this.isSubmitting,
    required this.onDeviceChanged,
    required this.onMeasurementPointChanged,
    required this.onSubmit,
    required this.isCommitted,
    required this.onRetryReconciliation,
  });

  final AdminOverview overview;
  final String siteName;
  final GlobalKey<FormState> formKey;
  final String? selectedSerialNumber;
  final String? selectedMeasurementPointId;
  final String? submissionError;
  final bool isSubmitting;
  final ValueChanged<String?> onDeviceChanged;
  final ValueChanged<String?> onMeasurementPointChanged;
  final VoidCallback onSubmit;
  final bool isCommitted;
  final VoidCallback onRetryReconciliation;

  @override
  Widget build(BuildContext context) {
    final assignedDeviceIds = overview.activeAssignments
        .map((assignment) => assignment.deviceId)
        .toSet();
    final assignedPointIds = overview.activeAssignments
        .map((assignment) => assignment.measurementPointId)
        .toSet();
    final devices = overview.devices
        .where((device) =>
            device.id != null &&
            device.serialNumber.trim().isNotEmpty &&
            !assignedDeviceIds.contains(device.id))
        .toList();
    final points = overview.measurementPoints
        .where((point) => !assignedPointIds.contains(point.id))
        .toList();

    return Form(
      key: formKey,
      child: ListView(
        padding: const EdgeInsets.all(20),
        children: [
          Text('Site: $siteName'),
          const SizedBox(height: 8),
          const Text(
            'Select one existing eligible Device and one unoccupied Measurement Point.',
          ),
          const SizedBox(height: 16),
          _SelectionField<String>(
            key: const Key('bind-device-field'),
            label: 'Device serial number',
            value: selectedSerialNumber,
            requiredMessage: 'Device is required.',
            options: devices
                .map(
                  (device) => _SelectionOption(
                    key: Key('bind-device-option-${device.serialNumber}'),
                    value: device.serialNumber,
                    label: device.serialNumber,
                  ),
                )
                .toList(),
            enabled: !isSubmitting && !isCommitted,
            onChanged: onDeviceChanged,
          ),
          const SizedBox(height: 16),
          _SelectionField<String>(
            key: const Key('bind-measurement-point-field'),
            label: 'Measurement Point',
            value: selectedMeasurementPointId,
            requiredMessage: 'Measurement Point is required.',
            options: points
                .map(
                  (point) => _SelectionOption(
                    key: Key('bind-point-option-${point.id}'),
                    value: point.id,
                    label: point.name,
                  ),
                )
                .toList(),
            enabled: !isSubmitting && !isCommitted,
            onChanged: onMeasurementPointChanged,
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
              key: const Key('bind-refresh-retry-button'),
              onPressed: isSubmitting ? null : onRetryReconciliation,
              child: const Text('Retry refresh'),
            ),
          FilledButton(
            key: const Key('bind-submit-button'),
            onPressed: isSubmitting || isCommitted ? null : onSubmit,
            child: isSubmitting
                ? const SizedBox.square(
                    dimension: 20,
                    child: CircularProgressIndicator(strokeWidth: 2),
                  )
                : const Text('Bind Device'),
          ),
        ],
      ),
    );
  }
}

class _SelectionOption<T> {
  const _SelectionOption({
    required this.key,
    required this.value,
    required this.label,
  });

  final Key key;
  final T value;
  final String label;
}

class _SelectionField<T> extends FormField<T> {
  _SelectionField({
    required Key super.key,
    required String label,
    required T? value,
    required String requiredMessage,
    required List<_SelectionOption<T>> options,
    required bool enabled,
    required ValueChanged<T?> onChanged,
  }) : super(
          initialValue: value,
          validator: (candidate) => candidate == null ? requiredMessage : null,
          builder: (state) => InputDecorator(
            decoration: InputDecoration(
              labelText: label,
              border: const OutlineInputBorder(),
              errorText: state.errorText,
            ),
            child: Column(
              children: options
                  .map(
                    (option) => ListTile(
                      key: option.key,
                      selected: state.value == option.value,
                      leading: Icon(
                        state.value == option.value
                            ? Icons.radio_button_checked
                            : Icons.radio_button_unchecked,
                      ),
                      title: Text(option.label),
                      contentPadding: EdgeInsets.zero,
                      onTap: enabled
                          ? () {
                              state.didChange(option.value);
                              onChanged(option.value);
                            }
                          : null,
                    ),
                  )
                  .toList(),
            ),
          ),
        );
}
