import '../../domain/models/admin_overview.dart';
import '../../domain/models/device_assignment.dart';
import '../../domain/models/device_inventory.dart';
import '../../domain/models/measurement_point.dart';

class AdminOverviewDto {
  const AdminOverviewDto({
    required this.measurementPoints,
    required this.devices,
    required this.activeAssignments,
    required this.assignmentHistory,
  });
  final List<MeasurementPoint> measurementPoints;
  final List<DeviceInventory> devices;
  final List<DeviceAssignment> activeAssignments;
  final List<DeviceAssignment> assignmentHistory;

  factory AdminOverviewDto.fromJson(Object? value) {
    if (value is! Map ||
        value.keys.any((k) => k is! String) ||
        value.length != 4) {
      throw const FormatException('Invalid admin overview');
    }
    const keys = {
      'measurementPoints',
      'devices',
      'activeAssignments',
      'assignmentHistory',
    };
    if (!value.keys.every(keys.contains)) {
      throw const FormatException('Invalid admin overview');
    }
    List<Object?> list(String key) {
      final v = value[key];
      if (v is! List) {
        throw const FormatException('Invalid admin overview');
      }
      return v.cast<Object?>();
    }

    return AdminOverviewDto(
      measurementPoints: list(
        'measurementPoints',
      ).map(_point).toList(growable: false),
      devices: list('devices').map(_device).toList(growable: false),
      activeAssignments: list(
        'activeAssignments',
      ).map(_assignment).toList(growable: false),
      assignmentHistory: list(
        'assignmentHistory',
      ).map(_assignment).toList(growable: false),
    );
  }
  AdminOverview toModel() => AdminOverview(
        measurementPoints: measurementPoints,
        devices: devices,
        activeAssignments: activeAssignments,
        assignmentHistory: assignmentHistory,
      );
}

MeasurementPoint _point(Object? value) {
  final m = _map(value, {'id', 'shopId', 'name'});
  return MeasurementPoint(
    id: _string(m, 'id'),
    shopId: _string(m, 'shopId'),
    name: _string(m, 'name'),
  );
}

DeviceInventory _device(Object? value) {
  if (value is! Map || value.keys.any((k) => k is! String)) {
    throw const FormatException('Invalid admin overview');
  }
  final keys = value.keys.cast<String>().toSet();
  const required = {'id', 'name', 'serialNumber', 'macAddress', 'status'};
  if (!keys.containsAll(required) ||
      (keys.length != 5 && keys.length != 6) ||
      (keys.length == 6 && !keys.contains('lifecycleStatus'))) {
    throw const FormatException('Invalid admin overview');
  }
  final m = value.cast<String, Object?>();
  return DeviceInventory(
    id: _string(m, 'id'),
    name: _string(m, 'name'),
    // Legacy inventory rows may have no serial and are represented by an
    // empty string. Keep the row in the projection; UI actions filter it
    // out where a serial is required.
    serialNumber: _allowEmptyString(m, 'serialNumber'),
    macAddress: _string(m, 'macAddress'),
    status: _string(m, 'status'),
    lifecycleStatus: keys.contains('lifecycleStatus')
        ? _lifecycle(m, 'lifecycleStatus')
        : 'ACTIVE',
  );
}

DeviceAssignment _assignment(Object? value) {
  final m = _map(value, {
    'id',
    'deviceId',
    'measurementPointId',
    'validFrom',
    'validTo',
  });
  final raw = m['validTo'];
  return DeviceAssignment(
    id: _string(m, 'id'),
    deviceId: _string(m, 'deviceId'),
    measurementPointId: _string(m, 'measurementPointId'),
    validFrom: _date(m, 'validFrom'),
    validTo: raw == null ? null : parseAdminDate(raw),
  );
}

Map<String, Object?> _map(Object? value, Set<String> keys) {
  if (value is! Map ||
      value.keys.any((k) => k is! String) ||
      value.length != keys.length ||
      !value.keys.every(keys.contains)) {
    throw const FormatException('Invalid admin overview');
  }
  return value.cast<String, Object?>();
}

String _string(Map<String, Object?> m, String key) {
  final v = m[key];
  if (v is! String || v.trim().isEmpty) {
    throw const FormatException('Invalid admin overview');
  }
  return v;
}

String _lifecycle(Map<String, Object?> m, String key) {
  final value = _string(m, key);
  if (!const {'ACTIVE', 'DISABLED', 'RETIRED'}.contains(value)) {
    throw const FormatException('Invalid admin overview');
  }
  return value;
}

String _allowEmptyString(Map<String, Object?> m, String key) {
  final v = m[key];
  if (v is! String) {
    throw const FormatException('Invalid admin overview');
  }
  return v;
}

DateTime _date(Map<String, Object?> m, String key) => parseAdminDate(m[key]);

// Assignment timestamps are protocol values, not user-entered local dates.
// Require the RFC3339 date-time separator and an explicit UTC/offset suffix;
// DateTime.tryParse alone also accepts date-only and local timestamps.
final _adminRfc3339 = RegExp(
  r'^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d+)?(?:Z|[+-](?:[01]\d|2[0-3]):[0-5]\d)$',
);

DateTime parseAdminDate(Object? value) {
  if (value is! String) {
    throw const FormatException('Invalid admin overview');
  }
  final match = _adminRfc3339.firstMatch(value);
  if (match == null) {
    throw const FormatException('Invalid admin overview');
  }
  final components = [for (var i = 1; i <= 6; i++) int.parse(match.group(i)!)];
  final local = DateTime.utc(
    components[0],
    components[1],
    components[2],
    components[3],
    components[4],
    components[5],
  );
  if (local.year != components[0] ||
      local.month != components[1] ||
      local.day != components[2] ||
      local.hour != components[3] ||
      local.minute != components[4] ||
      local.second != components[5]) {
    throw const FormatException('Invalid admin overview');
  }
  final d = DateTime.tryParse(value);
  if (d == null) {
    throw const FormatException('Invalid admin overview');
  }
  return d.toUtc();
}
