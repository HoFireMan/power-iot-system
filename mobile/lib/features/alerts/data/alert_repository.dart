import 'package:dio/dio.dart';
import 'package:power_iot_app/core/network/authenticated_http_client.dart';
import 'package:power_iot_app/features/alerts/domain/models/alert.dart';

class AlertRepository {
  const AlertRepository(this.client);
  final AuthenticatedHttpClient client;
  Future<AlertHistoryPage> fetchHistory(
    String shopId, {
    String? cursor,
    int limit = 20,
  }) async {
    final response = await client.dio.get<Object?>(
      '/api/v1/shops/${Uri.encodeComponent(shopId)}/alerts',
      queryParameters: <String, dynamic>{
        'limit': limit,
        if (cursor != null) 'cursor': cursor,
      },
    );
    return AlertHistoryPage.fromJson(response.data as Map<String, dynamic>);
  }

  Future<AlertSettings> fetchSettings(String measurementPointId) async {
    final response = await client.dio.get<Object?>(
      '/api/v1/admin/measurement-points/${Uri.encodeComponent(measurementPointId)}/alert-settings',
    );
    return AlertSettings.fromJson(response.data as Map<String, dynamic>);
  }

  Future<AlertSettings> updateSettings(
    String measurementPointId,
    AlertSettings settings,
  ) async {
    final response = await client.dio.put<Object?>(
      '/api/v1/admin/measurement-points/${Uri.encodeComponent(measurementPointId)}/alert-settings',
      data: <String, dynamic>{
        'dailyLimitKwh': settings.dailyLimitKwh,
        'monthlyLimitKwh': settings.monthlyLimitKwh,
        'nonUsageStartTime': settings.nonUsageStartTime,
        'nonUsageEndTime': settings.nonUsageEndTime,
        'isEnabled': settings.isEnabled,
      },
    );
    return AlertSettings.fromJson(response.data as Map<String, dynamic>);
  }

  static bool isUnauthorized(Object error) =>
      error is DioException && error.response?.statusCode == 401;
}
