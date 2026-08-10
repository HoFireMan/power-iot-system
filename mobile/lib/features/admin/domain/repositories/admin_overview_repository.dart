import '../models/admin_overview.dart';
import '../models/measurement_point.dart';

/// Product input for creating a logical Measurement Point in the current Site.
class CreateMeasurementPointInput {
  const CreateMeasurementPointInput({
    required this.requestIdentity,
    required this.shopId,
    required this.name,
  });

  final String requestIdentity;
  final String shopId;
  final String name;
}

/// Product-data boundary for the admin overview.
abstract interface class AdminOverviewRepository {
  Future<AdminOverview> loadOverview();

  Future<MeasurementPoint> createMeasurementPoint(
    CreateMeasurementPointInput input,
  );
}
