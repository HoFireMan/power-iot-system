import 'dart:async';
import 'dart:convert';
import 'dart:typed_data';

import 'package:dio/dio.dart';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:power_iot_app/core/network/authenticated_http_client.dart';
import 'package:power_iot_app/features/admin/domain/models/admin_binding_audit.dart';
import 'package:power_iot_app/features/admin/presentation/providers/admin_binding_audit_history_provider.dart';
import 'package:power_iot_app/features/admin/presentation/screens/admin_audit_history_screen.dart';
import 'package:power_iot_app/features/auth/auth_controller.dart';
import 'package:power_iot_app/features/shops/domain/models/shop.dart';
import 'package:power_iot_app/features/shops/domain/repositories/shops_repository.dart';
import 'package:power_iot_app/features/shops/providers/remote_shop_provider.dart';

late _TestSession _session;

void main() {
  setUp(() async {
    final client = _authenticatedClient();
    final auth = AuthController(client);
    await auth.login(account: 'admin', password: 'password');
    _session = _TestSession(client, auth);
  });
  tearDown(() => _session.client.close());

  testWidgets('shows initial loading before audit history results', (
    tester,
  ) async {
    final adapter = _AuditAdapter()..deferNext('shop-1');
    final harness = await _pumpAuditHistory(tester, adapter);
    addTearDown(harness.dispose);

    await tester.pump();
    expect(find.text('Loading audit history…'), findsOneWidget);

    adapter.complete('shop-1');
    await tester.pumpAndSettle();
    expect(find.text('replace'), findsOneWidget);
  });

  testWidgets('renders results and the empty state', (tester) async {
    final adapter = _AuditAdapter();
    final harness = await _pumpAuditHistory(tester, adapter);
    addTearDown(harness.dispose);
    await tester.pumpAndSettle();

    expect(find.text('replace'), findsOneWidget);
    expect(find.text('relocate'), findsOneWidget);

    adapter.empty = true;
    await tester.tap(find.byKey(const Key('admin-audit-action-filter')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Bind').last);
    await tester.pumpAndSettle();
    expect(find.text('No audit history available.'), findsOneWidget);
  });

  testWidgets('renders an error and retries successfully', (tester) async {
    final adapter = _AuditAdapter()..failNext = true;
    final harness = await _pumpAuditHistory(tester, adapter);
    addTearDown(harness.dispose);
    await tester.pumpAndSettle();

    expect(
      find.text('Unable to load audit history. Please retry.'),
      findsOneWidget,
    );
    expect(adapter.auditRequests, hasLength(1));

    await tester.tap(find.text('Retry'));
    await tester.pumpAndSettle();
    expect(adapter.auditRequests, hasLength(2));
    expect(find.text('replace'), findsOneWidget);
  });

  testWidgets(
    'typing the Measurement Point filter does not submit before Apply',
    (tester) async {
      final adapter = _AuditAdapter();
      final harness = await _pumpAuditHistory(tester, adapter);
      addTearDown(harness.dispose);
      await tester.pumpAndSettle();
      final callsBeforeTyping = adapter.auditRequests.length;

      await tester.enterText(
        find.byKey(const Key('admin-audit-measurement-point-filter')),
        'mp-filter',
      );
      await tester.pump();

      expect(adapter.auditRequests, hasLength(callsBeforeTyping));
      expect(
        find.byKey(const Key('admin-audit-measurement-point-filter')),
        findsOneWidget,
      );
      expect(find.text('Loading audit history…'), findsNothing);
    },
  );

  testWidgets('typing the Device filter does not submit before Apply', (
    tester,
  ) async {
    final adapter = _AuditAdapter();
    final harness = await _pumpAuditHistory(tester, adapter);
    addTearDown(harness.dispose);
    await tester.pumpAndSettle();
    final callsBeforeTyping = adapter.auditRequests.length;

    await tester.enterText(
      find.byKey(const Key('admin-audit-device-filter')),
      'device-filter',
    );
    await tester.pump();

    expect(adapter.auditRequests, hasLength(callsBeforeTyping));
    expect(find.byKey(const Key('admin-audit-device-filter')), findsOneWidget);
    expect(find.text('Loading audit history…'), findsNothing);
  });

  testWidgets('Apply sends exact draft filters and resets pagination', (
    tester,
  ) async {
    final adapter = _AuditAdapter();
    final harness = await _pumpAuditHistory(tester, adapter);
    addTearDown(harness.dispose);
    await tester.pumpAndSettle();

    await _tapLoadMore(tester);
    expect(adapter.auditRequests.last.queryParameters['cursor'], 'cursor-2');
    expect(find.text('unbind'), findsOneWidget);

    await tester.enterText(
      find.byKey(const Key('admin-audit-measurement-point-filter')),
      '  mp-filter  ',
    );
    await tester.enterText(
      find.byKey(const Key('admin-audit-device-filter')),
      '  device-filter  ',
    );
    await tester.tap(find.text('Apply'));
    await tester.pumpAndSettle();

    final request = adapter.auditRequests.last;
    expect(request.queryParameters['measurementPointId'], 'mp-filter');
    expect(request.queryParameters['deviceId'], 'device-filter');
    expect(request.queryParameters.containsKey('cursor'), isFalse);
    expect(find.text('unbind'), findsNothing);
    expect(find.text('bind'), findsOneWidget);
  });

  testWidgets('loads the next page using the applied query cursor', (
    tester,
  ) async {
    final adapter = _AuditAdapter();
    final harness = await _pumpAuditHistory(tester, adapter);
    addTearDown(harness.dispose);
    await tester.pumpAndSettle();

    await _tapLoadMore(tester);
    final request = adapter.auditRequests.last;
    expect(request.queryParameters['cursor'], 'cursor-2');
    expect(request.queryParameters.containsKey('measurementPointId'), isFalse);
    expect(request.queryParameters.containsKey('deviceId'), isFalse);
    expect(find.text('unbind'), findsOneWidget);
    expect(find.text('Load more'), findsNothing);
  });

  testWidgets('late prior-Shop response never renders after a Shop switch', (
    tester,
  ) async {
    final adapter = _AuditAdapter();
    final deferredShops = _DeferredShopAudits();
    final harness = await _pumpAuditHistory(
      tester,
      adapter,
      shops: _shops,
      auditLoader: deferredShops.load,
    );
    addTearDown(harness.dispose);

    await tester.pump();
    await tester.pump();
    expect(deferredShops.calls, hasLength(1));
    harness.shops.selectShop('shop-b');
    await tester.pump();
    await tester.pump();
    expect(deferredShops.calls, hasLength(2));

    deferredShops.complete('shop-a');
    await tester.pump();
    expect(_textContaining('Shop A response'), findsNothing);

    deferredShops.complete('shop-b');
    await tester.pumpAndSettle();
    expect(_textContaining('Shop B response'), findsOneWidget);
    expect(_textContaining('Shop A response'), findsNothing);
  });

  testWidgets('Replace presents only the returned Device and point data', (
    tester,
  ) async {
    final adapter = _AuditAdapter();
    final harness = await _pumpAuditHistory(tester, adapter);
    addTearDown(harness.dispose);
    await tester.pumpAndSettle();

    expect(
      _textContaining('Device: Replacement device (current)'),
      findsOneWidget,
    );
    expect(_textContaining('old-device'), findsNothing);
    expect(_textContaining('Old device'), findsNothing);
  });

  testWidgets('Relocate presents only returned source and target points', (
    tester,
  ) async {
    final adapter = _AuditAdapter();
    final harness = await _pumpAuditHistory(tester, adapter);
    addTearDown(harness.dispose);
    await tester.pumpAndSettle();

    expect(
      _textContaining(
        'Relocation: Source point (current) → Target point (current)',
      ),
      findsOneWidget,
    );
    expect(_textContaining('invented'), findsNothing);
  });
}

Finder _textContaining(String value) => find.byWidgetPredicate(
      (widget) => widget is Text && widget.data?.contains(value) == true,
    );

Future<void> _tapLoadMore(WidgetTester tester) async {
  final loadMore = find.text('Load more');
  if (loadMore.evaluate().isEmpty) {
    await tester.drag(find.byType(ListView), const Offset(0, -800));
    await tester.pumpAndSettle();
  }
  await tester.ensureVisible(loadMore);
  await tester.tap(loadMore);
  await tester.pumpAndSettle();
}

Future<_Harness> _pumpAuditHistory(
  WidgetTester tester,
  _AuditAdapter adapter, {
  Future<AdminBindingAuditHistoryPage> Function(AdminAuditHistoryQuery)?
      auditLoader,
  List<Shop> shops = const [
    Shop(
      id: 'shop-1',
      code: 'ONE',
      name: 'Shop One',
      address: null,
      phone: null,
      isHead: false,
    ),
  ],
}) async {
  final client = _session.client;
  final auth = _session.auth;
  client.dio.httpClientAdapter = adapter;
  final shopsNotifier = ShopsNotifier(_ShopsRepository(shops), client);
  shopsNotifier.state = ShopsState.success(
    ShopsSnapshot(shops: shops, currentShopId: shops.first.id),
  );
  await tester.pumpWidget(
    ProviderScope(
      overrides: [
        authClientProvider.overrideWithValue(client),
        authControllerProvider.overrideWith((ref) => auth),
        shopsProvider.overrideWith((ref) => shopsNotifier),
        if (auditLoader != null)
          adminAuditHistoryQueryProvider.overrideWith(
            (ref, query) => auditLoader(query),
          ),
      ],
      child: const MaterialApp(home: AdminAuditHistoryScreen()),
    ),
  );
  return _Harness(shopsNotifier);
}

AuthenticatedHttpClient _authenticatedClient() {
  final dio = Dio(BaseOptions(baseUrl: 'https://development.invalid'))
    ..httpClientAdapter = _LoginAdapter();
  return AuthenticatedHttpClient(
    baseUrl: Uri.parse('https://development.invalid'),
    session: AuthSession(_MemoryStore()),
    dio: dio,
  );
}

class _TestSession {
  _TestSession(this.client, this.auth);
  final AuthenticatedHttpClient client;
  final AuthController auth;
}

class _Harness {
  _Harness(this.shops);
  final ShopsNotifier shops;

  // ProviderScope owns the notifier and disposes it at the end of the test.
  void dispose() {}
}

class _MemoryStore implements RefreshTokenStore {
  String? token;

  @override
  Future<String?> read() async => token;
  @override
  Future<void> write(String value) async => token = value;
  @override
  Future<void> clear() async => token = null;
}

class _ShopsRepository implements ShopsRepository {
  _ShopsRepository(this.shops);
  final List<Shop> shops;

  @override
  Future<ShopsSnapshot> fetchShops() async =>
      ShopsSnapshot(shops: shops, currentShopId: shops.first.id);
}

class _LoginAdapter implements HttpClientAdapter {
  @override
  Future<ResponseBody> fetch(
    RequestOptions options,
    Stream<Uint8List>? requestStream,
    Future<void>? cancelFuture,
  ) async {
    return ResponseBody.fromString(
      '{"tokenType":"Bearer","accessToken":"access",'
      '"refreshToken":"refresh",'
      '"accessTokenExpiresAt":"2030-01-01T00:00:00Z",'
      '"refreshTokenExpiresAt":"2030-02-01T00:00:00Z"}',
      200,
      headers: <String, List<String>>{
        Headers.contentTypeHeader: <String>[Headers.jsonContentType],
      },
    );
  }

  @override
  void close({bool force = false}) {}
}

class _DeferredShopAudits {
  final calls = <AdminAuditHistoryQuery>[];
  final _pending = <String, Completer<AdminBindingAuditHistoryPage>>{};

  Future<AdminBindingAuditHistoryPage> load(AdminAuditHistoryQuery query) {
    calls.add(query);
    final completer = Completer<AdminBindingAuditHistoryPage>();
    _pending[query.shopId] = completer;
    return completer.future;
  }

  void complete(String shopId) {
    _pending.remove(shopId)?.complete(
          AdminBindingAuditHistoryPage(
            items: [
              AdminBindingAudit.fromJson(
                _audit('bind',
                    pointName:
                        '${shopId == 'shop-a' ? 'Shop A' : 'Shop B'} response'),
              ),
            ],
          ),
        );
  }
}

class _AuditAdapter implements HttpClientAdapter {
  final requests = <RequestOptions>[];
  final _deferred = <String, Completer<ResponseBody>>{};
  bool empty = false;
  bool failNext = false;

  List<RequestOptions> get auditRequests => requests
      .where((request) => request.path.endsWith('/admin/binding-audits'))
      .toList();

  _AuditAdapter deferNext(String shopId) {
    _deferred[shopId] = Completer<ResponseBody>();
    return this;
  }

  void complete(String shopId) {
    final pending = _deferred.remove(shopId);
    pending?.complete(_responseFor(shopId, const {}));
  }

  @override
  Future<ResponseBody> fetch(
    RequestOptions options,
    Stream<Uint8List>? requestStream,
    Future<void>? cancelFuture,
  ) async {
    requests.add(options);
    if (options.path == '/api/v1/auth/login') {
      return ResponseBody.fromString(
        '{"tokenType":"Bearer","accessToken":"access","refreshToken":"refresh","accessTokenExpiresAt":"2099-01-01T00:00:00Z","refreshTokenExpiresAt":"2099-02-01T00:00:00Z"}',
        200,
        headers: {
          Headers.contentTypeHeader: [Headers.jsonContentType],
        },
      );
    }
    if (!options.path.endsWith('/admin/binding-audits')) {
      throw StateError('Unexpected request ${options.path}');
    }
    final shopId = options.path.split('/')[4];
    final pending = _deferred[shopId];
    if (pending != null) return pending.future;
    if (failNext) {
      failNext = false;
      throw DioException(
        requestOptions: options,
        response: Response<void>(requestOptions: options, statusCode: 500),
      );
    }
    return _responseFor(shopId, options.queryParameters);
  }

  ResponseBody _responseFor(String shopId, Map<Object?, Object?> query) {
    final cursor = query['cursor'];
    final measurementPointId = query['measurementPointId'];
    final deviceId = query['deviceId'];
    final items = empty
        ? const <Map<String, dynamic>>[]
        : cursor != null
            ? [_audit('unbind', pointName: 'Unbind point')]
            : measurementPointId != null || deviceId != null
                ? [_audit('bind', pointName: 'Filtered point')]
                : [
                    _audit(
                      'replace',
                      pointName: 'Replacement point',
                      deviceName: 'Replacement device',
                    ),
                    _audit(
                      'relocate',
                      pointName: 'Source point',
                      newPointName: 'Target point',
                      deviceName: 'Relocated device',
                    ),
                  ];
    if (items.isNotEmpty && shopId == 'shop-a') {
      items[0]['measurementPoint'] ??= <String, String>{
        'id': 'mp-a',
        'currentDisplayName': 'Shop A response',
      };
    }
    if (items.isNotEmpty && shopId == 'shop-b') {
      items[0]['measurementPoint'] ??= <String, String>{
        'id': 'mp-b',
        'currentDisplayName': 'Shop B response',
      };
    }
    final body = <String, dynamic>{
      'items': items,
      if (cursor == null && !empty) 'nextCursor': 'cursor-2',
    };
    return ResponseBody.fromString(
      jsonEncode(body),
      200,
      headers: {
        Headers.contentTypeHeader: [Headers.jsonContentType],
      },
    );
  }

  @override
  void close({bool force = false}) {}
}

Map<String, dynamic> _audit(
  String action, {
  String pointName = 'Point',
  String? newPointName,
  String deviceName = 'Device',
}) {
  final point = <String, dynamic>{
    'id': 'mp-old',
    'currentDisplayName': pointName,
  };
  return {
    'id': 'audit-$action',
    'operationId': 'operation-$action',
    'action': action,
    'occurredAt': '2026-01-01T00:00:00Z',
    'effectiveAt': null,
    'reason': action == 'replace' ? 'replacement' : null,
    'actor': {'id': 'actor-1', 'currentDisplayName': 'Admin'},
    'measurementPoint': action == 'bind' ? point : null,
    'device': {
      'id': action == 'replace' ? 'new-device' : 'device-1',
      'serialNumber': 'SERIAL',
      'mac': 'AABBCCDDEEFF',
      'currentDisplayName': deviceName,
    },
    'oldMeasurementPoint':
        action == 'relocate' || action == 'unbind' ? point : null,
    'newMeasurementPoint': action == 'relocate'
        ? {'id': 'mp-new', 'currentDisplayName': newPointName ?? 'Target point'}
        : action == 'replace'
            ? point
            : null,
    'oldAssignmentId': action == 'relocate' ? 'old-assignment' : null,
    'newAssignmentId': action == 'relocate' ? 'new-assignment' : null,
  };
}

const _shops = <Shop>[
  Shop(
    id: 'shop-a',
    code: 'A',
    name: 'Shop A',
    address: null,
    phone: null,
    isHead: false,
  ),
  Shop(
    id: 'shop-b',
    code: 'B',
    name: 'Shop B',
    address: null,
    phone: null,
    isHead: false,
  ),
];
