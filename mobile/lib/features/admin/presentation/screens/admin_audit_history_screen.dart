import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:power_iot_app/features/admin/data/repositories/admin_binding_audit_repository.dart';
import 'package:power_iot_app/features/admin/domain/models/admin_binding_audit.dart';
import 'package:power_iot_app/features/admin/presentation/providers/admin_binding_audit_history_provider.dart';
import 'package:power_iot_app/features/admin/presentation/providers/admin_overview_provider.dart';
import 'package:power_iot_app/features/auth/auth_controller.dart';
import 'package:power_iot_app/features/shops/providers/remote_shop_provider.dart';

const adminBindingAuditActions = <String, String>{
  '': 'All actions',
  'create_measurement_point': 'Create Measurement Point',
  'bind': 'Bind',
  'replace': 'Replace',
  'relocate': 'Relocate',
  'unbind': 'Unbind',
};

class AdminAuditHistoryScreen extends ConsumerStatefulWidget {
  const AdminAuditHistoryScreen({super.key});
  @override
  ConsumerState<AdminAuditHistoryScreen> createState() =>
      _AdminAuditHistoryScreenState();
}

class _AdminAuditHistoryScreenState
    extends ConsumerState<AdminAuditHistoryScreen> {
  String _action = '';
  String _draftMeasurementPointId = '';
  String _draftDeviceId = '';
  String _appliedMeasurementPointId = '';
  String _appliedDeviceId = '';
  String? _shopKey;
  String? _cursor;
  int? _cursorGeneration;
  final List<AdminBindingAudit> _more = [];
  bool _loadingMore = false;
  int _paginationGeneration = 0;

  AdminAuditHistoryQuery _query(String shopId) => AdminAuditHistoryQuery(
        shopId,
        _action,
        _appliedMeasurementPointId,
        _appliedDeviceId,
      );

  void _resetPagination() {
    setState(() {
      _paginationGeneration++;
      _more.clear();
      _cursor = null;
      _loadingMore = false;
    });
  }

  void _applyFilters() {
    setState(() {
      _paginationGeneration++;
      _appliedMeasurementPointId = _draftMeasurementPointId.trim();
      _appliedDeviceId = _draftDeviceId.trim();
      _more.clear();
      _cursor = null;
      _loadingMore = false;
    });
  }

  Future<void> _loadMore(AdminAuditHistoryQuery query) async {
    if (_loadingMore || _cursor == null) return;
    final generation = _paginationGeneration;
    final cursor = _cursor;
    setState(() => _loadingMore = true);
    try {
      final page = await RemoteAdminBindingAuditRepository(
        ref.read(authClientProvider),
        query.shopId,
      ).load(
        action: query.action,
        measurementPointId: query.measurementPointId,
        deviceId: query.deviceId,
        cursor: cursor,
      );
      if (!mounted) {
        return;
      }
      // The request is only applied while the same Shop/filter query remains
      // visible. This prevents a late page from leaking into a switched view.
      if (_paginationGeneration != generation ||
          _shopKey != query.shopId ||
          query != _query(query.shopId)) {
        return;
      }
      setState(() {
        _more.addAll(page.items);
        _cursor = page.nextCursor;
      });
    } catch (_) {
      if (mounted && _paginationGeneration == generation) {
        ScaffoldMessenger.of(context).showSnackBar(
          const SnackBar(content: Text('Unable to load more audit history.')),
        );
      }
    } finally {
      if (mounted && _paginationGeneration == generation) {
        setState(() => _loadingMore = false);
      }
    }
  }

  @override
  Widget build(BuildContext context) {
    final shops = ref.watch(shopsProvider);
    final shopId = selectedAdminShopId(shops);
    if (_shopKey != shopId) {
      // This is deliberately local presentation state. The provider family
      // below owns request identity, so old in-flight results are discarded.
      _shopKey = shopId;
      _paginationGeneration++;
      _cursor = null;
      _more.clear();
      _loadingMore = false;
    }
    if (!ref.watch(authControllerProvider).isAuthenticated || shopId == null) {
      return const Scaffold(
        body: Center(child: Text('No authorized Shop is available.')),
      );
    }
    final query = _query(shopId);
    final state = ref.watch(adminAuditHistoryQueryProvider(query));
    return Scaffold(
      appBar: AppBar(title: const Text('Admin Audit History')),
      body: state.when(
        loading: () => const Center(child: Text('Loading audit history…')),
        error: (error, _) => Center(
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              const Text('Unable to load audit history. Please retry.'),
              const SizedBox(height: 12),
              OutlinedButton(
                onPressed: () =>
                    ref.invalidate(adminAuditHistoryQueryProvider(query)),
                child: const Text('Retry'),
              ),
            ],
          ),
        ),
        data: (page) {
          if (_cursorGeneration != _paginationGeneration) {
            _cursorGeneration = _paginationGeneration;
            _cursor = page.nextCursor;
          }
          final items = [...page.items, ..._more];
          return Column(
            children: [
              _AuditFilters(
                action: _action,
                measurementPointId: _draftMeasurementPointId,
                deviceId: _draftDeviceId,
                onAction: (value) {
                  setState(() => _action = value ?? '');
                  _resetPagination();
                },
                onMeasurementPointId: (value) =>
                    setState(() => _draftMeasurementPointId = value),
                onDeviceId: (value) => setState(() => _draftDeviceId = value),
                onApply: _applyFilters,
              ),
              Expanded(
                child: items.isEmpty
                    ? const Center(child: Text('No audit history available.'))
                    : ListView.builder(
                        itemCount: items.length + (_cursor == null ? 0 : 1),
                        itemBuilder: (context, index) {
                          if (index == items.length) {
                            return Center(
                              child: _loadingMore
                                  ? const CircularProgressIndicator()
                                  : OutlinedButton(
                                      onPressed: () => _loadMore(query),
                                      child: const Text('Load more'),
                                    ),
                            );
                          }
                          return _AuditTile(audit: items[index]);
                        },
                      ),
              ),
            ],
          );
        },
      ),
    );
  }
}

class _AuditFilters extends StatelessWidget {
  const _AuditFilters({
    required this.action,
    required this.measurementPointId,
    required this.deviceId,
    required this.onAction,
    required this.onMeasurementPointId,
    required this.onDeviceId,
    required this.onApply,
  });
  final String action, measurementPointId, deviceId;
  final ValueChanged<String?> onAction;
  final ValueChanged<String> onMeasurementPointId, onDeviceId;
  final VoidCallback onApply;
  @override
  Widget build(BuildContext context) => Card(
        margin: const EdgeInsets.all(12),
        child: Padding(
          padding: const EdgeInsets.all(12),
          child: Column(
            children: [
              DropdownButtonFormField<String>(
                key: const Key('admin-audit-action-filter'),
                initialValue: action,
                items: adminBindingAuditActions.entries
                    .map(
                      (entry) => DropdownMenuItem(
                        value: entry.key,
                        child: Text(entry.value),
                      ),
                    )
                    .toList(),
                onChanged: onAction,
                decoration: const InputDecoration(labelText: 'Action'),
              ),
              TextFormField(
                key: const Key('admin-audit-measurement-point-filter'),
                initialValue: measurementPointId,
                onChanged: onMeasurementPointId,
                decoration: const InputDecoration(
                  labelText: 'Measurement Point ID',
                ),
              ),
              TextFormField(
                key: const Key('admin-audit-device-filter'),
                initialValue: deviceId,
                onChanged: onDeviceId,
                decoration: const InputDecoration(labelText: 'Device ID'),
              ),
              Align(
                alignment: Alignment.centerRight,
                child:
                    TextButton(onPressed: onApply, child: const Text('Apply')),
              ),
            ],
          ),
        ),
      );
}

class _AuditTile extends StatelessWidget {
  const _AuditTile({required this.audit});
  final AdminBindingAudit audit;

  String _label(String? currentName, String id) =>
      currentName == null || currentName.trim().isEmpty
          ? id
          : '$currentName (current)';

  @override
  Widget build(BuildContext context) {
    final point = audit.measurementPoint;
    final oldPoint = audit.oldMeasurementPoint;
    final newPoint = audit.newMeasurementPoint;
    final contextPoint = point ?? newPoint ?? oldPoint;
    final device = audit.device;
    return Card(
      child: ListTile(
        title: Text(audit.action),
        subtitle: Text(
          [
            'Occurred: ${audit.occurredAt.toLocal()}',
            if (audit.effectiveAt != null)
              'Effective: ${audit.effectiveAt!.toLocal()}',
            'Actor: ${_label(audit.actor.currentDisplayName, audit.actor.id)}',
            if (audit.action != 'relocate' && contextPoint != null)
              'Measurement Point: ${_label(contextPoint.currentDisplayName, contextPoint.id)}',
            if (audit.action == 'relocate' &&
                oldPoint != null &&
                newPoint != null)
              'Relocation: ${_label(oldPoint.currentDisplayName, oldPoint.id)} → ${_label(newPoint.currentDisplayName, newPoint.id)}',
            if (device != null)
              'Device: ${_label(device.currentDisplayName, device.id)}',
            if (device?.serialNumber != null) 'Serial: ${device!.serialNumber}',
            if (audit.reason?.isNotEmpty == true) 'Reason: ${audit.reason}',
          ].join('\n'),
        ),
      ),
    );
  }
}
