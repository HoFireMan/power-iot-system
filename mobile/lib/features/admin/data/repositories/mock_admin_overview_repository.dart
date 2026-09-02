import '../../domain/models/admin_overview.dart';
import '../../domain/models/device_assignment.dart';
import '../../domain/models/device_inventory.dart';
import '../../domain/models/device_ref.dart';
import '../../domain/models/measurement_point.dart';
import '../../domain/repositories/admin_overview_repository.dart';

/// Deterministic development data for the admin overview and binding flow.
class MockAdminOverviewRepository implements AdminOverviewRepository {
  MockAdminOverviewRepository()
      : _measurementPoints = [
          const MeasurementPoint(
            id: '00000000-0000-4000-8000-000000000001',
            shopId: 's1',
            name: 'Main Hall',
          ),
          const MeasurementPoint(
            id: '00000000-0000-4000-8000-000000000099',
            shopId: 's1',
            name: 'Kitchen',
          ),
        ];

  final List<MeasurementPoint> _measurementPoints;
  final Map<String, MeasurementPoint> _committedCreates = {};
  final Map<String, DeviceAssignment> _committedBindings = {};
  final Map<String, String> _bindingRequests = {};
  final Map<String, DeviceAssignment> _committedReplacements = {};
  final Map<String, String> _replacementRequests = {};
  final Map<String, DeviceAssignment> _committedRelocations = {};
  final Map<String, String> _relocationRequests = {};
  final Map<String, DeviceAssignment> _committedUnbindings = {};
  final Map<String, String> _unbindRequests = {};
  final List<DeviceAssignment> _assignmentHistory = [];
  final List<DeviceAssignment> _activeAssignments = [];
  final List<DeviceInventory> _devices = const [
    DeviceInventory(
      id: 'device-001',
      name: 'Meter A',
      serialNumber: 'SN-METER-001',
      macAddress: 'AABBCCDDEE01',
      status: 'Online',
    ),
    DeviceInventory(
      id: 'device-002',
      name: 'Meter B',
      serialNumber: 'SN-METER-002',
      macAddress: 'AABBCCDDEE02',
      status: 'Standby',
    ),
  ];
  int _nextCreatedIdentity = 2;
  int _nextAssignmentIdentity = 1;
  DateTime _nextTransitionTime = DateTime.utc(2026, 8, 10, 0, 1);

  /// Overrides the next mock replacement transition boundary.
  DateTime? nextReplacementEffectiveTime;

  /// Changes the current assignment before the next replacement validation.
  bool changeCurrentAssignmentBeforeNextReplacement = false;

  /// Commits the next replacement and then simulates losing its response.
  bool loseResponseAfterNextReplacement = false;

  /// Overrides the next mock relocation transition boundary.
  DateTime? nextRelocationEffectiveTime;

  /// Changes the current assignment before the next relocation validation.
  bool changeCurrentAssignmentBeforeNextRelocation = false;

  /// Commits the next relocation and then simulates losing its response.
  bool loseResponseAfterNextRelocation = false;

  /// Overrides the next mock unbind transition boundary.
  DateTime? nextUnbindEffectiveTime;

  /// Changes the current assignment before the next unbind validation.
  bool changeCurrentAssignmentBeforeNextUnbind = false;

  /// Commits the next unbind and then simulates losing its response.
  bool loseResponseAfterNextUnbind = false;

  /// Makes only the next create call fail before mutation.
  bool failNextCreation = false;

  /// Commits the next create and then simulates losing its response.
  bool loseResponseAfterNextCreation = false;

  /// Commits the next bind and then simulates losing its response.
  bool loseResponseAfterNextBinding = false;

  @override
  Future<AdminOverview> loadOverview() async {
    return AdminOverview(
      measurementPoints: List.unmodifiable(_measurementPoints),
      devices: List.unmodifiable(_devices),
      activeAssignments: List.unmodifiable(_activeAssignments),
      assignmentHistory: List.unmodifiable(_assignmentHistory),
    );
  }

  @override
  Future<MeasurementPoint> createMeasurementPoint(
    CreateMeasurementPointInput input,
  ) async {
    final trimmedName = input.name.trim();
    if (trimmedName.isEmpty || input.name.runes.length > 100) {
      throw ArgumentError.value(input.name, 'name');
    }
    if (input.shopId.trim().isEmpty) {
      throw ArgumentError.value(input.shopId, 'shopId');
    }
    if (input.requestIdentity.trim().isEmpty) {
      throw ArgumentError.value(input.requestIdentity, 'requestIdentity');
    }

    final committed = _committedCreates[input.requestIdentity];
    if (committed != null) {
      if (committed.shopId == input.shopId && committed.name == trimmedName) {
        return committed;
      }
      throw StateError('Creation request identity was reused.');
    }

    if (failNextCreation) {
      failNextCreation = false;
      throw StateError('Deterministic mock creation failure');
    }

    final point = MeasurementPoint(
      id: '00000000-0000-4000-8000-${_nextCreatedIdentity.toString().padLeft(12, '0')}',
      shopId: input.shopId,
      name: trimmedName,
    );
    _nextCreatedIdentity++;
    _measurementPoints.add(point);
    _committedCreates[input.requestIdentity] = point;

    if (loseResponseAfterNextCreation) {
      loseResponseAfterNextCreation = false;
      throw StateError('Deterministic mock response loss after commit');
    }

    return point;
  }

  @override
  Future<List<DeviceAssignment>> loadAssignmentHistory() async {
    return List.unmodifiable(_assignmentHistory);
  }

  @override
  Future<DeviceAssignment> bindDevice(BindDeviceInput input) async {
    final requestIdentity = input.requestIdentity.trim();
    if (requestIdentity.isEmpty) {
      throw ArgumentError.value(input.requestIdentity, 'requestIdentity');
    }
    if (input.measurementPointId.trim().isEmpty) {
      throw ArgumentError.value(input.measurementPointId, 'measurementPointId');
    }

    final fingerprint = _bindingFingerprint(input);
    final committed = _committedBindings[requestIdentity];
    if (committed != null) {
      if (_bindingRequests[requestIdentity] == fingerprint) {
        return committed;
      }
      throw StateError('Binding request identity was reused.');
    }

    final point = _measurementPoints.cast<MeasurementPoint?>().firstWhere(
          (candidate) => candidate!.id == input.measurementPointId,
          orElse: () => null,
        );
    if (point == null) {
      throw StateError('Measurement Point not found.');
    }

    final device = _resolveDevice(input.deviceRef);
    if (device == null) {
      throw StateError('Device not found.');
    }
    if (device.serialNumber.trim().isEmpty ||
        !_isCanonicalMac(device.macAddress)) {
      throw StateError('Device is not eligible.');
    }
    if (_activeAssignments
        .any((assignment) => assignment.deviceId == device.id)) {
      throw StateError('Device is already assigned.');
    }
    if (_activeAssignments.any(
      (assignment) => assignment.measurementPointId == point.id,
    )) {
      throw StateError('Measurement Point is occupied.');
    }

    final assignment = DeviceAssignment(
      id: 'assignment-${_nextAssignmentIdentity.toString().padLeft(3, '0')}',
      deviceId: device.id!,
      measurementPointId: point.id,
      validFrom: DateTime.utc(2026, 8, 10),
    );
    _nextAssignmentIdentity++;
    _activeAssignments.add(assignment);
    _assignmentHistory.add(assignment);
    _committedBindings[requestIdentity] = assignment;
    _bindingRequests[requestIdentity] = fingerprint;

    if (loseResponseAfterNextBinding) {
      loseResponseAfterNextBinding = false;
      throw StateError('Deterministic mock response loss after commit');
    }

    return assignment;
  }

  @override
  Future<DeviceAssignment> replaceDevice(ReplaceDeviceInput input) async {
    final requestIdentity = input.requestIdentity.trim();
    final currentAssignmentId = input.currentAssignmentId.trim();
    if (requestIdentity.isEmpty) {
      throw ArgumentError.value(input.requestIdentity, 'requestIdentity');
    }
    if (currentAssignmentId.isEmpty) {
      throw ArgumentError.value(
        input.currentAssignmentId,
        'currentAssignmentId',
      );
    }

    final fingerprint = _replacementFingerprint(input);
    final committed = _committedReplacements[requestIdentity];
    if (committed != null) {
      if (_replacementRequests[requestIdentity] == fingerprint) {
        return committed;
      }
      throw StateError('Replacement request identity was reused.');
    }

    if (changeCurrentAssignmentBeforeNextReplacement) {
      changeCurrentAssignmentBeforeNextReplacement = false;
      _simulateCurrentAssignmentChange(currentAssignmentId);
    }

    final current = _activeAssignments.cast<DeviceAssignment?>().firstWhere(
          (assignment) => assignment!.id == currentAssignmentId,
          orElse: () => null,
        );
    if (current == null) {
      throw StateError('Current assignment is no longer current.');
    }
    final currentDevice = _devices.cast<DeviceInventory?>().firstWhere(
          (device) => device!.id == current.deviceId,
          orElse: () => null,
        );
    if (currentDevice == null) {
      throw StateError('Current Device not found.');
    }
    final replacement = _resolveDevice(input.replacementDeviceRef);
    if (replacement == null) {
      throw StateError('Replacement Device not found.');
    }
    if (replacement.serialNumber.trim().isEmpty ||
        !_isCanonicalMac(replacement.macAddress)) {
      throw StateError('Replacement Device is not eligible.');
    }
    if (replacement.id == currentDevice.id) {
      throw StateError('Replacement Device must differ from current Device.');
    }
    if (_activeAssignments.any(
      (assignment) => assignment.deviceId == replacement.id,
    )) {
      throw StateError('Replacement Device is already assigned.');
    }

    final transitionTime = _sampleTransitionTime();
    if (!transitionTime.isAfter(current.validFrom)) {
      throw StateError('Replacement transition time is invalid.');
    }

    final closed = DeviceAssignment(
      id: current.id,
      deviceId: current.deviceId,
      measurementPointId: current.measurementPointId,
      validFrom: current.validFrom,
      validTo: transitionTime,
    );
    final replacementAssignment = DeviceAssignment(
      id: 'assignment-${_nextAssignmentIdentity.toString().padLeft(3, '0')}',
      deviceId: replacement.id!,
      measurementPointId: current.measurementPointId,
      validFrom: transitionTime,
    );
    _nextAssignmentIdentity++;
    _replaceHistoryEntry(closed);
    _activeAssignments.removeWhere(
      (assignment) => assignment.id == current.id,
    );
    _activeAssignments.add(replacementAssignment);
    _assignmentHistory.add(replacementAssignment);
    _committedReplacements[requestIdentity] = replacementAssignment;
    _replacementRequests[requestIdentity] = fingerprint;

    if (loseResponseAfterNextReplacement) {
      loseResponseAfterNextReplacement = false;
      throw StateError('Deterministic mock response loss after commit');
    }

    return replacementAssignment;
  }

  @override
  Future<DeviceAssignment> unbindDevice(UnbindDeviceInput input) async {
    final requestIdentity = input.requestIdentity.trim();
    final currentAssignmentId = input.currentAssignmentId.trim();
    final reason = input.reason.trim();
    if (requestIdentity.isEmpty) {
      throw ArgumentError.value(input.requestIdentity, 'requestIdentity');
    }
    if (currentAssignmentId.isEmpty) {
      throw ArgumentError.value(
        input.currentAssignmentId,
        'currentAssignmentId',
      );
    }

    final fingerprint = _unbindFingerprint(
      currentAssignmentId: currentAssignmentId,
      reason: reason,
    );
    final committed = _committedUnbindings[requestIdentity];
    if (committed != null) {
      if (_unbindRequests[requestIdentity] == fingerprint) {
        return committed;
      }
      throw StateError('Unbind request identity was reused.');
    }

    if (changeCurrentAssignmentBeforeNextUnbind) {
      changeCurrentAssignmentBeforeNextUnbind = false;
      _simulateCurrentAssignmentChange(
        currentAssignmentId,
        effectiveTime: _sampleUnbindTime(),
      );
    }

    final current = _activeAssignments.cast<DeviceAssignment?>().firstWhere(
          (assignment) => assignment!.id == currentAssignmentId,
          orElse: () => null,
        );
    if (current == null) {
      final existsInHistory = _assignmentHistory.any(
        (assignment) => assignment.id == currentAssignmentId,
      );
      throw StateError(
        existsInHistory
            ? 'Current assignment is no longer current.'
            : 'Assignment not found.',
      );
    }

    final transitionTime = _sampleUnbindTime();
    if (!transitionTime.isAfter(current.validFrom)) {
      throw StateError('Unbind transition time is invalid.');
    }

    final closed = DeviceAssignment(
      id: current.id,
      deviceId: current.deviceId,
      measurementPointId: current.measurementPointId,
      validFrom: current.validFrom,
      validTo: transitionTime,
    );
    _replaceHistoryEntry(closed);
    _activeAssignments.removeWhere(
      (assignment) => assignment.id == current.id,
    );
    _committedUnbindings[requestIdentity] = closed;
    _unbindRequests[requestIdentity] = fingerprint;

    if (loseResponseAfterNextUnbind) {
      loseResponseAfterNextUnbind = false;
      throw StateError('Deterministic mock response loss after commit');
    }

    return closed;
  }

  @override
  Future<DeviceAssignment> relocateDevice(RelocateDeviceInput input) async {
    final requestIdentity = input.requestIdentity.trim();
    final currentAssignmentId = input.currentAssignmentId.trim();
    final targetMeasurementPointId = input.targetMeasurementPointId.trim();
    if (requestIdentity.isEmpty) {
      throw ArgumentError.value(input.requestIdentity, 'requestIdentity');
    }
    if (currentAssignmentId.isEmpty) {
      throw ArgumentError.value(
        input.currentAssignmentId,
        'currentAssignmentId',
      );
    }
    if (targetMeasurementPointId.isEmpty) {
      throw ArgumentError.value(
        input.targetMeasurementPointId,
        'targetMeasurementPointId',
      );
    }

    final fingerprint = _relocationFingerprint(input);
    final committed = _committedRelocations[requestIdentity];
    if (committed != null) {
      if (_relocationRequests[requestIdentity] == fingerprint) {
        return committed;
      }
      throw StateError('Relocation request identity was reused.');
    }

    if (changeCurrentAssignmentBeforeNextRelocation) {
      changeCurrentAssignmentBeforeNextRelocation = false;
      _simulateCurrentAssignmentChange(
        currentAssignmentId,
        effectiveTime: _sampleRelocationTime(),
      );
    }

    final current = _activeAssignments.cast<DeviceAssignment?>().firstWhere(
          (assignment) => assignment!.id == currentAssignmentId,
          orElse: () => null,
        );
    if (current == null) {
      throw StateError('Current assignment is no longer current.');
    }
    final source = _measurementPoints.cast<MeasurementPoint?>().firstWhere(
          (point) => point!.id == current.measurementPointId,
          orElse: () => null,
        );
    if (source == null) {
      throw StateError('Source Measurement Point not found.');
    }
    final target = _measurementPoints.cast<MeasurementPoint?>().firstWhere(
          (point) => point!.id == targetMeasurementPointId,
          orElse: () => null,
        );
    if (target == null) {
      throw StateError('Target Measurement Point not found.');
    }
    if (target.id == source.id) {
      throw StateError('Relocation target must differ from source.');
    }
    final currentDevice = _devices.cast<DeviceInventory?>().firstWhere(
          (device) => device!.id == current.deviceId,
          orElse: () => null,
        );
    if (currentDevice == null) {
      throw StateError('Current Device not found.');
    }
    if (_activeAssignments.any(
      (assignment) => assignment.measurementPointId == target.id,
    )) {
      throw StateError('Relocation target is already occupied.');
    }

    final transitionTime = _sampleRelocationTime();
    if (!transitionTime.isAfter(current.validFrom)) {
      throw StateError('Relocation transition time is invalid.');
    }

    final closed = DeviceAssignment(
      id: current.id,
      deviceId: current.deviceId,
      measurementPointId: current.measurementPointId,
      validFrom: current.validFrom,
      validTo: transitionTime,
    );
    final relocatedAssignment = DeviceAssignment(
      id: 'assignment-${_nextAssignmentIdentity.toString().padLeft(3, '0')}',
      deviceId: currentDevice.id!,
      measurementPointId: target.id,
      validFrom: transitionTime,
    );
    _nextAssignmentIdentity++;
    _replaceHistoryEntry(closed);
    _activeAssignments.removeWhere(
      (assignment) => assignment.id == current.id,
    );
    _activeAssignments.add(relocatedAssignment);
    _assignmentHistory.add(relocatedAssignment);
    _committedRelocations[requestIdentity] = relocatedAssignment;
    _relocationRequests[requestIdentity] = fingerprint;

    if (loseResponseAfterNextRelocation) {
      loseResponseAfterNextRelocation = false;
      throw StateError('Deterministic mock response loss after commit');
    }

    return relocatedAssignment;
  }

  DateTime _sampleTransitionTime() {
    final requested = nextReplacementEffectiveTime;
    if (requested != null) {
      nextReplacementEffectiveTime = null;
      return requested;
    }
    final sampled = _nextTransitionTime;
    _nextTransitionTime = sampled.add(const Duration(minutes: 1));
    return sampled;
  }

  DateTime _sampleRelocationTime() {
    final requested = nextRelocationEffectiveTime;
    if (requested != null) {
      nextRelocationEffectiveTime = null;
      return requested;
    }
    final sampled = _nextTransitionTime;
    _nextTransitionTime = sampled.add(const Duration(minutes: 1));
    return sampled;
  }

  DateTime _sampleUnbindTime() {
    final requested = nextUnbindEffectiveTime;
    if (requested != null) {
      nextUnbindEffectiveTime = null;
      return requested;
    }
    final sampled = _nextTransitionTime;
    _nextTransitionTime = sampled.add(const Duration(minutes: 1));
    return sampled;
  }

  void _simulateCurrentAssignmentChange(
    String currentAssignmentId, {
    DateTime? effectiveTime,
  }) {
    final current = _activeAssignments.cast<DeviceAssignment?>().firstWhere(
          (assignment) => assignment!.id == currentAssignmentId,
          orElse: () => null,
        );
    if (current == null) {
      return;
    }
    final transitionTime = effectiveTime ?? _sampleTransitionTime();
    if (!transitionTime.isAfter(current.validFrom)) {
      return;
    }
    final closed = DeviceAssignment(
      id: current.id,
      deviceId: current.deviceId,
      measurementPointId: current.measurementPointId,
      validFrom: current.validFrom,
      validTo: transitionTime,
    );
    final changed = DeviceAssignment(
      id: 'assignment-${_nextAssignmentIdentity.toString().padLeft(3, '0')}',
      deviceId: current.deviceId,
      measurementPointId: current.measurementPointId,
      validFrom: transitionTime,
    );
    _nextAssignmentIdentity++;
    _replaceHistoryEntry(closed);
    _activeAssignments.removeWhere(
      (assignment) => assignment.id == current.id,
    );
    _activeAssignments.add(changed);
    _assignmentHistory.add(changed);
  }

  void _replaceHistoryEntry(DeviceAssignment replacement) {
    final index = _assignmentHistory.indexWhere(
      (assignment) => assignment.id == replacement.id,
    );
    if (index >= 0) {
      _assignmentHistory[index] = replacement;
    }
  }

  String _replacementFingerprint(ReplaceDeviceInput input) {
    final ref = input.replacementDeviceRef;
    return '${input.currentAssignmentId}|${ref.id}|${ref.serialNumber}|${ref.macAddress}|${input.reason}';
  }

  String _relocationFingerprint(RelocateDeviceInput input) {
    return '${input.currentAssignmentId}|${input.targetMeasurementPointId}|${input.reason}';
  }

  String _unbindFingerprint({
    required String currentAssignmentId,
    required String reason,
  }) {
    return '$currentAssignmentId|$reason';
  }

  DeviceInventory? _resolveDevice(DeviceRef ref) {
    final hasIdentifier =
        ref.id != null || ref.serialNumber != null || ref.macAddress != null;
    if (!hasIdentifier) {
      throw ArgumentError('At least one Device reference is required.');
    }

    final matches = <DeviceInventory>[];
    for (final device in _devices) {
      final matchesId = ref.id == null || ref.id == device.id;
      final matchesSerial =
          ref.serialNumber == null || ref.serialNumber == device.serialNumber;
      final matchesMac =
          ref.macAddress == null || ref.macAddress == device.macAddress;
      if (matchesId && matchesSerial && matchesMac) {
        matches.add(device);
      }
    }
    if (matches.length == 1) {
      return matches.single;
    }
    if (matches.isEmpty &&
        ref.id != null &&
        _devices.any((device) => device.id == ref.id)) {
      throw StateError('Device identifiers are inconsistent.');
    }
    if (matches.isEmpty &&
        ref.serialNumber != null &&
        _devices.any((device) => device.serialNumber == ref.serialNumber)) {
      throw StateError('Device identifiers are inconsistent.');
    }
    if (matches.isEmpty &&
        ref.macAddress != null &&
        _devices.any((device) => device.macAddress == ref.macAddress)) {
      throw StateError('Device identifiers are inconsistent.');
    }
    return null;
  }

  String _bindingFingerprint(BindDeviceInput input) {
    final ref = input.deviceRef;
    return '${input.measurementPointId}|${ref.id}|${ref.serialNumber}|${ref.macAddress}';
  }

  bool _isCanonicalMac(String? value) {
    if (value == null || value.length != 12) {
      return false;
    }
    return RegExp(r'^[0-9A-F]{12}$').hasMatch(value);
  }
}
