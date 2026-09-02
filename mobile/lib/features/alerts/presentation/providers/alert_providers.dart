import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:power_iot_app/features/auth/auth_controller.dart';
import 'package:power_iot_app/features/alerts/data/alert_repository.dart';
import 'package:power_iot_app/features/alerts/domain/models/alert.dart';

final alertRepositoryProvider = Provider<AlertRepository>((ref) => AlertRepository(ref.watch(authClientProvider)));
final alertHistoryProvider = FutureProvider.autoDispose.family<AlertHistoryPage, String>((ref, key) {
  final parts = key.split('|');
  return ref.watch(alertRepositoryProvider).fetchHistory(parts.first, measurementPointRef: parts.length > 1 && parts[1].isNotEmpty ? parts[1] : null);
});
final alertSettingsProvider = FutureProvider.autoDispose.family<AlertSettings, String>((ref, key) {
  final parts = key.split('|');
  return ref.watch(alertRepositoryProvider).fetchSettings(parts.first, parts[1]);
});
