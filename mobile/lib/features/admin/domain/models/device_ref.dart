/// Identity evidence used to resolve an existing inventory Device.
///
/// The mock binding screen intentionally supplies only [serialNumber]. When
/// more than one identifier is supplied, the repository requires all values
/// to resolve to the same existing Device.
class DeviceRef {
  const DeviceRef({
    this.id,
    this.serialNumber,
    this.macAddress,
  });

  final String? id;
  final String? serialNumber;
  final String? macAddress;
}
