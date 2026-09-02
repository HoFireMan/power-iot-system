import 'dart:convert';

import 'package:shared_preferences/shared_preferences.dart';
import 'package:power_iot_app/features/dashboard/data/dtos/dashboard_dto.dart';
import 'package:power_iot_app/features/dashboard/domain/models/dashboard.dart';

const dashboardCacheVersion = 1;

/// A durable Dashboard snapshot together with the time its authoritative
/// response was committed to local storage.
final class DashboardCacheSnapshot {
  const DashboardCacheSnapshot({
    required this.dashboard,
    required this.cachedAt,
  });

  final Dashboard dashboard;
  final DateTime cachedAt;
}

/// Dashboard-specific persistence boundary. It intentionally does not expose
/// arbitrary key/value or token storage operations.
abstract interface class DashboardCache {
  Future<DashboardCacheSnapshot?> read(String userId, String shopId);

  Future<void> write(
    String userId,
    String shopId,
    Dashboard dashboard, {
    bool Function()? isCurrent,
  });

  Future<void> delete(String userId, String shopId);
}

/// No-op fallback used by direct notifier consumers that do not opt into
/// durable caching, including existing non-cache tests.
final class NoopDashboardCache implements DashboardCache {
  const NoopDashboardCache();

  @override
  Future<DashboardCacheSnapshot?> read(String userId, String shopId) async =>
      null;

  @override
  Future<void> write(
    String userId,
    String shopId,
    Dashboard dashboard, {
    bool Function()? isCurrent,
  }) async {}

  @override
  Future<void> delete(String userId, String shopId) async {}
}

/// SharedPreferences adapter for the small, non-secret Dashboard projection.
/// Raw storage keys and JSON are kept inside this adapter rather than in the
/// Dashboard provider.
final class SharedPreferencesDashboardCache implements DashboardCache {
  SharedPreferencesDashboardCache(
    this._preferences, {
    DateTime Function()? clock,
  }) : _clock = clock ?? DateTime.now;

  final Future<SharedPreferences> _preferences;
  final DateTime Function() _clock;

  @override
  Future<DashboardCacheSnapshot?> read(String userId, String shopId) async {
    final preferences = await _preferences;
    final raw = preferences.getString(_key(userId, shopId));
    if (raw == null) return null;
    try {
      final envelope = _decode(raw);
      if (envelope.userId != userId || envelope.shopId != shopId) return null;
      return DashboardCacheSnapshot(
        dashboard: envelope.dashboard,
        cachedAt: envelope.cachedAt,
      );
    } on FormatException {
      // Corrupt, incompatible, or unexpected local data fails closed. Remove
      // it best-effort so a permanently bad entry is not reparsed forever.
      try {
        await preferences.remove(_key(userId, shopId));
      } catch (_) {}
      return null;
    } on Object {
      // A platform storage failure is also a cache miss. The authoritative
      // network path must remain unaffected by local persistence.
      return null;
    }
  }

  @override
  Future<void> write(
    String userId,
    String shopId,
    Dashboard dashboard, {
    bool Function()? isCurrent,
  }) async {
    if (userId.isEmpty || shopId.isEmpty || dashboard.shop.id != shopId) {
      throw const FormatException('Dashboard cache identity mismatch');
    }
    final preferences = await _preferences;
    if (isCurrent != null && !isCurrent()) return;
    final envelope = <String, Object?>{
      'version': dashboardCacheVersion,
      'userId': userId,
      'shopId': shopId,
      'cachedAt': _clock().toUtc().toIso8601String(),
      'dashboard': _dashboardJson(dashboard),
    };
    await preferences.setString(_key(userId, shopId), jsonEncode(envelope));
  }

  @override
  Future<void> delete(String userId, String shopId) async {
    final preferences = await _preferences;
    await preferences.remove(_key(userId, shopId));
  }

  static String _key(String userId, String shopId) {
    final encodedUser = base64Url.encode(utf8.encode(userId));
    final encodedShop = base64Url.encode(utf8.encode(shopId));
    return 'power_iot.dashboard_cache.v1.$encodedUser.$encodedShop';
  }

  static Map<String, Object?> _dashboardJson(Dashboard dashboard) => {
        'shop': {
          'id': dashboard.shop.id,
          'code': dashboard.shop.code,
          'name': dashboard.shop.name,
        },
        'generatedAt': dashboard.generatedAt.toUtc().toIso8601String(),
        'currentPowerW': dashboard.currentPowerW,
        'dailyKwh': dashboard.dailyKwh,
        'monthlyKwh': dashboard.monthlyKwh,
        'dailyKg': dashboard.dailyKg,
        'monthlyKg': dashboard.monthlyKg,
        'devices': dashboard.devices
            .map(
              (device) => <String, Object?>{
                'measurementPointRef': device.measurementPointRef,
                'name': device.name,
                'isOnline': device.isOnline,
                'lastSeen': device.lastSeen?.toUtc().toIso8601String(),
              },
            )
            .toList(growable: false),
      };

  static _DecodedEnvelope _decode(String raw) {
    final root = _map(jsonDecode(raw), 'cache envelope');
    _exactKeys(
        root,
        const {
          'version',
          'userId',
          'shopId',
          'cachedAt',
          'dashboard',
        },
        'cache envelope');
    if (root['version'] is! int || root['version'] != dashboardCacheVersion) {
      throw const FormatException('Unsupported cache version');
    }
    final userId = _requiredString(root['userId'], 'userId');
    final shopId = _requiredString(root['shopId'], 'shopId');
    final cachedAt = _instant(root['cachedAt'], 'cachedAt');
    final dashboardValue = root['dashboard'];
    _validateDashboardDates(dashboardValue);
    final dashboard = DashboardDto.fromJson(dashboardValue).toModel();
    if (dashboard.shop.id != shopId) {
      throw const FormatException('Dashboard Shop does not match cache Shop');
    }
    return _DecodedEnvelope(
      userId: userId,
      shopId: shopId,
      cachedAt: cachedAt,
      dashboard: dashboard,
    );
  }

  static void _validateDashboardDates(Object? value) {
    final dashboard = _map(value, 'dashboard');
    _instant(dashboard['generatedAt'], 'generatedAt');
    final devices = dashboard['devices'];
    if (devices is! List) throw const FormatException('Invalid devices');
    for (final value in devices) {
      final device = _map(value, 'device');
      final lastSeen = device['lastSeen'];
      if (lastSeen != null) _instant(lastSeen, 'lastSeen');
    }
  }

  static DateTime _instant(Object? value, String name) {
    if (value is! String || !_rfc3339.hasMatch(value)) {
      throw FormatException('Invalid $name');
    }
    final match = _rfc3339.firstMatch(value);
    final year = int.parse(match!.group(1)!);
    final month = int.parse(match.group(2)!);
    final day = int.parse(match.group(3)!);
    final calendar = DateTime.utc(year, month, day);
    if (calendar.year != year ||
        calendar.month != month ||
        calendar.day != day) {
      throw FormatException('Invalid $name');
    }
    final parsed = DateTime.tryParse(value);
    if (parsed == null) throw FormatException('Invalid $name');
    return parsed;
  }

  static Map<String, Object?> _map(Object? value, String name) {
    if (value is! Map || value.keys.any((key) => key is! String)) {
      throw FormatException('Invalid $name');
    }
    return value.map((key, value) => MapEntry(key as String, value));
  }

  static void _exactKeys(
    Map<String, Object?> map,
    Set<String> keys,
    String name,
  ) {
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

  static final _rfc3339 = RegExp(
    r'^(\d{4})-(\d{2})-(\d{2})T(?:[01]\d|2[0-3]):(?:[0-5]\d):(?:[0-5]\d)(?:\.\d+)?(?:Z|[+-](?:[01]\d|2[0-3]):[0-5]\d)$',
  );
}

final class _DecodedEnvelope {
  const _DecodedEnvelope({
    required this.userId,
    required this.shopId,
    required this.cachedAt,
    required this.dashboard,
  });

  final String userId;
  final String shopId;
  final DateTime cachedAt;
  final Dashboard dashboard;
}
