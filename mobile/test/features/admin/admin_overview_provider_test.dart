import 'dart:async';
import 'dart:typed_data';

import 'package:dio/dio.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:power_iot_app/core/network/authenticated_http_client.dart';
import 'package:power_iot_app/features/admin/domain/models/admin_overview.dart';
import 'package:power_iot_app/features/admin/domain/models/device_assignment.dart';
import 'package:power_iot_app/features/admin/domain/models/measurement_point.dart';
import 'package:power_iot_app/features/admin/domain/repositories/admin_overview_repository.dart';
import 'package:power_iot_app/features/admin/presentation/providers/admin_overview_provider.dart';
import 'package:power_iot_app/features/auth/auth_controller.dart';
import 'package:power_iot_app/features/shops/domain/models/shop.dart';
import 'package:power_iot_app/features/shops/domain/repositories/shops_repository.dart';
import 'package:power_iot_app/features/profile/presentation/providers/profile_provider.dart';
import 'package:power_iot_app/features/shops/providers/remote_shop_provider.dart';

void main() {
  test('late old-Shop overview cannot overwrite the newly selected Shop',
      () async {
    final client = _authenticatedClient();
    final auth = AuthController(client);
    await auth.login(account: 'admin', password: 'password');

    final shopsRepository = _ControllableShopsRepository();
    final shopsNotifier = ShopsNotifier(shopsRepository, client);
    final shopsReady = shopsNotifier.stream.firstWhere(
      (state) => state.status == RemoteStatus.success,
    );
    shopsRepository.initial.complete(_shopsSnapshot);
    await shopsReady;

    final shopA = _DeferredAdminOverviewRepository(
      request: Completer<void>(),
      result: Completer<AdminOverview>(),
    );
    final shopB = _DeferredAdminOverviewRepository(
      request: Completer<void>(),
      result: Completer<AdminOverview>(),
    );
    const aOverview = AdminOverview(
      measurementPoints: [
        MeasurementPoint(id: 'mp-a', shopId: 'shop-a', name: 'Shop A MP'),
      ],
      devices: [],
    );
    const bOverview = AdminOverview(
      measurementPoints: [
        MeasurementPoint(id: 'mp-b', shopId: 'shop-b', name: 'Shop B MP'),
      ],
      devices: [],
    );
    final container = ProviderContainer(
      overrides: [
        authClientProvider.overrideWithValue(client),
        authControllerProvider.overrideWith((ref) => auth),
        shopsProvider.overrideWith((ref) => shopsNotifier),
        adminOverviewRepositoryProvider.overrideWith((ref) {
          final shopId = selectedAdminShopId(ref.watch(shopsProvider));
          return shopId == 'shop-a' ? shopA : shopB;
        }),
      ],
    );
    addTearDown(() {
      container.dispose();
      client.close();
    });

    final providerSubscription = container.listen<AsyncValue<AdminOverview>>(
      adminOverviewProvider,
      (_, __) {},
      fireImmediately: true,
    );
    addTearDown(providerSubscription.close);

    await shopA.request.future;
    shopsNotifier.selectShop('shop-b');
    await shopB.request.future;

    shopA.result.complete(aOverview);
    await Future<void>.delayed(Duration.zero);
    shopB.result.complete(bOverview);

    final result = await container.read(adminOverviewProvider.future);
    expect(result, same(bOverview));
    expect(shopA.loadCalls, 1);
    expect(shopB.loadCalls, 1);
  });
}

const _shopsSnapshot = ShopsSnapshot(
  shops: [
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
  ],
  currentShopId: 'shop-a',
);

class _ControllableShopsRepository implements ShopsRepository {
  final Completer<ShopsSnapshot> initial = Completer<ShopsSnapshot>();

  @override
  Future<ShopsSnapshot> fetchShops() => initial.future;
}

class _DeferredAdminOverviewRepository implements AdminOverviewRepository {
  _DeferredAdminOverviewRepository(
      {required this.request, required this.result});

  final Completer<void> request;
  final Completer<AdminOverview> result;
  var loadCalls = 0;

  @override
  Future<AdminOverview> loadOverview() {
    loadCalls++;
    if (!request.isCompleted) request.complete();
    return result.future;
  }

  Future<T> _unused<T>() => Future<T>.error(
        UnsupportedError('mutation is not used by this test repository'),
      );

  @override
  Future<MeasurementPoint> createMeasurementPoint(
    CreateMeasurementPointInput input,
  ) =>
      _unused();

  @override
  Future<DeviceAssignment> bindDevice(BindDeviceInput input) => _unused();

  @override
  Future<DeviceAssignment> replaceDevice(ReplaceDeviceInput input) => _unused();

  @override
  Future<DeviceAssignment> relocateDevice(RelocateDeviceInput input) =>
      _unused();

  @override
  Future<DeviceAssignment> unbindDevice(UnbindDeviceInput input) => _unused();

  @override
  Future<List<DeviceAssignment>> loadAssignmentHistory() => _unused();
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

class _MemoryStore implements RefreshTokenStore {
  String? token;

  @override
  Future<String?> read() async => token;

  @override
  Future<void> write(String value) async => token = value;

  @override
  Future<void> clear() async => token = null;
}
