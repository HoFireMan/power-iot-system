import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../data/repositories/mock_admin_overview_repository.dart';
import '../../domain/models/admin_overview.dart';
import '../../domain/repositories/admin_overview_repository.dart';

final adminOverviewRepositoryProvider = Provider<AdminOverviewRepository>(
  (ref) => const MockAdminOverviewRepository(),
);

final adminOverviewProvider = FutureProvider<AdminOverview>((ref) {
  return ref.watch(adminOverviewRepositoryProvider).loadOverview();
});
