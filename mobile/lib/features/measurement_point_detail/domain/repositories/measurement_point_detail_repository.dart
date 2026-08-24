import '../models/measurement_point_detail.dart';

abstract interface class MeasurementPointDetailRepository {
  Future<MeasurementPointDetail> fetchMeasurementPointDetail(
      String shopId, String measurementPointRef);
}
