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

class UnbindDeviceScreen extends ConsumerStatefulWidget {
  const UnbindDeviceScreen({required this.assignmentId, super.key});

  final String assignmentId;

  @override
  ConsumerState<UnbindDeviceScreen> createState() => _UnbindDeviceScreenState();
}

class _UnbindDeviceScreenState extends ConsumerState<UnbindDeviceScreen> {
  static const _failureMessage =
      'Unable to unbind Device. Please check the current assignment and try again.';

  final _reasonController = TextEditingController();
  bool _isSubmitting = false;
  bool _isCommitted = false;
  String? _submissionError;

  @override
  void initState() {
    super.initState();
    final pending = ref.read(unbindDeviceRequestIdentitySourceProvider).pending;
    if (pending?.currentAssignmentId == widget.assignmentId) {
      _reasonController.text = pending!.reason;
    }
  }

  @override
  void dispose() {
    _reasonController.dispose();
    super.dispose();
  }

  Future<void> _submit(DeviceAssignment current) async {
    if (_isSubmitting || _isCommitted) {
      return;
    }

    setState(() {
      _isSubmitting = true;
      _submissionError = null;
    });
    final reason = _reasonController.text.trim();
    final requestIdentity =
        ref.read(unbindDeviceRequestIdentitySourceProvider).identityFor(
              currentAssignmentId: current.id,
              reason: reason,
            );

    try {
      await ref.read(adminOverviewRepositoryProvider).unbindDevice(
            UnbindDeviceInput(
              requestIdentity: requestIdentity,
              currentAssignmentId: current.id,
              reason: reason,
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
        .read(unbindDeviceRequestIdentitySourceProvider)
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
              'Device unbound, but the latest view could not be loaded.';
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
        appBar: AppBar(title: const Text('Unbind Device')),
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

    return _UnbindForm(
      currentDevice: device,
      currentPoint: point,
      reasonController: _reasonController,
      submissionError: _submissionError,
      isSubmitting: _isSubmitting,
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
                  'Device unbound, but the latest view could not be loaded.',
              textAlign: TextAlign.center,
              style: TextStyle(color: Theme.of(context).colorScheme.error),
            ),
            OutlinedButton(
              key: const Key('unbind-refresh-retry-button'),
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

class _UnbindForm extends StatelessWidget {
  const _UnbindForm({
    required this.currentDevice,
    required this.currentPoint,
    required this.reasonController,
    required this.submissionError,
    required this.isSubmitting,
    required this.onSubmit,
    required this.isCommitted,
    required this.onRetryReconciliation,
  });

  final DeviceInventory currentDevice;
  final MeasurementPoint currentPoint;
  final TextEditingController reasonController;
  final String? submissionError;
  final bool isSubmitting;
  final VoidCallback onSubmit;
  final bool isCommitted;
  final VoidCallback onRetryReconciliation;

  @override
  Widget build(BuildContext context) {
    return ListView(
      padding: const EdgeInsets.all(20),
      children: [
        Text(
          'Current Device: ${currentDevice.serialNumber} (${currentDevice.id})',
        ),
        const SizedBox(height: 8),
        Text(
          'Measurement Point: ${currentPoint.name} (${currentPoint.id})',
        ),
        const SizedBox(height: 16),
        const Text(
          'This closes the current assignment. The Device and Measurement Point remain available, and assignment history is retained.',
        ),
        const SizedBox(height: 8),
        const Text('Effective time is controlled by the mock server.'),
        const SizedBox(height: 16),
        TextField(
          key: const Key('unbind-reason-field'),
          controller: reasonController,
          enabled: !isSubmitting && !isCommitted,
          maxLines: 3,
          decoration: const InputDecoration(
            labelText: 'Reason (optional)',
            border: OutlineInputBorder(),
          ),
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
            key: const Key('unbind-refresh-retry-button'),
            onPressed: isSubmitting ? null : onRetryReconciliation,
            child: const Text('Retry refresh'),
          ),
        FilledButton(
          key: const Key('unbind-submit-button'),
          onPressed: isSubmitting || isCommitted ? null : onSubmit,
          child: isSubmitting
              ? const SizedBox.square(
                  dimension: 20,
                  child: CircularProgressIndicator(strokeWidth: 2),
                )
              : const Text('Unbind Device'),
        ),
      ],
    );
  }
}
