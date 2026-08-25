import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:power_iot_app/features/auth/auth_controller.dart';
import 'package:power_iot_app/features/billing/data/repositories/billing_configuration_repository_impl.dart';
import 'package:power_iot_app/features/billing/domain/models/billing_configuration.dart';
import 'package:power_iot_app/features/billing/domain/repositories/billing_configuration_repository.dart';

final billingConfigurationRepositoryProvider =
    Provider<BillingConfigurationRepository>((ref) {
  return RemoteBillingConfigurationRepository(ref.watch(authClientProvider));
});

final billingConfigurationProvider = FutureProvider.autoDispose
    .family<BillingConfiguration, String>((ref, shopId) {
  return ref.watch(billingConfigurationRepositoryProvider).fetch(shopId);
});
