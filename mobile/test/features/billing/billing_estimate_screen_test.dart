import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:power_iot_app/core/network/authenticated_http_client.dart';
import 'package:power_iot_app/features/billing/domain/models/billing_estimate.dart';
import 'package:power_iot_app/features/billing/domain/repositories/billing_estimate_repository.dart';
import 'package:power_iot_app/features/billing/presentation/providers/billing_estimate_provider.dart';
import 'package:power_iot_app/features/billing/presentation/screens/billing_estimate_screen.dart';
import 'package:power_iot_app/features/shops/domain/models/shop.dart';
import 'package:power_iot_app/features/shops/domain/repositories/shops_repository.dart';
import 'package:power_iot_app/features/shops/providers/remote_shop_provider.dart';

class _Store implements RefreshTokenStore {
  @override
  Future<String?> read() async => null;
  @override
  Future<void> write(String token) async {}
  @override
  Future<void> clear() async {}
}

class _Shops implements ShopsRepository {
  @override
  Future<ShopsSnapshot> fetchShops() async => const ShopsSnapshot(
        shops: [
          Shop(
              id: '7',
              code: 'S7',
              name: 'Test Shop',
              address: null,
              phone: null,
              isHead: false),
        ],
        currentShopId: '7',
      );
}

class _Estimates implements BillingEstimateRepository {
  _Estimates(this.value);
  final BillingEstimate value;
  final months = <String>[];

  @override
  Future<BillingEstimate> fetch(String shopId, String month) async {
    months.add(month);
    return value;
  }
}

BillingEstimate _estimate({
  BillingEstimateStatus status = BillingEstimateStatus.completeEstimate,
  String? usage = '500',
  BillingEstimateCharges? charges = const BillingEstimateCharges(
    energyCharge: '72.3',
    minimumMonthlyCharge: '100.0',
    minimumChargeAdjustment: '27.7',
    estimatedTotal: '100',
  ),
  String coverage = '0.5',
}) =>
    BillingEstimate(
      status: status,
      month: '2026-08',
      period: const BillingEstimatePeriod(
        start: '2026-07-31T16:00:00Z',
        end: '2026-08-31T16:00:00Z',
        timezone: 'Asia/Taipei',
      ),
      shop: const BillingEstimateShop(id: '7', code: 'S7', name: 'Test Shop'),
      tariff: const BillingEstimateTariff(
        electricityTariff: 'LIGHTING_COMMERCIAL',
        planCode: 'LIGHTING_COMMERCIAL_NON_TOU',
        usageClass: null,
        season: 'SUMMER',
      ),
      rateSet: const BillingEstimateRateSet(
        version: 'TAIPOWER_2025_10_01',
        currency: 'TWD',
        includesTax: true,
      ),
      energy: BillingEstimateEnergy(
        usageKwh: usage,
        expectedDurationSeconds: 100,
        observedDurationSeconds: 50,
        coverage: coverage,
      ),
      tiers: const [
        BillingEstimateTier(
          fromKwh: '0',
          toKwh: '330',
          usageKwh: '330',
          ratePerKwh: '2.71',
          subtotal: '894.3',
        ),
      ],
      charges: charges,
      warnings: const ['BOTTOM_DEGREE_NOT_MODELED'],
    );

Future<void> _pump(
  WidgetTester tester,
  BillingEstimate estimate, {
  DateTime? initialMonth,
}) async {
  final client = AuthenticatedHttpClient(
    baseUrl: Uri.parse('https://test.invalid'),
    session: AuthSession(_Store()),
  );
  final estimates = _Estimates(estimate);
  addTearDown(client.close);
  await tester.pumpWidget(
    ProviderScope(
      overrides: [
        shopsRepositoryProvider.overrideWithValue(_Shops()),
        billingEstimateRepositoryProvider.overrideWithValue(estimates),
      ],
      child: MaterialApp(
        home: BillingEstimateScreen(
          initialMonth: initialMonth ?? DateTime(2026, 8),
        ),
      ),
    ),
  );
  await tester.pumpAndSettle();
}

void main() {
  testWidgets(
      'complete estimate renders server amount, coverage, tiers, and disclaimer',
      (tester) async {
    await _pump(
        tester,
        _estimate(
            status: BillingEstimateStatus.completeEstimate, coverage: '1'));
    expect(find.text('預估電費'), findsOneWidget);
    expect(find.text('NT\$100'), findsOneWidget);
    expect(find.text('100.0%'), findsOneWidget);
    expect(find.text('級距明細'), findsOneWidget);
    expect(find.textContaining('實際應繳金額仍以台電帳單為準'), findsOneWidget);
  });

  testWidgets('partial data and minimum adjustment are visible',
      (tester) async {
    await _pump(
        tester, _estimate(status: BillingEstimateStatus.partialDataEstimate));
    expect(find.text('部分期間缺少監測資料，預估金額可能偏低。'), findsOneWidget);
    expect(find.text('最低月費調整'), findsOneWidget);
    expect(find.text('NT\$27.7'), findsOneWidget);
  });

  testWidgets('no data does not fabricate a zero-dollar amount',
      (tester) async {
    await _pump(
      tester,
      _estimate(
          status: BillingEstimateStatus.noData,
          usage: null,
          charges: null,
          coverage: '0'),
    );
    expect(find.text('無可用監測資料'), findsOneWidget);
    expect(find.textContaining('NT\$0'), findsNothing);
  });

  testWidgets(
      'historical month selector moves backward without client calculation',
      (tester) async {
    await _pump(tester, _estimate());
    expect(find.text('2026 年 08 月'), findsOneWidget);
    await tester.tap(find.byIcon(Icons.chevron_left));
    await tester.pumpAndSettle();
    expect(find.text('2026 年 07 月'), findsOneWidget);
  });
}
