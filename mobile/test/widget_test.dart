// This is a basic Flutter widget test.
//
// To perform an interaction with a widget in your test, use the WidgetTester
// utility in the flutter_test package. For example, you can send tap and scroll
// gestures. You can also use WidgetTester to find child widgets in the widget
// tree, read text, and verify that the values of widget properties are correct.

import 'dart:convert';
import 'dart:typed_data';

import 'package:dio/dio.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:power_iot_app/core/network/authenticated_http_client.dart';
import 'package:power_iot_app/features/auth/auth_controller.dart';
import 'package:power_iot_app/features/auth/screens/login_screen.dart';
import 'package:power_iot_app/main.dart';

class _LoginStore implements RefreshTokenStore {
  @override
  Future<String?> read() async => null;

  @override
  Future<void> write(String token) async {}

  @override
  Future<void> clear() async {}
}

class _LoginAdapter implements HttpClientAdapter {
  @override
  Future<ResponseBody> fetch(
    RequestOptions options,
    Stream<Uint8List>? requestStream,
    Future<void>? cancelFuture,
  ) async {
    return ResponseBody.fromString(
      jsonEncode(<String, String>{'code': 'INVALID_CREDENTIALS'}),
      401,
      headers: <String, List<String>>{
        Headers.contentTypeHeader: <String>['application/json'],
      },
    );
  }

  @override
  void close({bool force = false}) {}
}

void main() {
  testWidgets('Counter increments smoke test', (WidgetTester tester) async {
    // Build our app and trigger a frame.
    // 注意：原本的範例是用 const MyApp()，但我們現在的主程式叫 PowerIoTApp
    // 如果您的 main.dart 裡的類別名稱是 PowerIoTApp，這裡也要改
    await tester.pumpWidget(const PowerIoTApp());

    // 由於我們已經改寫了整個 App 結構 (變成了登入頁)，原本的計數器測試已經不適用了。
    // 這裡我們改寫一個簡單的測試：確認是否能看到 "電力管家" 標題

    // Verify that our app starts with the Login screen title.
    // 尋找登入頁面上的文字
    expect(find.text('電力管家'), findsOneWidget);
    expect(find.text('登入'), findsOneWidget); // 尋找登入按鈕文字
  });

  testWidgets('failed login clears the password field',
      (WidgetTester tester) async {
    final dio = Dio(BaseOptions(baseUrl: 'https://test.invalid'))
      ..httpClientAdapter = _LoginAdapter();
    final client = AuthenticatedHttpClient(
      baseUrl: Uri.parse('https://test.invalid'),
      session: AuthSession(_LoginStore()),
      dio: dio,
    );
    await tester.pumpWidget(
      ProviderScope(
        overrides: [authClientProvider.overrideWithValue(client)],
        child: const MaterialApp(home: LoginScreen()),
      ),
    );

    final fields = find.byType(TextField);
    await tester.enterText(fields.at(0), 'account');
    await tester.enterText(fields.at(1), 'secret');
    await tester.tap(find.text('登入'));
    await tester.pumpAndSettle();

    expect(tester.widget<TextField>(fields.at(1)).controller!.text, isEmpty);
  });
}
