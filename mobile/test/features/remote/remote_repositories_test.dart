import 'dart:async';
import 'dart:convert';
import 'dart:typed_data';

import 'package:dio/dio.dart';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:power_iot_app/core/network/authenticated_http_client.dart';
import 'package:power_iot_app/features/auth/auth_controller.dart';
import 'package:power_iot_app/core/network/remote_error.dart';
import 'package:power_iot_app/features/profile/data/repositories/profile_repository_impl.dart';
import 'package:power_iot_app/features/profile/domain/models/user_profile.dart';
import 'package:power_iot_app/features/profile/domain/repositories/profile_repository.dart';
import 'package:power_iot_app/features/profile/presentation/providers/profile_provider.dart';
import 'package:power_iot_app/features/profile/screens/profile_screen.dart';
import 'package:power_iot_app/features/shops/data/repositories/shops_repository_impl.dart';
import 'package:power_iot_app/features/shops/domain/models/shop.dart';
import 'package:power_iot_app/features/shops/domain/repositories/shops_repository.dart';
import 'package:power_iot_app/features/shops/providers/remote_shop_provider.dart';
import 'package:power_iot_app/features/shops/screens/shop_list_screen.dart';

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
        Headers.contentTypeHeader: ['application/json']
      },
    );

AuthenticatedHttpClient _client(_Adapter adapter, _Store store) {
  final session = AuthSession(store);
  return AuthenticatedHttpClient(
    baseUrl: Uri.parse('https://test.invalid'),
    session: session,
    dio: Dio(BaseOptions(baseUrl: 'https://test.invalid'))
      ..httpClientAdapter = adapter,
  );
}

Map<String, Object?> _profileJson() => {
      'id': '7',
      'account': 'assistant',
      'name': 'Remote User',
      'email': null,
      'phone': '+886900000000',
      'isAdmin': false,
      'currentShopId': null,
    };

TokenPair _pair(String access, String refresh) => TokenPair(
      accessToken: access,
      refreshToken: refresh,
      accessTokenExpiresAt: DateTime.utc(2030),
      refreshTokenExpiresAt: DateTime.utc(2030, 2),
    );

void main() {
  test('GET /me parses safe nullable profile fields', () async {
    late _Adapter adapter;
    adapter = _Adapter((request) async {
      expect(request.uri.path, '/api/v1/me');
      expect(request.headers['Authorization'], 'Bearer access');
      return _json(200, _profileJson());
    });
    final store = _Store();
    final client = _client(adapter, store);
    await client.session.setTokens(TokenPair(
      accessToken: 'access',
      refreshToken: 'refresh',
      accessTokenExpiresAt: DateTime.utc(2030),
      refreshTokenExpiresAt: DateTime.utc(2030, 2),
    ));

    final profile = await RemoteProfileRepository(client).fetchProfile();
    expect(profile, isA<UserProfile>());
    expect(profile.currentShopId, isNull);
    expect(profile.email, isNull);
    expect(profile.phone, '+886900000000');
  });

  test('GET /shops accepts empty shops and null currentShopId', () async {
    final adapter = _Adapter((request) async {
      expect(request.uri.path, '/api/v1/shops');
      return _json(200, {'shops': [], 'currentShopId': null});
    });
    final store = _Store();
    final client = _client(adapter, store);
    final shops = await RemoteShopsRepository(client).fetchShops();

    expect(shops.shops, isEmpty);
    expect(shops.currentShopId, isNull);
  });

  test('GET /shops parses authorized shop fields without admin expansion', () {
    final snapshot = ShopsSnapshot.fromJson({
      'shops': [
        {
          'id': '8',
          'code': 'SHOP-8',
          'name': 'Authorized Shop',
          'address': null,
          'phone': null,
          'isHead': true,
        },
      ],
      'currentShopId': '8',
    });
    expect(snapshot.shops, hasLength(1));
    expect(snapshot.shops.single.id, '8');
    expect(snapshot.currentShopId, '8');
  });

  test('GET /shops accepts an optional tariff field', () {
    final shop = Shop.fromJson({
      'id': '1',
      'code': 'devseed-shop',
      'name': 'Development Seed Shop',
      'address': null,
      'phone': null,
      'isHead': false,
      'tariff': 'LIGHTING_COMMERCIAL',
    });

    expect(shop.tariff, 'LIGHTING_COMMERCIAL');
  });

  test('GET /shops accepts an explicitly null tariff', () {
    final shop = Shop.fromJson({
      'id': '1',
      'code': 'devseed-shop',
      'name': 'Development Seed Shop',
      'address': null,
      'phone': null,
      'isHead': false,
      'tariff': null,
    });

    expect(shop.tariff, isNull);
  });

  test('GET /shops rejects unknown fields', () {
    expect(
      () => Shop.fromJson({
        'id': '1',
        'code': 'devseed-shop',
        'name': 'Development Seed Shop',
        'address': null,
        'phone': null,
        'isHead': false,
        'unexpected': true,
      }),
      throwsFormatException,
    );
  });

  test('GET /shops rejects a missing required field', () {
    final missingId = <String, Object?>{
      'code': 'devseed-shop',
      'name': 'Development Seed Shop',
      'address': null,
      'phone': null,
      'isHead': false,
    };
    final missingIsHead = <String, Object?>{
      'id': '1',
      'code': 'devseed-shop',
      'name': 'Development Seed Shop',
      'address': null,
      'phone': null,
    };

    expect(() => Shop.fromJson(missingId), throwsFormatException);
    expect(() => Shop.fromJson(missingIsHead), throwsFormatException);
  });

  test('GET /shops rejects a malformed tariff', () {
    expect(
      () => Shop.fromJson({
        'id': '1',
        'code': 'devseed-shop',
        'name': 'Development Seed Shop',
        'address': null,
        'phone': null,
        'isHead': false,
        'tariff': 466,
      }),
      throwsFormatException,
    );
  });

  test('GET /shops snapshot accepts a shop with tariff', () {
    final snapshot = ShopsSnapshot.fromJson({
      'shops': [
        {
          'id': '1',
          'code': 'devseed-shop',
          'name': 'Development Seed Shop',
          'address': null,
          'phone': null,
          'isHead': false,
          'tariff': 'LIGHTING_COMMERCIAL',
        },
      ],
      'currentShopId': '1',
    });

    expect(snapshot.shops.single.tariff, 'LIGHTING_COMMERCIAL');
  });

  test('malformed remote shapes do not fabricate profile or shop data', () {
    expect(() => UserProfile.fromJson({'name': 'only'}), throwsFormatException);
    expect(() => ShopsSnapshot.fromJson({'shops': []}), throwsFormatException);
  });

  test('unrecoverable protected 401 clears the auth session', () async {
    final adapter = _Adapter((request) async => _json(401, {}));
    final store = _Store();
    final client = _client(adapter, store);
    await client.session.setTokens(TokenPair(
      accessToken: 'expired',
      refreshToken: 'refresh',
      accessTokenExpiresAt: DateTime.utc(2030),
      refreshTokenExpiresAt: DateTime.utc(2030, 2),
    ));

    await expectLater(
      RemoteProfileRepository(client).fetchProfile(),
      throwsA(isA<DioException>()),
    );
    expect(client.session.isAuthenticated, isFalse);
    expect(client.session.accessToken, isNull);
    expect(store.value, isNull);
  });

  test('profile notifier ignores stale 401 after a newer login', () async {
    final releaseOldRequest = Completer<void>();
    var calls = 0;
    final repository = _ProfileFake(() async {
      calls++;
      if (calls == 1) {
        await releaseOldRequest.future;
        throw const UnauthorizedException();
      }
      return const UserProfile(
        id: 'new',
        account: 'user-b',
        name: 'User B',
        email: null,
        phone: null,
        isAdmin: false,
        currentShopId: null,
      );
    });
    final store = _Store();
    final client = _client(_Adapter((_) async => _json(204, {})), store);
    final notifier = ProfileNotifier(repository, client);

    client.beginSession();
    await client.session.setTokens(_pair('new-access', 'new-refresh'));
    releaseOldRequest.complete();
    await Future<void>.delayed(Duration.zero);
    await Future<void>.delayed(Duration.zero);

    expect(client.session.accessToken, 'new-access');
    expect(store.value, 'new-refresh');
    expect(notifier.state.data?.account, 'user-b');
    notifier.dispose();
  });

  test('profile notifier rejects stale success and resets on logout/login',
      () async {
    final releaseOldRequest = Completer<void>();
    final releaseNewRequest = Completer<void>();
    var calls = 0;
    final repository = _ProfileFake(() async {
      calls++;
      if (calls == 1) {
        await releaseOldRequest.future;
        return const UserProfile(
          id: 'old',
          account: 'user-a',
          name: 'User A',
          email: null,
          phone: null,
          isAdmin: false,
          currentShopId: null,
        );
      }
      if (calls == 2) {
        await releaseNewRequest.future;
      }
      return const UserProfile(
        id: 'new',
        account: 'user-b',
        name: 'User B',
        email: null,
        phone: null,
        isAdmin: false,
        currentShopId: null,
      );
    });
    final store = _Store();
    final client = _client(_Adapter((_) async => _json(204, {})), store);
    final notifier = ProfileNotifier(repository, client);
    await Future<void>.delayed(Duration.zero);

    client.beginSession();
    await client.session.setTokens(_pair('new-access', 'new-refresh'));
    releaseOldRequest.complete();
    await Future<void>.delayed(Duration.zero);
    expect(notifier.state.data, isNull);

    releaseNewRequest.complete();
    await Future<void>.delayed(Duration.zero);
    expect(notifier.state.data?.account, 'user-b');

    await client.logout();
    expect(notifier.state.data, isNull);
    expect(client.session.isAuthenticated, isFalse);
    notifier.dispose();
  });

  test('profile notifier represents loading then error without fallback data',
      () async {
    final release = Completer<void>();
    final repository = _ProfileFake(() async {
      await release.future;
      throw StateError('offline');
    });
    final store = _Store();
    final client = _client(_Adapter((_) async => _json(500, {})), store);
    final notifier = ProfileNotifier(repository, client);
    expect(notifier.state.status, RemoteStatus.loading);
    release.complete();
    await Future<void>.delayed(Duration.zero);
    await Future<void>.delayed(Duration.zero);
    expect(notifier.state.status, RemoteStatus.error);
    expect(notifier.state.data, isNull);
    notifier.dispose();
  });

  testWidgets('shop screen renders remote empty state without mock shops',
      (tester) async {
    final store = _Store();
    final client = _client(_Adapter((_) async => _json(500, {})), store);
    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          authClientProvider.overrideWithValue(client),
          shopsRepositoryProvider.overrideWithValue(_ShopsFake(
              () async => const ShopsSnapshot(shops: [], currentShopId: null))),
        ],
        child: const MaterialApp(home: ShopListScreen()),
      ),
    );
    await tester.pumpAndSettle();
    expect(find.text('目前沒有可用店家'), findsOneWidget);
    expect(find.textContaining('伺服器授權'), findsOneWidget);
  });

  testWidgets('admin profile binding menu opens the real admin route',
      (tester) async {
    final store = _Store();
    final client = _client(_Adapter((_) async => _json(200, {})), store);
    final router = GoRouter(
      initialLocation: '/profile',
      routes: [
        GoRoute(
          path: '/profile',
          builder: (context, state) => const ProfileScreen(),
        ),
        GoRoute(
          path: '/admin',
          builder: (context, state) => const Text('real admin route'),
        ),
      ],
    );
    addTearDown(router.dispose);
    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          authClientProvider.overrideWithValue(client),
          profileRepositoryProvider.overrideWithValue(_ProfileFake(() async {
            return const UserProfile(
              id: '1',
              account: 'admin',
              name: 'Admin',
              email: null,
              phone: null,
              isAdmin: true,
              currentShopId: null,
            );
          })),
        ],
        child: MaterialApp.router(routerConfig: router),
      ),
    );
    await tester.pumpAndSettle();
    final bindingMenu = find.text('綁定感測器');
    await tester.ensureVisible(bindingMenu);
    await tester.tap(bindingMenu);
    await tester.pumpAndSettle();
    expect(find.text('real admin route'), findsOneWidget);
    client.close();
  });

  testWidgets('profile screen renders loading before remote profile success',
      (tester) async {
    final release = Completer<void>();
    final store = _Store();
    final client = _client(_Adapter((_) async => _json(500, {})), store);
    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          authClientProvider.overrideWithValue(client),
          profileRepositoryProvider.overrideWithValue(_ProfileFake(() async {
            await release.future;
            return const UserProfile(
              id: '1',
              account: 'a',
              name: 'Actual Name',
              email: null,
              phone: null,
              isAdmin: false,
              currentShopId: null,
            );
          })),
        ],
        child: const MaterialApp(home: ProfileScreen()),
      ),
    );
    expect(find.byType(CircularProgressIndicator), findsOneWidget);
    release.complete();
    await tester.pumpAndSettle();
    expect(find.text('Hi! Actual Name'), findsOneWidget);
    expect(find.text('尚未設定主要店家'), findsOneWidget);
  });

  test('shops notifier rejects stale success after a newer login', () async {
    final releaseOldRequest = Completer<void>();
    final releaseNewRequest = Completer<void>();
    var calls = 0;
    final repository = _ShopsFake(() async {
      calls++;
      if (calls == 1) {
        await releaseOldRequest.future;
        return const ShopsSnapshot(
          shops: [
            Shop(
              id: 'old',
              code: 'OLD',
              name: 'User A Shop',
              address: null,
              phone: null,
              isHead: false,
            ),
          ],
          currentShopId: 'old',
        );
      }
      await releaseNewRequest.future;
      return const ShopsSnapshot(
        shops: [
          Shop(
            id: 'new',
            code: 'NEW',
            name: 'User B Shop',
            address: null,
            phone: null,
            isHead: false,
          ),
        ],
        currentShopId: 'new',
      );
    });
    final store = _Store();
    final client = _client(_Adapter((_) async => _json(204, {})), store);
    final notifier = ShopsNotifier(repository, client);
    await Future<void>.delayed(Duration.zero);

    client.beginSession();
    await client.session.setTokens(_pair('new-access', 'new-refresh'));
    releaseOldRequest.complete();
    await Future<void>.delayed(Duration.zero);
    expect(notifier.state.data, isNull);

    releaseNewRequest.complete();
    await Future<void>.delayed(Duration.zero);
    expect(notifier.state.data?.shops.single.id, 'new');
    notifier.dispose();
  });

  test('shops notifier exposes unauthorized and clears auth', () async {
    final store = _Store();
    final client = _client(_Adapter((_) async => _json(500, {})), store);
    await client.session.setTokens(TokenPair(
      accessToken: 'access',
      refreshToken: 'refresh',
      accessTokenExpiresAt: DateTime.utc(2030),
      refreshTokenExpiresAt: DateTime.utc(2030, 2),
    ));
    final notifier = ShopsNotifier(_ShopsFake(() async {
      throw const UnauthorizedException();
    }), client);
    await Future<void>.delayed(Duration.zero);
    await Future<void>.delayed(Duration.zero);
    expect(notifier.state.status, RemoteStatus.unauthorized);
    expect(client.session.isAuthenticated, isFalse);
    notifier.dispose();
  });
}

class _ProfileFake implements ProfileRepository {
  _ProfileFake(this.callback);
  final Future<UserProfile> Function() callback;

  @override
  Future<UserProfile> fetchProfile() => callback();
}

class _ShopsFake implements ShopsRepository {
  _ShopsFake(this.callback);
  final Future<ShopsSnapshot> Function() callback;

  @override
  Future<ShopsSnapshot> fetchShops() => callback();
}
