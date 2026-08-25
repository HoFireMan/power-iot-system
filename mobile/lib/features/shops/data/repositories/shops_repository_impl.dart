import 'package:power_iot_app/core/network/authenticated_http_client.dart';
import 'package:power_iot_app/features/shops/domain/models/shop.dart';
import 'package:power_iot_app/features/shops/domain/repositories/shops_repository.dart';

class RemoteShopsRepository implements ShopsRepository, ShopTariffMutation {
  const RemoteShopsRepository(this.client);

  final AuthenticatedHttpClient client;

  @override
  Future<ShopsSnapshot> fetchShops() async {
    final response = await client.dio.get<Object?>('/api/v1/shops');
    return ShopsSnapshot.fromJson(response.data);
  }

  @override
  Future<void> updateTariff(String shopId, String tariff) async {
    await client.dio.patch<Object?>(
      '/api/v1/shops/$shopId',
      data: {'tariff': tariff},
    );
  }
}
