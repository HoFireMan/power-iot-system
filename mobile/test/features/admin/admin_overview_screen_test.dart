import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:power_iot_app/features/admin/data/repositories/mock_admin_overview_repository.dart';
import 'package:power_iot_app/features/admin/domain/models/admin_overview.dart';
import 'package:power_iot_app/features/admin/domain/repositories/admin_overview_repository.dart';
import 'package:power_iot_app/features/admin/presentation/providers/admin_overview_provider.dart';
import 'package:power_iot_app/features/admin/presentation/screens/admin_overview_screen.dart';

void main() {
  testWidgets('Admin shell renders the two overview sections', (tester) async {
    await tester.pumpWidget(const _TestApp());
    await tester.pumpAndSettle();

    expect(find.text('Measurement Points'), findsOneWidget);
    expect(find.text('Devices / Inventory'), findsOneWidget);
  });

  testWidgets('mock repository success renders deterministic overview data',
      (tester) async {
    const repository = MockAdminOverviewRepository();
    final overview = await repository.loadOverview();
    expect(overview.measurementPoints.first.name, 'Main Hall');
    expect(overview.devices.first.serialNumber, 'SN-METER-001');

    await tester.pumpWidget(
      _TestApp(
        overrides: [
          adminOverviewRepositoryProvider.overrideWithValue(repository),
        ],
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('Main Hall'), findsOneWidget);
    expect(find.text('Meter A'), findsOneWidget);
    expect(find.text('SN-METER-001'), findsOneWidget);
    expect(find.text('Online'), findsOneWidget);
  });

  testWidgets('empty mock data renders explicit empty states', (tester) async {
    const emptyOverview = AdminOverview(
      measurementPoints: [],
      devices: [],
    );

    await tester.pumpWidget(
      _TestApp(
        overrides: [
          adminOverviewRepositoryProvider.overrideWithValue(
            const _FixedAdminOverviewRepository(emptyOverview),
          ),
        ],
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('No measurement points available.'), findsOneWidget);
    expect(find.text('No devices / inventory available.'), findsOneWidget);
  });

  testWidgets('loading state is presented while overview is pending',
      (tester) async {
    final pendingRepository = _PendingAdminOverviewRepository();

    await tester.pumpWidget(
      _TestApp(
        overrides: [
          adminOverviewRepositoryProvider.overrideWithValue(pendingRepository),
        ],
      ),
    );
    await tester.pump();

    expect(find.text('Loading admin overview…'), findsOneWidget);

    pendingRepository.completer.complete(
      const AdminOverview(measurementPoints: [], devices: []),
    );
    await tester.pumpAndSettle();
  });
}

class _TestApp extends StatelessWidget {
  const _TestApp({this.overrides = const []});

  final List<Override> overrides;

  @override
  Widget build(BuildContext context) {
    return ProviderScope(
      overrides: overrides,
      child: const MaterialApp(home: AdminOverviewScreen()),
    );
  }
}

class _FixedAdminOverviewRepository implements AdminOverviewRepository {
  const _FixedAdminOverviewRepository(this.overview);

  final AdminOverview overview;

  @override
  Future<AdminOverview> loadOverview() async => overview;
}

class _PendingAdminOverviewRepository implements AdminOverviewRepository {
  final Completer<AdminOverview> completer = Completer<AdminOverview>();

  @override
  Future<AdminOverview> loadOverview() => completer.future;
}
