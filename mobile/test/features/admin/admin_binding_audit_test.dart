import 'dart:typed_data';

import 'package:flutter_test/flutter_test.dart';
import 'package:dio/dio.dart';
import 'package:power_iot_app/core/network/authenticated_http_client.dart';
import 'package:power_iot_app/features/admin/data/repositories/admin_binding_audit_repository.dart';
import 'package:power_iot_app/features/admin/domain/models/admin_binding_audit.dart';

class _Adapter implements HttpClientAdapter {
  RequestOptions? request;
  @override
  Future<ResponseBody> fetch(RequestOptions options, Stream<Uint8List>? stream,
      Future<void>? cancelFuture) async {
    request = options;
    return ResponseBody.fromString(
      '{"items":[{"id":"a-1","operationId":"op-1","action":"relocate","occurredAt":"2026-01-02T00:00:00Z","effectiveAt":null,"reason":"move","actor":{"id":"4","currentDisplayName":"Admin"},"measurementPoint":null,"device":{"id":"8","serialNumber":"SERIAL","mac":"AABBCCDDEEFF","currentDisplayName":"Meter"},"oldMeasurementPoint":{"id":"old","currentDisplayName":"Old name"},"newMeasurementPoint":{"id":"new","currentDisplayName":"New name"},"oldAssignmentId":"before","newAssignmentId":"after"}],"nextCursor":"cursor"}',
      200,
      headers: {
        Headers.contentTypeHeader: [Headers.jsonContentType]
      },
    );
  }

  @override
  void close({bool force = false}) {}
}

class _MemoryStore implements RefreshTokenStore {
  @override
  Future<String?> read() async => null;
  @override
  Future<void> write(String token) async {}
  @override
  Future<void> clear() async {}
}

void main() {
  test('parses historical identifiers separately from current names', () {
    final page = AdminBindingAuditHistoryPage.fromJson({
      'items': [
        {
          'id': 'a-1',
          'operationId': 'op-1',
          'action': 'bind',
          'occurredAt': '2026-01-01T00:00:00Z',
          'actor': {'id': '1'},
        }
      ]
    });
    expect(page.items.single.id, 'a-1');
    expect(page.items.single.action, 'bind');
    expect(page.nextCursor, isNull);
  });

  test('parses all five action projection shapes without inventing references', () {
    final actions = <String, Map<String, dynamic>>{
      'create_measurement_point': {
        'measurementPoint': {'id': 'mp-create'},
      },
      'bind': {
        'device': {'id': 'device-bind'},
        'newMeasurementPoint': {'id': 'mp-bind'},
      },
      'replace': {
        'device': {'id': 'device-replacement'},
        'oldMeasurementPoint': {'id': 'mp-replace'},
        'newMeasurementPoint': {'id': 'mp-replace'},
      },
      'relocate': {
        'device': {'id': 'device-relocate'},
        'oldMeasurementPoint': {'id': 'mp-old'},
        'newMeasurementPoint': {'id': 'mp-new'},
      },
      'unbind': {
        'device': {'id': 'device-unbind'},
        'oldMeasurementPoint': {'id': 'mp-unbind'},
      },
    };
    for (final entry in actions.entries) {
      final audit = AdminBindingAudit.fromJson({
        'id': 'audit-${entry.key}',
        'operationId': 'operation-${entry.key}',
        'action': entry.key,
        'occurredAt': '2026-01-01T00:00:00Z',
        'reason': null,
        'actor': {'id': 'actor-1'},
        ...entry.value,
      });
      expect(audit.operationId, startsWith('operation-'));
      expect(audit.reason, isNull);
      if (entry.key == 'replace') {
        expect(audit.oldMeasurementPoint!.id, audit.newMeasurementPoint!.id);
      }
    }
  });

  test('repository sends Shop and exact server filters', () async {
    final adapter = _Adapter();
    final dio = Dio(BaseOptions(baseUrl: 'https://invalid'))
      ..httpClientAdapter = adapter;
    final client = AuthenticatedHttpClient(
      baseUrl: Uri.parse('https://invalid'),
      session: AuthSession(_MemoryStore()),
      dio: dio,
    );
    final page = await RemoteAdminBindingAuditRepository(client, '2').load(
      action: 'relocate',
      measurementPointId: 'new',
      deviceId: '8',
      cursor: 'before',
      limit: 20,
    );
    expect(page.items.single.oldMeasurementPoint!.currentDisplayName, 'Old name');
    expect(adapter.request!.path, '/api/v1/shops/2/admin/binding-audits');
    expect(adapter.request!.queryParameters['action'], 'relocate');
    expect(adapter.request!.queryParameters['measurementPointId'], 'new');
    expect(adapter.request!.queryParameters['deviceId'], '8');
    expect(adapter.request!.queryParameters['cursor'], 'before');
  });
}
