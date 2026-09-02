import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../../../core/network/remote_error.dart';
import '../../data/repositories/admin_overview_repository_impl.dart';
import '../../data/repositories/mock_admin_overview_repository.dart';
import '../../../auth/auth_controller.dart';
import '../../../profile/presentation/providers/profile_provider.dart';
import '../../../shops/providers/remote_shop_provider.dart';
import '../../domain/models/admin_overview.dart';
import '../../domain/repositories/admin_overview_repository.dart';
import '../../../shops/domain/models/shop.dart';

/// Resolves the authenticated admin's view shop from the server-authorized
/// snapshot. The selected shop is a local view preference; the snapshot is
/// the only authority and its first shop is the safe default when the server
/// has no current-shop preference.
Shop? selectedAdminShop(ShopsState state) {
  final snapshot = state.data;
  if (state.status != RemoteStatus.success || snapshot == null) {
    return null;
  }
  final selectedId = state.selectedShopId;
  if (selectedId != null) {
    for (final shop in snapshot.shops) {
      if (shop.id == selectedId) return shop;
    }
  }
  // CurrentShopID is only a server-provided view preference. It may select a
  // Shop for display when that Shop is present in the authorized snapshot, but
  // it never grants authorization and is never sent as an authority value.
  for (final shop in snapshot.shops) {
    if (shop.id == snapshot.currentShopId) return shop;
  }
  return snapshot.shops.isEmpty ? null : snapshot.shops.first;
}

String? selectedAdminShopId(ShopsState state) => selectedAdminShop(state)?.id;

class PendingCreateMeasurementPointRequest {
  const PendingCreateMeasurementPointRequest({
    required this.requestIdentity,
    required this.shopId,
    required this.name,
  });

  final String requestIdentity;
  final String shopId;
  final String name;
}

abstract interface class CreateMeasurementPointRequestIdentitySource {
  PendingCreateMeasurementPointRequest? get pending;

  String identityFor({
    required String shopId,
    required String name,
  });

  void complete(String requestIdentity);

  /// Drops unresolved identity at an authentication-session boundary.
  void resetForAuthEpoch();

  /// Explicitly abandons the unresolved command. This is only called by a
  /// deliberate user start-over action; failed delivery is never cleared
  /// implicitly.
  void abandon();
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

  /// Drops unresolved identity at an authentication-session boundary.
  void resetForAuthEpoch();
}

class MockCreateMeasurementPointRequestIdentitySource
    implements CreateMeasurementPointRequestIdentitySource {
  int _nextIdentity = 1;
  PendingCreateMeasurementPointRequest? _pending;

  @override
  PendingCreateMeasurementPointRequest? get pending => _pending;

  @override
  String identityFor({
    required String shopId,
    required String name,
  }) {
    final normalizedShopId = shopId.trim();
    final normalizedName = name.trim();
    final pending = _pending;
    if (pending != null &&
        pending.shopId == normalizedShopId &&
        pending.name == normalizedName) {
      return pending.requestIdentity;
    }

    final identity =
        'mock-create-measurement-point-${_nextIdentity.toString().padLeft(3, '0')}';
    _nextIdentity++;
    _pending = PendingCreateMeasurementPointRequest(
      requestIdentity: identity,
      shopId: normalizedShopId,
      name: normalizedName,
    );
    return identity;
  }

  @override
  void complete(String requestIdentity) {
    if (_pending?.requestIdentity == requestIdentity) {
      _pending = null;
    }
  }

  @override
  void abandon() {
    _pending = null;
  }

  @override
  void resetForAuthEpoch() {
    _pending = null;
  }
}

final createMeasurementPointRequestIdentitySourceProvider =
    Provider<CreateMeasurementPointRequestIdentitySource>((ref) {
  final source = MockCreateMeasurementPointRequestIdentitySource();
  final client = ref.read(authClientProvider);
  void onAuthEpochChanged(int _) => source.resetForAuthEpoch();
  client.addAuthEpochListener(onAuthEpochChanged);
  ref.onDispose(() => client.removeAuthEpochListener(onAuthEpochChanged));
  return source;
});

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

  @override
  void resetForAuthEpoch() {
    _pending = null;
  }
}

final bindDeviceRequestIdentitySourceProvider =
    Provider<BindDeviceRequestIdentitySource>((ref) {
  final source = MockBindDeviceRequestIdentitySource();
  final client = ref.read(authClientProvider);
  void onAuthEpochChanged(int _) => source.resetForAuthEpoch();
  client.addAuthEpochListener(onAuthEpochChanged);
  ref.onDispose(() => client.removeAuthEpochListener(onAuthEpochChanged));
  return source;
});

class PendingReplaceDeviceRequest {
  const PendingReplaceDeviceRequest({
    required this.requestIdentity,
    required this.currentAssignmentId,
    required this.serialNumber,
  });

  final String requestIdentity;
  final String currentAssignmentId;
  final String serialNumber;
}

abstract interface class ReplaceDeviceRequestIdentitySource {
  PendingReplaceDeviceRequest? get pending;

  String identityFor({
    required String currentAssignmentId,
    required String serialNumber,
  });

  void complete(String requestIdentity);

  void resetForAuthEpoch();
}

class MockReplaceDeviceRequestIdentitySource
    implements ReplaceDeviceRequestIdentitySource {
  int _nextIdentity = 1;
  PendingReplaceDeviceRequest? _pending;

  @override
  PendingReplaceDeviceRequest? get pending => _pending;

  @override
  String identityFor({
    required String currentAssignmentId,
    required String serialNumber,
  }) {
    final pending = _pending;
    if (pending != null &&
        pending.currentAssignmentId == currentAssignmentId &&
        pending.serialNumber == serialNumber) {
      return pending.requestIdentity;
    }

    final identity =
        'mock-replace-device-${_nextIdentity.toString().padLeft(3, '0')}';
    _nextIdentity++;
    _pending = PendingReplaceDeviceRequest(
      requestIdentity: identity,
      currentAssignmentId: currentAssignmentId,
      serialNumber: serialNumber,
    );
    return identity;
  }

  @override
  void complete(String requestIdentity) {
    if (_pending?.requestIdentity == requestIdentity) {
      _pending = null;
    }
  }

  @override
  void resetForAuthEpoch() {
    _pending = null;
  }
}

final replaceDeviceRequestIdentitySourceProvider =
    Provider<ReplaceDeviceRequestIdentitySource>((ref) {
  final source = MockReplaceDeviceRequestIdentitySource();
  final client = ref.read(authClientProvider);
  void onAuthEpochChanged(int _) => source.resetForAuthEpoch();
  client.addAuthEpochListener(onAuthEpochChanged);
  ref.onDispose(() => client.removeAuthEpochListener(onAuthEpochChanged));
  return source;
});

class PendingRelocateDeviceRequest {
  const PendingRelocateDeviceRequest({
    required this.requestIdentity,
    required this.currentAssignmentId,
    required this.targetMeasurementPointId,
  });

  final String requestIdentity;
  final String currentAssignmentId;
  final String targetMeasurementPointId;
}

abstract interface class RelocateDeviceRequestIdentitySource {
  PendingRelocateDeviceRequest? get pending;

  String identityFor({
    required String currentAssignmentId,
    required String targetMeasurementPointId,
  });

  void complete(String requestIdentity);

  void resetForAuthEpoch();
}

class MockRelocateDeviceRequestIdentitySource
    implements RelocateDeviceRequestIdentitySource {
  int _nextIdentity = 1;
  PendingRelocateDeviceRequest? _pending;

  @override
  PendingRelocateDeviceRequest? get pending => _pending;

  @override
  String identityFor({
    required String currentAssignmentId,
    required String targetMeasurementPointId,
  }) {
    final pending = _pending;
    if (pending != null &&
        pending.currentAssignmentId == currentAssignmentId &&
        pending.targetMeasurementPointId == targetMeasurementPointId) {
      return pending.requestIdentity;
    }

    final identity =
        'mock-relocate-device-${_nextIdentity.toString().padLeft(3, '0')}';
    _nextIdentity++;
    _pending = PendingRelocateDeviceRequest(
      requestIdentity: identity,
      currentAssignmentId: currentAssignmentId,
      targetMeasurementPointId: targetMeasurementPointId,
    );
    return identity;
  }

  @override
  void complete(String requestIdentity) {
    if (_pending?.requestIdentity == requestIdentity) {
      _pending = null;
    }
  }

  @override
  void resetForAuthEpoch() {
    _pending = null;
  }
}

final relocateDeviceRequestIdentitySourceProvider =
    Provider<RelocateDeviceRequestIdentitySource>((ref) {
  final source = MockRelocateDeviceRequestIdentitySource();
  final client = ref.read(authClientProvider);
  void onAuthEpochChanged(int _) => source.resetForAuthEpoch();
  client.addAuthEpochListener(onAuthEpochChanged);
  ref.onDispose(() => client.removeAuthEpochListener(onAuthEpochChanged));
  return source;
});

class PendingUnbindDeviceRequest {
  const PendingUnbindDeviceRequest({
    required this.requestIdentity,
    required this.currentAssignmentId,
    required this.reason,
  });

  final String requestIdentity;
  final String currentAssignmentId;
  final String reason;
}

abstract interface class UnbindDeviceRequestIdentitySource {
  PendingUnbindDeviceRequest? get pending;

  String identityFor({
    required String currentAssignmentId,
    String reason = '',
  });

  void complete(String requestIdentity);

  void resetForAuthEpoch();
}

class MockUnbindDeviceRequestIdentitySource
    implements UnbindDeviceRequestIdentitySource {
  int _nextIdentity = 1;
  PendingUnbindDeviceRequest? _pending;

  @override
  PendingUnbindDeviceRequest? get pending => _pending;

  @override
  String identityFor({
    required String currentAssignmentId,
    String reason = '',
  }) {
    final normalizedAssignmentId = currentAssignmentId.trim();
    final normalizedReason = reason.trim();
    final pending = _pending;
    if (pending != null &&
        pending.currentAssignmentId == normalizedAssignmentId &&
        pending.reason == normalizedReason) {
      return pending.requestIdentity;
    }

    final identity =
        'mock-unbind-device-${_nextIdentity.toString().padLeft(3, '0')}';
    _nextIdentity++;
    _pending = PendingUnbindDeviceRequest(
      requestIdentity: identity,
      currentAssignmentId: normalizedAssignmentId,
      reason: normalizedReason,
    );
    return identity;
  }

  @override
  void complete(String requestIdentity) {
    if (_pending?.requestIdentity == requestIdentity) {
      _pending = null;
    }
  }

  @override
  void resetForAuthEpoch() {
    _pending = null;
  }
}

final unbindDeviceRequestIdentitySourceProvider =
    Provider<UnbindDeviceRequestIdentitySource>((ref) {
  final source = MockUnbindDeviceRequestIdentitySource();
  final client = ref.read(authClientProvider);
  void onAuthEpochChanged(int _) => source.resetForAuthEpoch();
  client.addAuthEpochListener(onAuthEpochChanged);
  ref.onDispose(() => client.removeAuthEpochListener(onAuthEpochChanged));
  return source;
});

final adminOverviewRepositoryProvider =
    Provider<AdminOverviewRepository>((ref) {
  // Keep unauthenticated preview/tests deterministic; every accepted session
  // uses the real scoped HTTP projection.
  if (!ref.watch(authControllerProvider).isAuthenticated) {
    return MockAdminOverviewRepository();
  }
  final shops = ref.watch(shopsProvider);
  if (shops.status == RemoteStatus.unauthorized) {
    throw const UnauthorizedException();
  }
  if (shops.status == RemoteStatus.error && shops.error != null) {
    // Preserve the transport error so the screen can apply the existing safe
    // authorization/validation/conflict/network/server category mapping.
    throw shops.error!;
  }
  final shopId = selectedAdminShopId(shops);
  // Do not fall back to the legacy mock shop state for authenticated calls.
  // Until the server-authorized snapshot contains a Shop, no request is
  // authorized or sent; the screen can expose its retry action instead.
  if (shopId == null || shopId.trim().isEmpty) {
    throw StateError('no authorized shop is available');
  }
  return RemoteAdminOverviewRepository(ref.watch(authClientProvider), shopId);
});

/// Reload the authoritative remote Shop snapshot before retrying its dependent
/// overview. Unauthenticated preview routes continue using their local mock.
Future<void> retryAdminOverview(WidgetRef ref) async {
  if (ref.read(authControllerProvider).isAuthenticated) {
    // Always replace the cached snapshot: a successful response may still be
    // stale after a revocation or shop membership change.
    await ref.read(shopsProvider.notifier).load();
  }
  // Await the replacement projection rather than merely invalidating it. A
  // mutation screen remains locked until the authoritative refresh finishes.
  final refreshedOverview = ref.refresh(adminOverviewProvider.future);
  await refreshedOverview;
}

final adminOverviewProvider = FutureProvider<AdminOverview>((ref) {
  // The repository provider watches the server-authorized Shop snapshot. A
  // Shop selection therefore invalidates this FutureProvider, and Riverpod
  // discards an older in-flight result instead of publishing stale rows.
  return ref.watch(adminOverviewRepositoryProvider).loadOverview();
});
