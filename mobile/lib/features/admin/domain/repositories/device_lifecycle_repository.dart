import 'admin_overview_repository.dart';

/// Explicit capability for administrative Device lifecycle commands.
abstract interface class DeviceLifecycleRepository {
  Future<void> disableDevice(DeviceLifecycleInput input);
  Future<void> enableDevice(DeviceLifecycleInput input);
  Future<void> retireDevice(DeviceLifecycleInput input);
}
