import '../models/admin_overview.dart';
import '../models/device_assignment.dart';
import '../models/device_ref.dart';
import '../models/measurement_point.dart';

/// Product input for binding an existing eligible Device to an existing empty MP.
class BindDeviceInput {
  const BindDeviceInput({
    required this.requestIdentity,
    required this.deviceRef,
    required this.measurementPointId,
  });

  final String requestIdentity;
  final DeviceRef deviceRef;
  final String measurementPointId;
}

/// Product input for replacing the physical Device at a current assignment.
class ReplaceDeviceInput {
  const ReplaceDeviceInput({
    required this.requestIdentity,
    required this.currentAssignmentId,
    required this.replacementDeviceRef,
    this.reason = '',
  });

  final String requestIdentity;
  final String currentAssignmentId;
  final DeviceRef replacementDeviceRef;
  final String reason;
}

/// Product input for relocating a currently assigned Device to an existing
/// unoccupied Measurement Point.
class RelocateDeviceInput {
  const RelocateDeviceInput({
    required this.requestIdentity,
    required this.currentAssignmentId,
    required this.targetMeasurementPointId,
    this.reason = '',
  });

  final String requestIdentity;
  final String currentAssignmentId;
  final String targetMeasurementPointId;
  final String reason;
}

/// Product input for creating a logical Measurement Point in the current Site.
class CreateMeasurementPointInput {
  const CreateMeasurementPointInput({
    required this.requestIdentity,
    required this.shopId,
    required this.name,
  });

  final String requestIdentity;
  final String shopId;
  final String name;
}

/// Product-data boundary for the admin overview.
abstract interface class AdminOverviewRepository {
  Future<AdminOverview> loadOverview();

  Future<MeasurementPoint> createMeasurementPoint(
    CreateMeasurementPointInput input,
  );

  Future<DeviceAssignment> bindDevice(BindDeviceInput input);

  Future<DeviceAssignment> replaceDevice(ReplaceDeviceInput input);

  Future<DeviceAssignment> relocateDevice(RelocateDeviceInput input);

  Future<List<DeviceAssignment>> loadAssignmentHistory();
}
