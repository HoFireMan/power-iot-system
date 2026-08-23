import 'dart:async';

import 'package:dio/dio.dart';
import 'package:flutter/foundation.dart';
import 'package:flutter_secure_storage/flutter_secure_storage.dart';

/// The only token material kept by the session after the access token is issued.
abstract interface class RefreshTokenStore {
  Future<String?> read();

  Future<void> write(String token);

  Future<void> clear();
}

/// A platform-backed refresh-token store. Access tokens and credentials are not
/// written here (or anywhere else by this foundation).
final class SecureRefreshTokenStore implements RefreshTokenStore {
  SecureRefreshTokenStore({
    FlutterSecureStorage? storage,
    this.key = 'power_iot.refresh_token',
  }) : _storage = storage ?? const FlutterSecureStorage();

  final FlutterSecureStorage _storage;
  final String key;

  @override
  Future<String?> read() => _storage.read(key: key);

  @override
  Future<void> write(String token) => _storage.write(key: key, value: token);

  @override
  Future<void> clear() => _storage.delete(key: key);
}

/// The wire token pair returned by /api/v1/auth/login and /api/v1/auth/refresh.
final class TokenPair {
  const TokenPair({
    required this.accessToken,
    required this.refreshToken,
    required this.accessTokenExpiresAt,
    required this.refreshTokenExpiresAt,
  });

  final String accessToken;
  final String refreshToken;
  final DateTime accessTokenExpiresAt;
  final DateTime refreshTokenExpiresAt;

  factory TokenPair.fromJson(Object? value) {
    if (value is! Map ||
        value.length != 5 ||
        value.keys.any((key) => key is! String) ||
        value['tokenType'] != 'Bearer') {
      throw const FormatException('Invalid token response');
    }
    const requiredKeys = <String>{
      'tokenType',
      'accessToken',
      'refreshToken',
      'accessTokenExpiresAt',
      'refreshTokenExpiresAt',
    };
    if (!value.keys.every(requiredKeys.contains)) {
      throw const FormatException('Invalid token response');
    }
    final accessToken = value['accessToken'];
    final refreshToken = value['refreshToken'];
    if (accessToken is! String ||
        accessToken.isEmpty ||
        refreshToken is! String ||
        refreshToken.isEmpty) {
      throw const FormatException('Invalid token response');
    }

    DateTime parseExpiry(String key) {
      final raw = value[key];
      if (raw is! String || !_rfc3339Pattern.hasMatch(raw)) {
        throw const FormatException('Invalid token response');
      }
      final match = _rfc3339Pattern.firstMatch(raw)!;
      final year = int.parse(match.group(1)!);
      final month = int.parse(match.group(2)!);
      final day = int.parse(match.group(3)!);
      final date = DateTime.utc(year, month, day);
      if (date.year != year || date.month != month || date.day != day) {
        throw const FormatException('Invalid token response');
      }
      final parsed = DateTime.tryParse(raw);
      if (parsed == null) throw const FormatException('Invalid token response');
      return parsed;
    }

    return TokenPair(
      accessToken: accessToken,
      refreshToken: refreshToken,
      accessTokenExpiresAt: parseExpiry('accessTokenExpiresAt'),
      refreshTokenExpiresAt: parseExpiry('refreshTokenExpiresAt'),
    );
  }

  bool isAccessExpired(DateTime now) => !now.isBefore(accessTokenExpiresAt);

  static final _rfc3339Pattern = RegExp(
    r'^(\d{4})-(\d{2})-(\d{2})T(?:[01]\d|2[0-3]):(?:[0-5]\d):(?:[0-5]\d)(?:\.\d+)?(?:Z|[+-](?:[01]\d|2[0-3]):[0-5]\d)$',
  );
}

/// Session state deliberately separates volatile access material from the
/// platform-backed refresh material.
final class AuthSession extends ChangeNotifier {
  AuthSession(this._refreshTokens);

  final RefreshTokenStore _refreshTokens;
  Future<void> _tokenStoreQueue = Future<void>.value();
  String? _accessToken;
  DateTime? _accessTokenExpiresAt;
  bool _authenticated = false;

  String? get accessToken => _accessToken;
  DateTime? get accessTokenExpiresAt => _accessTokenExpiresAt;
  bool get isAuthenticated => _authenticated;

  Future<void> setTokens(TokenPair pair) {
    return _enqueueTokenStore<void>(() async {
      await _refreshTokens.write(pair.refreshToken);
      _installVolatileTokens(pair);
    });
  }

  /// Stores a pair only if the caller's operation is still current. The
  /// checks and secure write are one serialized operation: a stale operation
  /// never clears a token installed by a newer session.
  Future<bool> setTokensIfCurrent(
    TokenPair pair,
    bool Function() isCurrent,
  ) {
    return _enqueueTokenStore<bool>(() async {
      if (!isCurrent()) return false;
      await _refreshTokens.write(pair.refreshToken);
      if (!isCurrent()) return false;
      _installVolatileTokens(pair);
      return true;
    });
  }

  Future<String?> readRefreshToken() => _refreshTokens.read();

  /// Clears both volatile state and the persisted refresh token. The volatile
  /// state is cleared even if the platform store reports an error.
  Future<void> clear() {
    return _enqueueTokenStore<void>(() async {
      _clearVolatileTokens();
      try {
        await _refreshTokens.clear();
      } catch (_) {
        // Logout/refresh failure must not leave an access token in memory.
      }
    });
  }

  /// Conditionally clears the session in the same queue as token writes. This
  /// is used by delayed operations whose failure must not clear a newer login.
  Future<bool> clearIfCurrent(bool Function() isCurrent) {
    return _enqueueTokenStore<bool>(() async {
      if (!isCurrent()) return false;
      _clearVolatileTokens();
      try {
        await _refreshTokens.clear();
      } catch (_) {
        // Logout/refresh failure must not leave an access token in memory.
      }
      return true;
    });
  }

  void _installVolatileTokens(TokenPair pair) {
    _accessToken = pair.accessToken;
    _accessTokenExpiresAt = pair.accessTokenExpiresAt;
    _authenticated = true;
    notifyListeners();
  }

  void _clearVolatileTokens() {
    _accessToken = null;
    _accessTokenExpiresAt = null;
    _authenticated = false;
    notifyListeners();
  }

  Future<T> _enqueueTokenStore<T>(Future<T> Function() operation) {
    final result = _tokenStoreQueue.then<T>((_) => operation());
    _tokenStoreQueue = result.then<void>(
      (_) {},
      onError: (Object _, StackTrace __) {},
    );
    return result;
  }
}

/// A configurable Dio client for the versioned Go API.
///
/// Refresh is serialized across all requests. A request can be retried once
/// after a 401; a retry can never enter another retry cycle.
final class AuthenticatedHttpClient {
  AuthenticatedHttpClient({
    required Uri baseUrl,
    required AuthSession session,
    Dio? dio,
    DateTime Function()? now,
  })  : _session = session,
        _now = now ?? DateTime.now,
        _dio = dio ?? Dio(BaseOptions(baseUrl: _baseUrl(baseUrl))) {
    final normalizedBaseUrl = _baseUrl(baseUrl);
    _dio.options.baseUrl = normalizedBaseUrl;
    _dio.interceptors.add(_AuthInterceptor(this));
  }

  final AuthSession _session;
  final DateTime Function() _now;
  final Dio _dio;
  Future<bool>? _refreshInFlight;
  int _authEpoch = 0;
  bool _logoutInFlight = false;
  final _authEpochListeners = <void Function(int)>[];

  Dio get dio => _dio;
  AuthSession get session => _session;
  int get authEpoch => _authEpoch;
  bool get isLogoutInProgress => _logoutInFlight;

  /// Listens for an authentication epoch boundary. Listeners are notified
  /// synchronously when a login/restore or logout invalidates prior requests.
  /// Callers must remove their listener when they are disposed.
  void addAuthEpochListener(void Function(int) listener) {
    _authEpochListeners.add(listener);
  }

  void removeAuthEpochListener(void Function(int) listener) {
    _authEpochListeners.remove(listener);
  }

  void _notifyAuthEpochChanged() {
    for (final listener in List<void Function(int)>.of(_authEpochListeners)) {
      listener(_authEpoch);
    }
  }

  /// Starts a new authentication session. Requests from the previous session
  /// must not be replayed with the new user's bearer.
  int beginSession() {
    final epoch = ++_authEpoch;
    _notifyAuthEpochChanged();
    return epoch;
  }

  bool isSessionCurrent(int generation) => generation == _authEpoch;

  /// Rotates the secure refresh token and keeps the new access token volatile.
  /// A supplied generation prevents a delayed restore from adopting a newer
  /// login's session epoch.
  Future<bool> refreshSession({int? generation}) =>
      _refresh(requestEpoch: generation);

  /// Revokes the active bearer. Local state is cleared even when the server
  /// reports an error or the request cannot be delivered.
  Future<void> logout() async {
    // Invalidate refresh work before starting the network request. A refresh
    // response may still arrive, but its epoch no longer permits installing
    // tokens after logout has started.
    final logoutEpoch = ++_authEpoch;
    _logoutInFlight = true;
    _notifyAuthEpochChanged();
    try {
      final access = _session.accessToken;
      if (access == null || access.isEmpty) return;
      await _dio.post<void>(
        '/api/v1/auth/logout',
        options: Options(extra: <String, Object>{_skipRefreshKey: true}),
      );
    } finally {
      await _session.clearIfCurrent(() => logoutEpoch == _authEpoch);
      _logoutInFlight = false;
    }
  }

  void close() => _dio.close(force: true);

  static String _baseUrl(Uri value) {
    _validateBaseUrl(value);
    final text = value.toString();
    return text.endsWith('/') ? text.substring(0, text.length - 1) : text;
  }

  static void _validateBaseUrl(Uri value) {
    if (!value.hasScheme || value.host.isEmpty) {
      throw ArgumentError.value(value, 'baseUrl', 'must be an absolute URL');
    }
    if (value.scheme != 'https' && value.scheme != 'http') {
      throw ArgumentError.value(value, 'baseUrl', 'must use http or https');
    }
    // Cleartext is intentionally limited to local development targets. No
    // production host is embedded in the mobile client.
    if (value.scheme == 'http' &&
        value.host != 'localhost' &&
        value.host != '127.0.0.1' &&
        value.host != '10.0.2.2' &&
        value.host != '::1') {
      throw ArgumentError.value(
        value,
        'baseUrl',
        'cleartext URLs are only allowed for local development',
      );
    }
  }

  Future<bool> _refresh({String? failedAccessToken, int? requestEpoch}) {
    if (_logoutInFlight ||
        (requestEpoch != null && requestEpoch != _authEpoch)) {
      return Future.value(false);
    }
    final currentAccess = _session.accessToken;
    if (failedAccessToken != null &&
        currentAccess != null &&
        currentAccess != failedAccessToken) {
      // Another request completed the rotation between this response and its
      // interceptor. Treat that replacement as the refresh result so this
      // waiter retries with the current access token without rotating again.
      return Future.value(requestEpoch == null || requestEpoch == _authEpoch);
    }

    final inFlight = _refreshInFlight;
    if (inFlight != null) return inFlight;

    final operation = _performRefresh(_authEpoch);
    _refreshInFlight = operation;
    operation.whenComplete(() {
      if (identical(_refreshInFlight, operation)) _refreshInFlight = null;
    });
    return operation;
  }

  Future<bool> _performRefresh(int epoch) async {
    try {
      final refreshToken = await _session.readRefreshToken();
      if (epoch != _authEpoch) return false;
      if (refreshToken == null || refreshToken.isEmpty) {
        await _session.clearIfCurrent(() => epoch == _authEpoch);
        return false;
      }
      final response = await _dio.post<Object?>(
        '/api/v1/auth/refresh',
        data: <String, String>{'refreshToken': refreshToken},
        options: Options(extra: <String, Object>{_skipAuthKey: true}),
      );
      final pair = TokenPair.fromJson(response.data);
      // Logout increments the epoch synchronously. Check again immediately
      // before committing the pair so a late response cannot restore a
      // session that the user has already invalidated.
      if (epoch != _authEpoch) return false;
      return _session.setTokensIfCurrent(
        pair,
        () => epoch == _authEpoch && !_logoutInFlight,
      );
    } catch (_) {
      await _session.clearIfCurrent(() => epoch == _authEpoch);
      return false;
    }
  }

  Future<void> _ensureFreshToken() async {
    final access = _session.accessToken;
    final expiry = _session.accessTokenExpiresAt;
    if (access == null || expiry == null || _now().isBefore(expiry)) return;
    await _refresh(failedAccessToken: access);
    if (!_session.isAuthenticated) {
      throw const AuthRefreshException();
    }
  }

  Future<Response<dynamic>> _retry(RequestOptions failed) async {
    final options = failed.copyWith(
      extra: <String, Object>{...failed.extra, _retriedKey: true},
    );
    return _dio.fetch<dynamic>(options);
  }
}

const _skipAuthKey = 'power_iot.skip_auth';
const _skipRefreshKey = 'power_iot.skip_refresh';
const _retriedKey = 'power_iot.auth_retried';
const _authEpochKey = 'power_iot.auth_epoch';

class AuthRefreshException implements Exception {
  const AuthRefreshException();

  @override
  String toString() => 'Authentication refresh failed';
}

final class _AuthSessionChangedException implements Exception {
  const _AuthSessionChangedException();
}

final class _AuthInterceptor extends Interceptor {
  _AuthInterceptor(this._client);

  final AuthenticatedHttpClient _client;

  @override
  Future<void> onRequest(
    RequestOptions options,
    RequestInterceptorHandler handler,
  ) async {
    if (options.extra[_skipAuthKey] == true) {
      handler.next(options);
      return;
    }
    final previousEpoch = options.extra[_authEpochKey];
    final requestEpoch =
        options.extra[_retriedKey] == true && previousEpoch is int
            ? previousEpoch
            : _client._authEpoch;
    options.extra[_authEpochKey] = requestEpoch;
    try {
      // A retry is already this request's single refresh opportunity. Do not
      // trigger a second proactive rotation while replaying it.
      if (options.extra[_retriedKey] != true &&
          options.extra[_skipRefreshKey] != true) {
        await _client._ensureFreshToken();
      }
      if (requestEpoch != _client._authEpoch) {
        handler.reject(
          DioException(
            requestOptions: options,
            error: const _AuthSessionChangedException(),
            type: DioExceptionType.cancel,
          ),
        );
        return;
      }
      final accessToken = _client.session.accessToken;
      if (accessToken != null && accessToken.isNotEmpty) {
        options.headers['Authorization'] = 'Bearer $accessToken';
      }
      handler.next(options);
    } catch (error, stackTrace) {
      handler.reject(
        DioException(
          requestOptions: options,
          error: error,
          stackTrace: stackTrace,
          type: DioExceptionType.unknown,
        ),
      );
    }
  }

  @override
  Future<void> onError(
    DioException err,
    ErrorInterceptorHandler handler,
  ) async {
    final options = err.requestOptions;
    if (options.extra[_skipAuthKey] == true ||
        options.extra[_skipRefreshKey] == true ||
        options.extra[_retriedKey] == true ||
        err.response?.statusCode != 401) {
      if (options.extra[_retriedKey] == true &&
          err.response?.statusCode == 401) {
        final failedAccess = options.headers['Authorization']
            ?.toString()
            .replaceFirst(RegExp(r'^Bearer '), '');
        final requestEpoch = options.extra[_authEpochKey];
        if (requestEpoch == _client._authEpoch &&
            failedAccess != null &&
            failedAccess == _client.session.accessToken) {
          await _client.session.clear();
        }
      }
      handler.next(err);
      return;
    }

    final failedAccess = options.headers['Authorization']
        ?.toString()
        .replaceFirst(RegExp(r'^Bearer '), '');
    final requestEpoch = options.extra[_authEpochKey];
    // The epoch binds the failed bearer to the current access session. A
    // replacement bearer in the same epoch is a serialized refresh result;
    // an earlier epoch (or no current access session) must not be replayed.
    if (requestEpoch != _client._authEpoch ||
        failedAccess == null ||
        failedAccess.isEmpty ||
        _client.session.accessToken == null) {
      handler.next(err);
      return;
    }
    final refreshed = await _client._refresh(
      failedAccessToken: failedAccess,
      requestEpoch: requestEpoch as int,
    );
    if (!refreshed || !_client.session.isAuthenticated) {
      handler.next(err);
      return;
    }
    try {
      handler.resolve(await _client._retry(options));
    } on DioException catch (retryError) {
      handler.next(retryError);
    }
  }
}
