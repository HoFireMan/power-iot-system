import 'package:flutter_test/flutter_test.dart';
import 'package:power_iot_app/config/base_url.dart';

void main() {
  test('explicit POWER_IOT_BASE_URL wins in development and release', () {
    final configured = resolvePowerIoTBaseUrl(
      configuredValue: 'https://api.example.test/root',
      development: true,
    );

    expect(configured.toString(), 'https://api.example.test/root');
    expect(
      resolvePowerIoTBaseUrl(
        configuredValue: 'https://api.example.test/root',
        development: false,
      ).toString(),
      'https://api.example.test/root',
    );
  });

  test('debug/local mode uses the loopback fallback', () {
    expect(
      resolvePowerIoTBaseUrl(configuredValue: '', development: true).toString(),
      developmentBaseUrl,
    );
  });

  test('release mode fails closed without explicit configuration', () {
    expect(
      () => resolvePowerIoTBaseUrl(configuredValue: '', development: false),
      throwsA(isA<EndpointConfigurationError>()),
    );
  });

  test('malformed and unsafe endpoints fail without exposing their value', () {
    for (final value in <String>[
      'not a URL',
      'ftp://api.example.test',
      'https://api.example.test?token=secret',
      'https://user:password@api.example.test',
      'http://api.example.test',
    ]) {
      expect(
        () => resolvePowerIoTBaseUrl(
          configuredValue: value,
          development: false,
        ),
        throwsA(isA<EndpointConfigurationError>()),
        reason: value,
      );
    }
  });
}
