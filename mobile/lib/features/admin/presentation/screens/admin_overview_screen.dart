import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../domain/models/admin_overview.dart';
import '../../domain/models/device_inventory.dart';
import '../../domain/models/measurement_point.dart';
import '../providers/admin_overview_provider.dart';

class AdminOverviewScreen extends ConsumerWidget {
  const AdminOverviewScreen({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final overview = ref.watch(adminOverviewProvider);

    Future<void> createMeasurementPoint() async {
      final createdName = await context.push<String>(
        '/admin/mock/create-measurement-point',
      );
      if (createdName == null || !context.mounted) {
        return;
      }

      ref.invalidate(adminOverviewProvider);
      ScaffoldMessenger.of(context).showSnackBar(
        SnackBar(content: Text('Measurement Point created: $createdName')),
      );
    }

    Future<void> bindDevice() async {
      final bound = await context.push<bool>('/admin/mock/bind-device');
      if (bound != true || !context.mounted) {
        return;
      }

      ref.invalidate(adminOverviewProvider);
      ScaffoldMessenger.of(context).showSnackBar(
        const SnackBar(content: Text('Device bound successfully.')),
      );
    }

    Future<void> replaceDevice(String assignmentId) async {
      final replaced = await context.push<bool>(
        '/admin/mock/replace-device/$assignmentId',
      );
      if (replaced != true || !context.mounted) {
        return;
      }

      ref.invalidate(adminOverviewProvider);
      ScaffoldMessenger.of(context).showSnackBar(
        const SnackBar(content: Text('Device replaced successfully.')),
      );
    }

    Future<void> relocateDevice(String assignmentId) async {
      final relocated = await context.push<bool>(
        '/admin/mock/relocate-device/$assignmentId',
      );
      if (relocated != true || !context.mounted) {
        return;
      }

      ref.invalidate(adminOverviewProvider);
      ScaffoldMessenger.of(context).showSnackBar(
        const SnackBar(content: Text('Device relocated successfully.')),
      );
    }

    Future<void> unbindDevice(String assignmentId) async {
      final unbound = await context.push<bool>(
        '/admin/mock/unbind-device/$assignmentId',
      );
      if (unbound != true || !context.mounted) {
        return;
      }

      ref.invalidate(adminOverviewProvider);
      ScaffoldMessenger.of(context).showSnackBar(
        const SnackBar(content: Text('Device unbound successfully.')),
      );
    }

    return Scaffold(
      appBar: AppBar(title: const Text('Admin Overview')),
      body: SafeArea(
        child: overview.when(
          loading: () => const Center(
            child: Text('Loading admin overview…'),
          ),
          error: (error, stackTrace) => Center(
            child: Text('Unable to load admin overview: $error'),
          ),
          data: (data) => _OverviewContent(
            overview: data,
            onReplace: replaceDevice,
            onRelocate: relocateDevice,
            onUnbind: unbindDevice,
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
    required this.onReplace,
    required this.onRelocate,
    required this.onUnbind,
  });

  final AdminOverview overview;
  final ValueChanged<String> onReplace;
  final ValueChanged<String> onRelocate;
  final ValueChanged<String> onUnbind;

  @override
  Widget build(BuildContext context) {
    final devicesById = {
      for (final device in overview.devices)
        if (device.id != null) device.id!: device,
    };
    final pointsById = {
      for (final point in overview.measurementPoints) point.id: point,
    };

    return ListView(
      padding: const EdgeInsets.all(20),
      children: [
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
          itemBuilder: (context, index) =>
              _DeviceTile(device: overview.devices[index]),
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
        Text(
          title,
          style: Theme.of(context).textTheme.titleLarge,
        ),
        const SizedBox(height: 12),
        if (childCount == 0)
          Text(emptyMessage)
        else
          ...List.generate(
            childCount,
            (index) => itemBuilder(context, index),
          ),
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
  const _DeviceTile({required this.device});

  final DeviceInventory device;

  @override
  Widget build(BuildContext context) {
    return Card(
      child: ListTile(
        leading: const Icon(Icons.electrical_services_outlined),
        title: Text(device.name),
        subtitle: Text(device.serialNumber),
        trailing: Text(device.status),
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
