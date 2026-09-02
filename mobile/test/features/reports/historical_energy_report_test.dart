import 'package:flutter_test/flutter_test.dart';
import 'package:power_iot_app/features/reports/domain/models/historical_energy_report.dart';

Map<String, Object?> _payload() => {
      'month': '2026-08',
      'timezone': 'Asia/Taipei',
      'period': {
        'start': '2026-07-31T16:00:00Z',
        'end': '2026-08-31T16:00:00Z',
        'cutoff': '2026-08-31T16:00:00Z',
        'snapshot': '2026-09-01T00:00:00Z',
      },
      'summary': {
        'status': 'PARTIAL',
        'usageKwh': '1.25',
        'expectedDurationSeconds': 3600,
        'observedDurationSeconds': 1800,
        'coverage': '0.5',
      },
      'measurementPoints': [
        {
          'measurementPointId': 'mp-1',
          'status': 'COMPLETE',
          'usageKwh': '0',
          'expectedDurationSeconds': 3600,
          'observedDurationSeconds': 3600,
          'coverage': '1',
          'warnings': <String>[],
        },
        {
          'measurementPointId': 'mp-2',
          'status': 'NO_DATA',
          'usageKwh': null,
          'expectedDurationSeconds': 3600,
          'observedDurationSeconds': 0,
          'coverage': '0',
          'warnings': ['LEGACY_EVIDENCE_EXCLUDED'],
        },
      ],
      'warnings': ['PARTIAL_MONITORING_DATA'],
    };

void main() {
  test('parses report facts including zero and null usage distinctly', () {
    final report = HistoricalEnergyReport.fromJson(_payload());

    expect(report.month, '2026-08');
    expect(report.timezone, 'Asia/Taipei');
    expect(report.summary.status, HistoricalEnergyStatus.partial);
    expect(report.summary.usageKwh, '1.25');
    expect(report.measurementPoints[0].usageKwh, '0');
    expect(report.measurementPoints[0].status, HistoricalEnergyStatus.complete);
    expect(report.measurementPoints[1].usageKwh, isNull);
    expect(report.measurementPoints[1].status, HistoricalEnergyStatus.noData);
    expect(report.warnings, ['PARTIAL_MONITORING_DATA']);
  });

  test('accepts every report status', () {
    for (final status in [
      'COMPLETE',
      'PARTIAL',
      'NO_DATA',
      'NO_EXPECTED_WINDOW',
    ]) {
      final payload = _payload();
      (payload['summary']! as Map<String, Object?>)['status'] = status;
      final report = HistoricalEnergyReport.fromJson(payload);
      expect(report.summary.status.code, status);
    }
  });

  test('rejects unknown status and malformed numeric values', () {
    final unknown = _payload();
    (unknown['summary']! as Map<String, Object?>)['status'] = 'UNKNOWN';
    expect(
        () => HistoricalEnergyReport.fromJson(unknown), throwsFormatException);

    final malformed = _payload();
    (malformed['summary']! as Map<String, Object?>)['usageKwh'] = 0;
    expect(() => HistoricalEnergyReport.fromJson(malformed),
        throwsFormatException);
  });
}
