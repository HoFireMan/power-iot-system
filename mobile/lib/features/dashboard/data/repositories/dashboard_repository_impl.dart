import 'package:dio/dio.dart';
import 'package:power_iot_app/core/network/authenticated_http_client.dart';
import 'package:power_iot_app/features/dashboard/data/dtos/dashboard_dto.dart';
import 'package:power_iot_app/features/dashboard/domain/models/dashboard.dart';
import 'package:power_iot_app/features/dashboard/domain/repositories/dashboard_repository.dart';

class DashboardShopNotFoundException implements Exception {
  const DashboardShopNotFoundException();
}

class RemoteDashboardRepository implements DashboardRepository {
  const RemoteDashboardRepository(this.client);

  final AuthenticatedHttpClient client;

  @override
  Future<Dashboard> fetchDashboard(String shopId) async {
    final normalizedShopId = shopId.trim();
    if (normalizedShopId.isEmpty) {
      throw ArgumentError.value(shopId, 'shopId');
    }

    try {
      final response = await client.dio.get<Object?>(
        '/api/v1/shops/${Uri.encodeComponent(normalizedShopId)}/dashboard',
      );
      return DashboardDto.fromJson(response.data).toModel();
    } on DioException catch (error) {
      final body = error.response?.data;
      if (error.response?.statusCode == 404 &&
          body is Map &&
          body['code'] == 'SHOP_NOT_FOUND') {
        throw const DashboardShopNotFoundException();
      }
      rethrow;
    }
  }
}
