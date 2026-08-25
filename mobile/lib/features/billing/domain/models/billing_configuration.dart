class BillingPlan {
  const BillingPlan({required this.code, this.usageClass});
  final String code;
  final String? usageClass;

  factory BillingPlan.fromJson(Object? value) {
    if (value is! Map) throw const FormatException('Invalid billing plan');
    final map = value.map((key, value) => MapEntry(key.toString(), value));
    if (map.keys.any((key) => !{'planCode', 'usageClass'}.contains(key)) ||
        !map.containsKey('planCode')) {
      throw const FormatException('Invalid billing plan');
    }
    final code = map['planCode'];
    final usageClass = map['usageClass'];
    if (code is! String ||
        code.isEmpty ||
        (usageClass != null && usageClass is! String)) {
      throw const FormatException('Invalid billing plan');
    }
    return BillingPlan(code: code, usageClass: usageClass as String?);
  }
}

class BillingAssignment {
  const BillingAssignment(
      {required this.planCode, required this.validFrom, this.validTo});
  final String planCode;
  final String validFrom;
  final String? validTo;

  factory BillingAssignment.fromJson(Object? value) {
    if (value is! Map) {
      throw const FormatException('Invalid billing assignment');
    }
    final map = value.map((key, value) => MapEntry(key.toString(), value));
    if (map.keys.any(
            (key) => !{'planCode', 'validFrom', 'validTo'}.contains(key)) ||
        !map.containsKey('planCode') ||
        !map.containsKey('validFrom')) {
      throw const FormatException('Invalid billing assignment');
    }
    if (map['planCode'] is! String ||
        map['validFrom'] is! String ||
        (map['validTo'] != null && map['validTo'] is! String)) {
      throw const FormatException('Invalid billing assignment');
    }
    return BillingAssignment(
      planCode: map['planCode'] as String,
      validFrom: map['validFrom'] as String,
      validTo: map['validTo'] as String?,
    );
  }
}

class BillingConfiguration {
  const BillingConfiguration({
    required this.shopId,
    required this.electricityTariff,
    required this.supported,
    required this.plans,
    required this.currentAssignment,
    required this.scheduledAssignment,
  });

  final String shopId;
  final String? electricityTariff;
  final bool supported;
  final List<BillingPlan> plans;
  final BillingAssignment? currentAssignment;
  final BillingAssignment? scheduledAssignment;

  factory BillingConfiguration.fromJson(Object? value) {
    if (value is! Map) {
      throw const FormatException('Invalid billing configuration');
    }
    final map = value.map((key, value) => MapEntry(key.toString(), value));
    const keys = {
      'shop',
      'supported',
      'compatiblePlans',
      'currentAssignment',
      'scheduledAssignment'
    };
    if (map.length != keys.length || !map.keys.every(keys.contains)) {
      throw const FormatException('Invalid billing configuration');
    }
    final shop = map['shop'];
    if (shop is! Map ||
        shop['id'] is! String ||
        shop['electricityTariff'] != null &&
            shop['electricityTariff'] is! String) {
      throw const FormatException('Invalid billing shop');
    }
    final plans = map['compatiblePlans'];
    if (map['supported'] is! bool || plans is! List) {
      throw const FormatException('Invalid billing configuration');
    }
    return BillingConfiguration(
      shopId: shop['id'] as String,
      electricityTariff: shop['electricityTariff'] as String?,
      supported: map['supported'] as bool,
      plans: List<BillingPlan>.unmodifiable(plans.map(BillingPlan.fromJson)),
      currentAssignment: map['currentAssignment'] == null
          ? null
          : BillingAssignment.fromJson(map['currentAssignment']),
      scheduledAssignment: map['scheduledAssignment'] == null
          ? null
          : BillingAssignment.fromJson(map['scheduledAssignment']),
    );
  }
}
