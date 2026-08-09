import '../../domain/models/admin_overview.dart';
import '../../domain/models/device_inventory.dart';
import '../../domain/models/measurement_point.dart';
import '../../domain/repositories/admin_overview_repository.dart';

/// Deterministic development data for the admin overview.
class MockAdminOverviewRepository implements AdminOverviewRepository {
  const MockAdminOverviewRepository();

  @override
  Future<AdminOverview> loadOverview() async {
    return const AdminOverview(
      measurementPoints: [
        MeasurementPoint(
          id: 'measurement-point-001',
          name: 'Main Hall',
        ),
      ],
      devices: [
        DeviceInventory(
          id: 'device-001',
          name: 'Meter A',
          serialNumber: 'SN-METER-001',
          status: 'Online',
        ),
      ],
    );
  }
}
