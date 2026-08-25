import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:power_iot_app/features/billing/domain/models/billing_configuration.dart';
import 'package:power_iot_app/features/shops/screens/shop_list_screen.dart';

BillingConfiguration _configuration({bool supported = true}) =>
    BillingConfiguration(
      shopId: '7',
      electricityTariff: supported ? 'LIGHTING_NONCOMMERCIAL' : 'LOW_VOLTAGE',
      supported: supported,
      plans: supported
          ? const [
              BillingPlan(
                code: 'LIGHTING_NONCOMMERCIAL_RESIDENTIAL_NON_TOU',
                usageClass: 'RESIDENTIAL',
              ),
              BillingPlan(
                code: 'LIGHTING_NONCOMMERCIAL_NONRESIDENTIAL_NON_TOU',
                usageClass: 'NON_RESIDENTIAL_NONCOMMERCIAL',
              ),
            ]
          : const [],
      currentAssignment: null,
      scheduledAssignment: null,
    );

void main() {
  testWidgets('administrator sees supported plans without rate selectors',
      (tester) async {
    await tester.pumpWidget(MaterialApp(
      home: BillingConfigurationDialog(
          configuration: _configuration(), editable: true),
    ));
    expect(find.text('住宅用'), findsNWidgets(2));
    expect(find.text('住宅以外非營業用'), findsNWidgets(2));
    expect(find.text('本月起生效'), findsOneWidget);
    expect(find.text('儲存'), findsOneWidget);
    expect(find.text('費率版本'), findsNothing);
    expect(find.text('SUMMER'), findsNothing);
  });

  testWidgets('normal user sees read-only configuration', (tester) async {
    await tester.pumpWidget(MaterialApp(
      home: BillingConfigurationDialog(
          configuration: _configuration(), editable: false),
    ));
    expect(find.text('僅限店家管理員修改'), findsOneWidget);
    expect(find.text('儲存'), findsNothing);
    final radio = tester.widget<RadioListTile<String>>(
        find.byType(RadioListTile<String>).first);
    // ignore: deprecated_member_use
    expect(radio.onChanged, isNull);
  });

  testWidgets(
      'unsupported tariff is explicit and has no configuration controls',
      (tester) async {
    await tester.pumpWidget(MaterialApp(
      home: BillingConfigurationDialog(
          configuration: _configuration(supported: false), editable: true),
    ));
    expect(find.text('目前尚未支援此電價類型的電費估算'), findsOneWidget);
    expect(find.byType(RadioListTile<String>), findsNothing);
  });
}
