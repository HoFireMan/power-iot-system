/// Presentation data for an active Device-to-Measurement Point relationship.
class DeviceAssignment {
  const DeviceAssignment({
    required this.id,
    required this.deviceId,
    required this.measurementPointId,
    this.active = true,
  });

  final String id;
  final String deviceId;
  final String measurementPointId;
  final bool active;
}
