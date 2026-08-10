import 'device_assignment.dart';
import 'device_inventory.dart';
import 'measurement_point.dart';

/// Product data shown by the admin overview.
class AdminOverview {
  const AdminOverview({
    required this.measurementPoints,
    required this.devices,
    this.activeAssignments = const [],
  });

  final List<MeasurementPoint> measurementPoints;
  final List<DeviceInventory> devices;
  final List<DeviceAssignment> activeAssignments;
}
