import '../models/billing_configuration.dart';

abstract interface class BillingConfigurationRepository {
  Future<BillingConfiguration> fetch(String shopId);
  Future<void> setPlan(String shopId, String planCode);
}
