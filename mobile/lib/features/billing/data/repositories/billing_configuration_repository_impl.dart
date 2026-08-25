import 'package:power_iot_app/core/network/authenticated_http_client.dart';
import '../../domain/models/billing_configuration.dart';
import '../../domain/repositories/billing_configuration_repository.dart';

class RemoteBillingConfigurationRepository
    implements BillingConfigurationRepository {
  const RemoteBillingConfigurationRepository(this.client);
  final AuthenticatedHttpClient client;

  @override
  Future<BillingConfiguration> fetch(String shopId) async {
    final response = await client.dio
        .get<Object?>('/api/v1/shops/$shopId/billing/configuration');
    return BillingConfiguration.fromJson(response.data);
  }

  @override
  Future<void> setPlan(String shopId, String planCode) async {
    await client.dio.put<Object?>(
      '/api/v1/shops/$shopId/billing/configuration',
      data: {'planCode': planCode},
    );
  }
}
