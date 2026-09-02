class AlertSettings {
  const AlertSettings({
    required this.measurementPointId,
    this.dailyLimitKwh,
    this.monthlyLimitKwh,
    required this.nonUsageStartTime,
    required this.nonUsageEndTime,
    required this.isEnabled,
    this.updatedAt,
  });
  final String measurementPointId;
  final double? dailyLimitKwh;
  final double? monthlyLimitKwh;
  final String nonUsageStartTime;
  final String nonUsageEndTime;
  final bool isEnabled;
  final DateTime? updatedAt;
  factory AlertSettings.fromJson(Map<String, dynamic> json) => AlertSettings(
        measurementPointId: json['measurementPointId'] as String,
        dailyLimitKwh: (json['dailyLimitKwh'] as num?)?.toDouble(),
        monthlyLimitKwh: (json['monthlyLimitKwh'] as num?)?.toDouble(),
        nonUsageStartTime: json['nonUsageStartTime'] as String? ?? '',
        nonUsageEndTime: json['nonUsageEndTime'] as String? ?? '',
        isEnabled: json['isEnabled'] as bool? ?? false,
        updatedAt: json['updatedAt'] == null
            ? null
            : DateTime.parse(json['updatedAt'] as String),
      );
}

class AlertRecord {
  const AlertRecord({
    required this.id,
    required this.measurementPointId,
    required this.measurementPointName,
    required this.type,
    required this.message,
    required this.power,
    required this.isRead,
    required this.recordedAt,
  });
  final String id;
  final String measurementPointId;
  final String measurementPointName;
  final String type;
  final String message;
  final double power;
  final bool isRead;
  final DateTime recordedAt;
  factory AlertRecord.fromJson(Map<String, dynamic> json) => AlertRecord(
        id: json['id'].toString(),
        measurementPointId: json['measurementPointId'] as String,
        measurementPointName: json['measurementPointName'] as String? ?? '',
        type: json['type'] as String? ?? '',
        message: json['message'] as String? ?? '',
        power: (json['power'] as num?)?.toDouble() ?? 0,
        isRead: json['isRead'] as bool? ?? false,
        recordedAt: DateTime.parse(json['recordedAt'] as String),
      );
}

class AlertHistoryPage {
  const AlertHistoryPage({required this.items, this.nextCursor});
  final List<AlertRecord> items;
  final String? nextCursor;
  factory AlertHistoryPage.fromJson(Map<String, dynamic> json) =>
      AlertHistoryPage(
        items: ((json['items'] as List<dynamic>? ?? const []))
            .map((e) => AlertRecord.fromJson(e as Map<String, dynamic>))
            .toList(growable: false),
        nextCursor: json['nextCursor'] as String?,
      );
}
