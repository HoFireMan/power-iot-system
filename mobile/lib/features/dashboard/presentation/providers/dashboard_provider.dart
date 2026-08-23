import 'dart:async';

import 'package:flutter/widgets.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:power_iot_app/config/dashboard_poll_config.dart';
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
  const DashboardState.success(this.data, {this.error})
      : status = DashboardStatus.success;
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

typedef DashboardClock = DateTime Function();
typedef DashboardTimerFactory = Timer Function(
    Duration duration, void Function(Timer) callback);

final class DashboardNotifier extends StateNotifier<DashboardState> {
  DashboardNotifier(
    this.repository,
    this.authClient,
    this.shopId, {
    Duration? pollInterval,
    DashboardClock? clock,
    DashboardTimerFactory? timerFactory,
  })  : _pollInterval = pollInterval ?? dashboardPollDuration(),
        _clock = clock ?? DateTime.now,
        _timerFactory = timerFactory ?? Timer.periodic,
        super(const DashboardState.loading()) {
    _epoch = authClient.authEpoch;
    authClient.addAuthEpochListener(_authEpochChanged);
    authClient.session.addListener(_sessionChanged);
    load();
  }

  final DashboardRepository repository;
  final AuthenticatedHttpClient authClient;
  final String shopId;
  final Duration _pollInterval;
  final DashboardClock _clock;
  final DashboardTimerFactory _timerFactory;
  int _request = 0;
  late int _epoch;
  bool _loadAfterAuthentication = false;
  bool _reloadAfterCurrentRequest = false;
  bool _pollingSuspended = false;
  bool _appResumed = false;
  bool _routeVisible = false;
  Timer? _timer;
  Future<void>? _inFlight;
  DateTime? _lastSuccessfulAt;

  void _authEpochChanged(int epoch) {
    if (!mounted || epoch == _epoch) return;
    _epoch = epoch;
    _request++;
    _loadAfterAuthentication = !authClient.isLogoutInProgress;
    if (authClient.isLogoutInProgress) _pollingSuspended = true;
    state = const DashboardState.loading();
  }

  void _sessionChanged() {
    if (!mounted || !authClient.session.isAuthenticated) {
      _loadAfterAuthentication = false;
      return;
    }
    _pollingSuspended = false;
    _updatePolling();
    if (_loadAfterAuthentication) {
      _loadAfterAuthentication = false;
      if (_inFlight != null) {
        _reloadAfterCurrentRequest = true;
      } else {
        load();
      }
    }
  }

  /// Starts or stops polling when the host app changes lifecycle state.
  void setAppLifecycleState(AppLifecycleState lifecycleState) {
    if (!mounted) return;
    _appResumed = lifecycleState == AppLifecycleState.resumed;
    _updatePolling();
    if (_canBackgroundRefresh && _isStale) load(background: true);
  }

  /// Starts or stops polling when this route becomes covered or visible.
  void setRouteVisible(bool visible) {
    if (!mounted) return;
    _routeVisible = visible;
    _updatePolling();
    if (_canBackgroundRefresh && _isStale) load(background: true);
  }

  bool get _canBackgroundRefresh =>
      _appResumed && _routeVisible && !_pollingSuspended;

  bool get _isStale {
    final lastSuccess = _lastSuccessfulAt;
    return lastSuccess != null &&
        _clock().difference(lastSuccess) >= _pollInterval;
  }

  void _stopPolling() {
    _timer?.cancel();
    _timer = null;
  }

  void _updatePolling() {
    if (_canBackgroundRefresh) {
      _timer ??= _timerFactory(_pollInterval, _onPollTick);
    } else {
      _stopPolling();
    }
  }

  void _onPollTick(Timer _) {
    if (!_canBackgroundRefresh || !mounted) return;
    // A periodic tick is deliberately dropped while the previous request is
    // active. This prevents a slow endpoint from creating a request backlog.
    if (_inFlight != null) return;
    load(background: true);
  }

  Future<void> load({bool background = false}) async {
    if (!mounted || _inFlight != null) return;
    if (background && !_canBackgroundRefresh) return;

    final request = ++_request;
    final epoch = authClient.authEpoch;
    if (!background && mounted) state = const DashboardState.loading();

    final operation = _fetch(request, epoch, background);
    _inFlight = operation;
    try {
      await operation;
    } finally {
      if (identical(_inFlight, operation)) {
        _inFlight = null;
        if (_reloadAfterCurrentRequest) {
          _reloadAfterCurrentRequest = false;
          if (mounted &&
              authClient.session.isAuthenticated &&
              !authClient.isLogoutInProgress) {
            load();
          }
        }
      }
    }
  }

  Future<void> _fetch(int request, int epoch, bool background) async {
    try {
      final dashboard = await repository.fetchDashboard(shopId);
      if (mounted &&
          request == _request &&
          authClient.isSessionCurrent(epoch)) {
        _lastSuccessfulAt = _clock();
        state = DashboardState.success(dashboard);
      }
    } catch (error) {
      if (!mounted ||
          request != _request ||
          !authClient.isSessionCurrent(epoch)) {
        return;
      }
      if (isUnauthorizedError(error)) {
        _pollingSuspended = true;
        _stopPolling();
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
      } else if (background && state.data != null) {
        // Keep rendering the last good dashboard during transient outages.
        state = DashboardState.success(state.data!, error: error);
      } else {
        state = DashboardState.error(error);
      }
    }
  }

  @override
  void dispose() {
    _timer?.cancel();
    _timer = null;
    authClient.removeAuthEpochListener(_authEpochChanged);
    authClient.session.removeListener(_sessionChanged);
    super.dispose();
  }
}

final dashboardProvider = StateNotifierProvider.autoDispose
    .family<DashboardNotifier, DashboardState, String>((
  ref,
  shopId,
) {
  return DashboardNotifier(
    ref.watch(dashboardRepositoryProvider),
    ref.watch(authClientProvider),
    shopId,
  );
});
