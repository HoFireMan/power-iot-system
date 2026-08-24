import 'package:flutter_test/flutter_test.dart';
import 'package:power_iot_app/features/measurement_point_detail/data/dtos/measurement_point_detail_dto.dart';

void main() {
  test(
      'detail DTO preserves the scoped detail contract and hides optional admin data',
      () {
    final detail = MeasurementPointDetailDto.fromJson({
      'shop': {'code': 'S7', 'name': 'Shop'},
      'measurementPoint': {'name': 'Main Hall', 'status': 'online'},
      'currentPower': {'watts': 0, 'lastUpdatedAt': '2026-08-24T04:00:00Z'},
      'todayEnergy': {'kwh': 0, 'completeThrough': '2026-08-24T03:00:00Z'},
      'monthEnergy': {'kwh': null, 'completeThrough': null},
      'currentDevice': {
        'displayName': 'meter',
        'mac': 'AABBCCDDEEFF',
        'lastSeen': null
      },
      'assignmentHistory': [
        {
          'displayName': 'meter',
          'mac': 'AABBCCDDEEFF',
          'validFrom': '2026-08-24T00:00:00Z',
          'validTo': null
        },
      ],
    }).model;
    expect(detail.currentPowerW, 0);
    expect(detail.today.kwh, 0);
    expect(detail.month.kwh, isNull);
    expect(detail.technicalInfo, isNull);
    expect(detail.assignmentHistory, hasLength(1));
  });

  test('detail DTO preserves zero power and independent coverage states', () {
    final detail = MeasurementPointDetailDto.fromJson({
      'id': '00000000-0000-4000-8000-000000000001',
      'name': 'Main Hall',
      'shop': {'id': '7', 'code': 'S7', 'name': 'Shop'},
      'devices': <Object?>[],
      'currentPowerW': 0,
      'today': {
        'kwh': 0,
        'throughAt': '2026-08-24T01:00:00Z',
        'state': 'PROVEN',
      },
      'month': {'kwh': null, 'throughAt': null, 'state': 'GAP'},
      'generatedAt': '2026-08-24T04:00:00Z',
    }).model;

    expect(detail.currentPowerW, 0);
    expect(detail.today.kwh, 0);
    expect(detail.today.state, 'PROVEN');
    expect(detail.month.kwh, isNull);
    expect(detail.month.state, 'GAP');
  });

  test('detail DTO rejects unknown fields rather than fabricating data', () {
    expect(
      () => MeasurementPointDetailDto.fromJson({
        'id': 'point',
        'name': 'Main Hall',
        'shop': {'id': '7', 'code': 'S7', 'name': 'Shop'},
        'devices': <Object?>[],
        'currentPowerW': null,
        'today': {'kwh': null, 'throughAt': null, 'state': 'UNKNOWN'},
        'month': {'kwh': null, 'throughAt': null, 'state': 'UNKNOWN'},
        'generatedAt': '2026-08-24T04:00:00Z',
        'unexpected': true,
      }),
      throwsFormatException,
    );
  });
}
