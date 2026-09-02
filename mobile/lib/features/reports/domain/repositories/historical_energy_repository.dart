import '../models/historical_energy_report.dart';

abstract interface class HistoricalEnergyRepository {
  Future<HistoricalEnergyReport> fetch(String shopId, String month);
}
