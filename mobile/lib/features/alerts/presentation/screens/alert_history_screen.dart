import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../../domain/models/alert.dart';
import '../providers/alert_providers.dart';

class AlertHistoryScreen extends ConsumerStatefulWidget {
  const AlertHistoryScreen({required this.shopId, this.measurementPointRef, super.key});
  final String shopId;
  final String? measurementPointRef;
  @override ConsumerState<AlertHistoryScreen> createState() => _AlertHistoryScreenState();
}

class _AlertHistoryScreenState extends ConsumerState<AlertHistoryScreen> {
  late final TextEditingController _filterController;
  String? _activeFilter;
  final List<AlertRecord> _more = [];
  String? _nextCursor;
  bool _loadingMore = false;
  @override void initState() { super.initState(); _activeFilter = widget.measurementPointRef; _filterController = TextEditingController(text: widget.measurementPointRef ?? ''); }
  @override void dispose() { _filterController.dispose(); super.dispose(); }
  String get _key => '${widget.shopId}|${_activeFilter ?? ''}';
  void _applyFilter() { setState(() { _activeFilter = _filterController.text.trim().isEmpty ? null : _filterController.text.trim(); _more.clear(); _nextCursor = null; }); }
  Future<void> _loadMore() async {
    if (_loadingMore || _nextCursor == null) return;
    setState(() => _loadingMore = true);
    try {
      final page = await ref.read(alertRepositoryProvider).fetchHistory(widget.shopId, measurementPointRef: _activeFilter, cursor: _nextCursor);
      if (mounted) setState(() { _more.addAll(page.items); _nextCursor = page.nextCursor; });
    } catch (_) { if (mounted) ScaffoldMessenger.of(context).showSnackBar(const SnackBar(content: Text('載入更多警報失敗'))); }
    finally { if (mounted) setState(() => _loadingMore = false); }
  }
  @override Widget build(BuildContext context) {
    if (widget.shopId.isEmpty) return const Scaffold(body: Center(child: Text('目前沒有可用店家')));
    final state = ref.watch(alertHistoryProvider(_key));
    return Scaffold(
      appBar: AppBar(title: const Text('警報紀錄')),
      body: state.when(
        loading: () => const Center(child: CircularProgressIndicator()),
        error: (_, __) => Center(child: Column(mainAxisSize: MainAxisSize.min, children: [const Text('警報紀錄暫時無法取得'), const SizedBox(height: 12), OutlinedButton(onPressed: () => ref.invalidate(alertHistoryProvider(_key)), child: const Text('重試'))])),
        data: (page) {
          _nextCursor ??= page.nextCursor;
          final items = [...page.items, ..._more];
          return Column(children: [
            Padding(padding: const EdgeInsets.fromLTRB(16, 12, 16, 4), child: Row(children: [Expanded(child: TextField(controller: _filterController, decoration: const InputDecoration(labelText: '量測點 ID 篩選', hintText: '可留空查看全部'))), const SizedBox(width: 8), FilledButton(onPressed: _applyFilter, child: const Text('套用'))])),
            Expanded(child: items.isEmpty ? const Center(child: Text('尚無警報紀錄')) : RefreshIndicator(onRefresh: () async { _more.clear(); _nextCursor = null; ref.invalidate(alertHistoryProvider(_key)); await ref.read(alertHistoryProvider(_key).future); }, child: ListView.builder(itemCount: items.length + (_nextCursor == null ? 0 : 1), itemBuilder: (context, index) {
              if (index == items.length) return Padding(padding: const EdgeInsets.all(16), child: Center(child: _loadingMore ? const CircularProgressIndicator() : OutlinedButton(onPressed: _loadMore, child: const Text('載入更多'))));
              final alert = items[index];
              final device = alert.deviceName.isEmpty ? 'Device #${alert.deviceId}' : alert.deviceName;
              return ListTile(title: Text(alert.measurementPointName.isEmpty ? alert.measurementPointId : alert.measurementPointName), subtitle: Text('${alert.message}\n$device${alert.serialNumber == null ? '' : ' · ${alert.serialNumber}'}\n${alert.createdAt.toLocal()}'), isThreeLine: true, trailing: Text('${alert.power.toStringAsFixed(1)} W'));
            }))),
          ]);
        },
      ),
    );
  }
}
