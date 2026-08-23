import 'package:power_iot_app/features/dashboard/domain/models/dashboard.dart';

abstract interface class DashboardRepository {
  Future<Dashboard> fetchDashboard(String shopId);
}
