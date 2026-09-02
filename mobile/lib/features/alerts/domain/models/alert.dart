class AlertSettings {
  const AlertSettings({required this.measurementPointId, required this.isEnabled, required this.quietHoursStart, required this.quietHoursEnd, required this.powerThresholdW, this.updatedAt});
  final String measurementPointId;
  final bool isEnabled;
  final String quietHoursStart;
  final String quietHoursEnd;
  final double powerThresholdW;
  final DateTime? updatedAt;
  factory AlertSettings.fromJson(Map<String, dynamic> json) => AlertSettings(
    measurementPointId: json['measurementPointId'] as String,
    isEnabled: json['isEnabled'] as bool? ?? true,
    quietHoursStart: json['quietHoursStart'] as String? ?? '',
    quietHoursEnd: json['quietHoursEnd'] as String? ?? '',
    powerThresholdW: (json['powerThresholdW'] as num?)?.toDouble() ?? 10,
    updatedAt: json['updatedAt'] == null ? null : DateTime.parse(json['updatedAt'] as String),
  );
}

class AlertRecord {
  const AlertRecord({required this.id, required this.measurementPointId, required this.measurementPointName, required this.type, required this.message, required this.deviceId, required this.deviceName, this.serialNumber, required this.voltage, required this.current, required this.power, required this.createdAt});
  final String id;
  final String measurementPointId;
  final String measurementPointName;
  final String type;
  final String message;
  final int deviceId;
  final String deviceName;
  final String? serialNumber;
  final double voltage;
  final double current;
  final double power;
  final DateTime createdAt;
  factory AlertRecord.fromJson(Map<String, dynamic> json) {
    final point = json['measurementPoint'] as Map<String, dynamic>? ?? const {};
    final device = json['device'] as Map<String, dynamic>? ?? const {};
    final snapshot = json['snapshot'] as Map<String, dynamic>? ?? const {};
    return AlertRecord(
      id: json['id'].toString(), measurementPointId: point['id'] as String? ?? '', measurementPointName: point['name'] as String? ?? '',
      type: json['type'] as String? ?? '', message: json['message'] as String? ?? '',
      deviceId: (device['deviceId'] as num?)?.toInt() ?? 0, deviceName: device['name'] as String? ?? '', serialNumber: device['serialNumber'] as String?,
      voltage: (snapshot['voltage'] as num?)?.toDouble() ?? 0, current: (snapshot['current'] as num?)?.toDouble() ?? 0, power: (snapshot['power'] as num?)?.toDouble() ?? 0,
      createdAt: DateTime.parse(json['createdAt'] as String),
    );
  }
}

class AlertHistoryPage {
  const AlertHistoryPage({required this.items, this.nextCursor});
  final List<AlertRecord> items;
  final String? nextCursor;
  factory AlertHistoryPage.fromJson(Map<String, dynamic> json) => AlertHistoryPage(
    items: ((json['items'] as List<dynamic>? ?? const [])).map((e) => AlertRecord.fromJson(e as Map<String, dynamic>)).toList(growable: false),
    nextCursor: json['nextCursor'] as String?,
  );
}
