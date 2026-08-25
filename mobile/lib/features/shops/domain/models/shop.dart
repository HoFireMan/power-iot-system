class Shop {
  const Shop({
    required this.id,
    required this.code,
    required this.name,
    required this.address,
    required this.phone,
    required this.isHead,
    this.tariff,
  });

  final String id;
  final String code;
  final String name;
  final String? address;
  final String? phone;
  final bool isHead;
  final String? tariff;

  factory Shop.fromJson(Object? value) {
    if (value is! Map || value.keys.any((key) => key is! String)) {
      throw const FormatException('Invalid shop response');
    }
    final map = value.map((key, value) => MapEntry(key as String, value));
    const requiredKeys = {'id', 'code', 'name', 'address', 'phone', 'isHead'};
    const allowedKeys = {...requiredKeys, 'tariff'};
    if (!requiredKeys.every(map.containsKey) ||
        !map.keys.every(allowedKeys.contains)) {
      throw const FormatException('Invalid shop response');
    }
    return Shop(
      id: _shopString(map['id'], 'id'),
      code: _shopString(map['code'], 'code'),
      name: _shopString(map['name'], 'name'),
      address: _shopNullableString(map['address'], 'address'),
      phone: _shopNullableString(map['phone'], 'phone'),
      isHead: map['isHead'] is bool
          ? map['isHead'] as bool
          : (throw const FormatException('Invalid isHead')),
      tariff: _shopNullableString(map['tariff'], 'tariff'),
    );
  }
}

String _shopString(Object? value, String name) {
  if (value is! String || value.isEmpty) throw FormatException('Invalid $name');
  return value;
}

String? _shopNullableString(Object? value, String name) {
  if (value == null) return null;
  if (value is! String) throw FormatException('Invalid $name');
  return value;
}

class ShopsSnapshot {
  const ShopsSnapshot({required this.shops, required this.currentShopId});

  final List<Shop> shops;
  final String? currentShopId;

  factory ShopsSnapshot.fromJson(Object? value) {
    if (value is! Map || value.keys.any((key) => key is! String)) {
      throw const FormatException('Invalid shops response');
    }
    final map = value.map((key, value) => MapEntry(key as String, value));
    const keys = {'shops', 'currentShopId'};
    if (map.length != keys.length || !map.keys.every(keys.contains)) {
      throw const FormatException('Invalid shops response');
    }
    final rawShops = map['shops'];
    if (rawShops is! List) throw const FormatException('Invalid shops');
    return ShopsSnapshot(
      shops: List<Shop>.unmodifiable(rawShops.map(Shop.fromJson)),
      currentShopId: _shopNullableString(map['currentShopId'], 'currentShopId'),
    );
  }
}
