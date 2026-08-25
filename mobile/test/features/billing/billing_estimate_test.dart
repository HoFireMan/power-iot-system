import 'package:flutter_test/flutter_test.dart';
import 'package:power_iot_app/features/billing/domain/models/billing_estimate.dart';

Map<String, Object?> _payload({
  Object? status = 'COMPLETE_ESTIMATE',
  Object? usageKwh = '500',
  Object? charges = const {
    'energyCharge': '1533.5',
    'minimumMonthlyCharge': '100.0',
    'minimumChargeAdjustment': '0.0',
    'estimatedTotal': '1534',
  },
}) =>
    {
      'status': status,
      'month': '2026-08',
      'period': {
        'start': '2026-07-31T16:00:00Z',
        'end': '2026-08-31T16:00:00Z',
        'timezone': 'Asia/Taipei',
      },
      'shop': {'id': '7', 'code': 'S7', 'name': 'Test Shop'},
      'tariff': {
        'electricityTariff': 'LIGHTING_COMMERCIAL',
        'planCode': 'LIGHTING_COMMERCIAL_NON_TOU',
        'usageClass': null,
        'season': 'SUMMER',
      },
      'rateSet': {
        'version': 'TAIPOWER_2025_10_01',
        'currency': 'TWD',
        'includesTax': true,
      },
      'energy': {
        'usageKwh': usageKwh,
        'expectedDurationSeconds': 2678400,
        'observedDurationSeconds': usageKwh == null ? 0 : 2678400,
        'coverage': usageKwh == null ? '0' : '1',
      },
      'tiers': [
        {
          'fromKwh': '0',
          'toKwh': '330',
          'usageKwh': '330',
          'ratePerKwh': '2.71',
          'subtotal': '894.3',
        },
      ],
      'charges': charges,
      'warnings': ['BOTTOM_DEGREE_NOT_MODELED'],
    };

void main() {
  test('parses exact decimal strings, tiers, and charges', () {
    final estimate = BillingEstimate.fromJson(_payload());
    expect(estimate.status, BillingEstimateStatus.completeEstimate);
    expect(estimate.energy.usageKwh, '500');
    expect(estimate.charges?.estimatedTotal, '1534');
    expect(estimate.tiers.single.ratePerKwh, '2.71');
    expect(estimate.warnings, ['BOTTOM_DEGREE_NOT_MODELED']);
  });

  test('preserves no-data nulls and valid zero strings', () {
    final noData = BillingEstimate.fromJson(
      _payload(status: 'NO_DATA', usageKwh: null, charges: null),
    );
    expect(noData.energy.usageKwh, isNull);
    expect(noData.charges, isNull);

    final zero = BillingEstimate.fromJson(
      _payload(status: 'PARTIAL_DATA_ESTIMATE', usageKwh: '0'),
    );
    expect(zero.energy.usageKwh, '0');
    expect(zero.charges?.estimatedTotal, '1534');
  });

  test('accepts every stable domain status and rejects unknown status', () {
    for (final status in BillingEstimateStatus.values) {
      expect(
        BillingEstimate.fromJson(_payload(status: status.code)).status,
        status,
      );
    }
    expect(
      () => BillingEstimate.fromJson(_payload(status: 'UNKNOWN')),
      throwsFormatException,
    );
  });

  test('rejects unknown response fields', () {
    final payload = _payload()..['unexpected'] = true;
    expect(() => BillingEstimate.fromJson(payload), throwsFormatException);
  });
}
