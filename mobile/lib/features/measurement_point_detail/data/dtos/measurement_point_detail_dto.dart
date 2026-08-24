import '../../domain/models/measurement_point_detail.dart';

class MeasurementPointDetailDto {
  const MeasurementPointDetailDto(this.model);
  final MeasurementPointDetail model;

  factory MeasurementPointDetailDto.fromJson(Object? value) {
    final map = _map(value, 'measurement point detail');
    if (!map.containsKey('measurementPoint')) return _legacy(map);
    final allowed = <String>{
      'shop',
      'measurementPoint',
      'currentPower',
      'todayEnergy',
      'monthEnergy',
      'currentDevice',
      'assignmentHistory',
      'technicalInfo',
    };
    if (!map.keys.every(allowed.contains) ||
        !map.keys.toSet().containsAll(allowed.difference({'technicalInfo'}))) {
      throw const FormatException('Invalid measurement point detail');
    }
    final history = map['assignmentHistory'];
    if (history is! List) {
      throw const FormatException('Invalid assignmentHistory');
    }
    final technical = map['technicalInfo'];
    return MeasurementPointDetailDto(
      MeasurementPointDetail(
        shop: _shop(map['shop']),
        measurementPoint: _point(map['measurementPoint']),
        currentPower: _power(map['currentPower']),
        todayEnergy: _energy(map['todayEnergy'], 'todayEnergy'),
        monthEnergy: _energy(map['monthEnergy'], 'monthEnergy'),
        currentDevice: _nullableDevice(map['currentDevice']),
        assignmentHistory: List.unmodifiable(history.map(_assignment)),
        technicalInfo: technical == null ? null : _technical(technical),
      ),
    );
  }

  static MeasurementPointDetailDto _legacy(Map<String, Object?> map) {
    _exact(
        map,
        const {
          'id',
          'name',
          'shop',
          'devices',
          'currentPowerW',
          'today',
          'month',
          'generatedAt'
        },
        'legacy detail');
    final devices = map['devices'];
    if (devices is! List || devices.isNotEmpty) {
      throw const FormatException('Invalid legacy devices');
    }
    final shop = _map(map['shop'], 'shop');
    if (shop.length != 3 ||
        !shop.keys.toSet().containsAll(const {'id', 'code', 'name'})) {
      throw const FormatException('Invalid legacy shop');
    }
    return MeasurementPointDetailDto(MeasurementPointDetail(
      shop: MeasurementPointDetailShop(
          code: _string(shop['code'], 'shop.code'),
          name: _string(shop['name'], 'shop.name')),
      measurementPoint: MeasurementPointIdentity(
          name: _string(map['name'], 'name'), status: 'unbound'),
      currentPower: MeasurementPointCurrentPower(
          watts: _number(map['currentPowerW'], 'currentPowerW'),
          lastUpdatedAt: null),
      todayEnergy: _legacyEnergy(map['today'], 'today'),
      monthEnergy: _legacyEnergy(map['month'], 'month'),
      currentDevice: null,
      assignmentHistory: const [],
      technicalInfo: null,
    ));
  }

  static MeasurementPointEnergyWindow _legacyEnergy(
      Object? value, String name) {
    final map = _map(value, name);
    _exact(map, const {'kwh', 'throughAt', 'state'}, name);
    return MeasurementPointEnergyWindow(
        kwh: _number(map['kwh'], '$name.kwh'),
        completeThrough: _dateOrNull(map['throughAt'], '$name.throughAt'),
        state: _string(map['state'], '$name.state'));
  }

  static MeasurementPointDetailShop _shop(Object? value) {
    final map = _map(value, 'shop');
    _exact(map, const {'code', 'name'}, 'shop');
    return MeasurementPointDetailShop(
        code: _string(map['code'], 'shop.code'),
        name: _string(map['name'], 'shop.name'));
  }

  static MeasurementPointIdentity _point(Object? value) {
    final map = _map(value, 'measurementPoint');
    _exact(map, const {'name', 'status'}, 'measurementPoint');
    final status = _string(map['status'], 'measurementPoint.status');
    if (!const {'online', 'offline', 'unbound'}.contains(status)) {
      throw const FormatException('Invalid status');
    }
    return MeasurementPointIdentity(
        name: _string(map['name'], 'measurementPoint.name'), status: status);
  }

  static MeasurementPointCurrentPower _power(Object? value) {
    final map = _map(value, 'currentPower');
    _exact(map, const {'watts', 'lastUpdatedAt'}, 'currentPower');
    return MeasurementPointCurrentPower(
        watts: _number(map['watts'], 'currentPower.watts'),
        lastUpdatedAt:
            _dateOrNull(map['lastUpdatedAt'], 'currentPower.lastUpdatedAt'));
  }

  static MeasurementPointDetailDevice? _nullableDevice(Object? value) {
    if (value == null) return null;
    final map = _map(value, 'currentDevice');
    _exact(map, const {'displayName', 'mac', 'lastSeen'}, 'currentDevice');
    return MeasurementPointDetailDevice(
        displayName: _string(map['displayName'], 'currentDevice.displayName'),
        mac: _string(map['mac'], 'currentDevice.mac'),
        lastSeen: _dateOrNull(map['lastSeen'], 'currentDevice.lastSeen'));
  }

  static MeasurementPointAssignment _assignment(Object? value) {
    final map = _map(value, 'assignment');
    _exact(map, const {'displayName', 'mac', 'validFrom', 'validTo'},
        'assignment');
    final from = _dateOrNull(map['validFrom'], 'assignment.validFrom');
    if (from == null) {
      throw const FormatException('Invalid assignment.validFrom');
    }
    return MeasurementPointAssignment(
        displayName: _string(map['displayName'], 'assignment.displayName'),
        mac: _string(map['mac'], 'assignment.mac'),
        validFrom: from,
        validTo: _dateOrNull(map['validTo'], 'assignment.validTo'));
  }

  static MeasurementPointTechnicalInfo _technical(Object? value) {
    final map = _map(value, 'technicalInfo');
    _exact(map, const {'measurementPointId', 'deviceId'}, 'technicalInfo');
    return MeasurementPointTechnicalInfo(
        measurementPointId: _string(
            map['measurementPointId'], 'technicalInfo.measurementPointId'),
        deviceId: map['deviceId'] == null
            ? null
            : _string(map['deviceId'], 'technicalInfo.deviceId'));
  }

  static MeasurementPointEnergyWindow _energy(Object? value, String name) {
    final map = _map(value, name);
    _exact(map, const {'kwh', 'completeThrough'}, name);
    return MeasurementPointEnergyWindow(
        kwh: _number(map['kwh'], '$name.kwh'),
        completeThrough:
            _dateOrNull(map['completeThrough'], '$name.completeThrough'));
  }

  static Map<String, Object?> _map(Object? value, String name) {
    if (value is! Map || value.keys.any((key) => key is! String)) {
      throw FormatException('Invalid $name');
    }
    return value.map((key, value) => MapEntry(key as String, value));
  }

  static void _exact(Map<String, Object?> map, Set<String> keys, String name) {
    if (map.length != keys.length || !map.keys.every(keys.contains)) {
      throw FormatException('Invalid $name');
    }
  }

  static String _string(Object? value, String name) {
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

  static DateTime? _dateOrNull(Object? value, String name) {
    if (value == null) return null;
    if (value is! String) throw FormatException('Invalid $name');
    final date = DateTime.tryParse(value);
    if (date == null) throw FormatException('Invalid $name');
    return date;
  }
}
