// #C:\Code\PowerWork\power-iot-system\mobile\lib\config\router.dart
import 'package:go_router/go_router.dart';
import 'package:power_iot_app/features/auth/screens/login_screen.dart';
import 'package:power_iot_app/features/profile/screens/profile_screen.dart';
import 'package:power_iot_app/features/dashboard/dashboard_screen.dart';
import 'package:power_iot_app/features/devices/presentation/screens/device_list_screen.dart';
import 'package:power_iot_app/features/devices/presentation/screens/device_alert_screen.dart';
// 新增引用
import 'package:power_iot_app/features/shops/screens/shop_list_screen.dart';
import 'package:power_iot_app/features/admin/presentation/screens/admin_overview_screen.dart';
import 'package:power_iot_app/features/admin/presentation/screens/bind_device_screen.dart';
import 'package:power_iot_app/features/admin/presentation/screens/create_measurement_point_screen.dart';
import 'package:power_iot_app/features/admin/presentation/screens/replace_device_screen.dart';
import 'package:power_iot_app/features/admin/presentation/screens/relocate_device_screen.dart';
import 'package:power_iot_app/features/admin/presentation/screens/unbind_device_screen.dart';

final routerConfig = GoRouter(
  initialLocation: '/login',
  routes: [
    GoRoute(
      path: '/login',
      builder: (context, state) => const LoginScreen(),
    ),
    GoRoute(
      path: '/dashboard',
      builder: (context, state) => const DashboardScreen(),
    ),
    GoRoute(
      path: '/devices',
      builder: (context, state) => const DeviceListScreen(),
    ),
    GoRoute(
      path: '/devices/:id/alert',
      builder: (context, state) {
        final deviceId = state.pathParameters['id']!;
        return DeviceAlertScreen(deviceId: deviceId);
      },
    ),
    GoRoute(
      path: '/profile',
      builder: (context, state) => const ProfileScreen(),
    ),
    // 新增：店家列表路由
    GoRoute(
      path: '/shops',
      builder: (context, state) => const ShopListScreen(),
    ),
    GoRoute(
      path: '/admin/mock',
      builder: (context, state) => const AdminOverviewScreen(),
    ),
    GoRoute(
      path: '/admin/mock/create-measurement-point',
      builder: (context, state) => const CreateMeasurementPointScreen(),
    ),
    GoRoute(
      path: '/admin/mock/bind-device',
      builder: (context, state) => const BindDeviceScreen(),
    ),
    GoRoute(
      path: '/admin/mock/replace-device/:assignmentId',
      builder: (context, state) => ReplaceDeviceScreen(
        assignmentId: state.pathParameters['assignmentId']!,
      ),
    ),
    GoRoute(
      path: '/admin/mock/relocate-device/:assignmentId',
      builder: (context, state) => RelocateDeviceScreen(
        assignmentId: state.pathParameters['assignmentId']!,
      ),
    ),
    GoRoute(
      path: '/admin/mock/unbind-device/:assignmentId',
      builder: (context, state) => UnbindDeviceScreen(
        assignmentId: state.pathParameters['assignmentId']!,
      ),
    ),
  ],
);
