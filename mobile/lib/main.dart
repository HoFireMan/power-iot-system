// #C:\Code\PowerWork\power-iot-system\mobile\lib\main.dart
import 'package:flutter/foundation.dart'; // 用於判斷是否為 Release 模式
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:device_preview/device_preview.dart'; // 引入套件
import 'package:power_iot_app/config/router.dart';
import 'package:power_iot_app/config/theme.dart';

void main() {
  runApp(
    // 使用 DevicePreview 包裹
    DevicePreview(
      // 只有在 debug 模式下才啟用預覽，發布時會自動關閉
      enabled: !kReleaseMode,
      builder: (context) => const ProviderScope(
        child: PowerIoTApp(),
      ),
    ),
  );
}

class PowerIoTApp extends StatelessWidget {
  const PowerIoTApp({super.key});

  @override
  Widget build(BuildContext context) {
    return MaterialApp.router(
      title: 'Power IoT System',
      debugShowCheckedModeBanner: false,

      // DevicePreview 1.3.1 requires this flag for inherited preview metrics.
      // ignore: deprecated_member_use
      useInheritedMediaQuery: true,
      locale: DevicePreview.locale(context),
      builder: DevicePreview.appBuilder,

      // 套用主題
      theme: AppTheme.lightTheme,

      // 套用 GoRouter 路由配置
      routerConfig: routerConfig,
    );
  }
}
