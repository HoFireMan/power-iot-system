import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:power_iot_app/features/admin/domain/models/admin_overview.dart';
import 'package:power_iot_app/features/admin/domain/models/device_assignment.dart';
import 'package:power_iot_app/features/admin/domain/models/device_inventory.dart';
import 'package:power_iot_app/features/admin/domain/models/measurement_point.dart';
import 'package:power_iot_app/features/admin/domain/repositories/admin_overview_repository.dart';
import 'package:power_iot_app/features/admin/presentation/providers/admin_overview_provider.dart';
import 'package:power_iot_app/features/admin/presentation/screens/admin_overview_screen.dart';
import 'package:power_iot_app/features/admin/presentation/screens/bind_device_screen.dart';
import 'package:power_iot_app/features/admin/presentation/screens/create_measurement_point_screen.dart';
import 'package:power_iot_app/features/admin/presentation/screens/relocate_device_screen.dart';
import 'package:power_iot_app/features/admin/presentation/screens/replace_device_screen.dart';
import 'package:power_iot_app/features/admin/presentation/screens/unbind_device_screen.dart';

const _pointA = '00000000-0000-4000-8000-000000000001';
const _pointB = '00000000-0000-4000-8000-000000000099';

void main() {
  testWidgets('Create MP offers refresh retry after committed mutation',
      (tester) async {
    final repository = _PendingRepository()..failNextRefresh = true;
    final router = _router();
    await tester.pumpWidget(_App(repository, router));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Create Measurement Point'));
    await tester.pumpAndSettle();
    await tester.enterText(
        find.byKey(const Key('measurement-point-name-field')), 'New Point');
    await tester.tap(find.byType(FilledButton));
    repository.createCompleter.complete(const MeasurementPoint(
      id: '00000000-0000-4000-8000-000000000002',
      shopId: 's1',
      name: 'New Point',
    ));
    await tester.pumpAndSettle();
    expect(find.textContaining('Measurement Point created'), findsOneWidget);
    expect(
      tester.widget<FilledButton>(find.byType(FilledButton)).onPressed,
      isNull,
    );
    expect(repository.mutationCalls, 1);

    await tester
        .tap(find.byKey(const Key('create-measurement-point-refresh-retry')));
    await tester.pump();
    repository.refreshCompleter.complete(repository.overview);
    await tester.pumpAndSettle();
    expect(find.text('Admin Overview'), findsOneWidget);
    expect(repository.mutationCalls, 1);
  });

  testWidgets('Bind offers refresh retry after committed mutation',
      (tester) async {
    final repository = _PendingRepository()..failNextRefresh = true;
    await _open(tester, repository, '/admin/mock/bind-device');
    await tester.tap(find.byKey(const Key('bind-device-option-SN-METER-001')));
    await tester.tap(find.byKey(const Key('bind-point-option-$_pointA')));
    await _submitAndAssertLocked(
        tester, repository, const Key('bind-submit-button'));

    repository.bindCompleter.complete(_assignment('assignment-002', _pointA));
    await _pumpUntilVisible(
      tester,
      find.text('Device bound, but the latest view could not be loaded.'),
    );
    expect(find.byKey(const Key('bind-refresh-retry-button')), findsOneWidget);
    expect(find.byKey(const Key('bind-submit-button')), findsNothing);
    expect(repository.mutationCalls, 1);
    final mutationIdentity = repository.mutationIdentities.single;
    final popScope = tester.widget<PopScope>(find.byType(PopScope));
    expect(popScope.canPop, isFalse);

    await tester.tap(find.byKey(const Key('bind-refresh-retry-button')));
    await tester.pump();
    expect(find.byKey(const Key('bind-submit-button')), findsNothing);
    expect(repository.mutationCalls, 1);
    expect(repository.mutationIdentities, [mutationIdentity]);

    repository.refreshCompleter.complete(repository.overview);
    await tester.pumpAndSettle();
    expect(find.text('Admin Overview'), findsOneWidget);
    expect(repository.mutationCalls, 1);
    expect(repository.mutationIdentities, [mutationIdentity]);
  });

  testWidgets('Replace offers refresh retry after committed mutation',
      (tester) async {
    final repository = _PendingRepository(withBinding: true)
      ..failNextRefresh = true;
    await _open(
        tester, repository, '/admin/mock/replace-device/assignment-001');
    await tester
        .tap(find.byKey(const Key('replace-device-option-SN-METER-002')));
    await _submitAndAssertLocked(
        tester, repository, const Key('replace-submit-button'));

    repository.replaceCompleter.complete(
        _assignment('assignment-002', _pointA, deviceId: 'device-002'));
    await _pumpUntilVisible(
      tester,
      find.text('Device replaced, but the latest view could not be loaded.'),
    );
    expect(
        find.byKey(const Key('replace-refresh-retry-button')), findsOneWidget);
    expect(find.byKey(const Key('replace-submit-button')), findsNothing);
    expect(repository.mutationCalls, 1);
    final mutationIdentity = repository.mutationIdentities.single;
    final popScope = tester.widget<PopScope>(find.byType(PopScope));
    expect(popScope.canPop, isFalse);

    await tester.tap(find.byKey(const Key('replace-refresh-retry-button')));
    await tester.pump();
    expect(find.byKey(const Key('replace-submit-button')), findsNothing);
    expect(repository.mutationCalls, 1);
    expect(repository.mutationIdentities, [mutationIdentity]);

    repository.refreshCompleter.complete(repository.overview);
    await tester.pumpAndSettle();
    expect(find.text('Admin Overview'), findsOneWidget);
    expect(repository.mutationCalls, 1);
    expect(repository.mutationIdentities, [mutationIdentity]);
  });

  testWidgets('Relocate offers refresh retry after committed mutation',
      (tester) async {
    final repository = _PendingRepository(withBinding: true)
      ..failNextRefresh = true;
    await _open(
        tester, repository, '/admin/mock/relocate-device/assignment-001');
    await tester.tap(find.byKey(const Key('relocate-target-option-$_pointB')));
    await _submitAndAssertLocked(
        tester, repository, const Key('relocate-submit-button'));

    repository.relocateCompleter
        .complete(_assignment('assignment-002', _pointB));
    await _pumpUntilVisible(
      tester,
      find.text('Device relocated, but the latest view could not be loaded.'),
    );
    expect(
        find.byKey(const Key('relocate-refresh-retry-button')), findsOneWidget);
    expect(find.byKey(const Key('relocate-submit-button')), findsNothing);
    expect(repository.mutationCalls, 1);
    final mutationIdentity = repository.mutationIdentities.single;
    final popScope = tester.widget<PopScope>(find.byType(PopScope));
    expect(popScope.canPop, isFalse);

    await tester.tap(find.byKey(const Key('relocate-refresh-retry-button')));
    await tester.pump();
    expect(find.byKey(const Key('relocate-submit-button')), findsNothing);
    expect(repository.mutationCalls, 1);
    expect(repository.mutationIdentities, [mutationIdentity]);

    repository.refreshCompleter.complete(repository.overview);
    await tester.pumpAndSettle();
    expect(find.text('Admin Overview'), findsOneWidget);
    expect(repository.mutationCalls, 1);
    expect(repository.mutationIdentities, [mutationIdentity]);
  });

  testWidgets('Unbind offers refresh retry after committed mutation',
      (tester) async {
    final repository = _PendingRepository(withBinding: true)
      ..failNextRefresh = true;
    await _open(tester, repository, '/admin/mock/unbind-device/assignment-001');
    await _submitAndAssertLocked(
        tester, repository, const Key('unbind-submit-button'));

    repository.unbindCompleter.complete(_assignment('assignment-001', _pointA));
    await _pumpUntilVisible(
      tester,
      find.text('Device unbound, but the latest view could not be loaded.'),
    );
    expect(
        find.byKey(const Key('unbind-refresh-retry-button')), findsOneWidget);
    expect(find.byKey(const Key('unbind-submit-button')), findsNothing);
    expect(repository.mutationCalls, 1);
    final mutationIdentity = repository.mutationIdentities.single;
    final popScope = tester.widget<PopScope>(find.byType(PopScope));
    expect(popScope.canPop, isFalse);

    await tester.tap(find.byKey(const Key('unbind-refresh-retry-button')));
    await tester.pump();
    expect(find.byKey(const Key('unbind-submit-button')), findsNothing);
    expect(repository.mutationCalls, 1);
    expect(repository.mutationIdentities, [mutationIdentity]);

    repository.refreshCompleter.complete(repository.overview);
    await tester.pumpAndSettle();
    expect(find.text('Admin Overview'), findsOneWidget);
    expect(repository.mutationCalls, 1);
    expect(repository.mutationIdentities, [mutationIdentity]);
  });

  testWidgets('Create MP ignores duplicate submits through pending refresh',
      (tester) async {
    final repository = _PendingRepository();
    final router = _router();
    await tester.pumpWidget(_App(repository, router));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Create Measurement Point'));
    await tester.pumpAndSettle();
    await tester.enterText(
        find.byKey(const Key('measurement-point-name-field')), 'New Point');
    // The submit button keeps its public FilledButton type while its child
    // changes to a progress indicator during the pending mutation.
    final submit = find.byType(FilledButton);
    await tester.tap(submit);
    await tester.pump();
    expect(repository.mutationCalls, 1);
    _expectDisabledButton(tester, submit);

    repository.createCompleter.complete(const MeasurementPoint(
      id: '00000000-0000-4000-8000-000000000002',
      shopId: 's1',
      name: 'New Point',
    ));
    await tester.pump();
    // Submit again only after mutation completion, while refresh remains
    // pending. The public button must reject this duplicate.
    await tester.tap(submit);
    await tester.pump();
    expect(repository.mutationCalls, 1);
    _expectDisabledButton(tester, submit);
    repository.refreshCompleter.complete(repository.overview);
    await tester.pumpAndSettle();
  });

  testWidgets('Bind ignores duplicate submits through pending refresh',
      (tester) async {
    final repository = _PendingRepository();
    await _open(tester, repository, '/admin/mock/bind-device');
    await tester.tap(find.byKey(const Key('bind-device-option-SN-METER-001')));
    await tester.tap(find.byKey(const Key('bind-point-option-$_pointA')));
    await _submitAndAssertLocked(
        tester, repository, const Key('bind-submit-button'));
    repository.bindCompleter.complete(_assignment('assignment-002', _pointA));
    await tester.pump();
    // This is the second submit: mutation is complete, but refresh is pending.
    await tester.tap(find.byKey(const Key('bind-submit-button')));
    await tester.pump();
    expect(repository.mutationCalls, 1);
    _expectDisabledButton(tester, find.byKey(const Key('bind-submit-button')));
    repository.refreshCompleter.complete(repository.overview);
    await tester.pumpAndSettle();
  });

  testWidgets('Replace ignores duplicate submits through pending refresh',
      (tester) async {
    final repository = _PendingRepository(withBinding: true);
    await _open(
        tester, repository, '/admin/mock/replace-device/assignment-001');
    await tester
        .tap(find.byKey(const Key('replace-device-option-SN-METER-002')));
    await _submitAndAssertLocked(
        tester, repository, const Key('replace-submit-button'));
    repository.replaceCompleter.complete(
        _assignment('assignment-002', _pointA, deviceId: 'device-002'));
    await tester.pump();
    // This is the second submit: mutation is complete, but refresh is pending.
    await tester.tap(find.byKey(const Key('replace-submit-button')));
    await tester.pump();
    expect(repository.mutationCalls, 1);
    _expectDisabledButton(
        tester, find.byKey(const Key('replace-submit-button')));
    repository.refreshCompleter.complete(repository.overview);
    await tester.pumpAndSettle();
  });

  testWidgets('Relocate ignores duplicate submits through pending refresh',
      (tester) async {
    final repository = _PendingRepository(withBinding: true);
    await _open(
        tester, repository, '/admin/mock/relocate-device/assignment-001');
    await tester.tap(find.byKey(const Key('relocate-target-option-$_pointB')));
    await _submitAndAssertLocked(
        tester, repository, const Key('relocate-submit-button'));
    repository.relocateCompleter
        .complete(_assignment('assignment-002', _pointB));
    await tester.pump();
    // This is the second submit: mutation is complete, but refresh is pending.
    await tester.tap(find.byKey(const Key('relocate-submit-button')));
    await tester.pump();
    expect(repository.mutationCalls, 1);
    _expectDisabledButton(
        tester, find.byKey(const Key('relocate-submit-button')));
    repository.refreshCompleter.complete(repository.overview);
    await tester.pumpAndSettle();
  });

  testWidgets('Unbind ignores duplicate submits through pending refresh',
      (tester) async {
    final repository = _PendingRepository(withBinding: true);
    await _open(tester, repository, '/admin/mock/unbind-device/assignment-001');
    await _submitAndAssertLocked(
        tester, repository, const Key('unbind-submit-button'));
    repository.unbindCompleter.complete(_assignment('assignment-001', _pointA));
    await tester.pump();
    // This is the second submit: mutation is complete, but refresh is pending.
    await tester.tap(find.byKey(const Key('unbind-submit-button')));
    await tester.pump();
    expect(repository.mutationCalls, 1);
    _expectDisabledButton(
        tester, find.byKey(const Key('unbind-submit-button')));
    repository.refreshCompleter.complete(repository.overview);
    await tester.pumpAndSettle();
  });
}

Future<void> _pumpUntilVisible(WidgetTester tester, Finder finder) async {
  for (var i = 0; i < 10 && finder.evaluate().isEmpty; i++) {
    await tester.pump();
  }
  expect(finder, findsOneWidget);
}

void _expectDisabledButton(WidgetTester tester, Finder finder) {
  final button = tester.widget<FilledButton>(finder);
  expect(button.onPressed, isNull);
}

Future<void> _submitAndAssertLocked(
    WidgetTester tester, _PendingRepository repository, Key key) async {
  await tester.tap(find.byKey(key));
  await tester.pump();
  expect(repository.mutationCalls, 1);
  _expectDisabledButton(tester, find.byKey(key));
}

Future<void> _open(
    WidgetTester tester, _PendingRepository repository, String location) async {
  final router = _router();
  await tester.pumpWidget(_App(repository, router));
  await tester.pump();
  await tester.pump();
  unawaited(router.push(location));
  await tester.pump();
  await tester.pump();
}

GoRouter _router({String initialLocation = '/admin/mock'}) => GoRouter(
      initialLocation: initialLocation,
      routes: [
        GoRoute(
            path: '/admin/mock',
            builder: (_, __) => const AdminOverviewScreen()),
        GoRoute(
            path: '/admin/mock/create-measurement-point',
            builder: (_, __) => const CreateMeasurementPointScreen()),
        GoRoute(
            path: '/admin/mock/bind-device',
            builder: (_, __) => const BindDeviceScreen()),
        GoRoute(
            path: '/admin/mock/replace-device/:assignmentId',
            builder: (_, state) => ReplaceDeviceScreen(
                assignmentId: state.pathParameters['assignmentId']!)),
        GoRoute(
            path: '/admin/mock/relocate-device/:assignmentId',
            builder: (_, state) => RelocateDeviceScreen(
                assignmentId: state.pathParameters['assignmentId']!)),
        GoRoute(
            path: '/admin/mock/unbind-device/:assignmentId',
            builder: (_, state) => UnbindDeviceScreen(
                assignmentId: state.pathParameters['assignmentId']!)),
      ],
    );

class _App extends StatelessWidget {
  const _App(this.repository, this.router);
  final AdminOverviewRepository repository;
  final GoRouter router;

  @override
  Widget build(BuildContext context) => ProviderScope(
        overrides: [
          adminOverviewRepositoryProvider.overrideWithValue(repository)
        ],
        child: MaterialApp.router(routerConfig: router),
      );
}

class _PendingRepository implements AdminOverviewRepository {
  _PendingRepository({bool withBinding = false})
      : overview = AdminOverview(
          measurementPoints: const [
            MeasurementPoint(id: _pointA, shopId: 's1', name: 'Main Hall'),
            MeasurementPoint(id: _pointB, shopId: 's1', name: 'Kitchen'),
          ],
          devices: const [
            DeviceInventory(
                id: 'device-001',
                name: 'Meter A',
                serialNumber: 'SN-METER-001',
                macAddress: 'AABBCCDDEE01',
                status: 'Online'),
            DeviceInventory(
                id: 'device-002',
                name: 'Meter B',
                serialNumber: 'SN-METER-002',
                macAddress: 'AABBCCDDEE02',
                status: 'Standby'),
          ],
          activeAssignments: withBinding
              ? [
                  DeviceAssignment(
                    id: 'assignment-001',
                    deviceId: 'device-001',
                    measurementPointId: _pointA,
                    validFrom: DateTime(2026, 1, 1),
                  )
                ]
              : const [],
        );

  final AdminOverview overview;
  final Completer<AdminOverview> refreshCompleter = Completer();
  final Completer<MeasurementPoint> createCompleter = Completer();
  final Completer<DeviceAssignment> bindCompleter = Completer();
  final Completer<DeviceAssignment> replaceCompleter = Completer();
  final Completer<DeviceAssignment> relocateCompleter = Completer();
  final Completer<DeviceAssignment> unbindCompleter = Completer();
  int loadCalls = 0;
  int mutationCalls = 0;
  int createCalls = 0;
  final List<String> mutationIdentities = [];
  bool failNextRefresh = false;

  @override
  Future<AdminOverview> loadOverview() {
    loadCalls++;
    if (loadCalls == 1) return Future.value(overview);
    if (failNextRefresh) {
      failNextRefresh = false;
      return Future<AdminOverview>.error(StateError('refresh failed'));
    }
    return refreshCompleter.future;
  }

  @override
  Future<MeasurementPoint> createMeasurementPoint(
      CreateMeasurementPointInput input) {
    createCalls++;
    _recordMutation(input.requestIdentity);
    return createCompleter.future;
  }

  @override
  Future<DeviceAssignment> bindDevice(BindDeviceInput input) {
    _recordMutation(input.requestIdentity);
    return bindCompleter.future;
  }

  @override
  Future<DeviceAssignment> replaceDevice(ReplaceDeviceInput input) {
    _recordMutation(input.requestIdentity);
    return replaceCompleter.future;
  }

  @override
  Future<DeviceAssignment> relocateDevice(RelocateDeviceInput input) {
    _recordMutation(input.requestIdentity);
    return relocateCompleter.future;
  }

  @override
  Future<DeviceAssignment> unbindDevice(UnbindDeviceInput input) {
    _recordMutation(input.requestIdentity);
    return unbindCompleter.future;
  }

  void _recordMutation(String requestIdentity) {
    mutationCalls++;
    mutationIdentities.add(requestIdentity);
  }

  @override
  Future<List<DeviceAssignment>> loadAssignmentHistory() async => const [];
}

DeviceAssignment _assignment(String id, String point,
        {String deviceId = 'device-001'}) =>
    DeviceAssignment(
      id: id,
      deviceId: deviceId,
      measurementPointId: point,
      validFrom: DateTime(2026, 1, 1),
    );
