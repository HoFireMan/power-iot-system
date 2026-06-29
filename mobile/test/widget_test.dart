// This is a basic Flutter widget test.
//
// To perform an interaction with a widget in your test, use the WidgetTester
// utility in the flutter_test package. For example, you can send tap and scroll
// gestures. You can also use WidgetTester to find child widgets in the widget
// tree, read text, and verify that the values of widget properties are correct.

import 'package:flutter_test/flutter_test.dart';

import 'package:power_iot_app/main.dart';

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
}
