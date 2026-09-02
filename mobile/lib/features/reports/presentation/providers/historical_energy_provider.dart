import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:power_iot_app/features/auth/auth_controller.dart';
import 'package:power_iot_app/features/reports/data/repositories/historical_energy_repository_impl.dart';
import 'package:power_iot_app/features/reports/domain/models/historical_energy_report.dart';
import 'package:power_iot_app/features/reports/domain/repositories/historical_energy_repository.dart';

final historicalEnergyRepositoryProvider =
    Provider<HistoricalEnergyRepository>((ref) {
  return RemoteHistoricalEnergyRepository(ref.watch(authClientProvider));
});

/// The month is part of the provider key, so a late response for an old month
/// remains isolated and cannot overwrite the currently watched month.
final historicalEnergyProvider = FutureProvider.autoDispose
    .family<HistoricalEnergyReport, ({String shopId, String month})>(
        (ref, request) {
  return ref
      .watch(historicalEnergyRepositoryProvider)
      .fetch(request.shopId, request.month);
});
