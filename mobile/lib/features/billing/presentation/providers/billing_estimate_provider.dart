import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:power_iot_app/features/auth/auth_controller.dart';
import '../../data/repositories/billing_estimate_repository_impl.dart';
import '../../domain/models/billing_estimate.dart';
import '../../domain/repositories/billing_estimate_repository.dart';

final billingEstimateRepositoryProvider = Provider<BillingEstimateRepository>((
  ref,
) {
  return RemoteBillingEstimateRepository(ref.watch(authClientProvider));
});

final billingEstimateProvider = FutureProvider.autoDispose
    .family<BillingEstimate, ({String shopId, String month})>((ref, request) {
  return ref
      .watch(billingEstimateRepositoryProvider)
      .fetch(request.shopId, request.month);
});
