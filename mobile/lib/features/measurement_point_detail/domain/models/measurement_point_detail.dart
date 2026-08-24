class MeasurementPointDetail {
  const MeasurementPointDetail({
    required this.shop,
    required this.measurementPoint,
    required this.currentPower,
    required this.todayEnergy,
    required this.monthEnergy,
    required this.currentDevice,
    required this.assignmentHistory,
    required this.technicalInfo,
  });

  final MeasurementPointDetailShop shop;
  final MeasurementPointIdentity measurementPoint;
  final MeasurementPointCurrentPower currentPower;
  final MeasurementPointEnergyWindow todayEnergy;
  final MeasurementPointEnergyWindow monthEnergy;
  final MeasurementPointDetailDevice? currentDevice;
  final List<MeasurementPointAssignment> assignmentHistory;
  final MeasurementPointTechnicalInfo? technicalInfo;

  // Compatibility accessors for the pre-detail DTO seam.
  double? get currentPowerW => currentPower.watts;
  MeasurementPointEnergyWindow get today => todayEnergy;
  MeasurementPointEnergyWindow get month => monthEnergy;
}

class MeasurementPointIdentity {
  const MeasurementPointIdentity({required this.name, required this.status});
  final String name;
  final String status;
}

class MeasurementPointDetailShop {
  const MeasurementPointDetailShop({required this.code, required this.name});
  final String code;
  final String name;
}

class MeasurementPointCurrentPower {
  const MeasurementPointCurrentPower(
      {required this.watts, required this.lastUpdatedAt});
  final double? watts;
  final DateTime? lastUpdatedAt;
}

class MeasurementPointDetailDevice {
  const MeasurementPointDetailDevice({
    required this.displayName,
    required this.mac,
    required this.lastSeen,
  });
  final String displayName;
  final String mac;
  final DateTime? lastSeen;
}

class MeasurementPointAssignment {
  const MeasurementPointAssignment({
    required this.displayName,
    required this.mac,
    required this.validFrom,
    required this.validTo,
  });
  final String displayName;
  final String mac;
  final DateTime validFrom;
  final DateTime? validTo;
}

class MeasurementPointTechnicalInfo {
  const MeasurementPointTechnicalInfo({
    required this.measurementPointId,
    required this.deviceId,
  });
  final String measurementPointId;
  final String? deviceId;
}

class MeasurementPointEnergyWindow {
  const MeasurementPointEnergyWindow({
    required this.kwh,
    required this.completeThrough,
    this.state = '',
  });
  final double? kwh;
  final DateTime? completeThrough;
  final String state;
}
