import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:power_iot_app/core/network/authenticated_http_client.dart';
import 'package:power_iot_app/core/network/remote_error.dart';
import 'package:power_iot_app/features/auth/auth_controller.dart';
import 'package:power_iot_app/features/measurement_point_detail/data/repositories/measurement_point_detail_repository_impl.dart';
import 'package:power_iot_app/features/measurement_point_detail/domain/models/measurement_point_detail.dart';
import 'package:power_iot_app/features/measurement_point_detail/domain/repositories/measurement_point_detail_repository.dart';

final measurementPointDetailRepositoryProvider =
    Provider<MeasurementPointDetailRepository>(
  (ref) =>
      RemoteMeasurementPointDetailRepository(ref.watch(authClientProvider)),
);

enum MeasurementPointDetailStatus {
  loading,
  success,
  error,
  unauthorized,
  notFound,
}

class MeasurementPointDetailState {
  const MeasurementPointDetailState.loading()
      : status = MeasurementPointDetailStatus.loading,
        data = null,
        error = null;
  const MeasurementPointDetailState.success(this.data, {this.error})
      : status = MeasurementPointDetailStatus.success;
  const MeasurementPointDetailState.error(this.error)
      : status = MeasurementPointDetailStatus.error,
        data = null;
  const MeasurementPointDetailState.unauthorized()
      : status = MeasurementPointDetailStatus.unauthorized,
        data = null,
        error = null;
  const MeasurementPointDetailState.notFound()
      : status = MeasurementPointDetailStatus.notFound,
        data = null,
        error = null;
  final MeasurementPointDetailStatus status;
  final MeasurementPointDetail? data;
  final Object? error;
}

class MeasurementPointDetailNotifier
    extends StateNotifier<MeasurementPointDetailState> {
  MeasurementPointDetailNotifier(
      this.repository, this.authClient, this.shopId, this.measurementPointRef)
      : super(const MeasurementPointDetailState.loading()) {
    _epoch = authClient.authEpoch;
    authClient.addAuthEpochListener(_authEpochChanged);
    load();
  }
  final MeasurementPointDetailRepository repository;
  final AuthenticatedHttpClient authClient;
  final String shopId;
  final String measurementPointRef;
  late int _epoch;
  int _request = 0;
  void _authEpochChanged(int epoch) {
    if (!mounted || epoch == _epoch) return;
    _epoch = epoch;
    _request++;
    state = const MeasurementPointDetailState.loading();
  }

  Future<void> load() async {
    final request = ++_request;
    final epoch = authClient.authEpoch;
    state = const MeasurementPointDetailState.loading();
    try {
      final detail = await repository.fetchMeasurementPointDetail(
          shopId, measurementPointRef);
      if (mounted &&
          request == _request &&
          authClient.isSessionCurrent(epoch)) {
        state = MeasurementPointDetailState.success(detail);
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
        state = const MeasurementPointDetailState.unauthorized();
      } else if (error is MeasurementPointNotFoundException) {
        state = const MeasurementPointDetailState.notFound();
      } else {
        state = MeasurementPointDetailState.error(error);
      }
    }
  }

  @override
  void dispose() {
    authClient.removeAuthEpochListener(_authEpochChanged);
    super.dispose();
  }
}

final measurementPointDetailProvider = StateNotifierProvider.autoDispose.family<
    MeasurementPointDetailNotifier, MeasurementPointDetailState, String>(
  (ref, routeKey) {
    final parts = routeKey.split('|');
    return MeasurementPointDetailNotifier(
      ref.watch(measurementPointDetailRepositoryProvider),
      ref.watch(authClientProvider),
      parts.length > 1 ? parts[0] : '',
      parts.length > 1 ? parts[1] : routeKey,
    );
  },
);
