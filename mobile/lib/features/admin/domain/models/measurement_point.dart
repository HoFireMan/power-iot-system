/// Product data for a logical Measurement Point in the admin overview.
///
/// Shop is the current compatibility implementation of the Site context.
class MeasurementPoint {
  const MeasurementPoint({
    required this.id,
    required this.shopId,
    required this.name,
  });

  final String id;
  final String shopId;
  final String name;
}
