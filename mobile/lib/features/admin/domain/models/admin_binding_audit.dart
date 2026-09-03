/// A current name joined for readability. It is never a historical name
/// snapshot; the identifier and device identifiers are the historical facts.
class AdminBindingAuditActor {
  const AdminBindingAuditActor({required this.id, this.currentDisplayName});
  final String id;
  final String? currentDisplayName;

  factory AdminBindingAuditActor.fromJson(Object? value) {
    if (value is! Map) throw const FormatException('Invalid audit actor');
    final json = value.cast<String, dynamic>();
    return AdminBindingAuditActor(
      id: _required(json, 'id'),
      currentDisplayName: _optionalString(json, 'currentDisplayName'),
    );
  }
}

class AdminBindingAuditMeasurementPoint {
  const AdminBindingAuditMeasurementPoint({required this.id, this.currentDisplayName});
  final String id;
  final String? currentDisplayName;

  factory AdminBindingAuditMeasurementPoint.fromJson(Object? value) {
    if (value is! Map) throw const FormatException('Invalid audit MeasurementPoint');
    final json = value.cast<String, dynamic>();
    return AdminBindingAuditMeasurementPoint(
      id: _required(json, 'id'),
      currentDisplayName: _optionalString(json, 'currentDisplayName'),
    );
  }
}

class AdminBindingAuditDevice {
  const AdminBindingAuditDevice({
    required this.id,
    this.serialNumber,
    this.mac,
    this.currentDisplayName,
  });
  final String id;
  final String? serialNumber;
  final String? mac;
  final String? currentDisplayName;

  factory AdminBindingAuditDevice.fromJson(Object? value) {
    if (value is! Map) throw const FormatException('Invalid audit device');
    final json = value.cast<String, dynamic>();
    return AdminBindingAuditDevice(
      id: _required(json, 'id'),
      serialNumber: _optionalString(json, 'serialNumber'),
      mac: _optionalString(json, 'mac'),
      currentDisplayName: _optionalString(json, 'currentDisplayName'),
    );
  }
}

class AdminBindingAudit {
  const AdminBindingAudit({
    required this.id,
    required this.operationId,
    required this.action,
    required this.occurredAt,
    this.reason,
    required this.actor,
    this.effectiveAt,
    this.measurementPoint,
    this.device,
    this.oldMeasurementPoint,
    this.newMeasurementPoint,
    this.oldAssignmentId,
    this.newAssignmentId,
  });

  final String id;
  final String operationId;
  final String action;
  final DateTime occurredAt;
  final DateTime? effectiveAt;
  final String? reason;
  final AdminBindingAuditActor actor;
  final AdminBindingAuditMeasurementPoint? measurementPoint;
  final AdminBindingAuditDevice? device;
  final AdminBindingAuditMeasurementPoint? oldMeasurementPoint;
  final AdminBindingAuditMeasurementPoint? newMeasurementPoint;
  final String? oldAssignmentId;
  final String? newAssignmentId;

  factory AdminBindingAudit.fromJson(Map<String, dynamic> json) {
    return AdminBindingAudit(
      id: _required(json, 'id'),
      operationId: _required(json, 'operationId'),
      action: _required(json, 'action'),
      occurredAt: DateTime.parse(_required(json, 'occurredAt')),
      effectiveAt: json['effectiveAt'] == null
          ? null
          : DateTime.parse(json['effectiveAt'] as String),
      reason: _optionalString(json, 'reason'),
      actor: AdminBindingAuditActor.fromJson(json['actor']),
      measurementPoint: json['measurementPoint'] == null
          ? null
          : AdminBindingAuditMeasurementPoint.fromJson(json['measurementPoint']),
      device: json['device'] == null
          ? null
          : AdminBindingAuditDevice.fromJson(json['device']),
      oldMeasurementPoint: json['oldMeasurementPoint'] == null
          ? null
          : AdminBindingAuditMeasurementPoint.fromJson(json['oldMeasurementPoint']),
      newMeasurementPoint: json['newMeasurementPoint'] == null
          ? null
          : AdminBindingAuditMeasurementPoint.fromJson(json['newMeasurementPoint']),
      oldAssignmentId: _optionalString(json, 'oldAssignmentId'),
      newAssignmentId: _optionalString(json, 'newAssignmentId'),
    );
  }
}

class AdminBindingAuditHistoryPage {
  const AdminBindingAuditHistoryPage({required this.items, this.nextCursor});
  final List<AdminBindingAudit> items;
  final String? nextCursor;

  factory AdminBindingAuditHistoryPage.fromJson(Map<String, dynamic> json) =>
      AdminBindingAuditHistoryPage(
        items: ((json['items'] as List<dynamic>? ?? const [])).map((item) {
          if (item is! Map) throw const FormatException('Invalid audit item');
          return AdminBindingAudit.fromJson(item.cast<String, dynamic>());
        }).toList(growable: false),
        nextCursor: json['nextCursor'] as String?,
      );
}

String _required(Map<String, dynamic> json, String key) {
  final value = json[key];
  if (value is! String || value.trim().isEmpty) {
    throw const FormatException('Invalid admin binding audit response');
  }
  return value;
}

String? _optionalString(Map<String, dynamic> json, String key) {
  final value = json[key];
  if (value == null) return null;
  if (value is! String) throw const FormatException('Invalid audit nullable field');
  return value;
}
