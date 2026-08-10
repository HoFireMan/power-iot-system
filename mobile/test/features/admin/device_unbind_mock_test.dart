import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:power_iot_app/features/admin/data/repositories/mock_admin_overview_repository.dart';
import 'package:power_iot_app/features/admin/domain/models/device_ref.dart';
import 'package:power_iot_app/features/admin/domain/repositories/admin_overview_repository.dart';
import 'package:power_iot_app/features/admin/presentation/providers/admin_overview_provider.dart';
import 'package:power_iot_app/features/admin/presentation/screens/admin_overview_screen.dart';
import 'package:power_iot_app/features/admin/presentation/screens/unbind_device_screen.dart';
import 'package:power_iot_app/features/shops/providers/shop_provider.dart';

const _mainHallId = '00000000-0000-4000-8000-000000000001';

void main() {
  testWidgets('A: active relationship exposes an Unbind Device action',
      (tester) async {
    final repository = await _repositoryWithMainHallBinding();
    await tester.pumpWidget(_RouterTestApp(repository: repository));
    await tester.pumpAndSettle();

    expect(
      find.byKey(const Key('unbind-device-action-assignment-001')),
      findsOneWidget,
    );
  });

  testWidgets('B: unbind identifies the current Device and Measurement Point',
      (tester) async {
    final repository = await _repositoryWithMainHallBinding();
    await tester.pumpWidget(_RouterTestApp(repository: repository));
    await tester.pumpAndSettle();
    await _openUnbindScreen(tester);

    expect(
      find.text('Current Device: SN-METER-001 (device-001)'),
      findsOneWidget,
    );
    expect(
      find.text('Measurement Point: Main Hall ($_mainHallId)'),
      findsOneWidget,
    );
    expect(
      find.textContaining(
          'This closes the current assignment. The Device and Measurement Point remain available'),
      findsOneWidget,
    );
  });

  testWidgets('C: successful unbind returns and refreshes the overview',
      (tester) async {
    final repository = await _repositoryWithMainHallBinding();
    await tester.pumpWidget(_RouterTestApp(repository: repository));
    await tester.pumpAndSettle();
    await _openUnbindScreen(tester);
    await tester.enterText(
      find.byKey(const Key('unbind-reason-field')),
      'maintenance',
    );
    await tester.tap(find.byKey(const Key('unbind-submit-button')));
    await tester.pumpAndSettle();

    expect(find.text('Admin Overview'), findsOneWidget);
    expect(find.text('No active bindings.'), findsOneWidget);
    expect((await repository.loadOverview()).activeAssignments, isEmpty);
  });

  test('D: unbind closes only the active assignment at deterministic T',
      () async {
    final repository = await _repositoryWithMainHallBinding();
    final transition = DateTime.utc(2026, 8, 10, 2);
    repository.nextUnbindEffectiveTime = transition;

    final result = await repository.unbindDevice(
      const UnbindDeviceInput(
        requestIdentity: 'unbind-boundary-001',
        currentAssignmentId: 'assignment-001',
      ),
    );
    final history = await repository.loadAssignmentHistory();

    expect(result.id, 'assignment-001');
    expect(result.validTo, transition);
    expect(history, hasLength(1));
    expect(history.single.active, isFalse);
    expect((await repository.loadOverview()).activeAssignments, isEmpty);
  });

  test(
      'E/F/G/P/Q: unbind retains Device, MP, inventory, and assignment history',
      () async {
    final repository = await _repositoryWithMainHallBinding();
    final before = await repository.loadOverview();
    final beforeHistory = await repository.loadAssignmentHistory();

    await repository.unbindDevice(
      const UnbindDeviceInput(
        requestIdentity: 'unbind-retain-001',
        currentAssignmentId: 'assignment-001',
      ),
    );
    final after = await repository.loadOverview();
    final history = await repository.loadAssignmentHistory();

    expect(after.devices, orderedEquals(before.devices));
    expect(after.measurementPoints, orderedEquals(before.measurementPoints));
    expect(after.activeAssignments, isEmpty);
    expect(history, hasLength(beforeHistory.length));
    expect(history.single.id, beforeHistory.single.id);
    expect(history.single.deviceId, 'device-001');
    expect(history.single.measurementPointId, _mainHallId);
  });

  test('H/I: unbind creates no relocation target or replacement Device',
      () async {
    final repository = await _repositoryWithMainHallBinding();
    final before = await repository.loadOverview();

    await repository.unbindDevice(
      const UnbindDeviceInput(
        requestIdentity: 'unbind-no-target-001',
        currentAssignmentId: 'assignment-001',
      ),
    );
    final after = await repository.loadOverview();

    expect(after.devices, orderedEquals(before.devices));
    expect(after.measurementPoints, orderedEquals(before.measurementPoints));
    expect(after.activeAssignments, isEmpty);
    expect((await repository.loadAssignmentHistory()), hasLength(1));
  });

  test('J: stale/non-current unbind fails atomically', () async {
    final repository = await _repositoryWithMainHallBinding();
    repository.changeCurrentAssignmentBeforeNextUnbind = true;
    final beforeHistory = await repository.loadAssignmentHistory();

    await expectLater(
      repository.unbindDevice(
        const UnbindDeviceInput(
          requestIdentity: 'unbind-stale-001',
          currentAssignmentId: 'assignment-001',
        ),
      ),
      throwsA(
        isA<StateError>().having(
          (error) => error.message,
          'message',
          'Current assignment is no longer current.',
        ),
      ),
    );

    final overview = await repository.loadOverview();
    expect(overview.activeAssignments, hasLength(1));
    expect(overview.activeAssignments.single.deviceId, 'device-001');
    expect(overview.activeAssignments.single.measurementPointId, _mainHallId);
    expect((await repository.loadAssignmentHistory()), hasLength(2));
    expect(beforeHistory.single.validTo, isNull);
  });

  test('K: missing and already-unbound requests have distinct exact semantics',
      () async {
    final repository = await _repositoryWithMainHallBinding();

    await expectLater(
      repository.unbindDevice(
        const UnbindDeviceInput(
          requestIdentity: 'unbind-missing-001',
          currentAssignmentId: 'assignment-missing',
        ),
      ),
      throwsA(
        isA<StateError>().having(
          (error) => error.message,
          'message',
          'Assignment not found.',
        ),
      ),
    );

    const input = UnbindDeviceInput(
      requestIdentity: 'unbind-once-001',
      currentAssignmentId: 'assignment-001',
    );
    final committed = await repository.unbindDevice(input);
    await expectLater(
      repository.unbindDevice(
        const UnbindDeviceInput(
          requestIdentity: 'unbind-new-request-001',
          currentAssignmentId: 'assignment-001',
        ),
      ),
      throwsA(
        isA<StateError>().having(
          (error) => error.message,
          'message',
          'Current assignment is no longer current.',
        ),
      ),
    );
    expect((await repository.unbindDevice(input)).validTo, committed.validTo);
  });

  test('L: response loss replays same T/history without duplicate closure',
      () async {
    final repository = await _repositoryWithMainHallBinding();
    final transition = DateTime.utc(2026, 8, 10, 5);
    repository.nextUnbindEffectiveTime = transition;
    repository.loseResponseAfterNextUnbind = true;
    const input = UnbindDeviceInput(
      requestIdentity: 'unbind-retry-001',
      currentAssignmentId: 'assignment-001',
    );

    await expectLater(
        repository.unbindDevice(input), throwsA(isA<StateError>()));
    final replayed = await repository.unbindDevice(input);
    final history = await repository.loadAssignmentHistory();

    expect(replayed.validTo, transition);
    expect(history, hasLength(1));
    expect(history.single.validTo, transition);
    expect((await repository.loadOverview()).activeAssignments, isEmpty);
  });

  test('M: changed canonical command data with same identity is rejected',
      () async {
    final repository = await _repositoryWithMainHallBinding();
    await repository.unbindDevice(
      const UnbindDeviceInput(
        requestIdentity: 'unbind-reuse-001',
        currentAssignmentId: 'assignment-001',
        reason: 'first reason',
      ),
    );

    await expectLater(
      repository.unbindDevice(
        const UnbindDeviceInput(
          requestIdentity: 'unbind-reuse-001',
          currentAssignmentId: 'assignment-001',
          reason: 'changed reason',
        ),
      ),
      throwsA(
        isA<StateError>().having(
          (error) => error.message,
          'message',
          'Unbind request identity was reused.',
        ),
      ),
    );
  });

  test('N: Device identity and assignment context remain authoritative',
      () async {
    final repository = await _repositoryWithMainHallBinding();
    await repository.unbindDevice(
      const UnbindDeviceInput(
        requestIdentity: 'unbind-authority-001',
        currentAssignmentId: 'assignment-001',
      ),
    );

    final overview = await repository.loadOverview();
    expect(
      overview.measurementPoints
          .firstWhere((point) => point.id == _mainHallId)
          .shopId,
      's1',
    );
    expect(overview.devices, hasLength(2));
    expect(overview.activeAssignments, isEmpty);
  });

  testWidgets('O: current shop selection is not unbind authority',
      (tester) async {
    final repository = await _repositoryWithMainHallBinding();
    await tester.pumpWidget(
      _RouterTestApp(repository: repository, useDifferentCurrentShop: true),
    );
    await tester.pumpAndSettle();
    await _openUnbindScreen(tester);
    await tester.tap(find.byKey(const Key('unbind-submit-button')));
    await tester.pumpAndSettle();

    expect(find.text('Admin Overview'), findsOneWidget);
    expect((await repository.loadOverview()).activeAssignments, isEmpty);
  });

  testWidgets('response-loss retry survives route remount and replays identity',
      (tester) async {
    final repository = await _repositoryWithMainHallBinding();
    final transition = DateTime.utc(2026, 8, 10, 10);
    repository.nextUnbindEffectiveTime = transition;
    repository.loseResponseAfterNextUnbind = true;

    await tester.pumpWidget(_RouterTestApp(repository: repository));
    await tester.pumpAndSettle();
    await _openUnbindScreen(tester);
    await tester.enterText(
      find.byKey(const Key('unbind-reason-field')),
      'route retry',
    );
    await tester.tap(find.byKey(const Key('unbind-submit-button')));
    await tester.pumpAndSettle();

    expect(
      find.text(
        'Unable to unbind Device. Please check the current assignment and try again.',
      ),
      findsOneWidget,
    );
    await tester.pageBack();
    await tester.pumpAndSettle();
    await _openUnbindScreen(tester);
    expect(find.byKey(const Key('unbind-submit-button')), findsOneWidget);
    await tester.tap(find.byKey(const Key('unbind-submit-button')));
    await tester.pumpAndSettle();

    expect(find.text('Admin Overview'), findsOneWidget);
    expect(find.text('No active bindings.'), findsOneWidget);
    expect((await repository.loadAssignmentHistory()), hasLength(1));
    expect(
        (await repository.loadAssignmentHistory()).single.validTo, transition);
  });
}

Future<MockAdminOverviewRepository> _repositoryWithMainHallBinding() async {
  final repository = MockAdminOverviewRepository();
  await repository.bindDevice(
    const BindDeviceInput(
      requestIdentity: 'bind-before-unbind-helper',
      deviceRef: DeviceRef(serialNumber: 'SN-METER-001'),
      measurementPointId: _mainHallId,
    ),
  );
  return repository;
}

Future<void> _openUnbindScreen(WidgetTester tester) async {
  final action = find.byKey(const Key('unbind-device-action-assignment-001'));
  await tester.scrollUntilVisible(action, 200, maxScrolls: 10);
  await tester.pumpAndSettle();
  await tester.tap(action);
  await tester.pumpAndSettle();
}

class _RouterTestApp extends StatelessWidget {
  const _RouterTestApp({this.repository, this.useDifferentCurrentShop = false});

  final AdminOverviewRepository? repository;
  final bool useDifferentCurrentShop;

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
          path: '/admin/mock/unbind-device/:assignmentId',
          builder: (context, state) => UnbindDeviceScreen(
            assignmentId: state.pathParameters['assignmentId']!,
          ),
        ),
      ],
    );

    return ProviderScope(
      overrides: [
        if (repository != null)
          adminOverviewRepositoryProvider.overrideWithValue(repository!),
        if (useDifferentCurrentShop)
          shopProvider.overrideWith(
            (ref) => ShopNotifier()..selectShop('s2'),
          ),
      ],
      child: MaterialApp.router(routerConfig: router),
    );
  }
}
