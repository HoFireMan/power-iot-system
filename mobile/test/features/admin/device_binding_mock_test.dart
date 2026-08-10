import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:power_iot_app/features/admin/data/repositories/mock_admin_overview_repository.dart';
import 'package:power_iot_app/features/admin/domain/models/device_ref.dart';
import 'package:power_iot_app/features/admin/domain/repositories/admin_overview_repository.dart';
import 'package:power_iot_app/features/admin/presentation/providers/admin_overview_provider.dart';
import 'package:power_iot_app/features/admin/presentation/screens/admin_overview_screen.dart';
import 'package:power_iot_app/features/admin/presentation/screens/bind_device_screen.dart';

void main() {
  testWidgets('A: overview exposes the bind Device action', (tester) async {
    await tester.pumpWidget(const _RouterTestApp());
    await tester.pumpAndSettle();

    expect(find.text('Bind Device'), findsOneWidget);
  });

  testWidgets('B: bind requires both an existing Device and MP selection',
      (tester) async {
    await tester.pumpWidget(const _RouterTestApp());
    await tester.pumpAndSettle();
    await tester.tap(find.text('Bind Device'));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('bind-submit-button')));
    await tester.pump();

    expect(find.text('Device is required.'), findsOneWidget);
    expect(find.text('Measurement Point is required.'), findsOneWidget);
  });

  testWidgets('C: successful bind returns and visibly shows the relationship',
      (tester) async {
    final repository = MockAdminOverviewRepository();
    await tester.pumpWidget(_RouterTestApp(repository: repository));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Bind Device'));
    await tester.pumpAndSettle();

    await tester.tap(find.byKey(const Key('bind-device-field')));
    await tester.pumpAndSettle();
    await tester.tap(
      find.byKey(const Key('bind-device-option-SN-METER-001')),
    );
    await tester.tap(find.byKey(const Key('bind-measurement-point-field')));
    await tester.pumpAndSettle();
    await tester.tap(
      find.byKey(const Key(
        'bind-point-option-00000000-0000-4000-8000-000000000001',
      )),
    );
    await tester.tap(find.byKey(const Key('bind-submit-button')));
    await tester.pumpAndSettle();

    expect(find.text('Main Hall · SN-METER-001'), findsOneWidget);
    expect((await repository.loadOverview()).activeAssignments, hasLength(1));
  });

  testWidgets('H: response loss keeps the binding operation retryable',
      (tester) async {
    final repository = MockAdminOverviewRepository()
      ..loseResponseAfterNextBinding = true;
    await tester.pumpWidget(_RouterTestApp(repository: repository));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Bind Device'));
    await tester.pumpAndSettle();
    await tester.tap(
      find.byKey(const Key('bind-device-option-SN-METER-001')),
    );
    await tester.tap(
      find.byKey(const Key(
        'bind-point-option-00000000-0000-4000-8000-000000000001',
      )),
    );
    await tester.tap(find.byKey(const Key('bind-submit-button')));
    await tester.pumpAndSettle();

    expect(
      find.text(
          'Unable to bind Device. Please check the selections and try again.'),
      findsOneWidget,
    );
    await tester.pageBack();
    await tester.pumpAndSettle();
    expect(find.text('Admin Overview'), findsOneWidget);

    await tester.tap(find.text('Bind Device'));
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('bind-submit-button')), findsOneWidget);
    await tester.tap(find.byKey(const Key('bind-submit-button')));
    await tester.pumpAndSettle();

    expect((await repository.loadOverview()).activeAssignments.single.deviceId,
        'device-001');
  });

  test('D: bind does not create a Device or mutate its identity', () async {
    final repository = MockAdminOverviewRepository();
    final before = await repository.loadOverview();

    await repository.bindDevice(
      const BindDeviceInput(
        requestIdentity: 'bind-identity-001',
        deviceRef: DeviceRef(serialNumber: 'SN-METER-001'),
        measurementPointId: '00000000-0000-4000-8000-000000000001',
      ),
    );
    final after = await repository.loadOverview();

    expect(after.devices, orderedEquals(before.devices));
    expect(after.activeAssignments.single.deviceId, 'device-001');
    expect(after.activeAssignments.single.measurementPointId,
        '00000000-0000-4000-8000-000000000001');
  });

  test('E: the selected MP, not legacy device context, is assignment truth',
      () async {
    final repository = MockAdminOverviewRepository();

    await repository.bindDevice(
      const BindDeviceInput(
        requestIdentity: 'bind-context-001',
        deviceRef: DeviceRef(serialNumber: 'SN-METER-002'),
        measurementPointId: '00000000-0000-4000-8000-000000000001',
      ),
    );

    final overview = await repository.loadOverview();
    expect(
      overview.activeAssignments
          .firstWhere((assignment) => assignment.deviceId == 'device-002')
          .measurementPointId,
      '00000000-0000-4000-8000-000000000001',
    );
    expect(overview.measurementPoints.first.shopId, 's1');
  });

  test('F: occupied Device/MP conflicts leave state unchanged', () async {
    final repository = MockAdminOverviewRepository();
    const first = BindDeviceInput(
      requestIdentity: 'bind-conflict-001',
      deviceRef: DeviceRef(serialNumber: 'SN-METER-001'),
      measurementPointId: '00000000-0000-4000-8000-000000000001',
    );
    await repository.bindDevice(first);

    await expectLater(
      repository.bindDevice(
        const BindDeviceInput(
          requestIdentity: 'bind-conflict-002',
          deviceRef: DeviceRef(serialNumber: 'SN-METER-001'),
          measurementPointId: '00000000-0000-4000-8000-000000000099',
        ),
      ),
      throwsA(isA<StateError>()),
    );
    await expectLater(
      repository.bindDevice(
        const BindDeviceInput(
          requestIdentity: 'bind-conflict-003',
          deviceRef: DeviceRef(serialNumber: 'SN-METER-002'),
          measurementPointId: '00000000-0000-4000-8000-000000000001',
        ),
      ),
      throwsA(isA<StateError>()),
    );

    await repository.bindDevice(
      const BindDeviceInput(
        requestIdentity: 'bind-conflict-004',
        deviceRef: DeviceRef(serialNumber: 'SN-METER-002'),
        measurementPointId: '00000000-0000-4000-8000-000000000099',
      ),
    );
    expect((await repository.loadOverview()).activeAssignments, hasLength(2));
  });

  test('G: serial is the selected DeviceRef UX and inconsistent refs fail',
      () async {
    final repository = MockAdminOverviewRepository();
    await repository.bindDevice(
      const BindDeviceInput(
        requestIdentity: 'bind-ref-001',
        deviceRef: DeviceRef(serialNumber: 'SN-METER-001'),
        measurementPointId: '00000000-0000-4000-8000-000000000001',
      ),
    );

    await expectLater(
      repository.bindDevice(
        const BindDeviceInput(
          requestIdentity: 'bind-ref-002',
          deviceRef: DeviceRef(
            serialNumber: 'SN-METER-002',
            macAddress: 'AABBCCDDEEFF',
          ),
          measurementPointId: '00000000-0000-4000-8000-000000000099',
        ),
      ),
      throwsA(isA<StateError>()),
    );
  });

  test('H: response loss retry replays one active assignment', () async {
    final repository = MockAdminOverviewRepository()
      ..loseResponseAfterNextBinding = true;
    const input = BindDeviceInput(
      requestIdentity: 'bind-retry-001',
      deviceRef: DeviceRef(serialNumber: 'SN-METER-001'),
      measurementPointId: '00000000-0000-4000-8000-000000000001',
    );

    await expectLater(repository.bindDevice(input), throwsA(isA<StateError>()));
    final replayed = await repository.bindDevice(input);
    final overview = await repository.loadOverview();

    expect(replayed.id, 'assignment-001');
    expect(overview.activeAssignments, hasLength(1));
  });

  test('I: binding seam exposes no Replace operation or UI leakage', () async {
    final repository = MockAdminOverviewRepository();
    expect(repository, isA<AdminOverviewRepository>());
    expect((await repository.loadOverview()).activeAssignments, isEmpty);
  });
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
          path: '/admin/mock/bind-device',
          builder: (context, state) => const BindDeviceScreen(),
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
