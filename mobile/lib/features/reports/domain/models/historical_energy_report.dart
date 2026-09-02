enum HistoricalEnergyStatus {
  complete('COMPLETE'),
  partial('PARTIAL'),
  noData('NO_DATA'),
  noExpectedWindow('NO_EXPECTED_WINDOW');

  const HistoricalEnergyStatus(this.code);
  final String code;

  static HistoricalEnergyStatus fromCode(Object? value) {
    if (value is! String) {
      throw const FormatException('Invalid historical energy status');
    }
    return HistoricalEnergyStatus.values.firstWhere(
      (status) => status.code == value,
      orElse: () =>
          throw const FormatException('Invalid historical energy status'),
    );
  }
}

class HistoricalEnergyReport {
  const HistoricalEnergyReport({
    required this.month,
    required this.timezone,
    required this.period,
    required this.summary,
    required this.measurementPoints,
    required this.warnings,
  });

  final String month;
  final String timezone;
  final HistoricalEnergyPeriod period;
  final HistoricalEnergyFacts summary;
  final List<HistoricalEnergyPoint> measurementPoints;
  final List<String> warnings;

  factory HistoricalEnergyReport.fromJson(Object? value) {
    final map = _map(value, 'historical energy report');
    _exactKeys(map, const {
      'month',
      'timezone',
      'period',
      'summary',
      'measurementPoints',
      'warnings',
    });
    if (map['month'] is! String ||
        map['timezone'] is! String ||
        map['measurementPoints'] is! List ||
        map['warnings'] is! List) {
      throw const FormatException('Invalid historical energy report');
    }
    return HistoricalEnergyReport(
      month: map['month'] as String,
      timezone: map['timezone'] as String,
      period: HistoricalEnergyPeriod.fromJson(map['period']),
      summary: HistoricalEnergyFacts.fromJson(map['summary']),
      measurementPoints: List.unmodifiable(
        (map['measurementPoints'] as List).map(HistoricalEnergyPoint.fromJson),
      ),
      warnings: _warnings(map['warnings']),
    );
  }
}

class HistoricalEnergyPeriod {
  const HistoricalEnergyPeriod({
    required this.start,
    required this.end,
    required this.cutoff,
    required this.snapshot,
  });

  final String start;
  final String end;
  final String cutoff;
  final String snapshot;

  factory HistoricalEnergyPeriod.fromJson(Object? value) {
    final map = _map(value, 'historical energy period');
    _exactKeys(map, const {'start', 'end', 'cutoff', 'snapshot'});
    if (map.values.any((item) => item is! String)) {
      throw const FormatException('Invalid historical energy period');
    }
    return HistoricalEnergyPeriod(
      start: map['start'] as String,
      end: map['end'] as String,
      cutoff: map['cutoff'] as String,
      snapshot: map['snapshot'] as String,
    );
  }
}

class HistoricalEnergyFacts {
  const HistoricalEnergyFacts({
    required this.status,
    required this.usageKwh,
    required this.expectedDurationSeconds,
    required this.observedDurationSeconds,
    required this.coverage,
  });

  final HistoricalEnergyStatus status;
  final String? usageKwh;
  final int expectedDurationSeconds;
  final int observedDurationSeconds;
  final String? coverage;

  factory HistoricalEnergyFacts.fromJson(Object? value) {
    final map = _map(value, 'historical energy facts');
    _exactKeys(map, const {
      'status',
      'usageKwh',
      'expectedDurationSeconds',
      'observedDurationSeconds',
      'coverage',
    });
    if ((map['usageKwh'] != null && map['usageKwh'] is! String) ||
        (map['coverage'] != null && map['coverage'] is! String) ||
        map['expectedDurationSeconds'] is! int ||
        map['observedDurationSeconds'] is! int) {
      throw const FormatException('Invalid historical energy facts');
    }
    return HistoricalEnergyFacts(
      status: HistoricalEnergyStatus.fromCode(map['status']),
      usageKwh: map['usageKwh'] as String?,
      expectedDurationSeconds: map['expectedDurationSeconds'] as int,
      observedDurationSeconds: map['observedDurationSeconds'] as int,
      coverage: map['coverage'] as String?,
    );
  }
}

class HistoricalEnergyPoint extends HistoricalEnergyFacts {
  const HistoricalEnergyPoint({
    required this.measurementPointId,
    required super.status,
    required super.usageKwh,
    required super.expectedDurationSeconds,
    required super.observedDurationSeconds,
    required super.coverage,
    required this.warnings,
  });

  final String measurementPointId;
  final List<String> warnings;

  factory HistoricalEnergyPoint.fromJson(Object? value) {
    final map = _map(value, 'historical energy measurement point');
    _exactKeys(map, const {
      'measurementPointId',
      'status',
      'usageKwh',
      'expectedDurationSeconds',
      'observedDurationSeconds',
      'coverage',
      'warnings',
    });
    if (map['measurementPointId'] is! String || map['warnings'] is! List) {
      throw const FormatException(
          'Invalid historical energy measurement point');
    }
    final facts = HistoricalEnergyFacts.fromJson({
      'status': map['status'],
      'usageKwh': map['usageKwh'],
      'expectedDurationSeconds': map['expectedDurationSeconds'],
      'observedDurationSeconds': map['observedDurationSeconds'],
      'coverage': map['coverage'],
    });
    return HistoricalEnergyPoint(
      measurementPointId: map['measurementPointId'] as String,
      status: facts.status,
      usageKwh: facts.usageKwh,
      expectedDurationSeconds: facts.expectedDurationSeconds,
      observedDurationSeconds: facts.observedDurationSeconds,
      coverage: facts.coverage,
      warnings: _warnings(map['warnings']),
    );
  }
}

Map<String, Object?> _map(Object? value, String name) {
  if (value is! Map) throw FormatException('Invalid $name');
  final map = value.map((key, value) => MapEntry(key.toString(), value));
  if (map.keys.length != value.length ||
      value.keys.any((key) => key is! String)) {
    throw FormatException('Invalid $name');
  }
  return map;
}

void _exactKeys(Map<String, Object?> map, Set<String> keys) {
  if (map.length != keys.length || !map.keys.every(keys.contains)) {
    throw const FormatException('Invalid historical energy response');
  }
}

List<String> _warnings(Object? value) {
  if (value is! List) throw const FormatException('Invalid warnings');
  return List.unmodifiable(value.map((warning) {
    if (warning is! String) throw const FormatException('Invalid warning');
    return warning;
  }));
}
