import 'package:flutter_test/flutter_test.dart';
import 'package:power_iot_app/features/billing/domain/models/billing_configuration.dart';

Map<String, Object?> _payload({
  Object? supported = true,
  Object? currentAssignment,
  Object? scheduledAssignment,
  List<Object?> plans = const [
    {
      'planCode': 'LIGHTING_NONCOMMERCIAL_RESIDENTIAL_NON_TOU',
      'usageClass': 'RESIDENTIAL'
    },
    {
      'planCode': 'LIGHTING_NONCOMMERCIAL_NONRESIDENTIAL_NON_TOU',
      'usageClass': 'NON_RESIDENTIAL_NONCOMMERCIAL'
    },
  ],
}) =>
    {
      'shop': {'id': '7', 'electricityTariff': 'LIGHTING_NONCOMMERCIAL'},
      'supported': supported,
      'compatiblePlans': plans,
      'currentAssignment': currentAssignment,
      'scheduledAssignment': scheduledAssignment,
    };

void main() {
  test('parses two distinct noncommercial plans and assignments', () {
    final configuration = BillingConfiguration.fromJson(_payload(
      currentAssignment: {
        'planCode': 'LIGHTING_NONCOMMERCIAL_RESIDENTIAL_NON_TOU',
        'validFrom': '2026-08-01',
      },
      scheduledAssignment: {
        'planCode': 'LIGHTING_NONCOMMERCIAL_NONRESIDENTIAL_NON_TOU',
        'validFrom': '2026-09-01',
      },
    ));

    expect(configuration.supported, isTrue);
    expect(configuration.plans.map((plan) => plan.code), [
      'LIGHTING_NONCOMMERCIAL_RESIDENTIAL_NON_TOU',
      'LIGHTING_NONCOMMERCIAL_NONRESIDENTIAL_NON_TOU',
    ]);
    expect(configuration.currentAssignment?.validFrom, '2026-08-01');
    expect(configuration.scheduledAssignment?.validFrom, '2026-09-01');
  });

  test('preserves unsupported tariff state without fabricating plans', () {
    final configuration = BillingConfiguration.fromJson(_payload(
      supported: false,
      plans: const [],
    ));
    expect(configuration.supported, isFalse);
    expect(configuration.plans, isEmpty);
  });

  test('rejects unknown response fields', () {
    final payload = _payload()..['unknown'] = true;
    expect(() => BillingConfiguration.fromJson(payload), throwsFormatException);
  });
}
