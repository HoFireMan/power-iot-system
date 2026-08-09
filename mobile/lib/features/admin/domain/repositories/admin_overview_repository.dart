import '../models/admin_overview.dart';

/// Product-data boundary for loading the admin overview.
abstract interface class AdminOverviewRepository {
  Future<AdminOverview> loadOverview();
}
