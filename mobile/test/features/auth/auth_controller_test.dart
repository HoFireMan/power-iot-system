import 'dart:async';
import 'dart:convert';
import 'dart:typed_data';

import 'package:dio/dio.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:power_iot_app/core/network/authenticated_http_client.dart';
import 'package:power_iot_app/features/auth/auth_controller.dart';

class _Store implements RefreshTokenStore {
  String? value;
  int writes = 0;
  int clears = 0;

  @override
  Future<String?> read() async => value;

  @override
  Future<void> write(String token) async {
    value = token;
    writes++;
  }

  @override
  Future<void> clear() async {
    value = null;
    clears++;
  }
}

class _Adapter implements HttpClientAdapter {
  _Adapter(this.handler);

  final Future<ResponseBody> Function(RequestOptions) handler;
  final requests = <RequestOptions>[];

  @override
  Future<ResponseBody> fetch(
    RequestOptions options,
    Stream<Uint8List>? requestStream,
    Future<void>? cancelFuture,
  ) async {
    requests.add(options);
    return handler(options);
  }

  @override
  void close({bool force = false}) {}
}

Dio _dio(_Adapter adapter) => Dio(BaseOptions(baseUrl: 'https://test.invalid'))
  ..httpClientAdapter = adapter;

ResponseBody _json(int status, Object body) => ResponseBody.fromString(
      jsonEncode(body),
      status,
      headers: <String, List<String>>{
        Headers.contentTypeHeader: <String>['application/json'],
      },
    );

Map<String, Object> _tokens(String access, String refresh) => <String, Object>{
      'tokenType': 'Bearer',
      'accessToken': access,
      'refreshToken': refresh,
      'accessTokenExpiresAt': '2030-01-01T00:00:00Z',
      'refreshTokenExpiresAt': '2030-02-01T00:00:00Z',
    };

void main() {
  test('login sends account/password and stores refresh securely only',
      () async {
    final adapter = _Adapter((request) async {
      expect(request.uri.path, '/api/v1/auth/login');
      expect(request.data, contains('account'));
      expect(request.data, contains('password'));
      return _json(200, _tokens('volatile-access', 'secure-refresh'));
    });
    final store = _Store();
    final client = AuthenticatedHttpClient(
      baseUrl: Uri.parse('https://test.invalid'),
      session: AuthSession(store),
      dio: _dio(adapter),
    );
    final controller = AuthController(client);

    await controller.login(account: 'account', password: 'secret');

    expect(controller.isAuthenticated, isTrue);
    expect(client.session.accessToken, 'volatile-access');
    expect(store.value, 'secure-refresh');
    expect(store.value, isNot('volatile-access'));
    controller.dispose();
  });

  test('a delayed stale login response cannot replace a newer login', () async {
    final loginAStarted = Completer<void>();
    final releaseLoginA = Completer<void>();
    final adapter = _Adapter((request) async {
      if (request.uri.path != '/api/v1/auth/login') {
        return _json(200, <String, String>{'ok': 'yes'});
      }
      if (request.data.toString().contains('user-a')) {
        loginAStarted.complete();
        await releaseLoginA.future;
        return _json(200, _tokens('access-a', 'refresh-a'));
      }
      return _json(200, _tokens('access-b', 'refresh-b'));
    });
    final store = _Store();
    final client = AuthenticatedHttpClient(
      baseUrl: Uri.parse('https://test.invalid'),
      session: AuthSession(store),
      dio: _dio(adapter),
    );
    final controller = AuthController(client);

    final loginA = controller.login(account: 'user-a', password: 'secret');
    await loginAStarted.future;
    await controller.login(account: 'user-b', password: 'secret');
    releaseLoginA.complete();
    await loginA;

    expect(controller.isAuthenticated, isTrue);
    expect(client.session.accessToken, 'access-b');
    expect(store.value, 'refresh-b');
    controller.dispose();
  });

  test('a stale login failure cannot clear a newer login', () async {
    final loginAStarted = Completer<void>();
    final releaseLoginA = Completer<void>();
    final adapter = _Adapter((request) async {
      if (request.uri.path != '/api/v1/auth/login') {
        return _json(200, <String, String>{'ok': 'yes'});
      }
      if (request.data.toString().contains('user-a')) {
        loginAStarted.complete();
        await releaseLoginA.future;
        return _json(401, <String, Object>{'code': 'INVALID_CREDENTIALS'});
      }
      return _json(200, _tokens('access-b', 'refresh-b'));
    });
    final store = _Store();
    final client = AuthenticatedHttpClient(
      baseUrl: Uri.parse('https://test.invalid'),
      session: AuthSession(store),
      dio: _dio(adapter),
    );
    final controller = AuthController(client);

    final loginA = controller.login(account: 'user-a', password: 'secret');
    await loginAStarted.future;
    await controller.login(account: 'user-b', password: 'secret');
    releaseLoginA.complete();
    await expectLater(loginA, throwsA(isA<AuthFailure>()));

    expect(controller.isAuthenticated, isTrue);
    expect(client.session.accessToken, 'access-b');
    expect(store.value, 'refresh-b');
    controller.dispose();
  });

  test('invalid credentials expose one generic error and clear state',
      () async {
    final adapter = _Adapter((_) async => _json(401, <String, Object>{
          'code': 'INVALID_CREDENTIALS',
          'message': 'invalid credentials',
        }));
    final store = _Store()..value = 'old-refresh';
    final session = AuthSession(store);
    await session.setTokens(TokenPair(
      accessToken: 'old-access',
      refreshToken: 'old-refresh',
      accessTokenExpiresAt: testDate,
      refreshTokenExpiresAt: testDate,
    ));
    final client = AuthenticatedHttpClient(
      baseUrl: Uri.parse('https://test.invalid'),
      session: session,
      dio: _dio(adapter),
    );
    final controller = AuthController(client);

    await expectLater(
      controller.login(account: 'account', password: 'secret'),
      throwsA(
        isA<AuthFailure>().having(
          (failure) => failure.code,
          'code',
          'INVALID_CREDENTIALS',
        ),
      ),
    );
    expect(controller.isAuthenticated, isFalse);
    expect(session.accessToken, isNull);
    expect(store.value, isNull);
    controller.dispose();
  });

  test('fresh start without a refresh token resolves to unauthenticated',
      () async {
    final adapter = _Adapter((request) async {
      fail('refresh must not be called without a refresh token');
    });
    final client = AuthenticatedHttpClient(
      baseUrl: Uri.parse('https://test.invalid'),
      session: AuthSession(_Store()),
      dio: _dio(adapter),
    );
    final controller = AuthController(client);

    expect(controller.status, AuthStatus.restoring);
    await controller.restoreSession();

    expect(controller.status, AuthStatus.unauthenticated);
    expect(controller.isSessionReady, isFalse);
    controller.dispose();
  });

  test('invalid refresh resolves to unauthenticated and clears state',
      () async {
    final store = _Store()..value = 'invalid-refresh';
    final adapter = _Adapter((request) async {
      expect(request.uri.path, '/api/v1/auth/refresh');
      return _json(401, <String, String>{'code': 'INVALID_REFRESH'});
    });
    final client = AuthenticatedHttpClient(
      baseUrl: Uri.parse('https://test.invalid'),
      session: AuthSession(store),
      dio: _dio(adapter),
    );
    final controller = AuthController(client);

    await controller.restoreSession();

    expect(controller.status, AuthStatus.unauthenticated);
    expect(controller.isSessionReady, isFalse);
    expect(client.session.accessToken, isNull);
    expect(store.value, isNull);
    controller.dispose();
  });

  test('restore uses secure refresh token after an app restart', () async {
    final store = _Store()..value = 'restart-refresh';
    final adapter = _Adapter((request) async {
      expect(request.uri.path, '/api/v1/auth/refresh');
      return _json(200, _tokens('restored-access', 'rotated-refresh'));
    });
    final client = AuthenticatedHttpClient(
      baseUrl: Uri.parse('https://test.invalid'),
      session: AuthSession(store),
      dio: _dio(adapter),
    );
    final controller = AuthController(client);

    await controller.restoreSession();

    expect(adapter.requests, hasLength(1));
    expect(client.session.accessToken, 'restored-access');
    expect(controller.isAuthenticated, isTrue);
    expect(store.value, 'rotated-refresh');
    controller.dispose();
  });

  test('logout sends active bearer and clears both token locations', () async {
    final adapter = _Adapter((request) async {
      expect(request.uri.path, '/api/v1/auth/logout');
      expect(request.headers['Authorization'], 'Bearer active-access');
      return ResponseBody.fromString('', 204);
    });
    final store = _Store();
    final session = AuthSession(store);
    await session.setTokens(TokenPair(
      accessToken: 'active-access',
      refreshToken: 'refresh-secret',
      accessTokenExpiresAt: DateTime.utc(2030),
      refreshTokenExpiresAt: DateTime.utc(2030, 2),
    ));
    final controller = AuthController(AuthenticatedHttpClient(
      baseUrl: Uri.parse('https://test.invalid'),
      session: session,
      dio: _dio(adapter),
    ));

    await controller.logout();

    expect(session.accessToken, isNull);
    expect(session.isAuthenticated, isFalse);
    expect(store.value, isNull);
    controller.dispose();
  });
}

// A non-expired pair is needed only to prove login failure clears an existing
// session; keeping the value constant avoids putting credential material in a
// test log or assertion message.
final testDate = DateTime.utc(2030);
