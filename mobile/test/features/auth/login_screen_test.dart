import 'dart:convert';
import 'dart:typed_data';

import 'package:dio/dio.dart';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:power_iot_app/core/network/authenticated_http_client.dart';
import 'package:power_iot_app/features/auth/auth_controller.dart';
import 'package:power_iot_app/features/auth/screens/login_screen.dart';

class _Store implements RefreshTokenStore {
  @override
  Future<String?> read() async => null;

  @override
  Future<void> write(String token) async {}

  @override
  Future<void> clear() async {}
}

class _SuccessfulLoginAdapter implements HttpClientAdapter {
  @override
  Future<ResponseBody> fetch(
    RequestOptions options,
    Stream<Uint8List>? requestStream,
    Future<void>? cancelFuture,
  ) async {
    return ResponseBody.fromString(
      jsonEncode(<String, Object>{
        'tokenType': 'Bearer',
        'accessToken': 'access',
        'refreshToken': 'refresh',
        'accessTokenExpiresAt': '2030-01-01T00:00:00Z',
        'refreshTokenExpiresAt': '2030-02-01T00:00:00Z',
      }),
      200,
      headers: <String, List<String>>{
        Headers.contentTypeHeader: <String>['application/json'],
      },
    );
  }

  @override
  void close({bool force = false}) {}
}

void main() {
  testWidgets(
      'successful login clears password before removing the login screen',
      (WidgetTester tester) async {
    final dio = Dio(BaseOptions(baseUrl: 'https://test.invalid'))
      ..httpClientAdapter = _SuccessfulLoginAdapter();
    final client = AuthenticatedHttpClient(
      baseUrl: Uri.parse('https://test.invalid'),
      session: AuthSession(_Store()),
      dio: dio,
    );
    final auth = AuthController(client);
    final router = GoRouter(
      initialLocation: '/login',
      routes: [
        GoRoute(
          path: '/login',
          builder: (context, state) => const LoginScreen(),
        ),
        GoRoute(
          path: '/dashboard',
          builder: (context, state) => const Text('dashboard'),
        ),
      ],
    );
    addTearDown(() {
      router.dispose();
      client.close();
    });

    await tester.pumpWidget(
      ProviderScope(
        overrides: [authControllerProvider.overrideWith((ref) => auth)],
        child: MaterialApp.router(routerConfig: router),
      ),
    );
    await tester.pump();

    final fields = find.byType(TextField);
    await tester.enterText(fields.at(0), 'account');
    await tester.enterText(fields.at(1), 'secret');
    await tester.tap(find.text('登入'));
    await tester.pumpAndSettle();

    expect(find.text('dashboard'), findsOneWidget);
  });
}
