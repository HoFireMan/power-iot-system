import '../models/shop.dart';

abstract class ShopsRepository {
  Future<ShopsSnapshot> fetchShops();
}

abstract interface class ShopTariffMutation {
  Future<void> updateTariff(String shopId, String tariff);
}
