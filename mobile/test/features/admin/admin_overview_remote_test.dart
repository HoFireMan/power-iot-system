import 'dart:async';
import 'dart:typed_data';

import 'package:dio/dio.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:power_iot_app/core/network/authenticated_http_client.dart';
import 'package:power_iot_app/features/admin/data/dtos/admin_overview_dto.dart';
import 'package:power_iot_app/features/admin/data/repositories/admin_overview_repository_impl.dart';
import 'package:power_iot_app/features/admin/domain/repositories/admin_overview_repository.dart';
import 'package:power_iot_app/features/admin/domain/models/device_ref.dart';
import 'package:power_iot_app/features/admin/presentation/providers/admin_overview_provider.dart';
import 'package:power_iot_app/features/shops/domain/models/shop.dart';
import 'package:power_iot_app/features/shops/providers/remote_shop_provider.dart';
import 'package:power_iot_app/features/shops/domain/repositories/shops_repository.dart';

class _Adapter implements HttpClientAdapter {
  _Adapter(this.body);

  final String body;
  RequestOptions? request;

  @override
  Future<ResponseBody> fetch(
    RequestOptions options,
    Stream<Uint8List>? requestStream,
    Future<void>? cancelFuture,
  ) async {
    request = options;
    return ResponseBody.fromString(body, 200, headers: <String, List<String>>{
      Headers.contentTypeHeader: <String>[Headers.jsonContentType],
    });
  }

  @override
  void close({bool force = false}) {}
}

void main() {
  test('admin overview falls back to the first authorized shop', () {
    const first = Shop(
      id: '11',
      code: 'FIRST',
      name: 'First shop',
      address: null,
      phone: null,
      isHead: false,
    );
    const second = Shop(
      id: '22',
      code: 'SECOND',
      name: 'Second shop',
      address: null,
      phone: null,
      isHead: false,
    );
    const state = ShopsState.success(
      ShopsSnapshot(shops: <Shop>[first, second], currentShopId: '22'),
    );
    expect(selectedAdminShopId(state), '11');
    expect(selectedAdminShop(state)?.name, 'First shop');
    expect(selectedAdminShopId(state.withSelection('22')), '22');
  });

  test('remote Shop refresh preserves a still-authorized local selection',
      () async {
    const first = Shop(
      id: '11',
      code: 'FIRST',
      name: 'First shop',
      address: null,
      phone: null,
      isHead: false,
    );
    const second = Shop(
      id: '22',
      code: 'SECOND',
      name: 'Second shop',
      address: null,
      phone: null,
      isHead: false,
    );
    final repository = _RefreshingShopsRepository(
      first: const ShopsSnapshot(
        shops: <Shop>[first, second],
        currentShopId: '11',
      ),
      refreshed: const ShopsSnapshot(
        shops: <Shop>[first, second],
        currentShopId: '11',
      ),
    );
    final client = AuthenticatedHttpClient(
      baseUrl: Uri.parse('https://development.invalid'),
      session: AuthSession(_MemoryStore()),
    );
    await client.session.setTokens(_pair());
    final notifier = ShopsNotifier(repository, client);
    final initial = notifier.stream.firstWhere((state) => state.data != null);
    repository.firstLoad.complete();
    await initial;
    notifier.selectShop('22');

    final refreshed = notifier.stream.firstWhere((state) => state.data != null);
    await notifier.load();
    await refreshed;
    expect(notifier.state.selectedShopId, '22');
    expect(selectedAdminShopId(notifier.state), '22');
    notifier.dispose();
    client.close();
  });

  test('overview DTO rejects extra fields and parses the complete projection',
      () {
    final dto = AdminOverviewDto.fromJson(<String, Object?>{
      'measurementPoints': <Object?>[
        <String, Object?>{'id': 'mp', 'shopId': '7', 'name': 'Kitchen'},
      ],
      'devices': <Object?>[],
      'activeAssignments': <Object?>[],
      'assignmentHistory': <Object?>[],
    });
    expect(dto.measurementPoints.single.shopId, '7');
    expect(
      () => AdminOverviewDto.fromJson(<String, Object?>{
        'measurementPoints': <Object?>[],
        'devices': <Object?>[],
        'activeAssignments': <Object?>[],
        'assignmentHistory': <Object?>[],
        'unexpected': true,
      }),
      throwsFormatException,
    );
  });

  test('overview timestamps require RFC3339 date-time with UTC or offset', () {
    expect(parseAdminDate('2026-01-02T03:04:05Z'),
        DateTime.utc(2026, 1, 2, 3, 4, 5));
    expect(parseAdminDate('2026-01-02T03:04:05+08:00'),
        DateTime.utc(2026, 1, 1, 19, 4, 5));
    for (final value in <String>[
      '2026-01-02',
      '2026-01-02T03:04:05',
      '2026-01-02 03:04:05Z',
      '2026-01-02T03:04:05+0800',
    ]) {
      expect(() => parseAdminDate(value), throwsFormatException);
    }
  });

  test('overview accepts legacy devices with an empty serial', () {
    final dto = AdminOverviewDto.fromJson(<String, Object?>{
      'measurementPoints': <Object?>[],
      'devices': <Object?>[
        <String, Object?>{
          'id': '7',
          'name': 'Legacy Device',
          'serialNumber': '',
          'macAddress': 'AA0011223344',
          'status': 'Offline',
        },
      ],
      'activeAssignments': <Object?>[],
      'assignmentHistory': <Object?>[],
    });
    expect(dto.devices.single.serialNumber, isEmpty);
  });

  test('remote overview sends bearer and server Shop ID', () async {
    final adapter = _Adapter(
      '{"measurementPoints":[],"devices":[],"activeAssignments":[],"assignmentHistory":[]}',
    );
    final dio = Dio(BaseOptions(baseUrl: 'https://development.invalid'))
      ..httpClientAdapter = adapter;
    final client = AuthenticatedHttpClient(
      baseUrl: Uri.parse('https://development.invalid'),
      session: AuthSession(_MemoryStore()),
      dio: dio,
    );
    await client.session.setTokens(_pair());
    await RemoteAdminOverviewRepository(client, '42').loadOverview();
    expect(adapter.request?.headers['Authorization'], 'Bearer access');
    expect(adapter.request?.queryParameters['shopId'], '42');
    client.close();
  });

  test('remote mutation rejects extra fields and an unexpected action',
      () async {
    for (final body in <String>[
      '{"operationId":"op","action":"create_measurement_point","measurementPointId":"mp","extra":true}',
      '{"operationId":"op","action":"bind","measurementPointId":"mp"}',
    ]) {
      final adapter = _Adapter(body);
      final client = AuthenticatedHttpClient(
        baseUrl: Uri.parse('https://development.invalid'),
        session: AuthSession(_MemoryStore()),
        dio: Dio(BaseOptions(baseUrl: 'https://development.invalid'))
          ..httpClientAdapter = adapter,
      );
      await client.session.setTokens(_pair());
      await expectLater(
        RemoteAdminOverviewRepository(client, '42').createMeasurementPoint(
          const CreateMeasurementPointInput(
            requestIdentity: 'create-1',
            shopId: '42',
            name: 'Kitchen',
          ),
        ),
        throwsFormatException,
      );
      client.close();
    }
  });

  test('remote mutation rejects non-RFC3339 effectiveAt', () async {
    final adapter = _Adapter(
      '{"operationId":"op-1","action":"bind","deviceId":"7",'
      '"newMeasurementPointId":"point-1","newAssignmentId":"assignment-1",'
      '"effectiveAt":"2026-01-02"}',
    );
    final client = AuthenticatedHttpClient(
      baseUrl: Uri.parse('https://development.invalid'),
      session: AuthSession(_MemoryStore()),
      dio: Dio(BaseOptions(baseUrl: 'https://development.invalid'))
        ..httpClientAdapter = adapter,
    );
    await client.session.setTokens(_pair());
    await expectLater(
      RemoteAdminOverviewRepository(client, '42').bindDevice(
        const BindDeviceInput(
          requestIdentity: 'bind-1',
          deviceRef: DeviceRef(serialNumber: 'SERIAL-1'),
          measurementPointId: 'point-1',
        ),
      ),
      throwsFormatException,
    );
    client.close();
  });

  test('remote unbind preserves the overview validFrom interval', () async {
    final adapter = _RoutingAdapter(
      overview: '{"measurementPoints":[],"devices":[],"activeAssignments":[],'
          '"assignmentHistory":[{"id":"assignment-1","deviceId":"7",'
          '"measurementPointId":"point-1","validFrom":"2026-01-01T00:00:00Z",'
          '"validTo":null}]}',
      mutation: '{"operationId":"op-1","action":"unbind","deviceId":"7",'
          '"oldMeasurementPointId":"point-1","oldAssignmentId":"assignment-1",'
          '"effectiveAt":"2026-01-02T00:00:00Z"}',
    );
    final client = AuthenticatedHttpClient(
      baseUrl: Uri.parse('https://development.invalid'),
      session: AuthSession(_MemoryStore()),
      dio: Dio(BaseOptions(baseUrl: 'https://development.invalid'))
        ..httpClientAdapter = adapter,
    );
    await client.session.setTokens(_pair());
    final result =
        await RemoteAdminOverviewRepository(client, '42').unbindDevice(
      const UnbindDeviceInput(
        requestIdentity: 'unbind-1',
        currentAssignmentId: 'assignment-1',
      ),
    );
    expect(result.validFrom, DateTime.utc(2026, 1, 1));
    expect(result.validTo, DateTime.utc(2026, 1, 2));
    client.close();
  });

  test('admin errors are safe and distinguish conflict from authorization', () {
    final forbidden = DioException(
      requestOptions: RequestOptions(path: '/admin'),
      response: Response<dynamic>(
        requestOptions: RequestOptions(path: '/admin'),
        statusCode: 403,
        data: <String, Object?>{'code': 'FORBIDDEN', 'message': 'raw SQL'},
      ),
    );
    final conflict = DioException(
      requestOptions: RequestOptions(path: '/admin'),
      response: Response<dynamic>(
        requestOptions: RequestOptions(path: '/admin'),
        statusCode: 409,
        data: <String, Object?>{'code': 'CONFLICT', 'message': 'raw SQL'},
      ),
    );
    expect(adminErrorMessage(forbidden, 'fallback'), contains('authorized'));
    expect(adminErrorMessage(conflict, 'fallback'), contains('conflicts'));
    expect(adminErrorMessage(forbidden, 'fallback'), isNot(contains('SQL')));
  });
}

class _RefreshingShopsRepository implements ShopsRepository {
  _RefreshingShopsRepository({required this.first, required this.refreshed});

  final ShopsSnapshot first;
  final ShopsSnapshot refreshed;
  final firstLoad = Completer<void>();
  var requests = 0;

  @override
  Future<ShopsSnapshot> fetchShops() async {
    requests++;
    if (requests == 1) {
      await firstLoad.future;
      return first;
    }
    return refreshed;
  }
}

class _RoutingAdapter implements HttpClientAdapter {
  _RoutingAdapter({required this.overview, required this.mutation});

  final String overview;
  final String mutation;

  @override
  Future<ResponseBody> fetch(
    RequestOptions options,
    Stream<Uint8List>? requestStream,
    Future<void>? cancelFuture,
  ) async {
    return ResponseBody.fromString(
      options.method == 'GET' ? overview : mutation,
      200,
      headers: <String, List<String>>{
        Headers.contentTypeHeader: <String>[Headers.jsonContentType],
      },
    );
  }

  @override
  void close({bool force = false}) {}
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

TokenPair _pair() => TokenPair(
      accessToken: 'access',
      refreshToken: 'refresh',
      accessTokenExpiresAt: DateTime.now().add(const Duration(hours: 1)),
      refreshTokenExpiresAt: DateTime.now().add(const Duration(days: 1)),
    );
