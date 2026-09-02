import 'package:power_iot_app/core/network/authenticated_http_client.dart';
import 'package:power_iot_app/features/reports/domain/models/historical_energy_report.dart';
import 'package:power_iot_app/features/reports/domain/repositories/historical_energy_repository.dart';

class RemoteHistoricalEnergyRepository implements HistoricalEnergyRepository {
  const RemoteHistoricalEnergyRepository(this.client);

  final AuthenticatedHttpClient client;

  @override
  Future<HistoricalEnergyReport> fetch(String shopId, String month) async {
    final response = await client.dio.get<Object?>(
      '/api/v1/shops/${Uri.encodeComponent(shopId)}/reports/energy',
      queryParameters: <String, String>{'month': month},
    );
    return HistoricalEnergyReport.fromJson(response.data);
  }
}
