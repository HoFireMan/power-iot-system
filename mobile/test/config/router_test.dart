import 'package:flutter_test/flutter_test.dart';
import 'package:power_iot_app/config/router.dart';
import 'package:power_iot_app/core/network/authenticated_http_client.dart';
import 'package:power_iot_app/features/auth/auth_controller.dart';

class _Store implements RefreshTokenStore {
  @override
  Future<String?> read() async => null;

  @override
  Future<void> write(String token) async {}

  @override
  Future<void> clear() async {}
}

TokenPair _pair() => TokenPair(
      accessToken: 'access',
      refreshToken: 'refresh',
      accessTokenExpiresAt: DateTime.utc(2030),
      refreshTokenExpiresAt: DateTime.utc(2030, 2),
    );

void main() {
  test('protected routes redirect and authenticated restore enters dashboard',
      () async {
    final session = AuthSession(_Store());
    final client = AuthenticatedHttpClient(
      baseUrl: Uri.parse('https://test.invalid'),
      session: session,
    );
    final auth = AuthController(client);
    addTearDown(auth.dispose);

    for (final location in <String>[
      '/dashboard',
      '/profile',
      '/devices',
      '/devices/device-1/alert',
      '/shops/7/measurement-points/00000000-0000-4000-8000-000000000001',
      '/shops',
      '/admin/mock',
      '/admin/mock/bind-device',
    ]) {
      expect(authRedirect(auth, location), '/login');
    }

    // A pre-existing/injected volatile session is not accepted bootstrap.
    await session.setTokens(_pair());
    expect(auth.status, AuthStatus.restoring);
    expect(authRedirect(auth, '/login'), isNull);
    expect(authRedirect(auth, '/dashboard'), '/login');
    await session.clear();
    expect(authRedirect(auth, '/profile'), '/login');
  });
}
