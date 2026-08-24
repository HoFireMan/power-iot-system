import 'package:power_iot_app/features/dashboard/domain/models/dashboard.dart';

/// Strict decoder for the exact B7 dashboard response. Unknown or missing
/// fields are rejected so a changed backend contract cannot become fabricated
/// UI data.
class DashboardDto {
  const DashboardDto({
    required this.shop,
    required this.generatedAt,
    required this.currentPowerW,
    required this.dailyKwh,
    required this.monthlyKwh,
    required this.dailyKg,
    required this.monthlyKg,
    required this.devices,
  });

  final DashboardShop shop;
  final DateTime generatedAt;
  final double? currentPowerW;
  final double? dailyKwh;
  final double? monthlyKwh;
  final double? dailyKg;
  final double? monthlyKg;
  final List<DashboardDevice> devices;

  Dashboard toModel() => Dashboard(
        shop: shop,
        generatedAt: generatedAt,
        currentPowerW: currentPowerW,
        dailyKwh: dailyKwh,
        monthlyKwh: monthlyKwh,
        dailyKg: dailyKg,
        monthlyKg: monthlyKg,
        devices: devices,
      );

  factory DashboardDto.fromJson(Object? value) {
    final map = _map(value, 'dashboard');
    _exactKeys(
        map,
        const {
          'shop',
          'generatedAt',
          'currentPowerW',
          'dailyKwh',
          'monthlyKwh',
          'dailyKg',
          'monthlyKg',
          'devices',
        },
        'dashboard');

    final rawDevices = map['devices'];
    if (rawDevices is! List) throw const FormatException('Invalid devices');

    return DashboardDto(
      shop: _shop(map['shop']),
      generatedAt: _date(map['generatedAt'], 'generatedAt'),
      currentPowerW: _number(map['currentPowerW'], 'currentPowerW'),
      dailyKwh: _number(map['dailyKwh'], 'dailyKwh'),
      monthlyKwh: _number(map['monthlyKwh'], 'monthlyKwh'),
      dailyKg: _number(map['dailyKg'], 'dailyKg'),
      monthlyKg: _number(map['monthlyKg'], 'monthlyKg'),
      devices: List<DashboardDevice>.unmodifiable(
        rawDevices.map(_device),
      ),
    );
  }

  static DashboardShop _shop(Object? value) {
    final map = _map(value, 'shop');
    _exactKeys(map, const {'id', 'code', 'name'}, 'shop');
    return DashboardShop(
      id: _requiredString(map['id'], 'shop.id'),
      code: _requiredString(map['code'], 'shop.code'),
      name: _requiredString(map['name'], 'shop.name'),
    );
  }

  static DashboardDevice _device(Object? value) {
    final map = _map(value, 'device');
    _exactKeys(
        map,
        const {
          'measurementPointRef',
          'name',
          'isOnline',
          'lastSeen',
        },
        'device');
    final online = map['isOnline'];
    if (online is! bool) throw const FormatException('Invalid device.isOnline');
    return DashboardDevice(
      measurementPointRef: _requiredString(
          map['measurementPointRef'], 'device.measurementPointRef'),
      name: _requiredString(map['name'], 'device.name'),
      isOnline: online,
      lastSeen: _nullableDate(map['lastSeen'], 'device.lastSeen'),
    );
  }

  static Map<String, Object?> _map(Object? value, String name) {
    if (value is! Map || value.keys.any((key) => key is! String)) {
      throw FormatException('Invalid $name');
    }
    return value.map((key, value) => MapEntry(key as String, value));
  }

  static void _exactKeys(
      Map<String, Object?> map, Set<String> keys, String name) {
    if (map.length != keys.length || !map.keys.every(keys.contains)) {
      throw FormatException('Invalid $name');
    }
  }

  static String _requiredString(Object? value, String name) {
    if (value is! String || value.isEmpty) {
      throw FormatException('Invalid $name');
    }
    return value;
  }

  static double? _number(Object? value, String name) {
    if (value == null) return null;
    if (value is! num || !value.toDouble().isFinite) {
      throw FormatException('Invalid $name');
    }
    return value.toDouble();
  }

  static DateTime _date(Object? value, String name) {
    final result = _nullableDate(value, name);
    if (result == null) throw FormatException('Invalid $name');
    return result;
  }

  static DateTime? _nullableDate(Object? value, String name) {
    if (value == null) return null;
    if (value is! String) throw FormatException('Invalid $name');
    final result = DateTime.tryParse(value);
    if (result == null) throw FormatException('Invalid $name');
    return result;
  }
}
