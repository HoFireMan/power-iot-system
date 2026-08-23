import 'dart:async';
import 'dart:convert';
import 'dart:typed_data';

import 'package:dio/dio.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:power_iot_app/core/network/authenticated_http_client.dart';

class _MemoryRefreshTokens implements RefreshTokenStore {
  String? value;
  int writes = 0;
  int clears = 0;

  @override
  Future<String?> read() async => value;

  @override
  Future<void> write(String token) async {
    writes++;
    value = token;
  }

  @override
  Future<void> clear() async {
    clears++;
    value = null;
  }
}

class _DelayedWriteStore implements RefreshTokenStore {
  String? value;
  int writes = 0;
  int clears = 0;
  bool delayNextWrite = false;
  final writeStarted = Completer<void>();
  final releaseWrite = Completer<void>();

  @override
  Future<String?> read() async => value;

  @override
  Future<void> write(String token) async {
    writes++;
    if (delayNextWrite) {
      delayNextWrite = false;
      writeStarted.complete();
      await releaseWrite.future;
    }
    value = token;
  }

  @override
  Future<void> clear() async {
    clears++;
    value = null;
  }
}

class _FakeAdapter implements HttpClientAdapter {
  _FakeAdapter(this.handler);

  final Future<ResponseBody> Function(RequestOptions options) handler;
  final List<RequestOptions> requests = [];

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

Dio _dio(_FakeAdapter adapter) {
  return Dio(BaseOptions(baseUrl: 'https://development.invalid'))
    ..httpClientAdapter = adapter;
}

TokenPair _pair(String access, String refresh) => TokenPair(
      accessToken: access,
      refreshToken: refresh,
      accessTokenExpiresAt: DateTime.now().add(const Duration(hours: 1)),
      refreshTokenExpiresAt: DateTime.now().add(const Duration(days: 30)),
    );

Map<String, Object> _validTokenJson() => <String, Object>{
      'tokenType': 'Bearer',
      'accessToken': 'access',
      'refreshToken': 'refresh',
      'accessTokenExpiresAt': '2030-01-01T00:00:00Z',
      'refreshTokenExpiresAt': '2030-02-01T00:00:00Z',
    };

ResponseBody _json(int status, Object body) => ResponseBody.fromString(
      jsonEncode(body),
      status,
      headers: <String, List<String>>{
        Headers.contentTypeHeader: <String>['application/json'],
      },
    );

void main() {
  test('TokenPair rejects a missing expiry', () {
    for (final key in <String>[
      'accessTokenExpiresAt',
      'refreshTokenExpiresAt',
    ]) {
      final body = _validTokenJson()..remove(key);

      expect(
        () => TokenPair.fromJson(body),
        throwsA(isA<FormatException>()),
        reason: key,
      );
    }
  });

  test('TokenPair rejects malformed and non-string expiry values', () {
    for (final entry in <MapEntry<String, Object>>[
      const MapEntry('accessTokenExpiresAt', 'not-an-expiry'),
      const MapEntry('refreshTokenExpiresAt', 123),
    ]) {
      final body = _validTokenJson()..[entry.key] = entry.value;

      expect(
        () => TokenPair.fromJson(body),
        throwsA(isA<FormatException>()),
        reason: entry.key,
      );
    }
  });

  test('TokenPair rejects a missing or non-Bearer token type', () {
    final missing = _validTokenJson()..remove('tokenType');
    final nonBearer = _validTokenJson()..['tokenType'] = 'Basic';

    for (final body in <Map<String, Object>>[missing, nonBearer]) {
      expect(
        () => TokenPair.fromJson(body),
        throwsA(isA<FormatException>()),
      );
    }
  });

  test('base URL is configurable and rejects non-local cleartext', () {
    final store = _MemoryRefreshTokens();
    final client = AuthenticatedHttpClient(
      baseUrl: Uri.parse('https://api.example.test/root/'),
      session: AuthSession(store),
    );

    expect(client.dio.options.baseUrl, 'https://api.example.test/root');
    expect(
      () => AuthenticatedHttpClient(
        baseUrl: Uri.parse('http://api.example.test'),
        session: AuthSession(store),
      ),
      throwsArgumentError,
    );

    // The supplied Dio remains usable for local test/development endpoints;
    // production hosts are selected by the caller, not embedded in this code.
    expect(client.session.isAuthenticated, isFalse);
  });

  test('injects the in-memory access token as a bearer header', () async {
    late RequestOptions seen;
    final adapter = _FakeAdapter((options) async {
      seen = options;
      return _json(200, <String, String>{'ok': 'yes'});
    });
    final store = _MemoryRefreshTokens();
    final session = AuthSession(store);
    await session.setTokens(_pair('access-memory', 'refresh-secure'));
    final client = AuthenticatedHttpClient(
      baseUrl: Uri.parse('https://api.example.test'),
      session: session,
      dio: _dio(adapter),
    );

    await client.dio.get<void>('/api/v1/me');

    expect(seen.headers['Authorization'], 'Bearer access-memory');
  });

  test('secure store contains refresh token only; access token is memory-only',
      () async {
    final store = _MemoryRefreshTokens();
    final session = AuthSession(store);
    await session.setTokens(_pair('volatile-access', 'persisted-refresh'));

    expect(store.value, 'persisted-refresh');
    expect(session.accessToken, 'volatile-access');
    final restored = AuthSession(store);
    expect(restored.accessToken, isNull);
    expect(await restored.readRefreshToken(), 'persisted-refresh');
  });

  test('serializes concurrent 401 refresh and retries both requests once',
      () async {
    var refreshCalls = 0;
    var protectedCalls = 0;
    final refreshStarted = Completer<void>();
    final releaseRefresh = Completer<void>();
    final adapter = _FakeAdapter((options) async {
      if (options.uri.path == '/api/v1/auth/refresh') {
        refreshCalls++;
        refreshStarted.complete();
        await releaseRefresh.future;
        return _json(200, <String, String>{
          'tokenType': 'Bearer',
          'accessToken': 'new-access',
          'refreshToken': 'new-refresh',
          'accessTokenExpiresAt': '2030-01-01T00:00:00Z',
          'refreshTokenExpiresAt': '2030-02-01T00:00:00Z',
        });
      }
      protectedCalls++;
      final bearer = options.headers['Authorization'];
      if (bearer == 'Bearer old-access') return _json(401, <String, Object>{});
      return _json(200, <String, String>{'ok': 'yes'});
    });
    final store = _MemoryRefreshTokens()..value = 'old-refresh';
    final session = AuthSession(store);
    await session.setTokens(_pair('old-access', 'old-refresh'));
    final client = AuthenticatedHttpClient(
      baseUrl: Uri.parse('https://api.example.test'),
      session: session,
      dio: _dio(adapter),
    );

    final first = client.dio.get<void>('/api/v1/me');
    final second = client.dio.get<void>('/api/v1/shops');
    await refreshStarted.future;
    releaseRefresh.complete();
    await Future.wait(<Future<void>>[first, second]);

    expect(refreshCalls, 1);
    expect(protectedCalls, 4); // two original 401s and two retries
    expect(session.accessToken, 'new-access');
    expect(store.value, 'new-refresh');
  });

  test('a 401 response is retried once and never loops', () async {
    var refreshCalls = 0;
    var protectedCalls = 0;
    final adapter = _FakeAdapter((options) async {
      if (options.uri.path == '/api/v1/auth/refresh') {
        refreshCalls++;
        return _json(200, <String, String>{
          'tokenType': 'Bearer',
          'accessToken': 'replacement',
          'refreshToken': 'replacement-refresh',
          'accessTokenExpiresAt': '2000-01-01T00:00:00Z',
          'refreshTokenExpiresAt': '2030-02-01T00:00:00Z',
        });
      }
      protectedCalls++;
      return _json(401, <String, Object>{});
    });
    final store = _MemoryRefreshTokens();
    final session = AuthSession(store);
    await session.setTokens(_pair('old', 'refresh'));
    final client = AuthenticatedHttpClient(
      baseUrl: Uri.parse('https://api.example.test'),
      session: session,
      dio: _dio(adapter),
    );

    await expectLater(
      client.dio.get<void>('/api/v1/me'),
      throwsA(isA<DioException>()),
    );
    expect(refreshCalls, 1);
    expect(protectedCalls, 2);
    expect(session.accessToken, isNull);
    expect(session.isAuthenticated, isFalse);
    expect(store.value, isNull);
  });

  test('an unauthenticated 401 does not rotate a refresh token', () async {
    var refreshCalls = 0;
    final adapter = _FakeAdapter((options) async {
      if (options.uri.path == '/api/v1/auth/refresh') refreshCalls++;
      return _json(401, <String, Object>{});
    });
    final store = _MemoryRefreshTokens()..value = 'persisted-refresh';
    final client = AuthenticatedHttpClient(
      baseUrl: Uri.parse('https://api.example.test'),
      session: AuthSession(store),
      dio: _dio(adapter),
    );

    await expectLater(
      client.dio.get<void>('/api/v1/public'),
      throwsA(isA<DioException>()),
    );
    expect(refreshCalls, 0);
    expect(store.value, 'persisted-refresh');
  });

  test('a pre-logout 401 cannot retry with a newly logged-in bearer', () async {
    final requestStarted = Completer<void>();
    final releaseRequest = Completer<void>();
    var refreshCalls = 0;
    var protectedCalls = 0;
    final bearers = <String?>[];
    final adapter = _FakeAdapter((options) async {
      if (options.uri.path == '/api/v1/auth/refresh') {
        refreshCalls++;
        return _json(200, _validTokenJson());
      }
      if (options.uri.path == '/api/v1/auth/logout') {
        return _json(204, '');
      }
      protectedCalls++;
      bearers.add(options.headers['Authorization']?.toString());
      if (options.headers['Authorization'] == 'Bearer user-a') {
        requestStarted.complete();
        await releaseRequest.future;
        return _json(401, <String, Object>{});
      }
      return _json(200, <String, String>{'ok': 'yes'});
    });
    final store = _MemoryRefreshTokens();
    final session = AuthSession(store);
    await session.setTokens(_pair('user-a', 'refresh-a'));
    final client = AuthenticatedHttpClient(
      baseUrl: Uri.parse('https://api.example.test'),
      session: session,
      dio: _dio(adapter),
    );

    final request = client.dio.get<void>('/api/v1/me');
    await requestStarted.future;
    await client.logout();
    client.beginSession();
    await session.setTokens(_pair('user-b', 'refresh-b'));
    releaseRequest.complete();

    await expectLater(request, throwsA(isA<DioException>()));
    expect(refreshCalls, 0);
    expect(protectedCalls, 1);
    expect(bearers, <String?>['Bearer user-a']);
    expect(session.accessToken, 'user-b');
  });

  test('stale refresh write cannot clear a newer login token', () async {
    final store = _DelayedWriteStore()..value = 'refresh-a';
    final session = AuthSession(store);
    await session.setTokens(_pair('access-a', 'refresh-a'));
    store.delayNextWrite = true;
    final refreshStarted = Completer<void>();
    final adapter = _FakeAdapter((options) async {
      if (options.uri.path == '/api/v1/auth/refresh') {
        refreshStarted.complete();
        return _json(200, _validTokenJson());
      }
      if (options.uri.path == '/api/v1/auth/logout') {
        return _json(204, '');
      }
      return _json(200, <String, String>{'ok': 'yes'});
    });
    final client = AuthenticatedHttpClient(
      baseUrl: Uri.parse('https://api.example.test'),
      session: session,
      dio: _dio(adapter),
    );

    final refresh = client.refreshSession();
    await refreshStarted.future;
    await store.writeStarted.future;
    final logout = client.logout();
    client.beginSession();
    final loginWrite = session.setTokens(_pair('access-b', 'refresh-b'));
    store.releaseWrite.complete();

    expect(await refresh, isFalse);
    await Future.wait(<Future<void>>[logout, loginWrite]);
    expect(session.accessToken, 'access-b');
    expect(store.value, 'refresh-b');
  });

  test('refresh completing after logout cannot restore the session', () async {
    final refreshStarted = Completer<void>();
    final releaseRefresh = Completer<void>();
    final store = _MemoryRefreshTokens()..value = 'old-refresh';
    final session = AuthSession(store);
    await session.setTokens(_pair('old-access', 'old-refresh'));
    final adapter = _FakeAdapter((options) async {
      if (options.uri.path == '/api/v1/auth/refresh') {
        refreshStarted.complete();
        await releaseRefresh.future;
        return _json(200, _validTokenJson());
      }
      if (options.uri.path == '/api/v1/auth/logout') {
        return _json(204, '');
      }
      return _json(200, <String, String>{'ok': 'yes'});
    });
    final client = AuthenticatedHttpClient(
      baseUrl: Uri.parse('https://api.example.test'),
      session: session,
      dio: _dio(adapter),
    );

    final refresh = client.refreshSession();
    await refreshStarted.future;
    await client.logout();
    releaseRefresh.complete();

    expect(await refresh, isFalse);
    expect(session.isAuthenticated, isFalse);
    expect(session.accessToken, isNull);
    expect(store.value, isNull);
  });

  test('logout 401 does not start a refresh cycle', () async {
    var refreshCalls = 0;
    final adapter = _FakeAdapter((options) async {
      if (options.uri.path == '/api/v1/auth/refresh') refreshCalls++;
      return _json(401, <String, Object>{});
    });
    final store = _MemoryRefreshTokens();
    final session = AuthSession(store);
    await session.setTokens(_pair('active-access', 'refresh-secret'));
    final client = AuthenticatedHttpClient(
      baseUrl: Uri.parse('https://api.example.test'),
      session: session,
      dio: _dio(adapter),
    );

    await expectLater(client.logout(), throwsA(isA<DioException>()));

    expect(refreshCalls, 0);
    expect(session.isAuthenticated, isFalse);
    expect(store.value, isNull);
  });

  test('refresh failure clears access memory and secure refresh state',
      () async {
    final adapter = _FakeAdapter((options) async {
      if (options.uri.path == '/api/v1/auth/refresh') {
        return _json(401, <String, Object>{});
      }
      return _json(401, <String, Object>{});
    });
    final store = _MemoryRefreshTokens();
    final session = AuthSession(store);
    await session.setTokens(_pair('old', 'refresh'));
    final client = AuthenticatedHttpClient(
      baseUrl: Uri.parse('https://api.example.test'),
      session: session,
      dio: _dio(adapter),
    );

    await expectLater(
      client.dio.get<void>('/api/v1/me'),
      throwsA(isA<DioException>()),
    );
    expect(session.accessToken, isNull);
    expect(session.isAuthenticated, isFalse);
    expect(store.value, isNull);
    expect(store.clears, 1);
  });
}
