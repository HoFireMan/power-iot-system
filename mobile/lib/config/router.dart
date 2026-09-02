import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:power_iot_app/features/admin/presentation/screens/admin_overview_screen.dart';
import 'package:power_iot_app/features/admin/presentation/screens/assignment_history_screen.dart';
import 'package:power_iot_app/features/admin/presentation/screens/bind_device_screen.dart';
import 'package:power_iot_app/features/admin/presentation/screens/create_measurement_point_screen.dart';
import 'package:power_iot_app/features/admin/presentation/screens/relocate_device_screen.dart';
import 'package:power_iot_app/features/admin/presentation/screens/replace_device_screen.dart';
import 'package:power_iot_app/features/admin/presentation/screens/unbind_device_screen.dart';
import 'package:power_iot_app/features/auth/auth_controller.dart';
import 'package:power_iot_app/features/alerts/presentation/screens/alert_history_screen.dart';
import 'package:power_iot_app/features/alerts/presentation/screens/alert_settings_screen.dart';
import 'package:power_iot_app/features/auth/screens/login_screen.dart';
import 'package:power_iot_app/features/billing/presentation/screens/billing_estimate_screen.dart';
import 'package:power_iot_app/features/dashboard/dashboard_route_observer.dart';
import 'package:power_iot_app/features/dashboard/dashboard_screen.dart';
import 'package:power_iot_app/features/devices/presentation/screens/device_alert_screen.dart';
import 'package:power_iot_app/features/devices/presentation/screens/device_list_screen.dart';
import 'package:power_iot_app/features/profile/screens/profile_screen.dart';
import 'package:power_iot_app/features/reports/presentation/screens/historical_energy_report_screen.dart';
import 'package:power_iot_app/features/shops/screens/shop_list_screen.dart';
import 'package:power_iot_app/features/measurement_point_detail/presentation/screens/measurement_point_detail_screen.dart';

final routerProvider = Provider<GoRouter>((ref) {
  final auth = ref.watch(authControllerProvider);
  final router = createRouter(auth);
  ref.onDispose(router.dispose);
  return router;
});

GoRouter createRouter(AuthController auth) => GoRouter(
      initialLocation: '/login',
      refreshListenable: auth,
      observers: <NavigatorObserver>[dashboardRouteObserver],
      redirect: (context, state) => authRedirect(auth, state.uri.path),
      routes: [
        GoRoute(
            path: '/login', builder: (context, state) => const LoginScreen()),
        GoRoute(
          path: '/dashboard',
          builder: (context, state) => const DashboardScreen(),
        ),
        GoRoute(
          path: '/billing/estimate',
          builder: (context, state) => const BillingEstimateScreen(),
        ),
        GoRoute(
          path: '/reports/energy',
          builder: (context, state) => const HistoricalEnergyReportScreen(),
        ),
        GoRoute(
          path: '/devices',
          builder: (context, state) => const DeviceListScreen(),
        ),
        GoRoute(
          path: '/shops/:shopId/measurement-points/:measurementPointRef',
          builder: (context, state) => MeasurementPointDetailScreen(
            shopId: state.pathParameters['shopId']!,
            measurementPointRef: state.pathParameters['measurementPointRef']!,
          ),
        ),
        GoRoute(
          path: '/shops/:shopId/alerts',
          builder: (context, state) => AlertHistoryScreen(
            shopId: state.pathParameters['shopId']!,
            measurementPointRef: state.uri.queryParameters['measurementPointRef'],
          ),
        ),
        GoRoute(
          path: '/shops/:shopId/measurement-points/:measurementPointRef/alert-settings',
          builder: (context, state) => AlertSettingsScreen(
            shopId: state.pathParameters['shopId']!,
            measurementPointRef: state.pathParameters['measurementPointRef']!,
          ),
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
        GoRoute(
          path: '/shops',
          builder: (context, state) => const ShopListScreen(),
        ),
        GoRoute(
          path: '/admin',
          builder: (context, state) => const AdminOverviewScreen(),
        ),
        GoRoute(
          path: '/admin/assignment-history',
          builder: (context, state) => const AssignmentHistoryScreen(),
        ),
        GoRoute(
          path: '/admin/create-measurement-point',
          builder: (context, state) => const CreateMeasurementPointScreen(),
        ),
        GoRoute(
          path: '/admin/bind-device',
          builder: (context, state) => const BindDeviceScreen(),
        ),
        GoRoute(
          path: '/admin/replace-device/:assignmentId',
          builder: (context, state) => ReplaceDeviceScreen(
              assignmentId: state.pathParameters['assignmentId']!),
        ),
        GoRoute(
          path: '/admin/relocate-device/:assignmentId',
          builder: (context, state) => RelocateDeviceScreen(
              assignmentId: state.pathParameters['assignmentId']!),
        ),
        GoRoute(
          path: '/admin/unbind-device/:assignmentId',
          builder: (context, state) => UnbindDeviceScreen(
              assignmentId: state.pathParameters['assignmentId']!),
        ),
        GoRoute(
          path: '/admin/mock',
          builder: (context, state) => const AdminOverviewScreen(),
        ),
        GoRoute(
          path: '/admin/mock/assignment-history',
          builder: (context, state) => const AssignmentHistoryScreen(),
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

String? authRedirect(AuthController auth, String location) {
  final protected = _isProtectedLocation(location);
  if (auth.status == AuthStatus.restoring) {
    return protected ? '/login' : null;
  }
  if (!auth.isSessionReady && protected) return '/login';
  if (auth.isSessionReady && location == '/login') return '/dashboard';
  return null;
}

bool _isProtectedLocation(String location) =>
    location == '/dashboard' ||
    location == '/billing/estimate' ||
    location == '/reports/energy' ||
    location == '/profile' ||
    location == '/devices' ||
    location.startsWith('/devices/') ||
    location.startsWith('/shops/') &&
        location.contains('/measurement-points/') ||
    location == '/shops' ||
    location.startsWith('/shops/') && location.endsWith('/alerts') ||
    location == '/admin' ||
    location.startsWith('/admin/measurement-points/') ||
    location.startsWith('/admin/') ||
    location.startsWith('/admin/mock');
