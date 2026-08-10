import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../data/repositories/mock_admin_overview_repository.dart';
import '../../domain/models/admin_overview.dart';
import '../../domain/repositories/admin_overview_repository.dart';

abstract interface class CreateMeasurementPointRequestIdentitySource {
  String next();
}

class PendingBindDeviceRequest {
  const PendingBindDeviceRequest({
    required this.requestIdentity,
    required this.serialNumber,
    required this.measurementPointId,
  });

  final String requestIdentity;
  final String serialNumber;
  final String measurementPointId;
}

abstract interface class BindDeviceRequestIdentitySource {
  PendingBindDeviceRequest? get pending;

  String identityFor({
    required String serialNumber,
    required String measurementPointId,
  });

  void complete(String requestIdentity);
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

class MockBindDeviceRequestIdentitySource
    implements BindDeviceRequestIdentitySource {
  int _nextIdentity = 1;
  PendingBindDeviceRequest? _pending;

  @override
  PendingBindDeviceRequest? get pending => _pending;

  @override
  String identityFor({
    required String serialNumber,
    required String measurementPointId,
  }) {
    final pending = _pending;
    if (pending != null &&
        pending.serialNumber == serialNumber &&
        pending.measurementPointId == measurementPointId) {
      return pending.requestIdentity;
    }

    final identity =
        'mock-bind-device-${_nextIdentity.toString().padLeft(3, '0')}';
    _nextIdentity++;
    _pending = PendingBindDeviceRequest(
      requestIdentity: identity,
      serialNumber: serialNumber,
      measurementPointId: measurementPointId,
    );
    return identity;
  }

  @override
  void complete(String requestIdentity) {
    if (_pending?.requestIdentity == requestIdentity) {
      _pending = null;
    }
  }
}

final bindDeviceRequestIdentitySourceProvider =
    Provider<BindDeviceRequestIdentitySource>(
  (ref) => MockBindDeviceRequestIdentitySource(),
);

final adminOverviewRepositoryProvider = Provider<AdminOverviewRepository>(
  (ref) => MockAdminOverviewRepository(),
);

final adminOverviewProvider = FutureProvider<AdminOverview>((ref) {
  return ref.watch(adminOverviewRepositoryProvider).loadOverview();
});
