import '../../domain/models/admin_overview.dart';
import '../../domain/models/device_assignment.dart';
import '../../domain/models/device_inventory.dart';
import '../../domain/models/device_ref.dart';
import '../../domain/models/measurement_point.dart';
import '../../domain/repositories/admin_overview_repository.dart';

/// Deterministic development data for the admin overview and binding flow.
class MockAdminOverviewRepository implements AdminOverviewRepository {
  MockAdminOverviewRepository()
      : _measurementPoints = [
          const MeasurementPoint(
            id: '00000000-0000-4000-8000-000000000001',
            shopId: 's1',
            name: 'Main Hall',
          ),
          const MeasurementPoint(
            id: '00000000-0000-4000-8000-000000000099',
            shopId: 's1',
            name: 'Kitchen',
          ),
        ];

  final List<MeasurementPoint> _measurementPoints;
  final Map<String, MeasurementPoint> _committedCreates = {};
  final Map<String, DeviceAssignment> _committedBindings = {};
  final Map<String, String> _bindingRequests = {};
  final List<DeviceAssignment> _activeAssignments = [];
  final List<DeviceInventory> _devices = const [
    DeviceInventory(
      id: 'device-001',
      name: 'Meter A',
      serialNumber: 'SN-METER-001',
      macAddress: 'AABBCCDDEE01',
      status: 'Online',
    ),
    DeviceInventory(
      id: 'device-002',
      name: 'Meter B',
      serialNumber: 'SN-METER-002',
      macAddress: 'AABBCCDDEE02',
      status: 'Standby',
    ),
  ];
  int _nextCreatedIdentity = 2;
  int _nextAssignmentIdentity = 1;

  /// Makes only the next create call fail before mutation.
  bool failNextCreation = false;

  /// Commits the next create and then simulates losing its response.
  bool loseResponseAfterNextCreation = false;

  /// Commits the next bind and then simulates losing its response.
  bool loseResponseAfterNextBinding = false;

  @override
  Future<AdminOverview> loadOverview() async {
    return AdminOverview(
      measurementPoints: List.unmodifiable(_measurementPoints),
      devices: List.unmodifiable(_devices),
      activeAssignments: List.unmodifiable(_activeAssignments),
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

  @override
  Future<DeviceAssignment> bindDevice(BindDeviceInput input) async {
    final requestIdentity = input.requestIdentity.trim();
    if (requestIdentity.isEmpty) {
      throw ArgumentError.value(input.requestIdentity, 'requestIdentity');
    }
    if (input.measurementPointId.trim().isEmpty) {
      throw ArgumentError.value(input.measurementPointId, 'measurementPointId');
    }

    final fingerprint = _bindingFingerprint(input);
    final committed = _committedBindings[requestIdentity];
    if (committed != null) {
      if (_bindingRequests[requestIdentity] == fingerprint) {
        return committed;
      }
      throw StateError('Binding request identity was reused.');
    }

    final point = _measurementPoints.cast<MeasurementPoint?>().firstWhere(
          (candidate) => candidate!.id == input.measurementPointId,
          orElse: () => null,
        );
    if (point == null) {
      throw StateError('Measurement Point not found.');
    }

    final device = _resolveDevice(input.deviceRef);
    if (device == null) {
      throw StateError('Device not found.');
    }
    if (device.serialNumber.trim().isEmpty ||
        !_isCanonicalMac(device.macAddress)) {
      throw StateError('Device is not eligible.');
    }
    if (_activeAssignments
        .any((assignment) => assignment.deviceId == device.id)) {
      throw StateError('Device is already assigned.');
    }
    if (_activeAssignments.any(
      (assignment) => assignment.measurementPointId == point.id,
    )) {
      throw StateError('Measurement Point is occupied.');
    }

    final assignment = DeviceAssignment(
      id: 'assignment-${_nextAssignmentIdentity.toString().padLeft(3, '0')}',
      deviceId: device.id!,
      measurementPointId: point.id,
    );
    _nextAssignmentIdentity++;
    _activeAssignments.add(assignment);
    _committedBindings[requestIdentity] = assignment;
    _bindingRequests[requestIdentity] = fingerprint;

    if (loseResponseAfterNextBinding) {
      loseResponseAfterNextBinding = false;
      throw StateError('Deterministic mock response loss after commit');
    }

    return assignment;
  }

  DeviceInventory? _resolveDevice(DeviceRef ref) {
    final hasIdentifier =
        ref.id != null || ref.serialNumber != null || ref.macAddress != null;
    if (!hasIdentifier) {
      throw ArgumentError('At least one Device reference is required.');
    }

    final matches = <DeviceInventory>[];
    for (final device in _devices) {
      final matchesId = ref.id == null || ref.id == device.id;
      final matchesSerial =
          ref.serialNumber == null || ref.serialNumber == device.serialNumber;
      final matchesMac =
          ref.macAddress == null || ref.macAddress == device.macAddress;
      if (matchesId && matchesSerial && matchesMac) {
        matches.add(device);
      }
    }
    if (matches.length == 1) {
      return matches.single;
    }
    if (matches.isEmpty &&
        ref.id != null &&
        _devices.any((device) => device.id == ref.id)) {
      throw StateError('Device identifiers are inconsistent.');
    }
    if (matches.isEmpty &&
        ref.serialNumber != null &&
        _devices.any((device) => device.serialNumber == ref.serialNumber)) {
      throw StateError('Device identifiers are inconsistent.');
    }
    if (matches.isEmpty &&
        ref.macAddress != null &&
        _devices.any((device) => device.macAddress == ref.macAddress)) {
      throw StateError('Device identifiers are inconsistent.');
    }
    return null;
  }

  String _bindingFingerprint(BindDeviceInput input) {
    final ref = input.deviceRef;
    return '${input.measurementPointId}|${ref.id}|${ref.serialNumber}|${ref.macAddress}';
  }

  bool _isCanonicalMac(String? value) {
    if (value == null || value.length != 12) {
      return false;
    }
    return RegExp(r'^[0-9A-F]{12}$').hasMatch(value);
  }
}
