import 'dart:async';
import 'dart:convert';
import 'dart:typed_data';

import 'package:dio/dio.dart';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:power_iot_app/config/dashboard_poll_config.dart';
import 'package:power_iot_app/features/dashboard/dashboard_route_observer.dart';
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

Map<String, Object?> _payload({
  Object? currentPowerW = 0,
  Object? dailyKwh,
  Object? monthlyKwh,
  Object? dailyKg,
  Object? monthlyKg,
}) =>
    {
      'shop': {'id': '7', 'code': 'S7', 'name': 'Remote Shop'},
      'generatedAt': '2026-01-02T03:04:05Z',
      'currentPowerW': currentPowerW,
      'dailyKwh': dailyKwh,
      'monthlyKwh': monthlyKwh,
      'dailyKg': dailyKg,
      'monthlyKg': monthlyKg,
      'devices': [
        {
          'measurementPointRef': '00000000-0000-4000-8000-000000000001',
          'name': 'Online meter',
          'isOnline': true,
          'lastSeen': null,
        },
        {
          'measurementPointRef': '00000000-0000-4000-8000-000000000002',
          'name': 'Offline meter',
          'isOnline': false,
          'lastSeen': '2026-01-02T02:04:05Z',
        },
      ],
    };

Dashboard _dashboard({
  double? power = 0,
  double? dailyKwh,
  double? monthlyKwh,
  double? dailyKg,
  double? monthlyKg,
}) =>
    Dashboard(
      shop: const DashboardShop(id: '7', code: 'S7', name: 'Remote Shop'),
      generatedAt: DateTime.utc(2026, 1, 2),
      currentPowerW: power,
      dailyKwh: dailyKwh,
      monthlyKwh: monthlyKwh,
      dailyKg: dailyKg,
      monthlyKg: monthlyKg,
      devices: const [
        DashboardDevice(
          measurementPointRef: '00000000-0000-4000-8000-000000000001',
          name: 'Online meter',
          isOnline: true,
          lastSeen: null,
        ),
        DashboardDevice(
          measurementPointRef: '00000000-0000-4000-8000-000000000002',
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

  test('B7 DTO preserves zero and positive server carbon values', () {
    final dashboard = DashboardDto.fromJson(
      _payload(dailyKwh: 0, monthlyKwh: 12.5, dailyKg: 0, monthlyKg: 4.5202),
    ).toModel();

    expect(dashboard.dailyKwh, 0);
    expect(dashboard.monthlyKwh, 12.5);
    expect(dashboard.dailyKg, 0);
    expect(dashboard.monthlyKg, 4.5202);
  });

  test('B7 DTO rejects unknown fields instead of fabricating a shape', () {
    final payload = _payload()..['unexpected'] = 1;
    expect(() => DashboardDto.fromJson(payload), throwsFormatException);
  });

  test('B7 DTO rejects backend device IDs without an MP locator', () {
    final payload = _payload();
    payload['devices'] = [
      {
        'id': '1',
        'name': 'Online meter',
        'isOnline': true,
        'lastSeen': null,
      },
    ];
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

  testWidgets('dashboard renders current power and null energy values',
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
    expect(find.text('本日用電量'), findsOneWidget);
    expect(find.text('本月用電量'), findsOneWidget);
    expect(find.text('本日碳排放量'), findsOneWidget);
    expect(find.text('本月碳排放量'), findsOneWidget);
    expect(find.text('無資料'), findsNWidgets(4));
    expect(find.text('0.14'), findsNothing);
    expect(find.text('運轉中'), findsOneWidget);
    expect(find.text('已離線'), findsOneWidget);
    expect(find.text('最後連線未知'), findsNWidgets(2));
  });

  testWidgets('dashboard renders energy values without client aggregation',
      (tester) async {
    final client = _client(_Adapter((_) async => _json(500, {})), _Store());
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
          dashboardRepositoryProvider.overrideWithValue(
            _FunctionRepository(() async => _dashboard(
                  power: 12,
                  dailyKwh: 1,
                  monthlyKwh: 2.5,
                  dailyKg: 4.52,
                  monthlyKg: 11.75,
                )),
          ),
        ],
        child: const MaterialApp(home: DashboardScreen()),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('本日用電量'), findsOneWidget);
    expect(find.text('本月用電量'), findsOneWidget);
    expect(find.text('1 kWh'), findsOneWidget);
    expect(find.text('2.50 kWh'), findsOneWidget);
    expect(find.text('4.52 kgCO₂e'), findsOneWidget);
    expect(find.text('11.75 kgCO₂e'), findsOneWidget);
    expect(find.text('3.50 kWh'), findsNothing);
    expect(find.text('12 W'), findsOneWidget);
  });

  test('poll config accepts only positive integer overrides', () {
    expect(dashboardPollDuration().inSeconds, dashboardDefaultPollSeconds);
    expect(dashboardPollDuration(rawSeconds: '7').inSeconds, 7);
    expect(dashboardPollDuration(rawSeconds: '0').inSeconds,
        dashboardDefaultPollSeconds);
    expect(dashboardPollDuration(rawSeconds: '-2').inSeconds,
        dashboardDefaultPollSeconds);
  });

  test('notifier fetches immediately and refreshes on a periodic tick',
      () async {
    final repository = _QueueRepository([
      Future.value(_dashboard(power: 1)),
      Future.value(_dashboard(power: 2)),
    ]);
    final timers = _ManualTimers();
    final notifier = _notifier(repository, timerFactory: timers.create);
    expect(repository.calls, 1);
    await Future<void>.delayed(Duration.zero);
    notifier.setRouteVisible(true);
    notifier.setAppLifecycleState(AppLifecycleState.resumed);
    expect(timers.created.single.duration, const Duration(seconds: 3));
    timers.created.single.fire();
    await Future<void>.delayed(Duration.zero);
    expect(repository.calls, 2);
    expect(notifier.state.data?.currentPowerW, 2);
    notifier.dispose();
  });

  test('background success and error preserve the last successful data',
      () async {
    final repository = _CallbackRepository((call) async {
      if (call == 3) throw StateError('offline');
      return _dashboard(power: call.toDouble());
    });
    final timers = _ManualTimers();
    final notifier = _notifier(repository, timerFactory: timers.create);
    await Future<void>.delayed(Duration.zero);
    notifier.setRouteVisible(true);
    notifier.setAppLifecycleState(AppLifecycleState.resumed);
    timers.created.single.fire();
    await Future<void>.delayed(Duration.zero);
    expect(notifier.state.data?.currentPowerW, 2);
    timers.created.single.fire();
    await Future<void>.delayed(Duration.zero);
    expect(notifier.state.status, DashboardStatus.success);
    expect(notifier.state.data?.currentPowerW, 2);
    expect(notifier.state.error, isA<StateError>());
    notifier.dispose();
  });

  test('periodic ticks do not overlap or backlog requests', () async {
    final repository = _ControlledRepository();
    final timers = _ManualTimers();
    final notifier = _notifier(repository, timerFactory: timers.create);
    expect(repository.pending, hasLength(1));
    repository.completeNext(_dashboard(power: 1));
    await Future<void>.delayed(Duration.zero);
    notifier.setRouteVisible(true);
    notifier.setAppLifecycleState(AppLifecycleState.resumed);
    timers.created.single.fire();
    timers.created.single.fire();
    expect(repository.calls, 2);
    repository.completeNext(_dashboard(power: 2));
    await Future<void>.delayed(Duration.zero);
    expect(repository.calls, 2);
    timers.created.single.fire();
    expect(repository.calls, 3);
    notifier.dispose();
  });

  test('route reveal while paused waits for resume before stale refresh',
      () async {
    var now = DateTime.utc(2026, 1, 1);
    final repository = _QueueRepository([
      Future.value(_dashboard(power: 1)),
      Future.value(_dashboard(power: 2)),
    ]);
    final notifier = _notifier(
      repository,
      clock: () => now,
    );
    await Future<void>.delayed(Duration.zero);
    expect(repository.calls, 1);

    notifier.setAppLifecycleState(AppLifecycleState.paused);
    now = now.add(const Duration(seconds: 3));
    notifier.setRouteVisible(true);
    await Future<void>.delayed(Duration.zero);
    expect(repository.calls, 1);

    notifier.setAppLifecycleState(AppLifecycleState.resumed);
    await Future<void>.delayed(Duration.zero);
    expect(repository.calls, 2);
    expect(notifier.state.data?.currentPowerW, 2);
    notifier.dispose();
  });

  test('lifecycle and route visibility stop and safely resume polling',
      () async {
    final repository = _QueueRepository([
      Future.value(_dashboard()),
      Future.value(_dashboard(power: 2)),
      Future.value(_dashboard(power: 3)),
    ]);
    final timers = _ManualTimers();
    final notifier = _notifier(repository, timerFactory: timers.create);
    await Future<void>.delayed(Duration.zero);
    notifier.setRouteVisible(true);
    notifier.setAppLifecycleState(AppLifecycleState.resumed);
    final firstTimer = timers.created.single;
    notifier.setAppLifecycleState(AppLifecycleState.paused);
    firstTimer.fire();
    expect(repository.calls, 1);
    notifier.setAppLifecycleState(AppLifecycleState.resumed);
    notifier.setRouteVisible(false);
    final secondTimer = timers.created.last;
    secondTimer.fire();
    expect(repository.calls, 1);
    notifier.setRouteVisible(true);
    timers.created.last.fire();
    await Future<void>.delayed(Duration.zero);
    expect(repository.calls, 2);
    notifier.dispose();
  });

  testWidgets(
      'covered stale resumed dashboard does not fetch before route is hidden',
      (tester) async {
    var now = DateTime.utc(2026, 1, 1);
    final repository = _QueueRepository([Future.value(_dashboard())]);
    final timers = _ManualTimers();
    final notifier = _notifier(
      repository,
      clock: () => now,
      timerFactory: timers.create,
    );
    await tester.pumpWidget(
      ProviderScope(
        overrides: [
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
          dashboardProvider('7').overrideWith((ref) => notifier),
        ],
        child: MaterialApp(
          navigatorObservers: [dashboardRouteObserver],
          home: const DashboardScreen(),
        ),
      ),
    );
    await tester.pumpAndSettle();
    expect(timers.created, hasLength(1));
    final dashboardContext = tester.element(find.byType(DashboardScreen));
    now = now.add(const Duration(seconds: 3));

    Navigator.of(dashboardContext).push(
      MaterialPageRoute<void>(builder: (_) => const SizedBox()),
    );
    await tester.pumpAndSettle();
    expect(repository.calls, 1);
    expect(timers.created.single.isActive, isFalse);

    Navigator.of(dashboardContext).pop();
    await tester.pumpAndSettle();
    expect(timers.created, hasLength(2));
    expect(timers.created.last.isActive, isTrue);

    await tester.pumpWidget(const SizedBox());
    expect(timers.created.last.isActive, isFalse);
  });

  test('inactive, paused, detached, and hidden lifecycle states stop polling',
      () async {
    final repository = _QueueRepository([Future.value(_dashboard())]);
    final timers = _ManualTimers();
    final notifier = _notifier(repository, timerFactory: timers.create);
    await Future<void>.delayed(Duration.zero);
    notifier.setRouteVisible(true);
    notifier.setAppLifecycleState(AppLifecycleState.resumed);
    final timer = timers.created.single;

    for (final lifecycleState in <AppLifecycleState>[
      AppLifecycleState.inactive,
      AppLifecycleState.paused,
      AppLifecycleState.detached,
      AppLifecycleState.hidden,
    ]) {
      notifier.setAppLifecycleState(lifecycleState);
      expect(timer.isActive, isFalse);
      timer.fire();
      expect(repository.calls, 1);
      notifier.setAppLifecycleState(AppLifecycleState.resumed);
      notifier.setRouteVisible(false);
      notifier.setRouteVisible(true);
    }

    notifier.dispose();
  });

  test('unauthorized polling cannot restart on later visibility changes',
      () async {
    final repository = _CallbackRepository((call) async {
      if (call == 2) throw const UnauthorizedException();
      return _dashboard();
    });
    final timers = _ManualTimers();
    final notifier = _notifier(repository, timerFactory: timers.create);
    await Future<void>.delayed(Duration.zero);
    notifier.setRouteVisible(true);
    notifier.setAppLifecycleState(AppLifecycleState.resumed);
    final timer = timers.created.single;
    timer.fire();
    await Future<void>.delayed(Duration.zero);
    expect(notifier.state.status, DashboardStatus.unauthorized);
    expect(timers.created, hasLength(1));

    notifier.setRouteVisible(false);
    notifier.setRouteVisible(true);
    notifier.setAppLifecycleState(AppLifecycleState.paused);
    notifier.setAppLifecycleState(AppLifecycleState.resumed);
    expect(timers.created, hasLength(1));
    expect(timer.isActive, isFalse);
    notifier.dispose();
  });

  test('shop notifier instances remain isolated and dispose safely', () async {
    final firstRepository = _ControlledRepository();
    final secondRepository = _ControlledRepository();
    final first = _notifier(firstRepository, shopId: 'first');
    final second = _notifier(secondRepository, shopId: 'second');
    first.dispose();
    firstRepository.completeNext(_dashboard(power: 1));
    secondRepository.completeNext(_dashboard(power: 2));
    await Future<void>.delayed(Duration.zero);
    expect(second.state.data?.currentPowerW, 2);
    expect(firstRepository.shopIds, ['first']);
    expect(secondRepository.shopIds, ['second']);
    second.dispose();
  });

  test('unauthorized background refresh stops polling', () async {
    final repository = _CallbackRepository((call) async {
      if (call == 2) throw const UnauthorizedException();
      return _dashboard();
    });
    final timers = _ManualTimers();
    final notifier = _notifier(repository, timerFactory: timers.create);
    await Future<void>.delayed(Duration.zero);
    notifier.setRouteVisible(true);
    notifier.setAppLifecycleState(AppLifecycleState.resumed);
    final timer = timers.created.single;
    timer.fire();
    await Future<void>.delayed(Duration.zero);
    expect(notifier.state.status, DashboardStatus.unauthorized);
    expect(timer.isActive, isFalse);
    notifier.dispose();
  });

  test('timer is canceled when notifier is disposed', () async {
    final repository = _QueueRepository([Future.value(_dashboard())]);
    final timers = _ManualTimers();
    final notifier = _notifier(repository, timerFactory: timers.create);
    await Future<void>.delayed(Duration.zero);
    notifier.setRouteVisible(true);
    notifier.setAppLifecycleState(AppLifecycleState.resumed);
    final timer = timers.created.single;
    notifier.dispose();
    expect(timer.isActive, isFalse);
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

DashboardNotifier _notifier(
  DashboardRepository repository, {
  String shopId = '7',
  DashboardClock? clock,
  DashboardTimerFactory? timerFactory,
}) {
  final store = _Store();
  final client = _client(_Adapter((_) async => _json(204, {})), store);
  return DashboardNotifier(
    repository,
    client,
    shopId,
    pollInterval: const Duration(seconds: 3),
    clock: clock,
    timerFactory: timerFactory,
  );
}

class _QueueRepository implements DashboardRepository {
  _QueueRepository(this.responses);
  final List<Future<Dashboard>> responses;
  var calls = 0;

  @override
  Future<Dashboard> fetchDashboard(String shopId) {
    calls++;
    if (responses.isEmpty) return Future.value(_dashboard());
    return responses.removeAt(0);
  }
}

class _CallbackRepository implements DashboardRepository {
  _CallbackRepository(this.callback);
  final Future<Dashboard> Function(int call) callback;
  var calls = 0;

  @override
  Future<Dashboard> fetchDashboard(String shopId) => callback(++calls);
}

class _ControlledRepository implements DashboardRepository {
  final pending = <Completer<Dashboard>>[];
  final shopIds = <String>[];
  var calls = 0;

  @override
  Future<Dashboard> fetchDashboard(String shopId) {
    calls++;
    shopIds.add(shopId);
    final completer = Completer<Dashboard>();
    pending.add(completer);
    return completer.future;
  }

  void completeNext(Dashboard dashboard) {
    pending.removeAt(0).complete(dashboard);
  }
}

class _ManualTimers {
  final created = <_ManualTimer>[];

  Timer create(Duration duration, void Function(Timer) callback) {
    final timer = _ManualTimer(duration, callback);
    created.add(timer);
    return timer;
  }
}

class _ManualTimer implements Timer {
  _ManualTimer(this.duration, this.callback);

  final Duration duration;
  final void Function(Timer) callback;
  var _active = true;
  var _tick = 0;

  void fire() {
    if (!_active) return;
    _tick++;
    callback(this);
  }

  @override
  bool get isActive => _active;

  @override
  int get tick => _tick;

  @override
  void cancel() {
    _active = false;
  }
}
