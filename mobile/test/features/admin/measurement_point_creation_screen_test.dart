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
import 'package:power_iot_app/features/auth/auth_controller.dart';
import 'package:power_iot_app/features/shops/providers/shop_provider.dart';

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

  test('create identity source persists one command across recreated consumers',
      () {
    final source = MockCreateMeasurementPointRequestIdentitySource();

    final firstIdentity = source.identityFor(
      shopId: ' s1 ',
      name: ' Kitchen Circuit ',
    );
    final recreatedConsumerIdentity = source.identityFor(
      shopId: 's1',
      name: 'Kitchen Circuit',
    );

    expect(recreatedConsumerIdentity, firstIdentity);
    expect(source.pending?.shopId, 's1');
    expect(source.pending?.name, 'Kitchen Circuit');
  });

  test('auth epoch clears every root lifecycle identity source', () {
    final container = ProviderContainer();
    addTearDown(container.dispose);

    final create = container.read(
      createMeasurementPointRequestIdentitySourceProvider,
    );
    final bind = container.read(bindDeviceRequestIdentitySourceProvider);
    final replace = container.read(replaceDeviceRequestIdentitySourceProvider);
    final relocate =
        container.read(relocateDeviceRequestIdentitySourceProvider);
    final unbind = container.read(unbindDeviceRequestIdentitySourceProvider);

    final createIdentity = create.identityFor(shopId: 's1', name: 'Kitchen');
    final bindIdentity =
        bind.identityFor(serialNumber: 'SN-1', measurementPointId: 'p1');
    final replaceIdentity =
        replace.identityFor(currentAssignmentId: 'a1', serialNumber: 'SN-2');
    final relocateIdentity = relocate.identityFor(
      currentAssignmentId: 'a1',
      targetMeasurementPointId: 'p2',
    );
    final unbindIdentity =
        unbind.identityFor(currentAssignmentId: 'a1', reason: 'retire');

    container.read(authClientProvider).beginSession();

    expect(create.pending, isNull);
    expect(bind.pending, isNull);
    expect(replace.pending, isNull);
    expect(relocate.pending, isNull);
    expect(unbind.pending, isNull);
    expect(create.identityFor(shopId: 's1', name: 'Kitchen'),
        isNot(createIdentity));
    expect(bind.identityFor(serialNumber: 'SN-1', measurementPointId: 'p1'),
        isNot(bindIdentity));
    expect(
      replace.identityFor(currentAssignmentId: 'a1', serialNumber: 'SN-2'),
      isNot(replaceIdentity),
    );
    expect(
      relocate.identityFor(
        currentAssignmentId: 'a1',
        targetMeasurementPointId: 'p2',
      ),
      isNot(relocateIdentity),
    );
    expect(unbind.identityFor(currentAssignmentId: 'a1', reason: 'retire'),
        isNot(unbindIdentity));
  });

  test('changed create command gets a new identity', () {
    final source = MockCreateMeasurementPointRequestIdentitySource();

    final firstIdentity = source.identityFor(shopId: 's1', name: 'Kitchen');
    final changedIdentity =
        source.identityFor(shopId: 's1', name: 'Kitchen Circuit');

    expect(changedIdentity, isNot(firstIdentity));
    expect(source.pending?.requestIdentity, changedIdentity);
    expect(source.pending?.name, 'Kitchen Circuit');
  });

  test(
      'explicit create start-over abandons pending identity and allocates new one',
      () {
    final source = MockCreateMeasurementPointRequestIdentitySource();
    final firstIdentity = source.identityFor(shopId: 's1', name: 'Kitchen');

    source.abandon();
    final newIdentity = source.identityFor(shopId: 's1', name: 'Kitchen');

    expect(newIdentity, isNot(firstIdentity));
    expect(source.pending?.requestIdentity, newIdentity);
  });

  testWidgets('explicit start-over restores editing and navigation',
      (tester) async {
    final repository = _RecordingCreationRepository()..failNextCreation = true;
    final identitySource = MockCreateMeasurementPointRequestIdentitySource();
    final container = ProviderContainer(
      overrides: [
        adminOverviewRepositoryProvider.overrideWithValue(repository),
        createMeasurementPointRequestIdentitySourceProvider
            .overrideWithValue(identitySource),
      ],
    );
    addTearDown(container.dispose);

    await tester.pumpWidget(
      _RouterTestApp(
        router: _createAdminRouter(),
        providerContainer: container,
      ),
    );
    await tester.pumpAndSettle();
    await tester.tap(find.text('Create Measurement Point'));
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const Key('measurement-point-name-field')),
      'Start Over Point',
    );
    await tester.tap(find.text('Create Measurement Point'));
    await tester.pumpAndSettle();

    expect(find.text('Unable to create Measurement Point. Please try again.'),
        findsOneWidget);
    final firstIdentity = identitySource.pending?.requestIdentity;
    expect(firstIdentity, isNotNull);
    await tester
        .tap(find.byKey(const Key('create-measurement-point-start-over')));
    await tester.pump();

    expect(identitySource.pending, isNull);
    expect(
      tester
          .widget<TextFormField>(
            find.byKey(const Key('measurement-point-name-field')),
          )
          .enabled,
      isTrue,
    );
    await tester.pageBack();
    await tester.pumpAndSettle();
    expect(find.text('Admin Overview'), findsOneWidget);
  });

  test('response-loss retry through a recreated consumer does not duplicate',
      () async {
    final repository = MockAdminOverviewRepository()
      ..loseResponseAfterNextCreation = true;
    final source = MockCreateMeasurementPointRequestIdentitySource();
    final firstIdentity = source.identityFor(
      shopId: 's1',
      name: 'Retry Point',
    );

    await expectLater(
      repository.createMeasurementPoint(
        CreateMeasurementPointInput(
          requestIdentity: firstIdentity,
          shopId: 's1',
          name: 'Retry Point',
        ),
      ),
      throwsA(isA<StateError>()),
    );

    final recreatedConsumerIdentity = source.identityFor(
      shopId: 's1',
      name: ' Retry Point ',
    );
    final replayed = await repository.createMeasurementPoint(
      CreateMeasurementPointInput(
        requestIdentity: recreatedConsumerIdentity,
        shopId: 's1',
        name: 'Retry Point',
      ),
    );
    source.complete(recreatedConsumerIdentity);
    final overview = await repository.loadOverview();

    expect(recreatedConsumerIdentity, firstIdentity);
    expect(replayed.name, 'Retry Point');
    expect(
      overview.measurementPoints.where((point) => point.name == 'Retry Point'),
      hasLength(1),
    );
    expect(source.pending, isNull);
  });

  testWidgets(
      'route remount keeps unresolved response-loss creation protected and retries canonical command',
      (tester) async {
    final repository = _RecordingCreationRepository()
      ..loseResponseAfterNextCreation = true;
    final identitySource = MockCreateMeasurementPointRequestIdentitySource();
    final container = ProviderContainer(
      overrides: [
        adminOverviewRepositoryProvider.overrideWithValue(repository),
        createMeasurementPointRequestIdentitySourceProvider
            .overrideWithValue(identitySource),
        shopProvider.overrideWith((ref) => ShopNotifier()),
      ],
    );
    addTearDown(container.dispose);
    final router = _createAdminRouter();

    await tester.pumpWidget(
      _RouterTestApp(router: router, providerContainer: container),
    );
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
    final pendingBeforeRemount = identitySource.pending;
    expect(pendingBeforeRemount, isNotNull);
    expect(pendingBeforeRemount?.shopId, 's1');
    expect(pendingBeforeRemount?.name, 'Retry Point');
    expect(repository.requests, hasLength(1));
    final firstRequest = repository.requests.single;
    expect(firstRequest.requestIdentity, pendingBeforeRemount?.requestIdentity);
    expect(firstRequest.shopId, 's1');
    expect(firstRequest.name, 'Retry Point');
    final firstIdentity = pendingBeforeRemount?.requestIdentity;
    final committedBeforeRemount = await repository.loadOverview();
    expect(
      committedBeforeRemount.measurementPoints
          .where((point) => point.name == 'Retry Point'),
      hasLength(1),
    );
    expect(container.read(shopProvider).currentShop.id, 's1');
    container.read(shopProvider.notifier).selectShop('s2');
    expect(container.read(shopProvider).currentShop.id, 's2');

    // Replace the route programmatically: PopScope remains strict while the
    // failed screen is visible, and the next route gets a fresh widget state.
    router.go('/admin/mock');
    await tester.pumpAndSettle();
    unawaited(router.push('/admin/mock/create-measurement-point'));
    await tester.pumpAndSettle();

    final recreatedField = tester.widget<TextFormField>(
      find.byKey(const Key('measurement-point-name-field')),
    );
    expect(recreatedField.controller?.text, 'Retry Point');
    expect(identitySource.pending?.requestIdentity, firstIdentity);
    expect(recreatedField.enabled, isFalse);
    final retryButton = tester.widget<FilledButton>(
      find.widgetWithText(FilledButton, 'Create Measurement Point'),
    );
    expect(retryButton.onPressed, isNotNull);
    expect(find.byKey(const Key('create-measurement-point-start-over')),
        findsOneWidget);

    await tester.pageBack();
    await tester.pumpAndSettle();
    expect(find.text('New Measurement Point'), findsOneWidget);
    final pendingAfterBack = tester.widget<TextFormField>(
      find.byKey(const Key('measurement-point-name-field')),
    );
    expect(pendingAfterBack.controller?.text, 'Retry Point');
    expect(identitySource.pending?.requestIdentity, firstIdentity);

    await tester.tap(find.text('Create Measurement Point'));
    await tester.pumpAndSettle();

    final overview = await repository.loadOverview();
    expect(repository.requestIdentities, [firstIdentity, firstIdentity]);
    expect(repository.requests, hasLength(2));
    final retryRequest = repository.requests[1];
    expect(retryRequest.requestIdentity, firstRequest.requestIdentity);
    expect(retryRequest.requestIdentity, firstIdentity);
    expect(retryRequest.shopId, 's1');
    expect(retryRequest.shopId, firstRequest.shopId);
    expect(retryRequest.name, 'Retry Point');
    expect(retryRequest.name, firstRequest.name);
    expect(
      overview.measurementPoints.where((point) => point.name == 'Retry Point'),
      hasLength(1),
    );
    expect(identitySource.pending, isNull);
    expect(find.text('Admin Overview'), findsOneWidget);

    await tester.tap(find.text('Create Measurement Point'));
    await tester.pumpAndSettle();
    final editableField = tester.widget<TextFormField>(
      find.byKey(const Key('measurement-point-name-field')),
    );
    expect(editableField.enabled, isTrue);
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

GoRouter _createAdminRouter() {
  return GoRouter(
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
}

class _RouterTestApp extends StatelessWidget {
  const _RouterTestApp({
    this.repository,
    this.router,
    this.providerContainer,
  });

  final AdminOverviewRepository? repository;
  final GoRouter? router;
  final ProviderContainer? providerContainer;

  @override
  Widget build(BuildContext context) {
    final app =
        MaterialApp.router(routerConfig: router ?? _createAdminRouter());
    final container = providerContainer;
    if (container != null) {
      return UncontrolledProviderScope(container: container, child: app);
    }

    return ProviderScope(
      overrides: [
        if (repository != null)
          adminOverviewRepositoryProvider.overrideWithValue(repository!),
      ],
      child: app,
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
  Future<DeviceAssignment> unbindDevice(UnbindDeviceInput input) {
    throw UnsupportedError('Unbinding is not used by this test repository.');
  }

  @override
  Future<List<DeviceAssignment>> loadAssignmentHistory() async => const [];
}

class _RecordingCreationRepository extends MockAdminOverviewRepository {
  final List<String> requestIdentities = [];
  final List<CreateMeasurementPointInput> requests = [];

  @override
  Future<MeasurementPoint> createMeasurementPoint(
    CreateMeasurementPointInput input,
  ) {
    requestIdentities.add(input.requestIdentity);
    requests.add(input);
    return super.createMeasurementPoint(input);
  }
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
  Future<DeviceAssignment> unbindDevice(UnbindDeviceInput input) {
    throw UnsupportedError('Unbinding is not used by this test repository.');
  }

  @override
  Future<List<DeviceAssignment>> loadAssignmentHistory() async => const [];
}
