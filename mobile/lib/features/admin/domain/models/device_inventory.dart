/// Presentation data for a device in the admin inventory.
///
/// This model intentionally contains no ownership or authorization context.
class DeviceInventory {
  const DeviceInventory({
    required this.name,
    required this.serialNumber,
    required this.status,
    this.lifecycleStatus = 'ACTIVE',
    this.id,
    this.macAddress,
  });

  final String name;
  final String serialNumber;
  final String status;

  /// Authoritative administrative lifecycle, distinct from online presence.
  final String lifecycleStatus;
  final String? id;
  final String? macAddress;
}
