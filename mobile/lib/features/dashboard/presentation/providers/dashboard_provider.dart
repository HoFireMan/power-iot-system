import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:power_iot_app/core/network/authenticated_http_client.dart';
import 'package:power_iot_app/core/network/remote_error.dart';
import 'package:power_iot_app/features/auth/auth_controller.dart';
import 'package:power_iot_app/features/dashboard/data/repositories/dashboard_repository_impl.dart';
import 'package:power_iot_app/features/dashboard/domain/models/dashboard.dart';
import 'package:power_iot_app/features/dashboard/domain/repositories/dashboard_repository.dart';

enum DashboardStatus { loading, success, error, unauthorized, notFound }

class DashboardState {
  const DashboardState.loading()
      : status = DashboardStatus.loading,
        data = null,
        error = null;
  const DashboardState.success(this.data)
      : status = DashboardStatus.success,
        error = null;
  const DashboardState.error(this.error)
      : status = DashboardStatus.error,
        data = null;
  const DashboardState.unauthorized()
      : status = DashboardStatus.unauthorized,
        data = null,
        error = null;
  const DashboardState.notFound()
      : status = DashboardStatus.notFound,
        data = null,
        error = null;

  final DashboardStatus status;
  final Dashboard? data;
  final Object? error;
}

final dashboardRepositoryProvider = Provider<DashboardRepository>((ref) {
  return RemoteDashboardRepository(ref.watch(authClientProvider));
});

final class DashboardNotifier extends StateNotifier<DashboardState> {
  DashboardNotifier(this.repository, this.authClient, this.shopId)
      : super(const DashboardState.loading()) {
    _epoch = authClient.authEpoch;
    authClient.addAuthEpochListener(_authEpochChanged);
    authClient.session.addListener(_sessionChanged);
    load();
  }

  final DashboardRepository repository;
  final AuthenticatedHttpClient authClient;
  final String shopId;
  int _request = 0;
  late int _epoch;
  bool _loadAfterAuthentication = false;

  void _authEpochChanged(int epoch) {
    if (!mounted || epoch == _epoch) return;
    _epoch = epoch;
    _request++;
    _loadAfterAuthentication = !authClient.isLogoutInProgress;
    state = const DashboardState.loading();
  }

  void _sessionChanged() {
    if (!mounted || !authClient.session.isAuthenticated) {
      _loadAfterAuthentication = false;
      return;
    }
    if (_loadAfterAuthentication) {
      _loadAfterAuthentication = false;
      load();
    }
  }

  Future<void> load() async {
    final request = ++_request;
    final epoch = authClient.authEpoch;
    if (mounted) state = const DashboardState.loading();
    try {
      final dashboard = await repository.fetchDashboard(shopId);
      if (mounted &&
          request == _request &&
          authClient.isSessionCurrent(epoch)) {
        state = DashboardState.success(dashboard);
      }
    } catch (error) {
      if (!mounted ||
          request != _request ||
          !authClient.isSessionCurrent(epoch)) {
        return;
      }
      if (isUnauthorizedError(error)) {
        await authClient.session.clearIfCurrent(
          () => authClient.isSessionCurrent(epoch),
        );
        if (mounted &&
            request == _request &&
            authClient.isSessionCurrent(epoch)) {
          state = const DashboardState.unauthorized();
        }
      } else if (error is DashboardShopNotFoundException) {
        state = const DashboardState.notFound();
      } else {
        state = DashboardState.error(error);
      }
    }
  }

  @override
  void dispose() {
    authClient.removeAuthEpochListener(_authEpochChanged);
    authClient.session.removeListener(_sessionChanged);
    super.dispose();
  }
}

final dashboardProvider =
    StateNotifierProvider.family<DashboardNotifier, DashboardState, String>(
        (ref, shopId) {
  return DashboardNotifier(
    ref.watch(dashboardRepositoryProvider),
    ref.watch(authClientProvider),
    shopId,
  );
});
