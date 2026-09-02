import 'dart:async';
import 'dart:convert';
import 'dart:typed_data';

import 'package:dio/dio.dart';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:power_iot_app/core/network/remote_error.dart';
import 'package:power_iot_app/features/auth/auth_controller.dart';
import 'package:power_iot_app/features/dashboard/dashboard_screen.dart';
import 'package:power_iot_app/features/dashboard/data/cache/dashboard_cache.dart';
import 'package:power_iot_app/features/dashboard/domain/models/dashboard.dart';
import 'package:power_iot_app/features/dashboard/data/repositories/dashboard_repository_impl.dart';
import 'package:power_iot_app/features/dashboard/domain/repositories/dashboard_repository.dart';
import 'package:power_iot_app/features/dashboard/presentation/providers/dashboard_provider.dart';
import 'package:power_iot_app/features/profile/domain/models/user_profile.dart';
import 'package:power_iot_app/features/profile/domain/repositories/profile_repository.dart';
import 'package:power_iot_app/features/profile/presentation/providers/profile_provider.dart';
import 'package:power_iot_app/features/shops/domain/models/shop.dart';
import 'package:power_iot_app/features/shops/domain/repositories/shops_repository.dart';
import 'package:power_iot_app/features/shops/providers/remote_shop_provider.dart';
import 'package:power_iot_app/core/network/authenticated_http_client.dart';
import 'package:shared_preferences/shared_preferences.dart';

class _Store implements RefreshTokenStore {
  String? value;

  @override
  Future<String?> read() async => value;

  @override
  Future<void> write(String token) async => value = token;

  @override
  Future<void> clear() async => value = null;
}

class _Adapter implements HttpClientAdapter {
  @override
  Future<ResponseBody> fetch(RequestOptions options, Stream<Uint8List>? stream,
      Future<void>? cancelFuture) async {
    return ResponseBody.fromString('{}', 500);
  }

  @override
  void close({bool force = false}) {}
}

AuthenticatedHttpClient _authenticatedClient() {
  final client = AuthenticatedHttpClient(
    baseUrl: Uri.parse('https://test.invalid'),
    session: AuthSession(_Store()),
    dio: Dio(BaseOptions(baseUrl: 'https://test.invalid'))
      ..httpClientAdapter = _Adapter(),
  );
  return client;
}

Dashboard _dashboard({String shopId = 'shop-1', double? power = 0}) =>
    Dashboard(
      shop: DashboardShop(id: shopId, code: 'S-$shopId', name: 'Test Shop'),
      generatedAt: DateTime.utc(2026, 1, 2, 3, 4, 5),
      currentPowerW: power,
      dailyKwh: null,
      monthlyKwh: 12.5,
      dailyKg: 0,
      monthlyKg: null,
      devices: const [
        DashboardDevice(
          measurementPointRef: 'mp-1',
          name: 'Meter',
          isOnline: true,
          lastSeen: null,
        ),
      ],
    );

class _MemoryCache implements DashboardCache {
  _MemoryCache();
  final values = <String, DashboardCacheSnapshot>{};
  bool failWrites = false;

  String _key(String userId, String shopId) => '$userId/$shopId';

  @override
  Future<DashboardCacheSnapshot?> read(String userId, String shopId) async =>
      values[_key(userId, shopId)];

  @override
  Future<void> write(
    String userId,
    String shopId,
    Dashboard dashboard, {
    bool Function()? isCurrent,
  }) async {
    if (isCurrent != null && !isCurrent()) return;
    if (failWrites) throw StateError('storage unavailable');
    values[_key(userId, shopId)] = DashboardCacheSnapshot(
      dashboard: dashboard,
      cachedAt: DateTime.utc(2026, 1, 2),
    );
  }

  @override
  Future<void> delete(String userId, String shopId) async {
    values.remove(_key(userId, shopId));
  }
}

class _DelayedCache extends _MemoryCache {
  final releaseRead = Completer<void>();
  var waitForRead = true;

  @override
  Future<DashboardCacheSnapshot?> read(String userId, String shopId) async {
    if (waitForRead) await releaseRead.future;
    return super.read(userId, shopId);
  }
}

class _SequenceRepository implements DashboardRepository {
  _SequenceRepository(this.results);

  final List<Object> results;
  var calls = 0;

  @override
  Future<Dashboard> fetchDashboard(String shopId) async {
    final result = results[calls < results.length ? calls : results.length - 1];
    calls++;
    if (result is Dashboard Function()) return result();
    if (result is Dashboard) return result;
    throw result;
  }
}

Future<void> _installSession(AuthenticatedHttpClient client) async {
  await client.session.setTokens(TokenPair(
    accessToken: 'access',
    refreshToken: 'refresh',
    accessTokenExpiresAt: DateTime.utc(2030),
    refreshTokenExpiresAt: DateTime.utc(2030, 2),
  ));
}

Future<DashboardNotifier> _notifier({
  required _SequenceRepository repository,
  required _MemoryCache cache,
  String userId = 'user-1',
  String shopId = 'shop-1',
  bool shopAuthorized = true,
}) async {
  final client = _authenticatedClient();
  await _installSession(client);
  return DashboardNotifier(
    repository,
    client,
    shopId,
    cache: cache,
    userId: userId,
    shopAuthorized: shopAuthorized,
  );
}

void main() {
  setUp(() {
    SharedPreferences.setMockInitialValues({});
  });

  test('SharedPreferences cache round-trips the complete Dashboard envelope',
      () async {
    final preferences = SharedPreferences.getInstance();
    final cache = SharedPreferencesDashboardCache(
      preferences,
      clock: () => DateTime.utc(2026, 1, 3, 4, 5, 6),
    );

    await cache.write('user-1', 'shop-1', _dashboard());
    final result = await cache.read('user-1', 'shop-1');

    expect(result, isNotNull);
    expect(result!.cachedAt, DateTime.utc(2026, 1, 3, 4, 5, 6));
    expect(result.dashboard.generatedAt, DateTime.utc(2026, 1, 2, 3, 4, 5));
    expect(result.dashboard.currentPowerW, 0);
    expect(result.dashboard.dailyKwh, isNull);
    expect(result.dashboard.devices.single.measurementPointRef, 'mp-1');
  });

  test('cache rejects malformed, unsupported, and mismatched envelopes',
      () async {
    const key = 'power_iot.dashboard_cache.v1.dXNlci0x.c2hvcC0x';
    final preferences = await SharedPreferences.getInstance();
    final cache = SharedPreferencesDashboardCache(
      Future<SharedPreferences>.value(preferences),
    );

    await preferences.setString(key, '{"version":1}');
    expect(await cache.read('user-1', 'shop-1'), isNull);
    await preferences.setString(
      key,
      jsonEncode({
        'version': 99,
        'userId': 'user-1',
        'shopId': 'shop-1',
        'cachedAt': '2026-01-03T04:05:06Z',
        'dashboard': {},
      }),
    );
    expect(await cache.read('user-1', 'shop-1'), isNull);
    await cache.write('user-1', 'shop-1', _dashboard());
    final encoded = jsonDecode(preferences.getString(key)!) as Map;
    final dashboard = encoded['dashboard'] as Map;
    dashboard['shop'] = {'id': 'shop-2', 'code': 'S-shop-2', 'name': 'Other'};
    await preferences.setString(key, jsonEncode(encoded));
    expect(await cache.read('user-1', 'shop-1'), isNull);
  });

  test('fresh success persists and cache write failure does not fail it',
      () async {
    final cache = _MemoryCache();
    final notifier = await _notifier(
      repository: _SequenceRepository([_dashboard()]),
      cache: cache,
    );
    await Future<void>.delayed(Duration.zero);
    await Future<void>.delayed(Duration.zero);
    expect(cache.values, contains('user-1/shop-1'));
    expect(notifier.state.status, DashboardStatus.success);
    notifier.dispose();

    final failedCache = _MemoryCache()..failWrites = true;
    final failedNotifier = await _notifier(
      repository: _SequenceRepository([_dashboard()]),
      cache: failedCache,
    );
    await Future<void>.delayed(Duration.zero);
    await Future<void>.delayed(Duration.zero);

    expect(failedNotifier.state.status, DashboardStatus.success);
    expect(failedNotifier.state.isDurableCache, isFalse);
    failedNotifier.dispose();
  });

  test('transient foreground failure uses only the eligible scoped cache',
      () async {
    final cache = _MemoryCache();
    await cache.write('user-1', 'shop-1', _dashboard());
    final notifier = await _notifier(
      repository: _SequenceRepository([
        _dashboard(),
        () => throw DioException(
              requestOptions: RequestOptions(path: '/dashboard'),
              type: DioExceptionType.connectionError,
            ),
      ]),
      cache: cache,
    );
    await Future<void>.delayed(Duration.zero);
    await Future<void>.delayed(Duration.zero);
    await notifier.load();

    expect(notifier.state.status, DashboardStatus.success);
    expect(notifier.state.isDurableCache, isTrue);
    expect(notifier.state.cachedAt, DateTime.utc(2026, 1, 2));
    notifier.dispose();
  });

  test(
      'unauthorized, not found, malformed, and non-transient errors never use cache',
      () async {
    final cache = _MemoryCache();
    await cache.write('user-1', 'shop-1', _dashboard());
    for (final error in <Object>[
      const UnauthorizedException(),
      const DashboardShopNotFoundException(),
      const FormatException('bad response'),
      DioException(
        requestOptions: RequestOptions(path: '/dashboard'),
        response: Response(statusCode: 400, requestOptions: RequestOptions()),
      ),
    ]) {
      final notifier = await _notifier(
        repository: _SequenceRepository([error]),
        cache: cache,
      );
      await Future<void>.delayed(Duration.zero);
      await Future<void>.delayed(Duration.zero);
      expect(notifier.state.isDurableCache, isFalse);
      expect(notifier.state.data, isNull);
      notifier.dispose();
    }
  });

  test('mismatched user, Shop, and authorization are cache misses', () async {
    final cache = _MemoryCache();
    await cache.write('user-1', 'shop-1', _dashboard());
    for (final values in [
      (userId: 'user-2', shopId: 'shop-1', authorized: true),
      (userId: 'user-1', shopId: 'shop-2', authorized: true),
      (userId: 'user-1', shopId: 'shop-1', authorized: false),
    ]) {
      final notifier = await _notifier(
        repository: _SequenceRepository([
          () => throw DioException(
                requestOptions: RequestOptions(path: '/dashboard'),
                type: DioExceptionType.connectionError,
              ),
        ]),
        cache: cache,
        userId: values.userId,
        shopId: values.shopId,
        shopAuthorized: values.authorized,
      );
      await Future<void>.delayed(Duration.zero);
      await Future<void>.delayed(Duration.zero);
      expect(notifier.state.data, isNull);
      notifier.dispose();
    }
  });

  test('fresh data replaces a durable stale fallback', () async {
    final cache = _MemoryCache();
    await cache.write('user-1', 'shop-1', _dashboard(power: 1));
    final notifier = await _notifier(
      repository: _SequenceRepository([
        _dashboard(power: 1),
        () => throw DioException(
              requestOptions: RequestOptions(path: '/dashboard'),
              type: DioExceptionType.connectionError,
            ),
        _dashboard(power: 2),
      ]),
      cache: cache,
    );
    await Future<void>.delayed(Duration.zero);
    await Future<void>.delayed(Duration.zero);
    await notifier.load();
    expect(notifier.state.isDurableCache, isTrue);
    await notifier.load();
    expect(notifier.state.isDurableCache, isFalse);
    expect(notifier.state.data!.currentPowerW, 2);
    notifier.dispose();
  });

  test('background transient failure preserves the in-memory live snapshot',
      () async {
    final cache = _MemoryCache();
    final notifier = await _notifier(
      repository: _SequenceRepository([
        _dashboard(power: 1),
        () => throw DioException(
              requestOptions: RequestOptions(path: '/dashboard'),
              type: DioExceptionType.connectionError,
            ),
      ]),
      cache: cache,
    );
    await Future<void>.delayed(Duration.zero);
    await Future<void>.delayed(Duration.zero);
    notifier.setAppLifecycleState(AppLifecycleState.resumed);
    notifier.setRouteVisible(true);
    await notifier.load(background: true);
    expect(notifier.state.data!.currentPowerW, 1);
    expect(notifier.state.isDurableCache, isFalse);
    notifier.dispose();
  });

  test('late cache reads cannot publish after auth invalidation or disposal',
      () async {
    final cache = _DelayedCache();
    await cache.write('user-1', 'shop-1', _dashboard());
    final client = _authenticatedClient();
    await _installSession(client);
    final repository = _SequenceRepository([
      () => throw DioException(
            requestOptions: RequestOptions(path: '/dashboard'),
            type: DioExceptionType.connectionError,
          ),
    ]);
    final notifier = DashboardNotifier(
      repository,
      client,
      'shop-1',
      cache: cache,
      userId: 'user-1',
      shopAuthorized: true,
    );
    await Future<void>.delayed(Duration.zero);
    client.beginSession();
    cache.releaseRead.complete();
    await Future<void>.delayed(Duration.zero);
    await Future<void>.delayed(Duration.zero);
    expect(notifier.state.data, isNull);
    notifier.dispose();
  });

  test('logout removes cache presentation eligibility immediately', () async {
    final cache = _MemoryCache();
    await cache.write('user-1', 'shop-1', _dashboard());
    final client = _authenticatedClient();
    await _installSession(client);
    final notifier = DashboardNotifier(
      _SequenceRepository([
        () => throw DioException(
              requestOptions: RequestOptions(path: '/dashboard'),
              type: DioExceptionType.connectionError,
            ),
      ]),
      client,
      'shop-1',
      cache: cache,
      userId: 'user-1',
      shopAuthorized: true,
    );
    await Future<void>.delayed(Duration.zero);
    await Future<void>.delayed(Duration.zero);
    expect(notifier.state.isDurableCache, isTrue);
    try {
      await client.logout();
    } catch (_) {
      // The test adapter deliberately makes the logout request unavailable;
      // local epoch invalidation still must clear presentation immediately.
    }
    expect(notifier.state.data, isNull);
    notifier.dispose();
  });

  test('authorization helper rejects a remembered but unauthorized Shop', () {
    const state = ShopsState.success(
      ShopsSnapshot(
        shops: [
          Shop(
            id: 'shop-2',
            code: 'S2',
            name: 'Other',
            address: null,
            phone: null,
            isHead: false,
          ),
        ],
        currentShopId: 'shop-1',
      ),
    );
    expect(authorizedShopId(state), isNull);
  });

  testWidgets('cached Dashboard is visibly marked stale', (tester) async {
    final cache = _MemoryCache();
    await cache.write('user-1', 'shop-1', _dashboard());
    final repository = _SequenceRepository([
      () => throw DioException(
            requestOptions: RequestOptions(path: '/dashboard'),
            type: DioExceptionType.connectionError,
          ),
    ]);
    final client = _authenticatedClient();
    await _installSession(client);
    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          authClientProvider.overrideWithValue(client),
          dashboardCacheProvider.overrideWithValue(cache),
          dashboardRepositoryProvider.overrideWithValue(repository),
          profileRepositoryProvider.overrideWithValue(
            _TestProfileRepository(),
          ),
          shopsRepositoryProvider.overrideWithValue(
            _TestShopsRepository(),
          ),
        ],
        child: const MaterialApp(home: DashboardScreen()),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.textContaining('已儲存資料'), findsOneWidget);
    expect(find.textContaining('即時更新暫時無法取得'), findsOneWidget);
  });
}

class _TestProfileRepository implements ProfileRepository {
  @override
  Future<UserProfile> fetchProfile() async => const UserProfile(
        id: 'user-1',
        account: 'operator',
        name: 'Operator',
        email: null,
        phone: null,
        isAdmin: false,
        currentShopId: 'shop-1',
      );
}

class _TestShopsRepository implements ShopsRepository {
  @override
  Future<ShopsSnapshot> fetchShops() async => const ShopsSnapshot(
        shops: [
          Shop(
            id: 'shop-1',
            code: 'S-shop-1',
            name: 'Test Shop',
            address: null,
            phone: null,
            isHead: false,
          ),
        ],
        currentShopId: 'shop-1',
      );
}
