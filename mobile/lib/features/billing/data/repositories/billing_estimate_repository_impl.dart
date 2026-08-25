import 'package:power_iot_app/core/network/authenticated_http_client.dart';
import '../../domain/models/billing_estimate.dart';
import '../../domain/repositories/billing_estimate_repository.dart';

class RemoteBillingEstimateRepository implements BillingEstimateRepository {
  const RemoteBillingEstimateRepository(this.client);
  final AuthenticatedHttpClient client;

  @override
  Future<BillingEstimate> fetch(String shopId, String month) async {
    final response = await client.dio.get<Object?>(
      '/api/v1/shops/$shopId/billing/estimate',
      queryParameters: <String, String>{'month': month},
    );
    return BillingEstimate.fromJson(response.data);
  }
}
