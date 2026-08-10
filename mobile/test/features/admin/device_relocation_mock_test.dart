import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:power_iot_app/features/admin/data/repositories/mock_admin_overview_repository.dart';
import 'package:power_iot_app/features/admin/domain/models/device_ref.dart';
import 'package:power_iot_app/features/admin/domain/repositories/admin_overview_repository.dart';
import 'package:power_iot_app/features/admin/presentation/providers/admin_overview_provider.dart';
import 'package:power_iot_app/features/admin/presentation/screens/admin_overview_screen.dart';
import 'package:power_iot_app/features/admin/presentation/screens/relocate_device_screen.dart';

const _mainHallId = '00000000-0000-4000-8000-000000000001';
const _kitchenId = '00000000-0000-4000-8000-000000000099';

void main() {
  testWidgets('A: active relationship exposes a Relocate Device action',
      (tester) async {
    final repository = await _repositoryWithMainHallBinding();

    await tester.pumpWidget(_RouterTestApp(repository: repository));
    await tester.pumpAndSettle();

    expect(
      find.byKey(const Key('relocate-device-action-assignment-001')),
      findsOneWidget,
    );
  });

  testWidgets('B: relocate identifies current Device and source MP',
      (tester) async {
    final repository = await _repositoryWithMainHallBinding();

    await tester.pumpWidget(_RouterTestApp(repository: repository));
    await tester.pumpAndSettle();
    await _openRelocateScreen(tester);

    expect(
      find.text('Current Device: SN-METER-001 (device-001)'),
      findsOneWidget,
    );
    expect(
      find.text('Source Measurement Point: Main Hall ($_mainHallId)'),
      findsOneWidget,
    );
  });

  testWidgets('C: relocation requires a target Measurement Point',
      (tester) async {
    final repository = await _repositoryWithMainHallBinding();

    await tester.pumpWidget(_RouterTestApp(repository: repository));
    await tester.pumpAndSettle();
    await _openRelocateScreen(tester);
    await tester.tap(find.byKey(const Key('relocate-submit-button')));
    await tester.pump();

    expect(find.text('Target Measurement Point is required.'), findsOneWidget);
  });

  test('D: successful relocation keeps Device and moves it to target MP',
      () async {
    final repository = await _repositoryWithMainHallBinding();
    repository.nextRelocationEffectiveTime = DateTime.utc(2026, 8, 10, 2);

    final result = await repository.relocateDevice(
      const RelocateDeviceInput(
        requestIdentity: 'relocate-success-001',
        currentAssignmentId: 'assignment-001',
        targetMeasurementPointId: _kitchenId,
      ),
    );

    expect(result.deviceId, 'device-001');
    expect(result.measurementPointId, _kitchenId);
    final overview = await repository.loadOverview();
    expect(overview.activeAssignments.single.deviceId, 'device-001');
    expect(overview.activeAssignments.single.measurementPointId, _kitchenId);
  });

  test('E: relocation closes source and opens target at one boundary',
      () async {
    final repository = await _repositoryWithMainHallBinding();
    final transition = DateTime.utc(2026, 8, 10, 3);
    repository.nextRelocationEffectiveTime = transition;

    await repository.relocateDevice(
      const RelocateDeviceInput(
        requestIdentity: 'relocate-boundary-001',
        currentAssignmentId: 'assignment-001',
        targetMeasurementPointId: _kitchenId,
      ),
    );

    final history = await repository.loadAssignmentHistory();
    final source = history.singleWhere(
      (assignment) => assignment.id == 'assignment-001',
    );
    final target = history.singleWhere(
      (assignment) => assignment.id == 'assignment-002',
    );
    expect(source.validTo, transition);
    expect(target.validFrom, transition);
    expect(source.active, isFalse);
    expect(target.active, isTrue);
    expect(source.deviceId, target.deviceId);
    expect(source.measurementPointId, _mainHallId);
    expect(target.measurementPointId, _kitchenId);
  });

  test('F: occupied target fails without changing either relationship',
      () async {
    final repository = await _repositoryWithMainHallBinding();
    await repository.bindDevice(
      const BindDeviceInput(
        requestIdentity: 'bind-target-conflict-001',
        deviceRef: DeviceRef(serialNumber: 'SN-METER-002'),
        measurementPointId: _kitchenId,
      ),
    );
    final before = await repository.loadAssignmentHistory();

    await expectLater(
      repository.relocateDevice(
        const RelocateDeviceInput(
          requestIdentity: 'relocate-target-conflict-001',
          currentAssignmentId: 'assignment-001',
          targetMeasurementPointId: _kitchenId,
        ),
      ),
      throwsA(isA<StateError>()),
    );

    expect(await repository.loadAssignmentHistory(), orderedEquals(before));
    final overview = await repository.loadOverview();
    expect(
      overview.activeAssignments
          .singleWhere((assignment) => assignment.deviceId == 'device-001')
          .measurementPointId,
      _mainHallId,
    );
    expect(
      overview.activeAssignments
          .singleWhere((assignment) => assignment.deviceId == 'device-002')
          .measurementPointId,
      _kitchenId,
    );
  });

  test('G: source equal target is rejected as an invalid transition', () async {
    final repository = await _repositoryWithMainHallBinding();
    final before = await repository.loadAssignmentHistory();

    await expectLater(
      repository.relocateDevice(
        const RelocateDeviceInput(
          requestIdentity: 'relocate-same-point-001',
          currentAssignmentId: 'assignment-001',
          targetMeasurementPointId: _mainHallId,
        ),
      ),
      throwsA(isA<StateError>()),
    );

    expect(await repository.loadAssignmentHistory(), orderedEquals(before));
  });

  test('H: stale current assignment fails without partial mutation', () async {
    final repository = await _repositoryWithMainHallBinding();
    repository.changeCurrentAssignmentBeforeNextRelocation = true;
    repository.nextRelocationEffectiveTime = DateTime.utc(2026, 8, 10, 4);

    await expectLater(
      repository.relocateDevice(
        const RelocateDeviceInput(
          requestIdentity: 'relocate-stale-001',
          currentAssignmentId: 'assignment-001',
          targetMeasurementPointId: _kitchenId,
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
          .where((assignment) => assignment.measurementPointId == _kitchenId),
      isEmpty,
    );
  });

  test('I: relocation retains the exact physical Device identity', () async {
    final repository = await _repositoryWithMainHallBinding();
    repository.nextRelocationEffectiveTime = DateTime.utc(2026, 8, 10, 5);

    await repository.relocateDevice(
      const RelocateDeviceInput(
        requestIdentity: 'relocate-device-identity-001',
        currentAssignmentId: 'assignment-001',
        targetMeasurementPointId: _kitchenId,
      ),
    );

    final history = await repository.loadAssignmentHistory();
    expect(history.map((assignment) => assignment.deviceId),
        everyElement('device-001'));
    expect((await repository.loadOverview()).devices, hasLength(2));
  });

  test('J: relocation preserves both Measurement Point identities', () async {
    final repository = await _repositoryWithMainHallBinding();
    final before = await repository.loadOverview();
    repository.nextRelocationEffectiveTime = DateTime.utc(2026, 8, 10, 6);

    await repository.relocateDevice(
      const RelocateDeviceInput(
        requestIdentity: 'relocate-point-identity-001',
        currentAssignmentId: 'assignment-001',
        targetMeasurementPointId: _kitchenId,
      ),
    );

    final after = await repository.loadOverview();
    expect(after.measurementPoints, orderedEquals(before.measurementPoints));
    expect(
      after.activeAssignments.single.measurementPointId,
      _kitchenId,
    );
  });

  test('K: response loss replays one relocation at the same boundary',
      () async {
    final repository = await _repositoryWithMainHallBinding();
    final transition = DateTime.utc(2026, 8, 10, 7);
    repository.nextRelocationEffectiveTime = transition;
    repository.loseResponseAfterNextRelocation = true;
    const input = RelocateDeviceInput(
      requestIdentity: 'relocate-retry-001',
      currentAssignmentId: 'assignment-001',
      targetMeasurementPointId: _kitchenId,
    );

    await expectLater(
        repository.relocateDevice(input), throwsA(isA<StateError>()));
    final replayed = await repository.relocateDevice(input);
    final history = await repository.loadAssignmentHistory();

    expect(replayed.id, 'assignment-002');
    expect(history, hasLength(2));
    expect(history.first.validTo, transition);
    expect(history.last.validFrom, transition);
  });

  test('L: changed target data cannot replay a prior relocation', () async {
    final repository = await _repositoryWithMainHallBinding();
    repository.nextRelocationEffectiveTime = DateTime.utc(2026, 8, 10, 8);
    await repository.relocateDevice(
      const RelocateDeviceInput(
        requestIdentity: 'relocate-request-001',
        currentAssignmentId: 'assignment-001',
        targetMeasurementPointId: _kitchenId,
      ),
    );

    await expectLater(
      repository.relocateDevice(
        const RelocateDeviceInput(
          requestIdentity: 'relocate-request-001',
          currentAssignmentId: 'assignment-001',
          targetMeasurementPointId: _mainHallId,
        ),
      ),
      throwsA(isA<StateError>()),
    );
    expect((await repository.loadAssignmentHistory()), hasLength(2));
  });

  test('M: occupied target is never replaced by Relocate', () async {
    final repository = await _repositoryWithMainHallBinding();
    await repository.bindDevice(
      const BindDeviceInput(
        requestIdentity: 'bind-no-replace-leakage-001',
        deviceRef: DeviceRef(serialNumber: 'SN-METER-002'),
        measurementPointId: _kitchenId,
      ),
    );
    final before = await repository.loadOverview();

    await expectLater(
      repository.relocateDevice(
        const RelocateDeviceInput(
          requestIdentity: 'relocate-no-replace-leakage-001',
          currentAssignmentId: 'assignment-001',
          targetMeasurementPointId: _kitchenId,
        ),
      ),
      throwsA(isA<StateError>()),
    );

    final after = await repository.loadOverview();
    expect(after.measurementPoints, orderedEquals(before.measurementPoints));
    expect(after.devices, orderedEquals(before.devices));
    expect(after.activeAssignments, orderedEquals(before.activeAssignments));
  });

  test('N: successful Relocate does not leave Device unassigned', () async {
    final repository = await _repositoryWithMainHallBinding();
    repository.nextRelocationEffectiveTime = DateTime.utc(2026, 8, 10, 9);

    await repository.relocateDevice(
      const RelocateDeviceInput(
        requestIdentity: 'relocate-no-unbind-001',
        currentAssignmentId: 'assignment-001',
        targetMeasurementPointId: _kitchenId,
      ),
    );

    expect((await repository.loadOverview()).activeAssignments, hasLength(1));
  });

  test('O: Device.ShopID/current shop data is not relocation authority',
      () async {
    final repository = await _repositoryWithMainHallBinding();
    repository.nextRelocationEffectiveTime = DateTime.utc(2026, 8, 10, 10);

    await repository.relocateDevice(
      const RelocateDeviceInput(
        requestIdentity: 'relocate-no-shop-authority-001',
        currentAssignmentId: 'assignment-001',
        targetMeasurementPointId: _kitchenId,
      ),
    );

    final overview = await repository.loadOverview();
    expect(overview.activeAssignments.single.deviceId, 'device-001');
    expect(
      overview.measurementPoints
          .firstWhere((point) => point.id == _kitchenId)
          .shopId,
      's1',
    );
  });

  testWidgets('route remount retains unresolved target request identity',
      (tester) async {
    final repository = await _repositoryWithMainHallBinding();
    repository.nextRelocationEffectiveTime = DateTime.utc(2026, 8, 10, 11);
    repository.loseResponseAfterNextRelocation = true;

    await tester.pumpWidget(_RouterTestApp(repository: repository));
    await tester.pumpAndSettle();
    await _openRelocateScreen(tester);
    await tester
        .tap(find.byKey(const Key('relocate-target-option-$_kitchenId')));
    await tester.tap(find.byKey(const Key('relocate-submit-button')));
    await tester.pumpAndSettle();

    expect(
      find.text(
        'Unable to relocate Device. Please check the target Measurement Point and try again.',
      ),
      findsOneWidget,
    );
    await tester.pageBack();
    await tester.pumpAndSettle();
    await _openRelocateScreen(tester);
    await tester.tap(find.byKey(const Key('relocate-submit-button')));
    await tester.pumpAndSettle();

    expect(find.text('Kitchen · SN-METER-001'), findsOneWidget);
    expect(
      (await repository.loadOverview())
          .activeAssignments
          .single
          .measurementPointId,
      _kitchenId,
    );
    expect((await repository.loadAssignmentHistory()), hasLength(2));
  });
}

Future<MockAdminOverviewRepository> _repositoryWithMainHallBinding() async {
  final repository = MockAdminOverviewRepository();
  await repository.bindDevice(
    const BindDeviceInput(
      requestIdentity: 'bind-before-relocate-helper',
      deviceRef: DeviceRef(serialNumber: 'SN-METER-001'),
      measurementPointId: _mainHallId,
    ),
  );
  return repository;
}

Future<void> _openRelocateScreen(WidgetTester tester) async {
  final action = find.byKey(const Key('relocate-device-action-assignment-001'));
  await tester.scrollUntilVisible(action, 200, maxScrolls: 10);
  await tester.pumpAndSettle();
  await tester.tap(action);
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
          path: '/admin/mock/relocate-device/:assignmentId',
          builder: (context, state) => RelocateDeviceScreen(
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
