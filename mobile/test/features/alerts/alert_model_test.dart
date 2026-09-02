import 'package:flutter_test/flutter_test.dart';
import 'package:power_iot_app/features/alerts/domain/models/alert.dart';

void main() {
  test('parses the MP-centered settings contract without legacy thresholds', () {
    final settings = AlertSettings.fromJson({
      'measurementPointId': 'mp-1',
      'isEnabled': true,
      'quietHoursStart': '22:00',
      'quietHoursEnd': '06:00',
      'powerThresholdW': 12.5,
      'updatedAt': '2026-09-02T12:00:00Z',
    });
    expect(settings.measurementPointId, 'mp-1');
    expect(settings.quietHoursStart, '22:00');
    expect(settings.powerThresholdW, 12.5);
  });

  test('parses authoritative point and device provenance snapshots', () {
    final alert = AlertRecord.fromJson({
      'id': 7,
      'type': 'CURFEW_USAGE',
      'message': 'operation detected',
      'createdAt': '2026-09-02T12:00:00Z',
      'measurementPoint': {'id': 'mp-1', 'name': 'Kitchen'},
      'device': {'deviceId': 3, 'name': 'Sensor A', 'serialNumber': 'S-3'},
      'snapshot': {'voltage': 110.0, 'current': 1.2, 'power': 132.0},
    });
    expect(alert.measurementPointId, 'mp-1');
    expect(alert.deviceName, 'Sensor A');
    expect(alert.serialNumber, 'S-3');
    expect(alert.power, 132.0);
  });
}
