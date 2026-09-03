import 'package:flutter/foundation.dart';

const powerIoTBaseUrl = String.fromEnvironment('POWER_IOT_BASE_URL');
const developmentBaseUrl = 'http://localhost:8080';

/// A configuration failure that intentionally omits the configured value.
final class EndpointConfigurationError implements Exception {
  const EndpointConfigurationError(this.message);

  final String message;

  @override
  String toString() => 'EndpointConfigurationError: $message';
}

/// Resolves the API endpoint at the application composition seam.
///
/// Explicit configuration always wins. The loopback default exists only for
/// debug/local execution; release execution must provide POWER_IOT_BASE_URL.
Uri resolvePowerIoTBaseUrl({String? configuredValue, bool? development}) {
  final raw = (configuredValue ?? powerIoTBaseUrl).trim();
  final isDevelopment = development ?? kDebugMode;
  if (raw.isEmpty) {
    if (isDevelopment) return Uri.parse(developmentBaseUrl);
    throw const EndpointConfigurationError(
      'POWER_IOT_BASE_URL is required outside debug/local development',
    );
  }

  final parsed = Uri.tryParse(raw);
  if (parsed == null) {
    throw const EndpointConfigurationError('POWER_IOT_BASE_URL is malformed');
  }
  try {
    validatePowerIoTBaseUrl(parsed);
  } on ArgumentError {
    throw const EndpointConfigurationError('POWER_IOT_BASE_URL is invalid');
  }
  return parsed;
}

/// Validates an endpoint without logging or returning its configured value.
void validatePowerIoTBaseUrl(Uri value) {
  if (!value.hasScheme || value.host.isEmpty) {
    throw ArgumentError('base URL must be absolute');
  }
  if (value.scheme != 'https' && value.scheme != 'http') {
    throw ArgumentError('base URL must use HTTP or HTTPS');
  }
  if (value.userInfo.isNotEmpty || value.hasQuery || value.hasFragment) {
    throw ArgumentError(
        'base URL must not contain credentials, query, or fragment');
  }
  if (value.scheme == 'http' &&
      value.host != 'localhost' &&
      value.host != '127.0.0.1' &&
      value.host != '10.0.2.2' &&
      value.host != '::1') {
    throw ArgumentError('cleartext base URL is local-development only');
  }
}
