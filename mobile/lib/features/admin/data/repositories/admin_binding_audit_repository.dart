import 'package:power_iot_app/core/network/authenticated_http_client.dart';
import 'package:power_iot_app/features/admin/domain/models/admin_binding_audit.dart';

class RemoteAdminBindingAuditRepository {
  const RemoteAdminBindingAuditRepository(this.client, this.shopId);
  final AuthenticatedHttpClient client;
  final String shopId;

  Future<AdminBindingAuditHistoryPage> load({
    String? action,
    String? measurementPointId,
    String? deviceId,
    String? cursor,
    int limit = 50,
  }) async {
    final response = await client.dio.get<Object?>(
      '/api/v1/shops/${Uri.encodeComponent(shopId)}/admin/binding-audits',
      queryParameters: <String, dynamic>{
        'limit': limit,
        if (action != null && action.isNotEmpty) 'action': action,
        if (measurementPointId != null && measurementPointId.isNotEmpty)
          'measurementPointId': measurementPointId,
        if (deviceId != null && deviceId.isNotEmpty) 'deviceId': deviceId,
        if (cursor != null && cursor.isNotEmpty) 'cursor': cursor,
      },
    );
    if (response.data is! Map) {
      throw const FormatException('Invalid admin binding audit response');
    }
    return AdminBindingAuditHistoryPage.fromJson(
      (response.data as Map).cast<String, dynamic>(),
    );
  }
}
