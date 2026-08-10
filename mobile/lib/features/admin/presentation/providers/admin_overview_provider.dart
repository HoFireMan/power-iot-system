import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../data/repositories/mock_admin_overview_repository.dart';
import '../../domain/models/admin_overview.dart';
import '../../domain/repositories/admin_overview_repository.dart';

abstract interface class CreateMeasurementPointRequestIdentitySource {
  String next();
}

class MockCreateMeasurementPointRequestIdentitySource
    implements CreateMeasurementPointRequestIdentitySource {
  int _nextIdentity = 1;

  @override
  String next() {
    final identity =
        'mock-create-measurement-point-${_nextIdentity.toString().padLeft(3, '0')}';
    _nextIdentity++;
    return identity;
  }
}

final createMeasurementPointRequestIdentitySourceProvider =
    Provider<CreateMeasurementPointRequestIdentitySource>(
  (ref) => MockCreateMeasurementPointRequestIdentitySource(),
);

final adminOverviewRepositoryProvider = Provider<AdminOverviewRepository>(
  (ref) => MockAdminOverviewRepository(),
);

final adminOverviewProvider = FutureProvider<AdminOverview>((ref) {
  return ref.watch(adminOverviewRepositoryProvider).loadOverview();
});
