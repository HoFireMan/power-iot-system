import '../models/shop.dart';

abstract interface class ShopsRepository {
  Future<ShopsSnapshot> fetchShops();
}
