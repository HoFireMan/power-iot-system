import 'package:dio/dio.dart';
import 'package:power_iot_app/core/network/authenticated_http_client.dart';
import 'package:power_iot_app/features/measurement_point_detail/domain/models/measurement_point_detail.dart';
import 'package:power_iot_app/features/measurement_point_detail/domain/repositories/measurement_point_detail_repository.dart';
import '../dtos/measurement_point_detail_dto.dart';

class MeasurementPointNotFoundException implements Exception {
  const MeasurementPointNotFoundException();
}

class RemoteMeasurementPointDetailRepository
    implements MeasurementPointDetailRepository {
  const RemoteMeasurementPointDetailRepository(this.client);
  final AuthenticatedHttpClient client;

  @override
  Future<MeasurementPointDetail> fetchMeasurementPointDetail(
    String shopId,
    String measurementPointRef,
  ) async {
    final normalizedShop = shopId.trim();
    final normalizedRef = measurementPointRef.trim();
    if (normalizedShop.isEmpty) throw ArgumentError.value(shopId, 'shopId');
    if (normalizedRef.isEmpty) {
      throw ArgumentError.value(measurementPointRef, 'measurementPointRef');
    }
    try {
      final response = await client.dio.get<Object?>(
        '/api/v1/shops/${Uri.encodeComponent(normalizedShop)}/measurement-points/${Uri.encodeComponent(normalizedRef)}',
      );
      return MeasurementPointDetailDto.fromJson(response.data).model;
    } on DioException catch (error) {
      if (error.response?.statusCode == 404 &&
          error.response?.data is Map &&
          error.response?.data['code'] == 'MEASUREMENT_POINT_NOT_FOUND') {
        throw const MeasurementPointNotFoundException();
      }
      rethrow;
    }
  }
}
