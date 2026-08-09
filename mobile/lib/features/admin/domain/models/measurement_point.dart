/// Presentation data for a measurement point in the admin overview.
///
/// This model intentionally contains no ownership or authorization context.
class MeasurementPoint {
  const MeasurementPoint({
    required this.name,
    this.id,
  });

  final String name;
  final String? id;
}
