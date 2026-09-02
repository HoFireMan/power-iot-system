import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:power_iot_app/core/network/remote_error.dart';
import 'package:power_iot_app/features/admin/domain/models/admin_overview.dart';
import 'package:power_iot_app/features/admin/domain/models/device_assignment.dart';
import 'package:power_iot_app/features/admin/domain/models/device_inventory.dart';
import 'package:power_iot_app/features/admin/domain/models/measurement_point.dart';
import 'package:power_iot_app/features/admin/presentation/providers/admin_overview_provider.dart';
import 'package:power_iot_app/features/admin/presentation/screens/assignment_history_screen.dart';

void main() {
  test('history is newest-first, uses ID tie-break, and composes filters', () {
    final assignments = <DeviceAssignment>[
      _assignment('z', DateTime.utc(2026, 1, 2),
          point: 'mp-1', device: 'd-1', validTo: DateTime.utc(2026, 1, 3)),
      _assignment('a', DateTime.utc(2026, 1, 2),
          point: 'mp-2', device: 'd-2', validTo: DateTime.utc(2026, 1, 3)),
      _assignment('old', DateTime.utc(2025, 1, 1),
          point: 'mp-1', device: 'd-2', validTo: DateTime.utc(2025, 1, 2)),
      _assignment('active', DateTime.utc(2026, 1, 3),
          point: 'mp-1', device: 'd-1'),
    ];

    expect(
      sortAssignmentHistory(assignments).map((item) => item.id),
      ['active', 'a', 'z', 'old'],
    );
    expect(
      filterAssignmentHistory(
        assignments: assignments,
        status: AssignmentHistoryStatusFilter.ended,
        measurementPointId: 'mp-1',
        deviceId: 'd-2',
      ).map((item) => item.id),
      ['old'],
    );
    expect(
      filterAssignmentHistory(
        assignments: assignments,
        status: AssignmentHistoryStatusFilter.active,
      ).map((item) => item.id),
      ['active'],
    );
    expect(
      formatAdminTimestamp(DateTime.parse('2026-01-02T11:04:05.123456+08:00')),
      '2026-01-02 03:04:05.123456 UTC',
    );
  });

  testWidgets('renders readable joins and remains strictly read-only',
      (tester) async {
    await tester.pumpWidget(_TestApp(overview: _overview));
    await tester.pumpAndSettle();

    expect(find.text('Assignment History'), findsOneWidget);
    expect(find.text('Office · Meter B'), findsOneWidget);
    expect(find.text('Kitchen · Meter A'), findsOneWidget);
    expect(find.text('Serial: serial-1'), findsOneWidget);
    expect(find.text('MAC: AA:BB'), findsOneWidget);
    expect(find.text('Valid from: 2026-01-02 03:04:05 UTC'), findsOneWidget);
    expect(find.text('Valid to: 2026-01-03 03:04:05 UTC'), findsOneWidget);
    expect(find.text('Replace Device'), findsNothing);
    expect(find.text('Relocate Device'), findsNothing);
    expect(find.text('Unbind Device'), findsNothing);
  });

  testWidgets(
      'status and point/device filters expose empty and no-match states',
      (tester) async {
    await tester.pumpWidget(_TestApp(overview: _overview));
    await tester.pumpAndSettle();

    await tester.tap(find.byKey(const Key('assignment-history-status-filter')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Active').last);
    await tester.pumpAndSettle();
    expect(find.text('Kitchen · Meter A'), findsOneWidget);
    expect(find.text('No assignment history matches the selected filters.'),
        findsNothing);

    await tester.tap(
      find.byKey(const Key('assignment-history-measurement-point-filter')),
    );
    await tester.pumpAndSettle();
    await tester.tap(find.text('Office').last);
    await tester.pumpAndSettle();
    expect(find.text('No assignment history matches the selected filters.'),
        findsOneWidget);

    await tester.tap(find.byKey(const Key('assignment-history-status-filter')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('All').last);
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('assignment-history-device-filter')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Meter A').last);
    await tester.pumpAndSettle();
    expect(find.text('No assignment history matches the selected filters.'),
        findsOneWidget);
  });

  testWidgets('shows loading while the authorized overview is pending',
      (tester) async {
    final pending = Completer<AdminOverview>();
    await tester.pumpWidget(_TestApp(loader: () => pending.future));
    await tester.pump();

    expect(find.text('Loading admin overview…'), findsOneWidget);

    pending.complete(_overview);
    await tester.pumpAndSettle();
    expect(find.text('Kitchen · Meter A'), findsOneWidget);
  });

  testWidgets('retries a recoverable overview error', (tester) async {
    var attempts = 0;
    await tester.pumpWidget(
      _TestApp(
        loader: () async {
          attempts++;
          if (attempts == 1) throw const UnauthorizedException();
          return _overview;
        },
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('Authorization required. Please sign in again.'),
        findsOneWidget);
    await tester.tap(find.text('Retry'));
    await tester.pumpAndSettle();

    expect(attempts, 2);
    expect(find.text('Kitchen · Meter A'), findsOneWidget);
  });

  testWidgets('distinguishes empty history and safe error states',
      (tester) async {
    await tester.pumpWidget(
      const _TestApp(
          overview: AdminOverview(measurementPoints: [], devices: [])),
    );
    await tester.pumpAndSettle();
    expect(find.text('No assignment history available.'), findsOneWidget);

    await tester.pumpWidget(
      _TestApp(key: UniqueKey(), error: const UnauthorizedException()),
    );
    await tester.pumpAndSettle();
    expect(find.text('Authorization required. Please sign in again.'),
        findsOneWidget);
    expect(find.text('Retry'), findsOneWidget);

    await tester.pumpWidget(
      _TestApp(
        key: UniqueKey(),
        error: StateError('no authorized shop is available'),
      ),
    );
    await tester.pumpAndSettle();
    expect(find.text('No authorized Shop is available.'), findsOneWidget);
  });
}

DeviceAssignment _assignment(
  String id,
  DateTime validFrom, {
  required String point,
  required String device,
  DateTime? validTo,
}) =>
    DeviceAssignment(
      id: id,
      deviceId: device,
      measurementPointId: point,
      validFrom: validFrom,
      validTo: validTo,
    );

final _overview = AdminOverview(
  measurementPoints: const [
    MeasurementPoint(id: 'mp-1', shopId: 'shop-1', name: 'Kitchen'),
    MeasurementPoint(id: 'mp-2', shopId: 'shop-1', name: 'Office'),
  ],
  devices: const [
    DeviceInventory(
      id: 'd-1',
      name: 'Meter A',
      serialNumber: 'serial-1',
      macAddress: 'AA:BB',
      status: 'Online',
    ),
    DeviceInventory(
      id: 'd-2',
      name: 'Meter B',
      serialNumber: '',
      status: 'Offline',
    ),
  ],
  assignmentHistory: [
    _assignment(
      'ended',
      DateTime.utc(2026, 1, 2, 3, 4, 5),
      point: 'mp-2',
      device: 'd-2',
      validTo: DateTime.utc(2026, 1, 3, 3, 4, 5),
    ),
    _assignment(
      'active',
      DateTime.utc(2026, 1, 4),
      point: 'mp-1',
      device: 'd-1',
    ),
  ],
);

class _TestApp extends StatelessWidget {
  const _TestApp({super.key, this.overview, this.error, this.loader});

  final AdminOverview? overview;
  final Object? error;
  final Future<AdminOverview> Function()? loader;

  @override
  Widget build(BuildContext context) {
    return ProviderScope(
      key: key,
      overrides: [
        adminOverviewProvider.overrideWith((ref) {
          if (loader != null) return loader!();
          return () async {
            if (error != null) throw error!;
            return overview!;
          }();
        }),
      ],
      child: const MaterialApp(home: AssignmentHistoryScreen()),
    );
  }
}
