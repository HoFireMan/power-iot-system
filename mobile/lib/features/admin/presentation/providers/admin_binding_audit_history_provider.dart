import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:power_iot_app/features/admin/data/repositories/admin_binding_audit_repository.dart';
import 'package:power_iot_app/features/admin/domain/models/admin_binding_audit.dart';
import 'package:power_iot_app/features/auth/auth_controller.dart';

class AdminAuditHistoryQuery {
  const AdminAuditHistoryQuery(
    this.shopId,
    this.action,
    this.measurementPointId,
    this.deviceId,
  );
  final String shopId;
  final String action;
  final String measurementPointId;
  final String deviceId;
  @override
  bool operator ==(Object other) =>
      other is AdminAuditHistoryQuery &&
      other.shopId == shopId &&
      other.action == action &&
      other.measurementPointId == measurementPointId &&
      other.deviceId == deviceId;
  @override
  int get hashCode => Object.hash(shopId, action, measurementPointId, deviceId);
}

final adminAuditHistoryQueryProvider =
    FutureProvider.family<AdminBindingAuditHistoryPage, AdminAuditHistoryQuery>(
        (ref, query) {
  if (!ref.watch(authControllerProvider).isAuthenticated) {
    throw StateError('authentication required');
  }
  return RemoteAdminBindingAuditRepository(
    ref.watch(authClientProvider),
    query.shopId,
  ).load(
    action: query.action,
    measurementPointId: query.measurementPointId,
    deviceId: query.deviceId,
  );
});

/// The family key includes the authorized Shop and every server-side filter.
/// A response for an old key is never published to the newly selected view.
