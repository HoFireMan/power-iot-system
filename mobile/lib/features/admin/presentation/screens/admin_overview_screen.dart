import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../../auth/auth_controller.dart';
import '../../../profile/presentation/providers/profile_provider.dart';
import '../../../shops/providers/remote_shop_provider.dart';
import '../../domain/models/admin_overview.dart';
import '../../domain/models/device_inventory.dart';
import '../../domain/models/measurement_point.dart';
import '../../data/repositories/admin_overview_repository_impl.dart';
import '../../domain/repositories/admin_overview_repository.dart';
import '../../domain/repositories/device_lifecycle_repository.dart';
import '../providers/admin_overview_provider.dart';

class AdminOverviewScreen extends ConsumerStatefulWidget {
  const AdminOverviewScreen({super.key});

  @override
  ConsumerState<AdminOverviewScreen> createState() =>
      _AdminOverviewScreenState();
}

class _AdminOverviewScreenState extends ConsumerState<AdminOverviewScreen> {
  final _lifecyclePending = <String>{};
  final _lifecycleIdentities = <String, String>{};
  late final AuthController _authController;

  void _resetLifecycleState(int _) {
    _lifecyclePending.clear();
    _lifecycleIdentities.clear();
  }

  @override
  void initState() {
    super.initState();
    _authController = ref.read(authControllerProvider);
    _authController.client.addAuthEpochListener(_resetLifecycleState);
  }

  @override
  void dispose() {
    _authController.client.removeAuthEpochListener(_resetLifecycleState);
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final overview = ref.watch(adminOverviewProvider);
    final shops = ref.watch(shopsProvider);
    final waitingForAuthorizedShop =
        ref.watch(authControllerProvider).isAuthenticated &&
            shops.status == RemoteStatus.loading;
    Future<void> retryOverview() => retryAdminOverview(ref);

    // Compatibility routes are used only when the current router explicitly
    // mounted the legacy path. Standalone/test composition remains usable.
    var legacyMockRoute = false;
    try {
      legacyMockRoute = GoRouterState.of(
        context,
      ).uri.path.startsWith('/admin/mock');
    } on GoError {
      // No router state means no navigation has been requested yet.
    }
    final routePrefix = legacyMockRoute ? '/admin/mock' : '/admin';

    void openAssignmentHistory() {
      context.push('$routePrefix/assignment-history');
    }

    Future<void> createMeasurementPoint() async {
      final createdName = await context.push<String>(
        '$routePrefix/create-measurement-point',
      );
      if (createdName == null || !context.mounted) {
        return;
      }

      await retryAdminOverview(ref);
      if (!context.mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(
        SnackBar(content: Text('Measurement Point created: $createdName')),
      );
    }

    Future<void> bindDevice() async {
      final bound = await context.push<bool>('$routePrefix/bind-device');
      if (bound != true || !context.mounted) {
        return;
      }

      await retryAdminOverview(ref);
      if (!context.mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(
        const SnackBar(content: Text('Device bound successfully.')),
      );
    }

    Future<void> replaceDevice(String assignmentId) async {
      final replaced = await context.push<bool>(
        '$routePrefix/replace-device/$assignmentId',
      );
      if (replaced != true || !context.mounted) {
        return;
      }

      await retryAdminOverview(ref);
      if (!context.mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(
        const SnackBar(content: Text('Device replaced successfully.')),
      );
    }

    Future<void> relocateDevice(String assignmentId) async {
      final relocated = await context.push<bool>(
        '$routePrefix/relocate-device/$assignmentId',
      );
      if (relocated != true || !context.mounted) {
        return;
      }

      await retryAdminOverview(ref);
      if (!context.mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(
        const SnackBar(content: Text('Device relocated successfully.')),
      );
    }

    Future<void> changeLifecycle(DeviceInventory device, String target) async {
      final deviceId = device.id;
      if (deviceId == null) return;
      final key = '$deviceId:$target';
      if (_lifecyclePending.contains(key)) return;
      final confirmed = await showDialog<bool>(
        context: context,
        builder: (dialogContext) => AlertDialog(
          title: Text('$target device?'),
          content: Text(
            'This changes the administrative lifecycle of ${device.name}.',
          ),
          actions: [
            TextButton(
              onPressed: () => Navigator.pop(dialogContext, false),
              child: const Text('Cancel'),
            ),
            FilledButton(
              onPressed: () => Navigator.pop(dialogContext, true),
              child: const Text('Confirm'),
            ),
          ],
        ),
      );
      if (confirmed != true ||
          !context.mounted ||
          _lifecyclePending.contains(key)) {
        return;
      }
      _lifecyclePending.add(key);
      final identity = _lifecycleIdentities.putIfAbsent(
        key,
        () =>
            'flutter-device-lifecycle-${deviceId.replaceAll(RegExp(r'[^A-Za-z0-9_-]'), '')}-$target',
      );
      try {
        final repository = ref.read(adminOverviewRepositoryProvider);
        if (repository is! DeviceLifecycleRepository) {
          throw StateError('Device lifecycle is unavailable.');
        }
        final lifecycleRepository = repository as DeviceLifecycleRepository;
        final input = DeviceLifecycleInput(
          requestIdentity: identity,
          deviceId: deviceId,
        );
        if (target == 'DISABLED') {
          await lifecycleRepository.disableDevice(input);
        } else if (target == 'ACTIVE') {
          await lifecycleRepository.enableDevice(input);
        } else {
          await lifecycleRepository.retireDevice(input);
        }
        await retryAdminOverview(ref);
        if (context.mounted) {
          ScaffoldMessenger.of(context).showSnackBar(
            const SnackBar(content: Text('Device lifecycle updated.')),
          );
        }
      } catch (error) {
        if (context.mounted) {
          ScaffoldMessenger.of(context).showSnackBar(
            SnackBar(
              content: Text(
                adminErrorMessage(error, 'Unable to update device lifecycle.'),
              ),
            ),
          );
        }
      } finally {
        _lifecyclePending.remove(key);
      }
    }

    Future<void> unbindDevice(String assignmentId) async {
      final unbound = await context.push<bool>(
        '$routePrefix/unbind-device/$assignmentId',
      );
      if (unbound != true || !context.mounted) {
        return;
      }

      await retryAdminOverview(ref);
      if (!context.mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(
        const SnackBar(content: Text('Device unbound successfully.')),
      );
    }

    return Scaffold(
      appBar: AppBar(title: const Text('Admin Overview')),
      body: SafeArea(
        child: waitingForAuthorizedShop
            ? const Center(child: Text('Loading admin overview…'))
            : overview.when(
                loading: () =>
                    const Center(child: Text('Loading admin overview…')),
                error: (error, stackTrace) => Center(
                  child: Column(
                    mainAxisSize: MainAxisSize.min,
                    children: [
                      Text(
                        adminErrorMessage(
                          error,
                          'Unable to load admin overview. Please retry.',
                        ),
                      ),
                      const SizedBox(height: 12),
                      OutlinedButton(
                        onPressed: retryOverview,
                        child: const Text('Retry'),
                      ),
                    ],
                  ),
                ),
                data: (data) => _OverviewContent(
                  overview: data,
                  onOpenAssignmentHistory: openAssignmentHistory,
                  onOpenAuditHistory: () =>
                      context.push('$routePrefix/audit-history'),
                  onReplace: replaceDevice,
                  onRelocate: relocateDevice,
                  onUnbind: unbindDevice,
                  onLifecycle: changeLifecycle,
                ),
              ),
      ),
      floatingActionButton: Column(
        mainAxisSize: MainAxisSize.min,
        crossAxisAlignment: CrossAxisAlignment.end,
        children: [
          FloatingActionButton.extended(
            heroTag: 'bind-device-action',
            onPressed: bindDevice,
            icon: const Icon(Icons.link),
            label: const Text('Bind Device'),
          ),
          const SizedBox(height: 12),
          FloatingActionButton.extended(
            heroTag: 'create-measurement-point-action',
            onPressed: createMeasurementPoint,
            icon: const Icon(Icons.add_location_alt_outlined),
            label: const Text('Create Measurement Point'),
          ),
        ],
      ),
    );
  }
}

class _OverviewContent extends StatelessWidget {
  const _OverviewContent({
    required this.overview,
    required this.onOpenAssignmentHistory,
    required this.onOpenAuditHistory,
    required this.onReplace,
    required this.onRelocate,
    required this.onUnbind,
    required this.onLifecycle,
  });

  final AdminOverview overview;
  final VoidCallback onOpenAssignmentHistory;
  final VoidCallback onOpenAuditHistory;
  final ValueChanged<String> onReplace;
  final ValueChanged<String> onRelocate;
  final ValueChanged<String> onUnbind;
  final void Function(DeviceInventory device, String target) onLifecycle;

  @override
  Widget build(BuildContext context) {
    final devicesById = {
      for (final device in overview.devices)
        if (device.id != null) device.id!: device,
    };
    final pointsById = {
      for (final point in overview.measurementPoints) point.id: point,
    };
    final assignedDeviceIds = {
      for (final assignment in overview.activeAssignments) assignment.deviceId,
    };

    return ListView(
      padding: const EdgeInsets.all(20),
      children: [
        Card(
          child: ListTile(
            key: const Key('assignment-history-navigation'),
            leading: const Icon(Icons.history),
            title: const Text('Assignment History'),
            subtitle: const Text('View read-only Device assignment intervals'),
            trailing: const Icon(Icons.chevron_right),
            onTap: onOpenAssignmentHistory,
          ),
        ),
        const SizedBox(height: 24),
        _Section(
          title: 'Measurement Points',
          emptyMessage: 'No measurement points available.',
          childCount: overview.measurementPoints.length,
          itemBuilder: (context, index) =>
              _MeasurementPointTile(point: overview.measurementPoints[index]),
        ),
        const SizedBox(height: 24),
        _Section(
          title: 'Devices / Inventory',
          emptyMessage: 'No devices / inventory available.',
          childCount: overview.devices.length,
          itemBuilder: (context, index) {
            final device = overview.devices[index];
            return _DeviceTile(
              device: device,
              hasActiveAssignment:
                  device.id != null && assignedDeviceIds.contains(device.id),
              onLifecycle: onLifecycle,
            );
          },
        ),
        const SizedBox(height: 24),
        _Section(
          title: 'Active Bindings',
          emptyMessage: 'No active bindings.',
          childCount: overview.activeAssignments.length,
          itemBuilder: (context, index) {
            final assignment = overview.activeAssignments[index];
            final device = devicesById[assignment.deviceId];
            final point = pointsById[assignment.measurementPointId];
            return _BindingTile(
              key: Key('active-binding-${assignment.id}'),
              assignmentId: assignment.id,
              pointName: point?.name ?? assignment.measurementPointId,
              serialNumber: device?.serialNumber ?? assignment.deviceId,
              onReplace: () => onReplace(assignment.id),
              onRelocate: () => onRelocate(assignment.id),
              onUnbind: () => onUnbind(assignment.id),
            );
          },
        ),
        Card(
          child: ListTile(
            key: const Key('audit-history-navigation'),
            leading: const Icon(Icons.manage_history),
            title: const Text('Audit History'),
            subtitle: const Text('View read-only Admin Binding operations'),
            trailing: const Icon(Icons.chevron_right),
            onTap: onOpenAuditHistory,
          ),
        ),
      ],
    );
  }
}

class _Section extends StatelessWidget {
  const _Section({
    required this.title,
    required this.emptyMessage,
    required this.childCount,
    required this.itemBuilder,
  });

  final String title;
  final String emptyMessage;
  final int childCount;
  final IndexedWidgetBuilder itemBuilder;

  @override
  Widget build(BuildContext context) {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(title, style: Theme.of(context).textTheme.titleLarge),
        const SizedBox(height: 12),
        if (childCount == 0)
          Text(emptyMessage)
        else
          ...List.generate(childCount, (index) => itemBuilder(context, index)),
      ],
    );
  }
}

class _MeasurementPointTile extends StatelessWidget {
  const _MeasurementPointTile({required this.point});

  final MeasurementPoint point;

  @override
  Widget build(BuildContext context) {
    return Card(
      child: ListTile(
        leading: const Icon(Icons.location_on_outlined),
        title: Text(point.name),
      ),
    );
  }
}

class _DeviceTile extends StatelessWidget {
  const _DeviceTile({
    required this.device,
    required this.hasActiveAssignment,
    required this.onLifecycle,
  });

  final DeviceInventory device;
  final bool hasActiveAssignment;
  final void Function(DeviceInventory device, String target) onLifecycle;

  @override
  Widget build(BuildContext context) {
    final canChange = !hasActiveAssignment &&
        (device.lifecycleStatus == 'ACTIVE' ||
            device.lifecycleStatus == 'DISABLED');
    return Card(
      child: ListTile(
        leading: const Icon(Icons.electrical_services_outlined),
        title: Text(device.name),
        subtitle: Text(device.serialNumber),
        trailing: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            Text(device.status),
            const SizedBox(width: 8),
            Chip(label: Text(device.lifecycleStatus)),
            if (hasActiveAssignment)
              const Padding(
                padding: EdgeInsets.only(left: 8),
                child: Text('Assigned — unbind first'),
              ),
            if (canChange)
              PopupMenuButton<String>(
                tooltip: 'Device lifecycle actions',
                onSelected: (target) => onLifecycle(device, target),
                itemBuilder: (context) => [
                  if (device.lifecycleStatus == 'ACTIVE')
                    const PopupMenuItem(
                        value: 'DISABLED', child: Text('Disable'))
                  else
                    const PopupMenuItem(value: 'ACTIVE', child: Text('Enable')),
                  const PopupMenuItem(value: 'RETIRED', child: Text('Retire')),
                ],
              ),
          ],
        ),
      ),
    );
  }
}

class _BindingTile extends StatelessWidget {
  const _BindingTile({
    super.key,
    required this.assignmentId,
    required this.pointName,
    required this.serialNumber,
    required this.onReplace,
    required this.onRelocate,
    required this.onUnbind,
  });

  final String assignmentId;
  final String pointName;
  final String serialNumber;
  final VoidCallback onReplace;
  final VoidCallback onRelocate;
  final VoidCallback onUnbind;

  @override
  Widget build(BuildContext context) {
    return Card(
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          ListTile(
            leading: const Icon(Icons.link),
            title: Text('$pointName · $serialNumber'),
            subtitle: const Text('Active assignment'),
          ),
          Padding(
            padding: const EdgeInsets.only(left: 16, bottom: 8),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                TextButton(
                  key: Key('replace-device-action-$assignmentId'),
                  onPressed: onReplace,
                  child: const Text('Replace Device'),
                ),
                TextButton(
                  key: Key('relocate-device-action-$assignmentId'),
                  onPressed: onRelocate,
                  child: const Text('Relocate Device'),
                ),
                TextButton(
                  key: Key('unbind-device-action-$assignmentId'),
                  onPressed: onUnbind,
                  child: const Text('Unbind Device'),
                ),
              ],
            ),
          ),
        ],
      ),
    );
  }
}
