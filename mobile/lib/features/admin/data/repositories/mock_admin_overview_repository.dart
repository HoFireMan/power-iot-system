import '../../domain/models/admin_overview.dart';
import '../../domain/models/device_inventory.dart';
import '../../domain/models/measurement_point.dart';
import '../../domain/repositories/admin_overview_repository.dart';

/// Deterministic development data for the admin overview.
class MockAdminOverviewRepository implements AdminOverviewRepository {
  MockAdminOverviewRepository()
      : _measurementPoints = [
          const MeasurementPoint(
            id: '00000000-0000-4000-8000-000000000001',
            shopId: 's1',
            name: 'Main Hall',
          ),
        ];

  final List<MeasurementPoint> _measurementPoints;
  final Map<String, MeasurementPoint> _committedCreates = {};
  final List<DeviceInventory> _devices = const [
    DeviceInventory(
      id: 'device-001',
      name: 'Meter A',
      serialNumber: 'SN-METER-001',
      status: 'Online',
    ),
  ];
  int _nextCreatedIdentity = 2;

  /// Makes only the next create call fail before mutation.
  bool failNextCreation = false;

  /// Commits the next create and then simulates losing its response.
  bool loseResponseAfterNextCreation = false;

  @override
  Future<AdminOverview> loadOverview() async {
    return AdminOverview(
      measurementPoints: List.unmodifiable(_measurementPoints),
      devices: List.unmodifiable(_devices),
    );
  }

  @override
  Future<MeasurementPoint> createMeasurementPoint(
    CreateMeasurementPointInput input,
  ) async {
    final trimmedName = input.name.trim();
    if (trimmedName.isEmpty || input.name.runes.length > 100) {
      throw ArgumentError.value(input.name, 'name');
    }
    if (input.shopId.trim().isEmpty) {
      throw ArgumentError.value(input.shopId, 'shopId');
    }
    if (input.requestIdentity.trim().isEmpty) {
      throw ArgumentError.value(input.requestIdentity, 'requestIdentity');
    }

    final committed = _committedCreates[input.requestIdentity];
    if (committed != null) {
      if (committed.shopId == input.shopId && committed.name == trimmedName) {
        return committed;
      }
      throw StateError('Creation request identity was reused.');
    }

    if (failNextCreation) {
      failNextCreation = false;
      throw StateError('Deterministic mock creation failure');
    }

    final point = MeasurementPoint(
      id: '00000000-0000-4000-8000-${_nextCreatedIdentity.toString().padLeft(12, '0')}',
      shopId: input.shopId,
      name: trimmedName,
    );
    _nextCreatedIdentity++;
    _measurementPoints.add(point);
    _committedCreates[input.requestIdentity] = point;

    if (loseResponseAfterNextCreation) {
      loseResponseAfterNextCreation = false;
      throw StateError('Deterministic mock response loss after commit');
    }

    return point;
  }
}
