import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../../domain/models/alert.dart';
import '../providers/alert_providers.dart';

class AlertSettingsScreen extends ConsumerStatefulWidget {
  const AlertSettingsScreen({required this.measurementPointId, super.key});
  final String measurementPointId;
  @override
  ConsumerState<AlertSettingsScreen> createState() =>
      _AlertSettingsScreenState();
}

class _AlertSettingsScreenState extends ConsumerState<AlertSettingsScreen> {
  final _daily = TextEditingController();
  final _monthly = TextEditingController();
  final _start = TextEditingController();
  final _end = TextEditingController();
  bool _enabled = false;
  bool _initialized = false;
  bool _saving = false;
  @override
  void dispose() {
    _daily.dispose();
    _monthly.dispose();
    _start.dispose();
    _end.dispose();
    super.dispose();
  }

  void _fill(AlertSettings value) {
    if (_initialized) return;
    _initialized = true;
    _daily.text = value.dailyLimitKwh?.toString() ?? '';
    _monthly.text = value.monthlyLimitKwh?.toString() ?? '';
    _start.text = value.nonUsageStartTime;
    _end.text = value.nonUsageEndTime;
    _enabled = value.isEnabled;
  }

  Future<void> _save(AlertSettings old) async {
    double? number(String value) =>
        value.trim().isEmpty ? null : double.tryParse(value.trim());
    final daily = number(_daily.text);
    final monthly = number(_monthly.text);
    if ((_daily.text.trim().isNotEmpty && daily == null) ||
        (_monthly.text.trim().isNotEmpty && monthly == null)) {
      ScaffoldMessenger.of(
        context,
      ).showSnackBar(const SnackBar(content: Text('請輸入有效數值')));
      return;
    }
    setState(() => _saving = true);
    try {
      final value = await ref.read(alertRepositoryProvider).updateSettings(
            widget.measurementPointId,
            AlertSettings(
              measurementPointId: old.measurementPointId,
              dailyLimitKwh: daily,
              monthlyLimitKwh: monthly,
              nonUsageStartTime: _start.text.trim(),
              nonUsageEndTime: _end.text.trim(),
              isEnabled: _enabled,
            ),
          );
      if (mounted) {
        _initialized = false;
        ref.invalidate(alertSettingsProvider(widget.measurementPointId));
        _fill(value);
        ScaffoldMessenger.of(
          context,
        ).showSnackBar(const SnackBar(content: Text('警報設定已更新')));
      }
    } catch (_) {
      if (mounted) {
        ScaffoldMessenger.of(
          context,
        ).showSnackBar(const SnackBar(content: Text('更新失敗，請稍後再試')));
      }
    } finally {
      if (mounted) setState(() => _saving = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final state = ref.watch(alertSettingsProvider(widget.measurementPointId));
    return Scaffold(
      appBar: AppBar(title: const Text('量測點警報設定')),
      body: state.when(
        loading: () => const Center(child: CircularProgressIndicator()),
        error: (_, __) => const Center(child: Text('目前無法取得警報設定')),
        data: (value) {
          _fill(value);
          return ListView(
            padding: const EdgeInsets.all(20),
            children: [
              SwitchListTile(
                title: const Text('啟用警報'),
                value: _enabled,
                onChanged: (v) => setState(() => _enabled = v),
              ),
              TextField(
                controller: _daily,
                decoration: const InputDecoration(labelText: '每日用電上限（kWh）'),
                keyboardType: TextInputType.number,
              ),
              TextField(
                controller: _monthly,
                decoration: const InputDecoration(labelText: '每月用電上限（kWh）'),
                keyboardType: TextInputType.number,
              ),
              TextField(
                controller: _start,
                decoration: const InputDecoration(labelText: '非營業開始時間（HH:MM）'),
              ),
              TextField(
                controller: _end,
                decoration: const InputDecoration(labelText: '非營業結束時間（HH:MM）'),
              ),
              const SizedBox(height: 20),
              FilledButton(
                onPressed: _saving ? null : () => _save(value),
                child: Text(_saving ? '儲存中…' : '儲存設定'),
              ),
            ],
          );
        },
      ),
    );
  }
}
