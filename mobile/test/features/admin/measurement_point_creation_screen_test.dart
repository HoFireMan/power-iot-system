import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:power_iot_app/features/admin/data/repositories/mock_admin_overview_repository.dart';
import 'package:power_iot_app/features/admin/domain/models/admin_overview.dart';
import 'package:power_iot_app/features/admin/domain/models/device_assignment.dart';
import 'package:power_iot_app/features/admin/domain/models/measurement_point.dart';
import 'package:power_iot_app/features/admin/domain/repositories/admin_overview_repository.dart';
import 'package:power_iot_app/features/admin/presentation/providers/admin_overview_provider.dart';
import 'package:power_iot_app/features/admin/presentation/screens/admin_overview_screen.dart';
import 'package:power_iot_app/features/admin/presentation/screens/create_measurement_point_screen.dart';

void main() {
  testWidgets('Admin Overview exposes a create Measurement Point action',
      (tester) async {
    await tester.pumpWidget(const _RouterTestApp());
    await tester.pumpAndSettle();

    expect(find.text('Create Measurement Point'), findsOneWidget);
  });

  testWidgets('in-flight creation cannot be abandoned', (tester) async {
    final repository = _PendingCreationRepository();

    await tester.pumpWidget(_RouterTestApp(repository: repository));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Create Measurement Point'));
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const Key('measurement-point-name-field')),
      'Pending Point',
    );
    await tester.tap(find.text('Create Measurement Point'));
    await tester.pump();

    await tester.pageBack();
    await tester.pump();
    expect(find.text('New Measurement Point'), findsOneWidget);

    repository.completer.complete(
      const MeasurementPoint(
        id: '00000000-0000-4000-8000-000000000002',
        shopId: 's1',
        name: 'Pending Point',
      ),
    );
    await tester.pumpAndSettle();
    expect(find.text('Admin Overview'), findsOneWidget);
  });

  testWidgets('missing Measurement Point name does not submit', (tester) async {
    final repository = _RecordingRepository();

    await tester.pumpWidget(_FormTestApp(repository: repository));
    await tester.tap(find.text('Create Measurement Point'));
    await tester.pump();

    expect(find.text('Measurement Point name is required.'), findsOneWidget);
    expect(repository.createCallCount, 0);
  });

  testWidgets('valid creation returns to the overview with the new point',
      (tester) async {
    final repository = MockAdminOverviewRepository();

    await tester.pumpWidget(_RouterTestApp(repository: repository));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Create Measurement Point'));
    await tester.pumpAndSettle();

    await tester.enterText(
      find.byKey(const Key('measurement-point-name-field')),
      'Kitchen Circuit',
    );
    await tester.tap(find.text('Create Measurement Point'));
    await tester.pumpAndSettle();

    expect(find.text('Kitchen Circuit'), findsOneWidget);
    expect(find.text('Measurement Point created: Kitchen Circuit'),
        findsOneWidget);

    final overview = await repository.loadOverview();
    expect(overview.measurementPoints.last.name, 'Kitchen Circuit');
  });

  test('created Measurement Point has stable logical identity', () async {
    final repository = MockAdminOverviewRepository();

    await repository.createMeasurementPoint(
      const CreateMeasurementPointInput(
        requestIdentity: 'create-logical-point-001',
        shopId: 's1',
        name: 'Logical Point',
      ),
    );
    final overview = await repository.loadOverview();
    final created = overview.measurementPoints.last;

    expect(created.id, '00000000-0000-4000-8000-000000000002');
    expect(created.shopId, 's1');
    expect(created.id, isNot('device-001'));
    expect(created.id, isNot(contains('MAC')));
  });

  testWidgets('post-commit response loss is retryable without duplication',
      (tester) async {
    final repository = MockAdminOverviewRepository()
      ..loseResponseAfterNextCreation = true;

    await tester.pumpWidget(_RouterTestApp(repository: repository));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Create Measurement Point'));
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const Key('measurement-point-name-field')),
      'Retry Point',
    );

    await tester.tap(find.text('Create Measurement Point'));
    await tester.pumpAndSettle();

    expect(find.text('Unable to create Measurement Point. Please try again.'),
        findsOneWidget);
    await tester.pageBack();
    await tester.pumpAndSettle();
    expect(find.text('Unable to create Measurement Point. Please try again.'),
        findsOneWidget);

    var overview = await repository.loadOverview();
    expect(
      overview.measurementPoints.where((point) => point.name == 'Retry Point'),
      hasLength(1),
    );

    await tester.tap(find.text('Create Measurement Point'));
    await tester.pumpAndSettle();

    overview = await repository.loadOverview();
    expect(
      overview.measurementPoints.where((point) => point.name == 'Retry Point'),
      hasLength(1),
    );
  });

  test('creating a Measurement Point does not mutate device inventory',
      () async {
    final repository = MockAdminOverviewRepository();
    final before = await repository.loadOverview();

    await repository.createMeasurementPoint(
      const CreateMeasurementPointInput(
        requestIdentity: 'create-unbound-point-001',
        shopId: 's1',
        name: 'Unbound Point',
      ),
    );
    final after = await repository.loadOverview();

    expect(after.devices, orderedEquals(before.devices));
    expect(after.measurementPoints,
        hasLength(before.measurementPoints.length + 1));
  });

  test('post-commit response loss replays the same logical point', () async {
    final repository = MockAdminOverviewRepository()
      ..loseResponseAfterNextCreation = true;
    const input = CreateMeasurementPointInput(
      requestIdentity: 'create-replay-001',
      shopId: 's1',
      name: 'Replay Point',
    );

    await expectLater(
      repository.createMeasurementPoint(input),
      throwsA(isA<StateError>()),
    );
    final replayed = await repository.createMeasurementPoint(input);
    final overview = await repository.loadOverview();

    expect(replayed.id, '00000000-0000-4000-8000-000000000002');
    expect(
      overview.measurementPoints.where((point) => point.name == 'Replay Point'),
      hasLength(1),
    );
    await expectLater(
      repository.createMeasurementPoint(
        const CreateMeasurementPointInput(
          requestIdentity: 'create-replay-001',
          shopId: 's1',
          name: 'Changed Replay Point',
        ),
      ),
      throwsA(isA<StateError>()),
    );
    expect(
      overview.measurementPoints.where((point) => point.name == 'Replay Point'),
      hasLength(1),
    );
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
          path: '/admin/mock/create-measurement-point',
          builder: (context, state) => const CreateMeasurementPointScreen(),
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

class _FormTestApp extends StatelessWidget {
  const _FormTestApp({required this.repository});

  final AdminOverviewRepository repository;

  @override
  Widget build(BuildContext context) {
    return ProviderScope(
      overrides: [
        adminOverviewRepositoryProvider.overrideWithValue(repository),
      ],
      child: const MaterialApp(home: CreateMeasurementPointScreen()),
    );
  }
}

class _PendingCreationRepository implements AdminOverviewRepository {
  final Completer<MeasurementPoint> completer = Completer<MeasurementPoint>();

  @override
  Future<AdminOverview> loadOverview() async => const AdminOverview(
        measurementPoints: [],
        devices: [],
      );

  @override
  Future<MeasurementPoint> createMeasurementPoint(
    CreateMeasurementPointInput input,
  ) =>
      completer.future;

  @override
  Future<DeviceAssignment> bindDevice(BindDeviceInput input) {
    throw UnsupportedError('Binding is not used by this test repository.');
  }

  @override
  Future<DeviceAssignment> replaceDevice(ReplaceDeviceInput input) {
    throw UnsupportedError('Replacement is not used by this test repository.');
  }

  @override
  Future<DeviceAssignment> relocateDevice(RelocateDeviceInput input) {
    throw UnsupportedError('Relocation is not used by this test repository.');
  }

  @override
  Future<List<DeviceAssignment>> loadAssignmentHistory() async => const [];
}

class _RecordingRepository implements AdminOverviewRepository {
  int createCallCount = 0;

  @override
  Future<AdminOverview> loadOverview() async => const AdminOverview(
        measurementPoints: [],
        devices: [],
      );

  @override
  Future<MeasurementPoint> createMeasurementPoint(
    CreateMeasurementPointInput input,
  ) async {
    createCallCount++;
    throw StateError('should not be called for invalid form input');
  }

  @override
  Future<DeviceAssignment> bindDevice(BindDeviceInput input) {
    throw UnsupportedError('Binding is not used by this test repository.');
  }

  @override
  Future<DeviceAssignment> replaceDevice(ReplaceDeviceInput input) {
    throw UnsupportedError('Replacement is not used by this test repository.');
  }

  @override
  Future<DeviceAssignment> relocateDevice(RelocateDeviceInput input) {
    throw UnsupportedError('Relocation is not used by this test repository.');
  }

  @override
  Future<List<DeviceAssignment>> loadAssignmentHistory() async => const [];
}
