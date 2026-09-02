import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../../auth/auth_controller.dart';
import '../../../profile/presentation/providers/profile_provider.dart';
import '../../../shops/providers/remote_shop_provider.dart';
import '../../data/repositories/admin_overview_repository_impl.dart';
import '../../domain/models/admin_overview.dart';
import '../../domain/models/device_assignment.dart';
import '../../domain/models/device_inventory.dart';
import '../../domain/models/measurement_point.dart';
import '../providers/admin_overview_provider.dart';

/// The status choices are applied locally to the already-authorized overview
/// snapshot. They never result in another history request.
enum AssignmentHistoryStatusFilter { all, active, ended }

/// Returns the history rows in the product's stable presentation order.
///
/// [validFrom] is compared as an instant, not as a formatted/localized date.
List<DeviceAssignment> sortAssignmentHistory(
  Iterable<DeviceAssignment> assignments,
) {
  final result = assignments.toList(growable: false);
  result.sort((a, b) {
    final byDate = b.validFrom.compareTo(a.validFrom);
    return byDate == 0 ? a.id.compareTo(b.id) : byDate;
  });
  return result;
}

/// Applies the history filters with AND semantics and then sorts the result.
List<DeviceAssignment> filterAssignmentHistory({
  required Iterable<DeviceAssignment> assignments,
  AssignmentHistoryStatusFilter status = AssignmentHistoryStatusFilter.all,
  String? measurementPointId,
  String? deviceId,
}) {
  return sortAssignmentHistory(
    assignments.where((assignment) {
      final statusMatches = switch (status) {
        AssignmentHistoryStatusFilter.all => true,
        AssignmentHistoryStatusFilter.active => assignment.active,
        AssignmentHistoryStatusFilter.ended => !assignment.active,
      };
      return statusMatches &&
          (measurementPointId == null ||
              assignment.measurementPointId == measurementPointId) &&
          (deviceId == null || assignment.deviceId == deviceId);
    }),
  );
}

/// Read-only assignment interval history projected from [adminOverviewProvider].
class AssignmentHistoryScreen extends ConsumerStatefulWidget {
  const AssignmentHistoryScreen({super.key});

  @override
  ConsumerState<AssignmentHistoryScreen> createState() =>
      _AssignmentHistoryScreenState();
}

class _AssignmentHistoryScreenState
    extends ConsumerState<AssignmentHistoryScreen> {
  AssignmentHistoryStatusFilter _status = AssignmentHistoryStatusFilter.all;
  String? _measurementPointId;
  String? _deviceId;

  @override
  Widget build(BuildContext context) {
    final overview = ref.watch(adminOverviewProvider);
    final shops = ref.watch(shopsProvider);
    final waitingForAuthorizedShop =
        ref.watch(authControllerProvider).isAuthenticated &&
            shops.status == RemoteStatus.loading;
    return Scaffold(
      appBar: AppBar(title: const Text('Assignment History')),
      body: SafeArea(
        child: waitingForAuthorizedShop
            ? const Center(child: Text('Loading admin overview…'))
            : overview.when(
                loading: () =>
                    const Center(child: Text('Loading admin overview…')),
                error: (error, stackTrace) => _HistoryError(
                  error: error,
                  onRetry: () => retryAdminOverview(ref),
                ),
                data: (data) => _HistoryContent(
                  overview: data,
                  status: _status,
                  measurementPointId: _measurementPointId,
                  deviceId: _deviceId,
                  onStatusChanged: (value) {
                    setState(() => _status = value);
                  },
                  onMeasurementPointChanged: (value) {
                    setState(() => _measurementPointId = value);
                  },
                  onDeviceChanged: (value) {
                    setState(() => _deviceId = value);
                  },
                ),
              ),
      ),
    );
  }
}

class _HistoryError extends StatelessWidget {
  const _HistoryError({required this.error, required this.onRetry});

  final Object error;
  final VoidCallback onRetry;

  @override
  Widget build(BuildContext context) {
    final isNoShop = error.toString().toLowerCase().contains(
          'no authorized shop',
        );
    return Center(
      child: Padding(
        padding: const EdgeInsets.all(24),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            Text(
              isNoShop
                  ? 'No authorized Shop is available.'
                  : adminErrorMessage(
                      error,
                      'Unable to load admin overview. Please retry.',
                    ),
              textAlign: TextAlign.center,
            ),
            const SizedBox(height: 12),
            OutlinedButton(onPressed: onRetry, child: const Text('Retry')),
          ],
        ),
      ),
    );
  }
}

class _HistoryContent extends StatelessWidget {
  const _HistoryContent({
    required this.overview,
    required this.status,
    required this.measurementPointId,
    required this.deviceId,
    required this.onStatusChanged,
    required this.onMeasurementPointChanged,
    required this.onDeviceChanged,
  });

  final AdminOverview overview;
  final AssignmentHistoryStatusFilter status;
  final String? measurementPointId;
  final String? deviceId;
  final ValueChanged<AssignmentHistoryStatusFilter> onStatusChanged;
  final ValueChanged<String?> onMeasurementPointChanged;
  final ValueChanged<String?> onDeviceChanged;

  @override
  Widget build(BuildContext context) {
    final pointsById = <String, MeasurementPoint>{
      for (final point in overview.measurementPoints) point.id: point,
    };
    final devicesById = <String, DeviceInventory>{
      for (final device in overview.devices)
        if (device.id != null) device.id!: device,
    };
    final points = overview.measurementPoints.toList()..sort(_comparePoint);
    final devices = overview.devices.toList()..sort(_compareDevice);
    // A Shop change can replace the authorized snapshot while this screen is
    // mounted. Do not retain a filter value that is absent from the new
    // snapshot: DropdownButton requires its value to have one matching item.
    final selectedPointId =
        pointsById.containsKey(measurementPointId) ? measurementPointId : null;
    final selectedDeviceId =
        devicesById.containsKey(deviceId) ? deviceId : null;
    final rows = filterAssignmentHistory(
      assignments: overview.assignmentHistory,
      status: status,
      measurementPointId: selectedPointId,
      deviceId: selectedDeviceId,
    );

    return ListView(
      padding: const EdgeInsets.all(20),
      children: [
        _HistoryFilters(
          status: status,
          measurementPointId: selectedPointId,
          deviceId: selectedDeviceId,
          points: points,
          devices: devices,
          onStatusChanged: onStatusChanged,
          onMeasurementPointChanged: onMeasurementPointChanged,
          onDeviceChanged: onDeviceChanged,
        ),
        const SizedBox(height: 20),
        if (overview.assignmentHistory.isEmpty)
          const Text('No assignment history available.')
        else if (rows.isEmpty)
          const Text('No assignment history matches the selected filters.')
        else
          ...rows.map(
            (assignment) => _AssignmentTile(
              key: Key('assignment-history-${assignment.id}'),
              assignment: assignment,
              pointName: _nameOrId(
                pointsById[assignment.measurementPointId]?.name,
                assignment.measurementPointId,
              ),
              deviceName: _nameOrId(
                devicesById[assignment.deviceId]?.name,
                assignment.deviceId,
              ),
              device: devicesById[assignment.deviceId],
            ),
          ),
      ],
    );
  }
}

class _HistoryFilters extends StatelessWidget {
  const _HistoryFilters({
    required this.status,
    required this.measurementPointId,
    required this.deviceId,
    required this.points,
    required this.devices,
    required this.onStatusChanged,
    required this.onMeasurementPointChanged,
    required this.onDeviceChanged,
  });

  final AssignmentHistoryStatusFilter status;
  final String? measurementPointId;
  final String? deviceId;
  final List<MeasurementPoint> points;
  final List<DeviceInventory> devices;
  final ValueChanged<AssignmentHistoryStatusFilter> onStatusChanged;
  final ValueChanged<String?> onMeasurementPointChanged;
  final ValueChanged<String?> onDeviceChanged;

  @override
  Widget build(BuildContext context) {
    return Card(
      child: Padding(
        padding: const EdgeInsets.all(16),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text('Filters', style: Theme.of(context).textTheme.titleMedium),
            const SizedBox(height: 12),
            DropdownButtonFormField<AssignmentHistoryStatusFilter>(
              key: const Key('assignment-history-status-filter'),
              initialValue: status,
              decoration: const InputDecoration(labelText: 'Status'),
              items: const [
                DropdownMenuItem(
                  value: AssignmentHistoryStatusFilter.all,
                  child: Text('All'),
                ),
                DropdownMenuItem(
                  value: AssignmentHistoryStatusFilter.active,
                  child: Text('Active'),
                ),
                DropdownMenuItem(
                  value: AssignmentHistoryStatusFilter.ended,
                  child: Text('Ended'),
                ),
              ],
              onChanged: (value) {
                if (value != null) onStatusChanged(value);
              },
            ),
            const SizedBox(height: 12),
            DropdownButtonFormField<String>(
              key: const Key('assignment-history-measurement-point-filter'),
              initialValue: measurementPointId,
              decoration: const InputDecoration(labelText: 'Measurement Point'),
              items: [
                const DropdownMenuItem<String>(
                  value: null,
                  child: Text('All Measurement Points'),
                ),
                ...points.map(
                  (point) => DropdownMenuItem<String>(
                    value: point.id,
                    child: Text(_nameOrId(point.name, point.id)),
                  ),
                ),
              ],
              onChanged: onMeasurementPointChanged,
            ),
            const SizedBox(height: 12),
            DropdownButtonFormField<String>(
              key: const Key('assignment-history-device-filter'),
              initialValue: deviceId,
              decoration: const InputDecoration(labelText: 'Device'),
              items: [
                const DropdownMenuItem<String>(
                  value: null,
                  child: Text('All Devices'),
                ),
                ...devices.where((device) => device.id != null).map(
                      (device) => DropdownMenuItem<String>(
                        value: device.id,
                        child: Text(_nameOrId(device.name, device.id!)),
                      ),
                    ),
              ],
              onChanged: onDeviceChanged,
            ),
          ],
        ),
      ),
    );
  }
}

class _AssignmentTile extends StatelessWidget {
  const _AssignmentTile({
    super.key,
    required this.assignment,
    required this.pointName,
    required this.deviceName,
    required this.device,
  });

  final DeviceAssignment assignment;
  final String pointName;
  final String deviceName;
  final DeviceInventory? device;

  @override
  Widget build(BuildContext context) {
    final identifiers = <String>[
      if (device?.serialNumber.trim().isNotEmpty == true)
        'Serial: ${device!.serialNumber}',
      if (device?.macAddress?.trim().isNotEmpty == true)
        'MAC: ${device!.macAddress}',
    ];
    return Card(
      child: ListTile(
        leading: Icon(
          assignment.active ? Icons.link : Icons.link_off,
          semanticLabel: assignment.active ? 'Active' : 'Ended',
        ),
        title: Text('$pointName · $deviceName'),
        subtitle: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(assignment.active ? 'Active' : 'Ended'),
            Text('Valid from: ${formatAdminTimestamp(assignment.validFrom)}'),
            Text(
              'Valid to: ${assignment.validTo == null ? 'Active' : formatAdminTimestamp(assignment.validTo!)}',
            ),
            ...identifiers.map(Text.new),
            Text('Assignment ID: ${assignment.id}'),
          ],
        ),
      ),
    );
  }
}

String formatAdminTimestamp(DateTime instant) {
  final value = instant.toUtc();
  String two(int number) => number.toString().padLeft(2, '0');
  final fraction = value.millisecond == 0 && value.microsecond == 0
      ? ''
      : '.${value.millisecond.toString().padLeft(3, '0')}'
          '${value.microsecond.toString().padLeft(3, '0')}';
  return '${value.year.toString().padLeft(4, '0')}-'
      '${two(value.month)}-${two(value.day)} '
      '${two(value.hour)}:${two(value.minute)}:${two(value.second)}'
      '$fraction UTC';
}

String _nameOrId(String? name, String id) =>
    name == null || name.trim().isEmpty ? id : name;

int _comparePoint(MeasurementPoint a, MeasurementPoint b) {
  final byName = _nameOrId(a.name, a.id).compareTo(_nameOrId(b.name, b.id));
  return byName == 0 ? a.id.compareTo(b.id) : byName;
}

int _compareDevice(DeviceInventory a, DeviceInventory b) {
  final byName = _nameOrId(
    a.name,
    a.id ?? '',
  ).compareTo(_nameOrId(b.name, b.id ?? ''));
  return byName == 0 ? (a.id ?? '').compareTo(b.id ?? '') : byName;
}
