import 'package:dio/dio.dart';
import 'package:flutter/foundation.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:power_iot_app/core/network/authenticated_http_client.dart';

/// Development must provide the API endpoint explicitly. A local default keeps
/// the debug app usable without embedding a production host in the client.
const developmentBaseUrl = String.fromEnvironment(
  'POWER_IOT_BASE_URL',
  defaultValue: 'http://localhost:8080',
);

enum AuthStatus { restoring, authenticated, unauthenticated }

final class AuthFailure implements Exception {
  const AuthFailure(this.code);

  final String code;

  bool get isInvalidCredentials => code == 'INVALID_CREDENTIALS';
}

final class AuthController extends ChangeNotifier {
  AuthController(this.client) {
    client.session.addListener(_sessionChanged);
  }

  final AuthenticatedHttpClient client;
  AuthStatus _status = AuthStatus.restoring;
  bool _restoreStarted = false;
  // A volatile session is not, by itself, proof that bootstrap was accepted.
  // This authority is granted only by a successful restore or login operation.
  bool _acceptedSession = false;

  AuthStatus get status => _status;
  bool get isSessionReady =>
      _acceptedSession && _status == AuthStatus.authenticated;
  bool get isAuthenticated => isSessionReady;

  void _sessionChanged() {
    // Session clearing is authoritative (logout, failed refresh, terminal
    // 401), but an injected/pre-existing bearer must not promote bootstrap.
    if (!client.session.isAuthenticated) {
      _acceptedSession = false;
      final next = _status == AuthStatus.restoring
          ? AuthStatus.restoring
          : AuthStatus.unauthenticated;
      if (_status == next) return;
      _status = next;
      notifyListeners();
    }
  }

  void _setUnauthenticatedIfCurrent(int generation) {
    if (!client.isSessionCurrent(generation)) return;
    _acceptedSession = false;
    if (_status != AuthStatus.unauthenticated) {
      _status = AuthStatus.unauthenticated;
      notifyListeners();
    }
  }

  /// Attempts continuation using only the refresh token in secure storage.
  /// Missing, expired, and malformed refresh sessions all fail closed.
  Future<void> restoreSession() async {
    if (_restoreStarted) return;
    _restoreStarted = true;
    final generation = client.beginSession();
    String? refreshToken;
    try {
      refreshToken = await client.session.readRefreshToken();
    } catch (_) {
      await client.session.clearIfCurrent(
        () => client.isSessionCurrent(generation),
      );
      _setUnauthenticatedIfCurrent(generation);
      return;
    }
    if (refreshToken == null || refreshToken.isEmpty) {
      await client.session.clearIfCurrent(
        () => client.isSessionCurrent(generation),
      );
      _setUnauthenticatedIfCurrent(generation);
      return;
    }
    final restored = await client.refreshSession(generation: generation);
    if (!restored || !client.isSessionCurrent(generation)) {
      _setUnauthenticatedIfCurrent(generation);
      return;
    }
    _acceptedSession = true;
    _status = AuthStatus.authenticated;
    notifyListeners();
  }

  Future<void> login(
      {required String account, required String password}) async {
    if (account.isEmpty || password.isEmpty) {
      throw const AuthFailure('INVALID_CREDENTIALS');
    }
    final generation = client.beginSession();
    _acceptedSession = false;
    _status = AuthStatus.restoring;
    notifyListeners();
    try {
      final response = await client.dio.post<Object?>(
        '/api/v1/auth/login',
        data: <String, String>{'account': account, 'password': password},
        options: Options(extra: <String, Object>{skipAuthKey: true}),
      );
      final pair = TokenPair.fromJson(response.data);
      final installed = await client.session.setTokensIfCurrent(
        pair,
        () => client.isSessionCurrent(generation),
      );
      if (!installed || !client.isSessionCurrent(generation)) return;
      _acceptedSession = true;
      _status = AuthStatus.authenticated;
      notifyListeners();
    } on DioException catch (error) {
      await client.session.clearIfCurrent(
        () => client.isSessionCurrent(generation),
      );
      _setUnauthenticatedIfCurrent(generation);
      throw AuthFailure(_errorCode(error));
    } on FormatException {
      await client.session.clearIfCurrent(
        () => client.isSessionCurrent(generation),
      );
      _setUnauthenticatedIfCurrent(generation);
      throw const AuthFailure('INVALID_CREDENTIALS');
    } catch (_) {
      await client.session.clearIfCurrent(
        () => client.isSessionCurrent(generation),
      );
      _setUnauthenticatedIfCurrent(generation);
      throw const AuthFailure('AUTHENTICATION_FAILED');
    }
  }

  /// Logout is best effort at the network boundary, but local invalidation is
  /// unconditional so a terminal response can never reuse the old bearer.
  Future<void> logout() async {
    _acceptedSession = false;
    _status = AuthStatus.unauthenticated;
    notifyListeners();
    await client.logout();
  }

  @override
  void dispose() {
    client.session.removeListener(_sessionChanged);
    super.dispose();
  }

  static String _errorCode(DioException error) {
    final data = error.response?.data;
    if (data is Map && data['code'] is String) return data['code'] as String;
    return error.response?.statusCode == 401
        ? 'INVALID_CREDENTIALS'
        : 'AUTHENTICATION_FAILED';
  }
}

final authClientProvider = Provider<AuthenticatedHttpClient>((ref) {
  final session = AuthSession(SecureRefreshTokenStore());
  final client = AuthenticatedHttpClient(
    baseUrl: Uri.parse(developmentBaseUrl),
    session: session,
  );
  ref.onDispose(client.close);
  return client;
});

final authControllerProvider = ChangeNotifierProvider<AuthController>((ref) {
  final controller = AuthController(ref.watch(authClientProvider));
  controller.restoreSession();
  return controller;
});

/// Extra key shared with the network interceptor without exposing its internal
/// implementation details to callers.
const skipAuthKey = 'power_iot.skip_auth';
