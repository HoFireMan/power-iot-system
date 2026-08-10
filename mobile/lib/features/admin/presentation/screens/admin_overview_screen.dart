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
          data: (data) => _OverviewContent(overview: data),
        ),
      ),
      floatingActionButton: FloatingActionButton.extended(
        onPressed: createMeasurementPoint,
        icon: const Icon(Icons.add_location_alt_outlined),
        label: const Text('Create Measurement Point'),
      ),
    );
  }
}

class _OverviewContent extends StatelessWidget {
  const _OverviewContent({required this.overview});

  final AdminOverview overview;

  @override
  Widget build(BuildContext context) {
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
