import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:power_iot_app/features/admin/data/repositories/mock_admin_overview_repository.dart';
import 'package:power_iot_app/features/admin/domain/models/device_ref.dart';
import 'package:power_iot_app/features/admin/domain/repositories/admin_overview_repository.dart';
import 'package:power_iot_app/features/admin/presentation/providers/admin_overview_provider.dart';
import 'package:power_iot_app/features/admin/presentation/screens/admin_overview_screen.dart';
import 'package:power_iot_app/features/admin/presentation/screens/replace_device_screen.dart';

const _mainHallId = '00000000-0000-4000-8000-000000000001';

void main() {
  testWidgets('A: active relationship exposes a Replace Device action',
      (tester) async {
    final repository = MockAdminOverviewRepository();
    await repository.bindDevice(
      const BindDeviceInput(
        requestIdentity: 'bind-before-replace-001',
        deviceRef: DeviceRef(serialNumber: 'SN-METER-001'),
        measurementPointId: _mainHallId,
      ),
    );

    await tester.pumpWidget(_RouterTestApp(repository: repository));
    await tester.pumpAndSettle();

    expect(
      find.byKey(const Key('replace-device-action-assignment-001')),
      findsOneWidget,
    );
  });

  testWidgets('B: replace identifies the current Device and Measurement Point',
      (tester) async {
    final repository = MockAdminOverviewRepository();
    await repository.bindDevice(
      const BindDeviceInput(
        requestIdentity: 'bind-before-replace-002',
        deviceRef: DeviceRef(serialNumber: 'SN-METER-001'),
        measurementPointId: _mainHallId,
      ),
    );

    await tester.pumpWidget(_RouterTestApp(repository: repository));
    await tester.pumpAndSettle();
    final replaceAction =
        find.byKey(const Key('replace-device-action-assignment-001'));
    await tester.drag(find.byType(ListView), const Offset(0, -300));
    await tester.pumpAndSettle();
    await tester.ensureVisible(replaceAction);
    await tester.tap(replaceAction);
    await tester.pumpAndSettle();

    expect(
        find.text('Current Device: SN-METER-001 (device-001)'), findsOneWidget);
    expect(find.text('Measurement Point: Main Hall ($_mainHallId)'),
        findsOneWidget);
  });

  testWidgets('C: replacement requires an existing replacement Device',
      (tester) async {
    final repository = MockAdminOverviewRepository();
    await repository.bindDevice(
      const BindDeviceInput(
        requestIdentity: 'bind-before-replace-003',
        deviceRef: DeviceRef(serialNumber: 'SN-METER-001'),
        measurementPointId: _mainHallId,
      ),
    );

    await tester.pumpWidget(_RouterTestApp(repository: repository));
    await tester.pumpAndSettle();
    final replaceAction =
        find.byKey(const Key('replace-device-action-assignment-001'));
    await tester.drag(find.byType(ListView), const Offset(0, -300));
    await tester.pumpAndSettle();
    await tester.ensureVisible(replaceAction);
    await tester.tap(replaceAction);
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('replace-submit-button')));
    await tester.pump();

    expect(find.text('Replacement Device is required.'), findsOneWidget);
  });

  test('D: replacing a Device with itself is rejected', () async {
    final repository = await _repositoryWithMainHallBinding();
    final before = await repository.loadAssignmentHistory();

    await expectLater(
      repository.replaceDevice(
        const ReplaceDeviceInput(
          requestIdentity: 'replace-self-001',
          currentAssignmentId: 'assignment-001',
          replacementDeviceRef: DeviceRef(serialNumber: 'SN-METER-001'),
        ),
      ),
      throwsA(isA<StateError>()),
    );

    expect(await repository.loadAssignmentHistory(), orderedEquals(before));
  });

  testWidgets('E: successful replacement shows the new Device at the same MP',
      (tester) async {
    final repository = await _repositoryWithMainHallBinding();
    final before = await repository.loadOverview();
    repository.nextReplacementEffectiveTime = DateTime.utc(2026, 8, 10, 2);

    await tester.pumpWidget(_RouterTestApp(repository: repository));
    await tester.pumpAndSettle();
    await _openReplaceScreen(tester);
    await tester.tap(
      find.byKey(const Key('replace-device-option-SN-METER-002')),
    );
    await tester.tap(find.byKey(const Key('replace-submit-button')));
    await tester.pumpAndSettle();

    expect(find.text('Main Hall · SN-METER-002'), findsOneWidget);
    final overview = await repository.loadOverview();
    expect(overview.activeAssignments.single.deviceId, 'device-002');
    expect(overview.activeAssignments.single.measurementPointId, _mainHallId);
    expect(overview.measurementPoints, hasLength(2));
    expect(overview.devices, orderedEquals(before.devices));
  });

  test('F: replacement closes and opens assignments at one exact boundary',
      () async {
    final repository = await _repositoryWithMainHallBinding();
    final transition = DateTime.utc(2026, 8, 10, 3);
    repository.nextReplacementEffectiveTime = transition;

    await repository.replaceDevice(
      const ReplaceDeviceInput(
        requestIdentity: 'replace-boundary-001',
        currentAssignmentId: 'assignment-001',
        replacementDeviceRef: DeviceRef(serialNumber: 'SN-METER-002'),
      ),
    );

    final history = await repository.loadAssignmentHistory();
    final oldAssignment = history.singleWhere(
      (assignment) => assignment.id == 'assignment-001',
    );
    final newAssignment = history.singleWhere(
      (assignment) => assignment.id == 'assignment-002',
    );
    expect(oldAssignment.validTo, transition);
    expect(newAssignment.validFrom, transition);
    expect(oldAssignment.active, isFalse);
    expect(newAssignment.active, isTrue);
    expect(newAssignment.measurementPointId, oldAssignment.measurementPointId);
  });

  test('G: an actively assigned replacement Device is not stolen', () async {
    final repository = await _repositoryWithMainHallBinding();
    await repository.bindDevice(
      const BindDeviceInput(
        requestIdentity: 'bind-conflict-target-001',
        deviceRef: DeviceRef(serialNumber: 'SN-METER-002'),
        measurementPointId: '00000000-0000-4000-8000-000000000099',
      ),
    );
    final before = await repository.loadAssignmentHistory();

    await expectLater(
      repository.replaceDevice(
        const ReplaceDeviceInput(
          requestIdentity: 'replace-conflict-001',
          currentAssignmentId: 'assignment-001',
          replacementDeviceRef: DeviceRef(serialNumber: 'SN-METER-002'),
        ),
      ),
      throwsA(isA<StateError>()),
    );

    expect(await repository.loadAssignmentHistory(), orderedEquals(before));
    expect(
      (await repository.loadOverview())
          .activeAssignments
          .singleWhere((assignment) => assignment.deviceId == 'device-002')
          .measurementPointId,
      '00000000-0000-4000-8000-000000000099',
    );
  });

  test('H: stale current assignment fails without requested partial rewrite',
      () async {
    final repository = await _repositoryWithMainHallBinding();
    repository.changeCurrentAssignmentBeforeNextReplacement = true;
    repository.nextReplacementEffectiveTime = DateTime.utc(2026, 8, 10, 4);

    await expectLater(
      repository.replaceDevice(
        const ReplaceDeviceInput(
          requestIdentity: 'replace-stale-001',
          currentAssignmentId: 'assignment-001',
          replacementDeviceRef: DeviceRef(serialNumber: 'SN-METER-002'),
        ),
      ),
      throwsA(isA<StateError>()),
    );

    final overview = await repository.loadOverview();
    expect(overview.activeAssignments, hasLength(1));
    expect(overview.activeAssignments.single.deviceId, 'device-001');
    expect(overview.activeAssignments.single.measurementPointId, _mainHallId);
    expect(
      (await repository.loadAssignmentHistory())
          .where((assignment) => assignment.deviceId == 'device-002'),
      isEmpty,
    );
  });

  test('I: response loss replays one replacement at the same boundary',
      () async {
    final repository = await _repositoryWithMainHallBinding();
    final transition = DateTime.utc(2026, 8, 10, 5);
    repository.nextReplacementEffectiveTime = transition;
    repository.loseResponseAfterNextReplacement = true;
    const input = ReplaceDeviceInput(
      requestIdentity: 'replace-retry-001',
      currentAssignmentId: 'assignment-001',
      replacementDeviceRef: DeviceRef(serialNumber: 'SN-METER-002'),
    );

    await expectLater(
        repository.replaceDevice(input), throwsA(isA<StateError>()));
    final replayed = await repository.replaceDevice(input);
    final history = await repository.loadAssignmentHistory();

    expect(replayed.id, 'assignment-002');
    expect(history, hasLength(2));
    expect(history.first.validTo, transition);
    expect(history.last.validFrom, transition);
  });

  test('J: changed replacement request data requires a new identity', () async {
    final repository = await _repositoryWithMainHallBinding();
    repository.nextReplacementEffectiveTime = DateTime.utc(2026, 8, 10, 6);
    await repository.replaceDevice(
      const ReplaceDeviceInput(
        requestIdentity: 'replace-request-001',
        currentAssignmentId: 'assignment-001',
        replacementDeviceRef: DeviceRef(serialNumber: 'SN-METER-002'),
      ),
    );

    await expectLater(
      repository.replaceDevice(
        const ReplaceDeviceInput(
          requestIdentity: 'replace-request-001',
          currentAssignmentId: 'assignment-001',
          replacementDeviceRef: DeviceRef(serialNumber: 'SN-METER-002'),
          reason: 'changed request',
        ),
      ),
      throwsA(isA<StateError>()),
    );

    repository.nextReplacementEffectiveTime = DateTime.utc(2026, 8, 10, 7);
    await repository.replaceDevice(
      const ReplaceDeviceInput(
        requestIdentity: 'replace-request-002',
        currentAssignmentId: 'assignment-002',
        replacementDeviceRef: DeviceRef(serialNumber: 'SN-METER-001'),
      ),
    );
    expect(
      (await repository.loadOverview()).activeAssignments.single.deviceId,
      'device-001',
    );
  });

  testWidgets(
      'K: replace does not expose Bind, Relocate, Unbind, or time input',
      (tester) async {
    final repository = await _repositoryWithMainHallBinding();
    await tester.pumpWidget(_RouterTestApp(repository: repository));
    await tester.pumpAndSettle();
    await _openReplaceScreen(tester);

    expect(find.byKey(const Key('replace-submit-button')), findsOneWidget);
    expect(find.text('Bind Device'), findsNothing);
    expect(find.text('Relocate Device'), findsNothing);
    expect(find.text('Unbind Device'), findsNothing);
    expect(find.text('Effective time is controlled by the mock server.'),
        findsOneWidget);
    expect(find.byKey(const Key('replace-effective-time-field')), findsNothing);
  });

  test('L: replacement never creates or recreates a Measurement Point',
      () async {
    final repository = await _repositoryWithMainHallBinding();
    final before = await repository.loadOverview();
    repository.nextReplacementEffectiveTime = DateTime.utc(2026, 8, 10, 8);

    await repository.replaceDevice(
      const ReplaceDeviceInput(
        requestIdentity: 'replace-no-mp-recreate-001',
        currentAssignmentId: 'assignment-001',
        replacementDeviceRef: DeviceRef(serialNumber: 'SN-METER-002'),
      ),
    );
    final after = await repository.loadOverview();

    expect(after.measurementPoints, orderedEquals(before.measurementPoints));
    expect(after.activeAssignments.single.measurementPointId, _mainHallId);
  });

  test('M: mock replacement has no Device.ShopID authorization authority',
      () async {
    final repository = await _repositoryWithMainHallBinding();
    repository.nextReplacementEffectiveTime = DateTime.utc(2026, 8, 10, 9);

    await repository.replaceDevice(
      const ReplaceDeviceInput(
        requestIdentity: 'replace-no-shop-authority-001',
        currentAssignmentId: 'assignment-001',
        replacementDeviceRef: DeviceRef(serialNumber: 'SN-METER-002'),
      ),
    );

    final overview = await repository.loadOverview();
    expect(
      overview.measurementPoints
          .firstWhere((point) => point.id == _mainHallId)
          .shopId,
      's1',
    );
    expect(overview.activeAssignments.single.deviceId, 'device-002');
  });

  testWidgets('I: response-loss retry survives route remount', (tester) async {
    final repository = await _repositoryWithMainHallBinding();
    repository.nextReplacementEffectiveTime = DateTime.utc(2026, 8, 10, 10);
    repository.loseResponseAfterNextReplacement = true;
    await tester.pumpWidget(_RouterTestApp(repository: repository));
    await tester.pumpAndSettle();
    await _openReplaceScreen(tester);
    await tester.tap(
      find.byKey(const Key('replace-device-option-SN-METER-002')),
    );
    await tester.tap(find.byKey(const Key('replace-submit-button')));
    await tester.pumpAndSettle();

    expect(
      find.text(
        'Unable to replace Device. Please check the replacement selection and try again.',
      ),
      findsOneWidget,
    );
    await tester.pageBack();
    await tester.pumpAndSettle();
    await _openReplaceScreen(tester);
    expect(find.byKey(const Key('replace-submit-button')), findsOneWidget);
    await tester.tap(find.byKey(const Key('replace-submit-button')));
    await tester.pumpAndSettle();

    expect(find.text('Main Hall · SN-METER-002'), findsOneWidget);
    expect((await repository.loadAssignmentHistory()), hasLength(2));
  });
}

Future<MockAdminOverviewRepository> _repositoryWithMainHallBinding() async {
  final repository = MockAdminOverviewRepository();
  await repository.bindDevice(
    const BindDeviceInput(
      requestIdentity: 'bind-before-replace-helper',
      deviceRef: DeviceRef(serialNumber: 'SN-METER-001'),
      measurementPointId: _mainHallId,
    ),
  );
  return repository;
}

Future<void> _openReplaceScreen(WidgetTester tester) async {
  final replaceAction =
      find.byKey(const Key('replace-device-action-assignment-001'));
  await tester.scrollUntilVisible(replaceAction, 200, maxScrolls: 10);
  await tester.pumpAndSettle();
  await tester.tap(replaceAction);
  await tester.pumpAndSettle();
}

class _RouterTestApp extends StatelessWidget {
  const _RouterTestApp({this.repository});

  final AdminOverviewRepository? repository;

  @override
  Widget build(BuildContext context) {
    final router = GoRouter(
      initialLocation: '/admin/mock',
      routes: [
        GoRoute(
          path: '/admin/mock',
          builder: (context, state) => const AdminOverviewScreen(),
        ),
        GoRoute(
          path: '/admin/mock/replace-device/:assignmentId',
          builder: (context, state) => ReplaceDeviceScreen(
            assignmentId: state.pathParameters['assignmentId']!,
          ),
        ),
      ],
    );

    return ProviderScope(
      overrides: [
        if (repository != null)
          adminOverviewRepositoryProvider.overrideWithValue(repository!),
      ],
      child: MaterialApp.router(routerConfig: router),
    );
  }
}
