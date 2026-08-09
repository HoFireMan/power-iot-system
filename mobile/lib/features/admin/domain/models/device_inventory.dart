/// Presentation data for a device in the admin inventory.
///
/// This model intentionally contains no ownership or authorization context.
class DeviceInventory {
  const DeviceInventory({
    required this.name,
    required this.serialNumber,
    required this.status,
    this.id,
  });

  final String name;
  final String serialNumber;
  final String status;
  final String? id;
}
