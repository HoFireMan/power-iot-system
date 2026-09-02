import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:power_iot_app/features/reports/domain/models/historical_energy_report.dart';
import 'package:power_iot_app/features/reports/domain/repositories/historical_energy_repository.dart';
import 'package:power_iot_app/features/reports/presentation/providers/historical_energy_provider.dart';
import 'package:power_iot_app/features/reports/presentation/screens/historical_energy_report_screen.dart';
import 'package:power_iot_app/features/shops/domain/models/shop.dart';
import 'package:power_iot_app/features/shops/domain/repositories/shops_repository.dart';
import 'package:power_iot_app/features/shops/providers/remote_shop_provider.dart';

class _ShopsRepository implements ShopsRepository {
  @override
  Future<ShopsSnapshot> fetchShops() async => const ShopsSnapshot(
        shops: [
          Shop(
            id: '7',
            code: 'SHOP-7',
            name: 'Test Shop',
            address: null,
            phone: null,
            isHead: false,
          ),
        ],
        currentShopId: '7',
      );
}

class _FlakyShopsRepository implements ShopsRepository {
  int fetchCount = 0;
  bool failNext = true;

  @override
  Future<ShopsSnapshot> fetchShops() async {
    fetchCount++;
    if (failNext) {
      failNext = false;
      throw StateError('temporary shop snapshot failure');
    }
    return const ShopsSnapshot(
      shops: [
        Shop(
          id: '7',
          code: 'SHOP-7',
          name: 'Test Shop',
          address: null,
          phone: null,
          isHead: false,
        ),
      ],
      currentShopId: '7',
    );
  }
}

class _DelayedReportRepository implements HistoricalEnergyRepository {
  final requests = <String, Completer<HistoricalEnergyReport>>{};

  @override
  Future<HistoricalEnergyReport> fetch(String shopId, String month) {
    final completer = Completer<HistoricalEnergyReport>();
    requests[month] = completer;
    return completer.future;
  }
}

class _ReportRepository implements HistoricalEnergyRepository {
  _ReportRepository(this.reports);
  final Map<String, HistoricalEnergyReport> reports;
  final requests = <String>[];

  @override
  Future<HistoricalEnergyReport> fetch(String shopId, String month) async {
    requests.add('$shopId/$month');
    return reports[month]!;
  }
}

HistoricalEnergyReport _report(String month) => HistoricalEnergyReport(
      month: month,
      timezone: 'Asia/Taipei',
      period: const HistoricalEnergyPeriod(
        start: '2026-08-01T00:00:00Z',
        end: '2026-09-01T00:00:00Z',
        cutoff: '2026-09-01T00:00:00Z',
        snapshot: '2026-09-02T00:00:00Z',
      ),
      summary: const HistoricalEnergyFacts(
        status: HistoricalEnergyStatus.partial,
        usageKwh: '0',
        expectedDurationSeconds: 3600,
        observedDurationSeconds: 1800,
        coverage: '0.5',
      ),
      measurementPoints: const [
        HistoricalEnergyPoint(
          measurementPointId: 'mp-zero',
          status: HistoricalEnergyStatus.complete,
          usageKwh: '0',
          expectedDurationSeconds: 3600,
          observedDurationSeconds: 3600,
          coverage: '1',
          warnings: [],
        ),
        HistoricalEnergyPoint(
          measurementPointId: 'mp-empty',
          status: HistoricalEnergyStatus.noData,
          usageKwh: null,
          expectedDurationSeconds: 3600,
          observedDurationSeconds: 0,
          coverage: '0',
          warnings: ['CONFLICTING_TELEMETRY_EXCLUDED'],
        ),
      ],
      warnings: const ['PARTIAL_MONITORING_DATA'],
    );

String _monthKey(DateTime value) =>
    '${value.year.toString().padLeft(4, '0')}-${value.month.toString().padLeft(2, '0')}';

void main() {
  testWidgets('renders explicit shop snapshot failure with retry',
      (tester) async {
    final shops = _FlakyShopsRepository();
    final currentMonth = _monthKey(DateTime.now());
    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          shopsRepositoryProvider.overrideWithValue(shops),
          historicalEnergyRepositoryProvider.overrideWithValue(
            _ReportRepository({currentMonth: _report(currentMonth)}),
          ),
        ],
        child: const MaterialApp(home: HistoricalEnergyReportScreen()),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('目前無法取得店家資料'), findsOneWidget);
    expect(find.text('尚未選擇店家'), findsNothing);
    expect(find.text('重試'), findsOneWidget);

    await tester.tap(find.text('重試'));
    await tester.pumpAndSettle();
    expect(shops.fetchCount, 2);
    expect(find.text('歷史用電報表'), findsOneWidget);
  });

  testWidgets('clamps a future initial month to the current month',
      (tester) async {
    final current = DateTime.now();
    final currentKey = _monthKey(current);
    final reports = _ReportRepository({currentKey: _report(currentKey)});
    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          shopsRepositoryProvider.overrideWithValue(_ShopsRepository()),
          historicalEnergyRepositoryProvider.overrideWithValue(reports),
        ],
        child: MaterialApp(
          home: HistoricalEnergyReportScreen(
            initialMonth: DateTime(current.year, current.month + 1),
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(
        find.text(
            '${current.year} 年 ${current.month.toString().padLeft(2, '0')} 月'),
        findsOneWidget);
    expect(reports.requests, contains('7/$currentKey'));
  });

  testWidgets('preserves a past initial month', (tester) async {
    final current = DateTime.now();
    final past = DateTime(current.year, current.month - 2);
    final pastKey = _monthKey(past);
    final reports = _ReportRepository({pastKey: _report(pastKey)});
    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          shopsRepositoryProvider.overrideWithValue(_ShopsRepository()),
          historicalEnergyRepositoryProvider.overrideWithValue(reports),
        ],
        child: MaterialApp(
          home: HistoricalEnergyReportScreen(initialMonth: past),
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(
        find.text('${past.year} 年 ${past.month.toString().padLeft(2, '0')} 月'),
        findsOneWidget);
    expect(reports.requests, contains('7/$pastKey'));
  });

  testWidgets('renders zero, no-data, partial facts and changes month',
      (tester) async {
    final repository = _ReportRepository({
      '2026-08': _report('2026-08'),
      '2026-07': _report('2026-07'),
    });
    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          shopsRepositoryProvider.overrideWithValue(_ShopsRepository()),
          historicalEnergyRepositoryProvider.overrideWithValue(repository),
        ],
        child: MaterialApp(
          home: HistoricalEnergyReportScreen(
            initialMonth: DateTime.utc(2026, 8),
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('0 kWh'), findsNWidgets(2));
    expect(find.text('部分資料'), findsOneWidget);
    expect(find.text('完整'), findsOneWidget);
    await tester.drag(find.byType(ListView), const Offset(0, -600));
    await tester.pumpAndSettle();
    expect(find.text('0 kWh'), findsOneWidget);
    expect(find.text('無資料'), findsOneWidget);
    expect(find.text('無資料 kWh'), findsOneWidget);

    await tester.tap(find.byIcon(Icons.chevron_left));
    await tester.pumpAndSettle();
    expect(find.text('2026 年 07 月'), findsOneWidget);
    expect(repository.requests, contains('7/2026-07'));
  });

  test('month-keyed provider isolates a late previous-month response',
      () async {
    final repository = _DelayedReportRepository();
    final container = ProviderContainer(overrides: [
      historicalEnergyRepositoryProvider.overrideWithValue(repository),
    ]);
    addTearDown(container.dispose);

    final first = container.read(
      historicalEnergyProvider((shopId: '7', month: '2026-08')).future,
    );
    final second = container.read(
      historicalEnergyProvider((shopId: '7', month: '2026-09')).future,
    );
    repository.requests['2026-08']!.complete(_report('2026-08'));
    await first;
    repository.requests['2026-09']!.complete(_report('2026-09'));
    final latest = await second;

    expect(latest.month, '2026-09');
    expect(
      container
          .read(historicalEnergyProvider((shopId: '7', month: '2026-09')))
          .value
          ?.month,
      '2026-09',
    );
  });
}
