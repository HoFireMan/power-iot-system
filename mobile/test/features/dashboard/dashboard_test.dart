import 'dart:async';
import 'dart:convert';
import 'dart:typed_data';

import 'package:dio/dio.dart';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:power_iot_app/core/network/authenticated_http_client.dart';
import 'package:power_iot_app/core/network/remote_error.dart';
import 'package:power_iot_app/features/auth/auth_controller.dart';
import 'package:power_iot_app/features/dashboard/dashboard_screen.dart';
import 'package:power_iot_app/features/dashboard/data/dtos/dashboard_dto.dart';
import 'package:power_iot_app/features/dashboard/data/repositories/dashboard_repository_impl.dart';
import 'package:power_iot_app/features/dashboard/domain/models/dashboard.dart';
import 'package:power_iot_app/features/dashboard/domain/repositories/dashboard_repository.dart';
import 'package:power_iot_app/features/dashboard/presentation/providers/dashboard_provider.dart';
import 'package:power_iot_app/features/shops/domain/models/shop.dart';
import 'package:power_iot_app/features/shops/providers/remote_shop_provider.dart';
import 'package:power_iot_app/features/shops/domain/repositories/shops_repository.dart';

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
  _Adapter(this.handler);

  final Future<ResponseBody> Function(RequestOptions) handler;
  final requests = <RequestOptions>[];

  @override
  Future<ResponseBody> fetch(RequestOptions options, Stream<Uint8List>? stream,
      Future<void>? cancelFuture) async {
    requests.add(options);
    return handler(options);
  }

  @override
  void close({bool force = false}) {}
}

ResponseBody _json(int status, Object body) => ResponseBody.fromString(
      jsonEncode(body),
      status,
      headers: {
        Headers.contentTypeHeader: ['application/json'],
      },
    );

AuthenticatedHttpClient _client(_Adapter adapter, _Store store) {
  return AuthenticatedHttpClient(
    baseUrl: Uri.parse('https://test.invalid'),
    session: AuthSession(store),
    dio: Dio(BaseOptions(baseUrl: 'https://test.invalid'))
      ..httpClientAdapter = adapter,
  );
}

Map<String, Object?> _payload({Object? currentPowerW = 0}) => {
      'shop': {'id': '7', 'code': 'S7', 'name': 'Remote Shop'},
      'generatedAt': '2026-01-02T03:04:05Z',
      'currentPowerW': currentPowerW,
      'dailyKwh': null,
      'monthlyKwh': null,
      'dailyKg': null,
      'monthlyKg': null,
      'devices': [
        {
          'id': '1',
          'name': 'Online meter',
          'isOnline': true,
          'lastSeen': null,
        },
        {
          'id': '2',
          'name': 'Offline meter',
          'isOnline': false,
          'lastSeen': '2026-01-02T02:04:05Z',
        },
      ],
    };

Dashboard _dashboard({double? power = 0}) => Dashboard(
      shop: const DashboardShop(id: '7', code: 'S7', name: 'Remote Shop'),
      generatedAt: DateTime.utc(2026, 1, 2),
      currentPowerW: power,
      dailyKwh: null,
      monthlyKwh: null,
      dailyKg: null,
      monthlyKg: null,
      devices: const [
        DashboardDevice(
          id: '1',
          name: 'Online meter',
          isOnline: true,
          lastSeen: null,
        ),
        DashboardDevice(
          id: '2',
          name: 'Offline meter',
          isOnline: false,
          lastSeen: null,
        ),
      ],
    );

void main() {
  test('B7 DTO preserves null metrics, measured zero, and device fields', () {
    final dashboard = DashboardDto.fromJson(_payload()).toModel();

    expect(dashboard.currentPowerW, 0);
    expect(dashboard.currentPowerW, isNotNull);
    expect(dashboard.dailyKwh, isNull);
    expect(dashboard.monthlyKg, isNull);
    expect(dashboard.devices[0].isOnline, isTrue);
    expect(dashboard.devices[0].lastSeen, isNull);
    expect(dashboard.devices[1].isOnline, isFalse);
    expect(dashboard.devices[1].lastSeen, DateTime.utc(2026, 1, 2, 2, 4, 5));
  });

  test('B7 DTO rejects unknown fields instead of fabricating a shape', () {
    final payload = _payload()..['unexpected'] = 1;
    expect(() => DashboardDto.fromJson(payload), throwsFormatException);
  });

  test('remote repository targets only the selected shop path', () async {
    late _Adapter adapter;
    adapter = _Adapter((request) async {
      expect(request.method, 'GET');
      expect(request.uri.path, '/api/v1/shops/7/dashboard');
      expect(request.uri.query, isEmpty);
      expect(request.data, isNull);
      expect(request.headers['Authorization'], 'Bearer access');
      return _json(200, _payload(currentPowerW: 12.5));
    });
    final store = _Store();
    final client = _client(adapter, store);
    await client.session.setTokens(TokenPair(
      accessToken: 'access',
      refreshToken: 'refresh',
      accessTokenExpiresAt: DateTime.utc(2030),
      refreshTokenExpiresAt: DateTime.utc(2030, 2),
    ));

    final dashboard =
        await RemoteDashboardRepository(client).fetchDashboard('7');
    expect(dashboard.shop.id, '7');
    expect(dashboard.currentPowerW, 12.5);
    expect(adapter.requests, hasLength(1));
  });

  test('SHOP_NOT_FOUND is classified without exposing response details',
      () async {
    final store = _Store();
    final client = _client(
      _Adapter((_) async => _json(404, {
            'code': 'SHOP_NOT_FOUND',
            'message': 'shop not found',
            'requestId': 'private-id',
          })),
      store,
    );

    await expectLater(
      RemoteDashboardRepository(client).fetchDashboard('99'),
      throwsA(isA<DashboardShopNotFoundException>()),
    );
  });

  test('dashboard notifier rejects stale success after a newer login',
      () async {
    final releaseOldRequest = Completer<void>();
    final releaseNewRequest = Completer<void>();
    var calls = 0;
    final repository = _FunctionRepository(() async {
      calls++;
      if (calls == 1) {
        await releaseOldRequest.future;
        return _dashboard();
      }
      await releaseNewRequest.future;
      return _dashboard();
    });
    final store = _Store();
    final client = _client(_Adapter((_) async => _json(204, {})), store);
    final notifier = DashboardNotifier(repository, client, '7');
    await Future<void>.delayed(Duration.zero);

    client.beginSession();
    await client.session.setTokens(TokenPair(
      accessToken: 'new-access',
      refreshToken: 'new-refresh',
      accessTokenExpiresAt: DateTime.utc(2030),
      refreshTokenExpiresAt: DateTime.utc(2030, 2),
    ));
    releaseOldRequest.complete();
    await Future<void>.delayed(Duration.zero);
    expect(notifier.state.data, isNull);

    releaseNewRequest.complete();
    await Future<void>.delayed(Duration.zero);
    expect(notifier.state.data, isNotNull);
    notifier.dispose();
  });

  test('dashboard notifier has loading, error, unauthorized, and 404 states',
      () async {
    final store = _Store();
    final client = _client(_Adapter((_) async => _json(500, {})), store);
    final loading = _CompletingRepository();
    final notifier = DashboardNotifier(loading, client, '7');
    expect(notifier.state.status, DashboardStatus.loading);
    loading.completeError(StateError('offline'));
    await Future<void>.delayed(Duration.zero);
    await Future<void>.delayed(Duration.zero);
    expect(notifier.state.status, DashboardStatus.error);
    expect(notifier.state.data, isNull);
    notifier.dispose();

    final unauthorized = DashboardNotifier(
      _FunctionRepository(() async => throw const UnauthorizedException()),
      client,
      '7',
    );
    await Future<void>.delayed(Duration.zero);
    await Future<void>.delayed(Duration.zero);
    expect(unauthorized.state.status, DashboardStatus.unauthorized);
    unauthorized.dispose();

    final missing = DashboardNotifier(
      _FunctionRepository(
          () async => throw const DashboardShopNotFoundException()),
      client,
      '99',
    );
    await Future<void>.delayed(Duration.zero);
    await Future<void>.delayed(Duration.zero);
    expect(missing.state.status, DashboardStatus.notFound);
    missing.dispose();
  });

  testWidgets('dashboard renders remote zero/null values and no aggregates',
      (tester) async {
    final store = _Store();
    final client = _client(_Adapter((_) async => _json(500, {})), store);
    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          authClientProvider.overrideWithValue(client),
          shopsRepositoryProvider.overrideWithValue(
              _ShopsRepository(() async => const ShopsSnapshot(shops: [
                    Shop(
                      id: '7',
                      code: 'S7',
                      name: 'Remote Shop',
                      address: null,
                      phone: null,
                      isHead: false,
                    ),
                  ], currentShopId: '7'))),
          dashboardRepositoryProvider
              .overrideWithValue(_FunctionRepository(() async => _dashboard())),
        ],
        child: const MaterialApp(home: DashboardScreen()),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('0 W'), findsOneWidget);
    expect(find.text('本日用電量'), findsNothing);
    expect(find.text('0.14'), findsNothing);
    expect(find.text('運轉中'), findsOneWidget);
    expect(find.text('已離線'), findsOneWidget);
    expect(find.text('最後連線未知'), findsNWidgets(2));
  });

  testWidgets(
      'dashboard does not fabricate a first shop when selection is absent',
      (tester) async {
    final store = _Store();
    final client = _client(_Adapter((_) async => _json(500, {})), store);
    var dashboardCalls = 0;
    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          authClientProvider.overrideWithValue(client),
          shopsRepositoryProvider.overrideWithValue(_ShopsRepository(
              () async => const ShopsSnapshot(shops: [], currentShopId: null))),
          dashboardRepositoryProvider.overrideWithValue(_FunctionRepository(() {
            dashboardCalls++;
            return Future.value(_dashboard());
          })),
        ],
        child: const MaterialApp(home: DashboardScreen()),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('尚未選擇店家'), findsOneWidget);
    expect(dashboardCalls, 0);
  });
}

class _FunctionRepository implements DashboardRepository {
  _FunctionRepository(this.callback);
  final Future<Dashboard> Function() callback;

  @override
  Future<Dashboard> fetchDashboard(String shopId) => callback();
}

class _CompletingRepository implements DashboardRepository {
  final _completer = Completer<Dashboard>();

  void completeError(Object error) => _completer.completeError(error);

  @override
  Future<Dashboard> fetchDashboard(String shopId) => _completer.future;
}

class _ShopsRepository implements ShopsRepository {
  _ShopsRepository(this.callback);
  final Future<ShopsSnapshot> Function() callback;

  @override
  Future<ShopsSnapshot> fetchShops() => callback();
}
