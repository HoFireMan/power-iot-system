import 'dart:typed_data';

import 'package:dio/dio.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:power_iot_app/core/network/authenticated_http_client.dart';
import 'package:power_iot_app/features/reports/data/repositories/historical_energy_repository_impl.dart';

class _Adapter implements HttpClientAdapter {
  _Adapter(this.body);
  final String body;
  RequestOptions? request;

  @override
  Future<ResponseBody> fetch(
    RequestOptions options,
    Stream<Uint8List>? requestStream,
    Future<void>? cancelFuture,
  ) async {
    request = options;
    return ResponseBody.fromString(body, 200, headers: <String, List<String>>{
      Headers.contentTypeHeader: <String>[Headers.jsonContentType],
    });
  }

  @override
  void close({bool force = false}) {}
}

void main() {
  test('requests the versioned report route and month', () async {
    final adapter = _Adapter('''
      {
        "month":"2026-08", "timezone":"Asia/Taipei",
        "period":{"start":"2026-07-31T16:00:00Z","end":"2026-08-31T16:00:00Z","cutoff":"2026-08-31T16:00:00Z","snapshot":"2026-09-01T00:00:00Z"},
        "summary":{"status":"NO_DATA","usageKwh":null,"expectedDurationSeconds":3600,"observedDurationSeconds":0,"coverage":"0"},
        "measurementPoints":[], "warnings":[]
      }
    ''');
    final dio = Dio(BaseOptions(baseUrl: 'https://test.invalid'))
      ..httpClientAdapter = adapter;
    final client = AuthenticatedHttpClient(
      baseUrl: Uri.parse('https://test.invalid'),
      session: AuthSession(_Store()),
      dio: dio,
    );

    final report =
        await RemoteHistoricalEnergyRepository(client).fetch('7', '2026-08');

    expect(report.month, '2026-08');
    expect(adapter.request?.path, '/api/v1/shops/7/reports/energy');
    expect(adapter.request?.queryParameters['month'], '2026-08');
    client.close();
  });
}

class _Store implements RefreshTokenStore {
  @override
  Future<void> clear() async {}

  @override
  Future<String?> read() async => null;

  @override
  Future<void> write(String token) async {}
}
