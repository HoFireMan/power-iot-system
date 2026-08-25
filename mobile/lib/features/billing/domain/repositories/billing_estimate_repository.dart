import '../models/billing_estimate.dart';

abstract interface class BillingEstimateRepository {
  Future<BillingEstimate> fetch(String shopId, String month);
}
