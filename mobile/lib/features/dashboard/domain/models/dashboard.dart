/// The authoritative dashboard projection returned by the B7 endpoint.
class Dashboard {
  const Dashboard({
    required this.shop,
    required this.generatedAt,
    required this.currentPowerW,
    required this.dailyKwh,
    required this.monthlyKwh,
    required this.dailyKg,
    required this.monthlyKg,
    required this.devices,
  });

  final DashboardShop shop;
  final DateTime generatedAt;
  final double? currentPowerW;
  final double? dailyKwh;
  final double? monthlyKwh;
  final double? dailyKg;
  final double? monthlyKg;
  final List<DashboardDevice> devices;
}

class DashboardShop {
  const DashboardShop({
    required this.id,
    required this.code,
    required this.name,
  });

  final String id;
  final String code;
  final String name;
}

class DashboardDevice {
  const DashboardDevice({
    required this.measurementPointRef,
    required this.name,
    required this.isOnline,
    required this.lastSeen,
  });

  /// Opaque public resource locator for the shared Measurement Point detail.
  final String measurementPointRef;
  final String name;
  final bool isOnline;
  final DateTime? lastSeen;
}
