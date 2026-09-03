import 'package:flutter_test/flutter_test.dart';
import 'package:power_iot_app/features/admin/data/dtos/admin_overview_dto.dart';

void main() {
  test('admin overview preserves authoritative device lifecycle status', () {
    final dto = AdminOverviewDto.fromJson({
      'measurementPoints': <Object?>[],
      'devices': [
        {
          'id': '1',
          'name': 'Meter',
          'serialNumber': 'SN-1',
          'macAddress': 'AABBCCDDEEFF',
          'status': 'Offline',
          'lifecycleStatus': 'DISABLED',
        },
      ],
      'activeAssignments': <Object?>[],
      'assignmentHistory': <Object?>[],
    });
    expect(dto.devices.single.lifecycleStatus, 'DISABLED');
  });
}
