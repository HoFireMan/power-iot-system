import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../../domain/models/alert.dart';
import '../providers/alert_providers.dart';

class AlertSettingsScreen extends ConsumerStatefulWidget {
  const AlertSettingsScreen({required this.shopId, required this.measurementPointRef, super.key});
  final String shopId;
  final String measurementPointRef;
  @override ConsumerState<AlertSettingsScreen> createState() => _AlertSettingsScreenState();
}
class _AlertSettingsScreenState extends ConsumerState<AlertSettingsScreen> {
  final _start = TextEditingController(); final _end = TextEditingController(); final _threshold = TextEditingController();
  bool _enabled = true; bool _initialized = false; bool _saving = false;
  String get _key => '${widget.shopId}|${widget.measurementPointRef}';
  @override void dispose() { _start.dispose(); _end.dispose(); _threshold.dispose(); super.dispose(); }
  void _fill(AlertSettings value) { if (_initialized) return; _initialized = true; _start.text = value.quietHoursStart; _end.text = value.quietHoursEnd; _threshold.text = value.powerThresholdW.toString(); _enabled = value.isEnabled; }
  Future<void> _save(AlertSettings old) async {
    final threshold = double.tryParse(_threshold.text.trim());
    if (threshold == null || !threshold.isFinite || threshold <= 0 || (_start.text.trim().isEmpty != _end.text.trim().isEmpty) || (_start.text.trim().isNotEmpty && _start.text.trim() == _end.text.trim())) { ScaffoldMessenger.of(context).showSnackBar(const SnackBar(content: Text('請輸入有效設定（時間需為 HH:mm 且不可相同）'))); return; }
    setState(() => _saving = true);
    try {
      final value = await ref.read(alertRepositoryProvider).updateSettings(widget.shopId, widget.measurementPointRef, AlertSettings(measurementPointId: old.measurementPointId, isEnabled: _enabled, quietHoursStart: _start.text.trim(), quietHoursEnd: _end.text.trim(), powerThresholdW: threshold));
      if (mounted) { _initialized = false; ref.invalidate(alertSettingsProvider(_key)); _fill(value); ScaffoldMessenger.of(context).showSnackBar(const SnackBar(content: Text('警報設定已更新'))); }
    } catch (_) { if (mounted) ScaffoldMessenger.of(context).showSnackBar(const SnackBar(content: Text('更新失敗，請稍後再試'))); }
    finally { if (mounted) setState(() => _saving = false); }
  }
  @override Widget build(BuildContext context) {
    final state = ref.watch(alertSettingsProvider(_key));
    return Scaffold(appBar: AppBar(title: const Text('量測點警報設定')), body: state.when(
      loading: () => const Center(child: CircularProgressIndicator()),
      error: (_, __) => const Center(child: Text('目前無法取得警報設定（需要授權管理員權限）')),
      data: (value) { _fill(value); return ListView(padding: const EdgeInsets.all(20), children: [
        SwitchListTile(title: const Text('啟用警報'), value: _enabled, onChanged: (v) => setState(() => _enabled = v)),
        TextField(controller: _start, decoration: const InputDecoration(labelText: '安靜時段開始（HH:mm）')),
        TextField(controller: _end, decoration: const InputDecoration(labelText: '安靜時段結束（HH:mm）')),
        TextField(controller: _threshold, decoration: const InputDecoration(labelText: '安靜時段功率門檻（W）'), keyboardType: const TextInputType.numberWithOptions(decimal: true)),
        const SizedBox(height: 20), FilledButton(onPressed: _saving ? null : () => _save(value), child: Text(_saving ? '儲存中…' : '儲存設定')),
      ]); },
    ));
  }
}
