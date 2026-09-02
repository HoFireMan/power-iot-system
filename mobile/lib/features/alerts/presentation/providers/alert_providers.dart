import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:power_iot_app/features/auth/auth_controller.dart';
import 'package:power_iot_app/features/alerts/data/alert_repository.dart';
import 'package:power_iot_app/features/alerts/domain/models/alert.dart';

final alertRepositoryProvider = Provider<AlertRepository>(
  (ref) => AlertRepository(ref.watch(authClientProvider)),
);
final alertHistoryProvider =
    FutureProvider.autoDispose.family<AlertHistoryPage, String>(
  (ref, shopId) => ref.watch(alertRepositoryProvider).fetchHistory(shopId),
);
final alertSettingsProvider =
    FutureProvider.autoDispose.family<AlertSettings, String>(
  (ref, measurementPointId) =>
      ref.watch(alertRepositoryProvider).fetchSettings(measurementPointId),
);
