/// Presentation data for a historical half-open Device-to-Measurement Point
/// relationship: [validFrom, validTo).
class DeviceAssignment {
  const DeviceAssignment({
    required this.id,
    required this.deviceId,
    required this.measurementPointId,
    required this.validFrom,
    this.validTo,
  });

  final String id;
  final String deviceId;
  final String measurementPointId;
  final DateTime validFrom;
  final DateTime? validTo;

  bool get active => validTo == null;
}
