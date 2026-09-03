import 'package:flutter/foundation.dart'; // 用於判斷是否為 Release 模式
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:device_preview/device_preview.dart'; // 引入套件
import 'package:power_iot_app/config/router.dart';
import 'package:power_iot_app/config/theme.dart';
import 'package:power_iot_app/features/auth/auth_controller.dart';
import 'package:go_router/go_router.dart';

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
    try {
      // Preserve an embedding app's overrides (including fake auth clients).
      ProviderScope.containerOf(context, listen: false);
      return const _PowerIoTAppView();
    } on StateError {
      // The standalone widget test and embedders without a scope still get a
      // complete app instead of a provider lookup failure.
      return const ProviderScope(child: _PowerIoTAppView());
    }
  }
}

class _PowerIoTAppView extends ConsumerWidget {
  const _PowerIoTAppView();

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    ref.listen(authControllerProvider, (previous, next) {
      if (previous?.isAuthenticated == true && !next.isAuthenticated) {
        WidgetsBinding.instance.addPostFrameCallback((_) {
          if (context.mounted) context.go('/login');
        });
      }
    });
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
      routerConfig: ref.watch(routerProvider),
    );
  }
}
