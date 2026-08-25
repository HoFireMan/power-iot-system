enum BillingEstimateStatus {
  completeEstimate('COMPLETE_ESTIMATE'),
  partialDataEstimate('PARTIAL_DATA_ESTIMATE'),
  noData('NO_DATA'),
  configurationRequired('CONFIGURATION_REQUIRED'),
  unsupportedTariff('UNSUPPORTED_TARIFF'),
  unsupportedPeriod('UNSUPPORTED_PERIOD'),
  rateNotFound('RATE_NOT_FOUND');

  const BillingEstimateStatus(this.code);
  final String code;

  static BillingEstimateStatus fromCode(Object? value) {
    if (value is! String) {
      throw const FormatException('Invalid estimate status');
    }
    return BillingEstimateStatus.values.firstWhere(
      (status) => status.code == value,
      orElse: () => throw const FormatException('Invalid estimate status'),
    );
  }
}

class BillingEstimate {
  const BillingEstimate({
    required this.status,
    required this.month,
    required this.period,
    required this.shop,
    required this.tariff,
    required this.rateSet,
    required this.energy,
    required this.tiers,
    required this.charges,
    required this.warnings,
  });

  final BillingEstimateStatus status;
  final String month;
  final BillingEstimatePeriod period;
  final BillingEstimateShop shop;
  final BillingEstimateTariff tariff;
  final BillingEstimateRateSet rateSet;
  final BillingEstimateEnergy energy;
  final List<BillingEstimateTier> tiers;
  final BillingEstimateCharges? charges;
  final List<String> warnings;

  bool get hasAmount =>
      charges != null &&
      (status == BillingEstimateStatus.completeEstimate ||
          status == BillingEstimateStatus.partialDataEstimate);

  factory BillingEstimate.fromJson(Object? value) {
    if (value is! Map) throw const FormatException('Invalid billing estimate');
    final map = value.map((key, value) => MapEntry(key.toString(), value));
    const keys = {
      'status',
      'month',
      'period',
      'shop',
      'tariff',
      'rateSet',
      'energy',
      'tiers',
      'charges',
      'warnings',
    };
    if (map.length != keys.length ||
        !map.keys.every(keys.contains) ||
        map['month'] is! String ||
        map['tiers'] is! List ||
        map['warnings'] is! List) {
      throw const FormatException('Invalid billing estimate');
    }
    return BillingEstimate(
      status: BillingEstimateStatus.fromCode(map['status']),
      month: map['month'] as String,
      period: BillingEstimatePeriod.fromJson(map['period']),
      shop: BillingEstimateShop.fromJson(map['shop']),
      tariff: BillingEstimateTariff.fromJson(map['tariff']),
      rateSet: BillingEstimateRateSet.fromJson(map['rateSet']),
      energy: BillingEstimateEnergy.fromJson(map['energy']),
      tiers: List.unmodifiable(
        (map['tiers'] as List).map(BillingEstimateTier.fromJson),
      ),
      charges: map['charges'] == null
          ? null
          : BillingEstimateCharges.fromJson(map['charges']),
      warnings: List.unmodifiable(
        (map['warnings'] as List).map((value) {
          if (value is! String) throw const FormatException('Invalid warning');
          return value;
        }),
      ),
    );
  }
}

Map<String, Object?> _map(Object? value, String name) {
  if (value is! Map) throw FormatException('Invalid $name');
  return value.map((key, value) => MapEntry(key.toString(), value));
}

class BillingEstimatePeriod {
  const BillingEstimatePeriod({
    required this.start,
    required this.end,
    required this.timezone,
  });
  final String? start;
  final String? end;
  final String timezone;

  factory BillingEstimatePeriod.fromJson(Object? value) {
    final map = _map(value, 'period');
    if (map['start'] != null && map['start'] is! String ||
        map['end'] != null && map['end'] is! String ||
        map['timezone'] is! String) {
      throw const FormatException('Invalid estimate period');
    }
    return BillingEstimatePeriod(
      start: map['start'] as String?,
      end: map['end'] as String?,
      timezone: map['timezone'] as String,
    );
  }
}

class BillingEstimateShop {
  const BillingEstimateShop({
    required this.id,
    required this.code,
    required this.name,
  });
  final String id;
  final String code;
  final String name;

  factory BillingEstimateShop.fromJson(Object? value) {
    final map = _map(value, 'shop');
    if (map['id'] is! String ||
        map['code'] is! String ||
        map['name'] is! String) {
      throw const FormatException('Invalid estimate shop');
    }
    return BillingEstimateShop(
      id: map['id'] as String,
      code: map['code'] as String,
      name: map['name'] as String,
    );
  }
}

class BillingEstimateTariff {
  const BillingEstimateTariff({
    required this.electricityTariff,
    required this.planCode,
    required this.usageClass,
    required this.season,
  });
  final String electricityTariff;
  final String planCode;
  final String? usageClass;
  final String season;

  factory BillingEstimateTariff.fromJson(Object? value) {
    final map = _map(value, 'tariff');
    if (map['electricityTariff'] is! String ||
        map['planCode'] is! String ||
        map['season'] is! String ||
        map['usageClass'] != null && map['usageClass'] is! String) {
      throw const FormatException('Invalid estimate tariff');
    }
    return BillingEstimateTariff(
      electricityTariff: map['electricityTariff'] as String,
      planCode: map['planCode'] as String,
      usageClass: map['usageClass'] as String?,
      season: map['season'] as String,
    );
  }
}

class BillingEstimateRateSet {
  const BillingEstimateRateSet({
    required this.version,
    required this.currency,
    required this.includesTax,
  });
  final String version;
  final String currency;
  final bool includesTax;

  factory BillingEstimateRateSet.fromJson(Object? value) {
    final map = _map(value, 'rate set');
    if (map['version'] is! String ||
        map['currency'] is! String ||
        map['includesTax'] is! bool) {
      throw const FormatException('Invalid estimate rate set');
    }
    return BillingEstimateRateSet(
      version: map['version'] as String,
      currency: map['currency'] as String,
      includesTax: map['includesTax'] as bool,
    );
  }
}

class BillingEstimateEnergy {
  const BillingEstimateEnergy({
    required this.usageKwh,
    required this.expectedDurationSeconds,
    required this.observedDurationSeconds,
    required this.coverage,
  });
  final String? usageKwh;
  final int expectedDurationSeconds;
  final int observedDurationSeconds;
  final String? coverage;

  factory BillingEstimateEnergy.fromJson(Object? value) {
    final map = _map(value, 'energy');
    if (map['usageKwh'] != null && map['usageKwh'] is! String ||
        map['coverage'] != null && map['coverage'] is! String ||
        map['expectedDurationSeconds'] is! int ||
        map['observedDurationSeconds'] is! int) {
      throw const FormatException('Invalid estimate energy');
    }
    return BillingEstimateEnergy(
      usageKwh: map['usageKwh'] as String?,
      expectedDurationSeconds: map['expectedDurationSeconds'] as int,
      observedDurationSeconds: map['observedDurationSeconds'] as int,
      coverage: map['coverage'] as String?,
    );
  }
}

class BillingEstimateTier {
  const BillingEstimateTier({
    required this.fromKwh,
    required this.toKwh,
    required this.usageKwh,
    required this.ratePerKwh,
    required this.subtotal,
  });
  final String fromKwh;
  final String? toKwh;
  final String usageKwh;
  final String ratePerKwh;
  final String subtotal;

  factory BillingEstimateTier.fromJson(Object? value) {
    final map = _map(value, 'tier');
    if (map['fromKwh'] is! String ||
        map['usageKwh'] is! String ||
        map['ratePerKwh'] is! String ||
        map['subtotal'] is! String ||
        map['toKwh'] != null && map['toKwh'] is! String) {
      throw const FormatException('Invalid estimate tier');
    }
    return BillingEstimateTier(
      fromKwh: map['fromKwh'] as String,
      toKwh: map['toKwh'] as String?,
      usageKwh: map['usageKwh'] as String,
      ratePerKwh: map['ratePerKwh'] as String,
      subtotal: map['subtotal'] as String,
    );
  }
}

class BillingEstimateCharges {
  const BillingEstimateCharges({
    required this.energyCharge,
    required this.minimumMonthlyCharge,
    required this.minimumChargeAdjustment,
    required this.estimatedTotal,
  });
  final String energyCharge;
  final String minimumMonthlyCharge;
  final String minimumChargeAdjustment;
  final String estimatedTotal;

  factory BillingEstimateCharges.fromJson(Object? value) {
    final map = _map(value, 'charges');
    if (map['energyCharge'] is! String ||
        map['minimumMonthlyCharge'] is! String ||
        map['minimumChargeAdjustment'] is! String ||
        map['estimatedTotal'] is! String) {
      throw const FormatException('Invalid estimate charges');
    }
    return BillingEstimateCharges(
      energyCharge: map['energyCharge'] as String,
      minimumMonthlyCharge: map['minimumMonthlyCharge'] as String,
      minimumChargeAdjustment: map['minimumChargeAdjustment'] as String,
      estimatedTotal: map['estimatedTotal'] as String,
    );
  }
}
