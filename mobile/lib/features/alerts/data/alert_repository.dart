import 'package:dio/dio.dart';
import 'package:power_iot_app/core/network/authenticated_http_client.dart';
import 'package:power_iot_app/features/alerts/domain/models/alert.dart';

class AlertRepository {
  const AlertRepository(this.client);
  final AuthenticatedHttpClient client;

  Future<AlertHistoryPage> fetchHistory(String shopId, {String? measurementPointRef, String? cursor, int limit = 50}) async {
    final response = await client.dio.get<Object?>('/api/v1/shops/${Uri.encodeComponent(shopId)}/alerts', queryParameters: <String, dynamic>{
      'limit': limit,
      if (measurementPointRef != null && measurementPointRef.isNotEmpty) 'measurementPointRef': measurementPointRef,
      if (cursor != null) 'cursor': cursor,
    });
    return AlertHistoryPage.fromJson(response.data as Map<String, dynamic>);
  }

  Future<AlertSettings> fetchSettings(String shopId, String measurementPointRef) async {
    final response = await client.dio.get<Object?>('/api/v1/shops/${Uri.encodeComponent(shopId)}/measurement-points/${Uri.encodeComponent(measurementPointRef)}/alert-settings');
    return AlertSettings.fromJson(response.data as Map<String, dynamic>);
  }

  Future<AlertSettings> updateSettings(String shopId, String measurementPointRef, AlertSettings settings) async {
    final response = await client.dio.put<Object?>('/api/v1/shops/${Uri.encodeComponent(shopId)}/measurement-points/${Uri.encodeComponent(measurementPointRef)}/alert-settings', data: <String, dynamic>{
      'isEnabled': settings.isEnabled,
      'quietHoursStart': settings.quietHoursStart,
      'quietHoursEnd': settings.quietHoursEnd,
      'powerThresholdW': settings.powerThresholdW,
    });
    return AlertSettings.fromJson(response.data as Map<String, dynamic>);
  }

  static bool isUnauthorized(Object error) => error is DioException && error.response?.statusCode == 401;
}
