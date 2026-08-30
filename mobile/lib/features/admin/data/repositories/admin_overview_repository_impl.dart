import 'package:dio/dio.dart';
import 'package:power_iot_app/core/network/authenticated_http_client.dart';
import 'package:power_iot_app/features/admin/data/dtos/admin_overview_dto.dart';
import 'package:power_iot_app/features/admin/domain/models/admin_overview.dart';
import 'package:power_iot_app/features/admin/domain/models/device_assignment.dart';
import 'package:power_iot_app/features/admin/domain/models/device_ref.dart';
import 'package:power_iot_app/features/admin/domain/models/measurement_point.dart';
import 'package:power_iot_app/features/admin/domain/repositories/admin_overview_repository.dart';
import 'package:power_iot_app/core/network/remote_error.dart';

class RemoteAdminOverviewRepository implements AdminOverviewRepository {
  const RemoteAdminOverviewRepository(this.client, this.shopId);
  final AuthenticatedHttpClient client;
  final String shopId;

  @override
  Future<AdminOverview> loadOverview() async {
    final response = await client.dio.get<Object?>(
        '/api/v1/admin/device-bindings',
        queryParameters: {'shopId': shopId});
    return AdminOverviewDto.fromJson(response.data).toModel();
  }

  @override
  Future<MeasurementPoint> createMeasurementPoint(
      CreateMeasurementPointInput input) async {
    final body = await _post(
        '/api/v1/admin/measurement-points',
        {'shopId': _positive(input.shopId), 'name': input.name},
        input.requestIdentity,
        action: 'create_measurement_point',
        keys: const {'operationId', 'action', 'measurementPointId'});
    final id = _requiredString(body, 'measurementPointId');
    return MeasurementPoint(
        id: id, shopId: input.shopId, name: input.name.trim());
  }

  @override
  Future<DeviceAssignment> bindDevice(BindDeviceInput input) async {
    final body = await _post(
        '/api/v1/admin/device-bindings',
        {
          'deviceRef': _ref(input.deviceRef),
          'measurementPointId': input.measurementPointId
        },
        input.requestIdentity,
        action: 'bind',
        keys: const {
          'operationId',
          'action',
          'deviceId',
          'newMeasurementPointId',
          'newAssignmentId',
          'effectiveAt'
        });
    return _assignment(body,
        deviceKey: 'deviceId',
        pointKey: 'newMeasurementPointId',
        assignmentKey: 'newAssignmentId');
  }

  @override
  Future<DeviceAssignment> replaceDevice(ReplaceDeviceInput input) async {
    final body = await _post(
        '/api/v1/admin/device-bindings/${Uri.encodeComponent(input.currentAssignmentId)}/replace',
        {
          'replacementDeviceRef': _ref(input.replacementDeviceRef),
          'reason': input.reason
        },
        input.requestIdentity,
        action: 'replace',
        keys: const {
          'operationId',
          'action',
          'deviceId',
          'replacementDeviceId',
          'oldMeasurementPointId',
          'newMeasurementPointId',
          'oldAssignmentId',
          'newAssignmentId',
          'effectiveAt'
        });
    return _assignment(body,
        deviceKey: 'replacementDeviceId',
        pointKey: 'newMeasurementPointId',
        assignmentKey: 'newAssignmentId');
  }

  @override
  Future<DeviceAssignment> relocateDevice(RelocateDeviceInput input) async {
    final body = await _post(
        '/api/v1/admin/device-bindings/${Uri.encodeComponent(input.currentAssignmentId)}/relocate',
        {
          'targetMeasurementPointId': input.targetMeasurementPointId,
          'reason': input.reason
        },
        input.requestIdentity,
        action: 'relocate',
        keys: const {
          'operationId',
          'action',
          'deviceId',
          'oldMeasurementPointId',
          'newMeasurementPointId',
          'oldAssignmentId',
          'newAssignmentId',
          'effectiveAt'
        });
    return _assignment(body,
        deviceKey: 'deviceId',
        pointKey: 'newMeasurementPointId',
        assignmentKey: 'newAssignmentId');
  }

  @override
  Future<DeviceAssignment> unbindDevice(UnbindDeviceInput input) async {
    // The response's effectiveAt is only the closing boundary. Read the
    // authoritative assignment interval first so it cannot become both ends
    // of the presentation object.
    final overview = await loadOverview();
    DeviceAssignment? current;
    for (final assignment in overview.assignmentHistory) {
      if (assignment.id == input.currentAssignmentId) {
        current = assignment;
        break;
      }
    }
    if (current == null) {
      throw const FormatException('Current assignment is unavailable');
    }
    final body = await _post(
        '/api/v1/admin/device-bindings/${Uri.encodeComponent(input.currentAssignmentId)}/unbind',
        {'reason': input.reason},
        input.requestIdentity,
        action: 'unbind',
        keys: const {
          'operationId',
          'action',
          'deviceId',
          'oldMeasurementPointId',
          'oldAssignmentId',
          'effectiveAt'
        });
    final result = _assignment(body,
        deviceKey: 'deviceId',
        pointKey: 'oldMeasurementPointId',
        assignmentKey: 'oldAssignmentId',
        closed: true);
    return DeviceAssignment(
        id: result.id,
        deviceId: result.deviceId,
        measurementPointId: result.measurementPointId,
        validFrom: current.validFrom,
        validTo: result.validTo);
  }

  @override
  Future<List<DeviceAssignment>> loadAssignmentHistory() async =>
      (await loadOverview()).assignmentHistory;

  Future<Map<String, Object?>> _post(
      String path, Map<String, Object?> data, String identity,
      {required String action, required Set<String> keys}) async {
    final response = await client.dio.post<Object?>(path,
        data: data, options: Options(headers: {'Idempotency-Key': identity}));
    final value = response.data;
    if (value is! Map || value.keys.any((k) => k is! String)) {
      throw const FormatException('Invalid admin binding result');
    }
    final result = value.cast<String, Object?>();
    if (result.length != keys.length || !result.keys.every(keys.contains)) {
      throw const FormatException('Invalid admin binding result');
    }
    if (_requiredString(result, 'action') != action ||
        _requiredString(result, 'operationId').trim().isEmpty) {
      throw const FormatException('Invalid admin binding result');
    }
    return result;
  }

  Map<String, Object?> _ref(DeviceRef ref) => <String, Object?>{
        'deviceId': ref.id == null ? null : _positive(ref.id!),
        'serialNumber': ref.serialNumber,
        'mac': ref.macAddress
      };
  DeviceAssignment _assignment(Map<String, Object?> m,
      {required String deviceKey,
      required String pointKey,
      required String assignmentKey,
      bool closed = false}) {
    final assignment = _requiredString(m, assignmentKey),
        device = _requiredString(m, deviceKey),
        point = _requiredString(m, pointKey);
    final effective = _date(m, 'effectiveAt');
    return DeviceAssignment(
        id: assignment,
        deviceId: device,
        measurementPointId: point,
        validFrom: effective,
        validTo: closed ? effective : null);
  }
}

/// Converts transport/domain outcomes into safe, actionable UI categories.
/// Server messages are deliberately never surfaced to users.
String adminErrorMessage(Object error, String fallback) {
  if (error is UnauthorizedException) {
    return 'Authorization required. Please sign in again.';
  }
  if (error is DioException) {
    final code = error.response?.data is Map
        ? (error.response!.data as Map)['code']
        : null;
    if (error.response?.statusCode == 401 || code == 'UNAUTHORIZED') {
      return 'Authorization required. Please sign in again.';
    }
    if (error.response?.statusCode == 403 || code == 'FORBIDDEN') {
      return 'You are not authorized for this shop.';
    }
    if (code == 'VALIDATION_ERROR') {
      return 'Please check the submitted values.';
    }
    if (code == 'RESOURCE_NOT_FOUND' || code == 'SHOP_NOT_FOUND') {
      return 'The requested resource was not found or is outside your scope.';
    }
    if (code == 'CONFLICT') {
      return 'This operation conflicts with the current state.';
    }
    if (error.response == null) {
      return 'Network unavailable. Check your connection and retry.';
    }
    if ((error.response?.statusCode ?? 0) >= 500) {
      return 'Server unavailable. Please retry.';
    }
  }
  if (error is FormatException) {
    return 'Server returned an invalid response. Please retry.';
  }
  return fallback;
}

String _requiredString(Map<String, Object?> m, String key) {
  final v = m[key];
  if (v is! String || v.trim().isEmpty) {
    throw const FormatException('Invalid admin binding result');
  }
  return v;
}

DateTime _date(Map<String, Object?> m, String key) {
  try {
    return parseAdminDate(m[key]);
  } on FormatException {
    throw const FormatException('Invalid admin binding result');
  }
}

int _positive(String value) {
  final parsed = int.tryParse(value);
  if (parsed == null || parsed <= 0) {
    throw ArgumentError.value(value);
  }
  return parsed;
}
