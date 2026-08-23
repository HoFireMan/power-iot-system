import 'dart:typed_data';

import 'package:dio/dio.dart';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:power_iot_app/core/network/authenticated_http_client.dart';
import 'package:power_iot_app/features/auth/auth_controller.dart';
import 'package:power_iot_app/features/dashboard/domain/models/dashboard.dart';
import 'package:power_iot_app/features/dashboard/domain/repositories/dashboard_repository.dart';
import 'package:power_iot_app/features/dashboard/presentation/providers/dashboard_provider.dart';
import 'package:power_iot_app/features/devices/presentation/screens/device_list_screen.dart';
import 'package:power_iot_app/features/shops/domain/models/shop.dart';
import 'package:power_iot_app/features/shops/domain/repositories/shops_repository.dart';
import 'package:power_iot_app/features/shops/providers/remote_shop_provider.dart';

class _Store implements RefreshTokenStore {
  @override
  Future<String?> read() async => null;
  @override
  Future<void> write(String token) async {}
  @override
  Future<void> clear() async {}
}

class _Adapter implements HttpClientAdapter {
  @override
  Future<ResponseBody> fetch(RequestOptions options, Stream<Uint8List>? stream,
      Future<void>? cancelFuture) async {
    return ResponseBody.fromString('', 204);
  }

  @override
  void close({bool force = false}) {}
}

class _Dashboard implements DashboardRepository {
  _Dashboard(this.value);
  final Dashboard value;
  @override
  Future<Dashboard> fetchDashboard(String shopId) async {
    expect(shopId, 'shop-1');
    return value;
  }
}

Dashboard _dashboard(List<DashboardDevice> devices) => Dashboard(
      shop: const DashboardShop(id: 'shop-1', code: 'S1', name: 'Remote shop'),
      generatedAt: DateTime.utc(2026),
      currentPowerW: null,
      dailyKwh: null,
      monthlyKwh: null,
      dailyKg: null,
      monthlyKg: null,
      devices: devices,
    );

const _shop = Shop(
  id: 'shop-1',
  code: 'S1',
  name: 'Remote shop',
  address: null,
  phone: null,
  isHead: false,
);

Future<void> _pump(WidgetTester tester, Dashboard dashboard) async {
  final client = AuthenticatedHttpClient(
    baseUrl: Uri.parse('https://test.invalid'),
    session: AuthSession(_Store()),
    dio: Dio(BaseOptions(baseUrl: 'https://test.invalid'))
      ..httpClientAdapter = _Adapter(),
  );
  await tester.pumpWidget(
    ProviderScope(
      overrides: [
        authClientProvider.overrideWithValue(client),
        shopsRepositoryProvider.overrideWithValue(
          const _ShopsSnapshotProvider(),
        ),
        dashboardRepositoryProvider.overrideWithValue(_Dashboard(dashboard)),
      ],
      child: const MaterialApp(home: DeviceListScreen()),
    ),
  );
  await tester.pumpAndSettle();
}

class _ShopsSnapshotProvider implements ShopsRepository {
  const _ShopsSnapshotProvider();
  @override
  Future<ShopsSnapshot> fetchShops() async =>
      const ShopsSnapshot(shops: [_shop], currentShopId: 'shop-1');
}

void main() {
  testWidgets('device management renders B7 online/offline and null lastSeen',
      (tester) async {
    await _pump(
      tester,
      _dashboard(const [
        DashboardDevice(
            id: 'remote-online',
            name: 'Remote online',
            isOnline: true,
            lastSeen: null),
        DashboardDevice(
            id: 'remote-offline',
            name: 'Remote offline',
            isOnline: false,
            lastSeen: null),
      ]),
    );

    expect(find.text('Remote online'), findsOneWidget);
    expect(find.text('Remote offline'), findsOneWidget);
    expect(find.text('最後上線時間未知'), findsNWidgets(2));
    expect(find.text('其他 4AAB 插座'), findsNothing);
    expect(find.text('冷氣 4682'), findsNothing);
  });

  testWidgets('device management renders the remote empty state',
      (tester) async {
    await _pump(tester, _dashboard(const []));
    expect(find.text('目前沒有設備'), findsOneWidget);
  });
}
