import 'package:flutter/material.dart';
import 'package:go_router/go_router.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:power_iot_app/config/theme.dart';
import '../providers/measurement_point_detail_provider.dart';
import '../../domain/models/measurement_point_detail.dart';

class MeasurementPointDetailScreen extends ConsumerWidget {
  const MeasurementPointDetailScreen(
      {required this.shopId, required this.measurementPointRef, super.key});
  final String shopId;
  final String measurementPointRef;

  String get _routeKey => '$shopId|$measurementPointRef';

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final provider = measurementPointDetailProvider(_routeKey);
    final state = ref.watch(provider);
    Widget body;
    switch (state.status) {
      case MeasurementPointDetailStatus.loading:
        body = const Center(child: CircularProgressIndicator());
        break;
      case MeasurementPointDetailStatus.unauthorized:
        body = const _Message('登入狀態已失效');
        break;
      case MeasurementPointDetailStatus.notFound:
        body = const _Message('目前無法取得此量測點資料');
        break;
      case MeasurementPointDetailStatus.error:
        body = _RetryMessage(onRetry: () => ref.read(provider.notifier).load());
        break;
      case MeasurementPointDetailStatus.success:
        body = _Content(detail: state.data!, shopId: shopId, measurementPointRef: measurementPointRef);
        break;
    }
    return Scaffold(
      appBar: AppBar(title: const Text('設備詳情')),
      body: SafeArea(child: body),
    );
  }
}

class _Content extends StatelessWidget {
  const _Content({required this.detail, required this.shopId, required this.measurementPointRef});
  final MeasurementPointDetail detail;
  final String shopId;
  final String measurementPointRef;

  @override
  Widget build(BuildContext context) => ListView(
        padding: const EdgeInsets.all(20),
        children: [
          Text(detail.measurementPoint.name,
              style: const TextStyle(
                  fontSize: 28,
                  fontWeight: FontWeight.bold,
                  color: AppTheme.textPrimary)),
          const SizedBox(height: 4),
          Text('${detail.shop.code} ${detail.shop.name}',
              style: const TextStyle(color: AppTheme.textSecondary)),
          const SizedBox(height: 8),
          _Status(status: detail.measurementPoint.status),
          const SizedBox(height: 12),
          OutlinedButton.icon(
            onPressed: () => context.push('/shops/$shopId/alerts?measurementPointRef=$measurementPointRef'),
            icon: const Icon(Icons.notifications_none),
            label: const Text('警報紀錄'),
          ),
          if (detail.technicalInfo != null) ...[
            const SizedBox(height: 12),
            OutlinedButton.icon(
              onPressed: () => context.push('/shops/$shopId/measurement-points/$measurementPointRef/alert-settings'),
              icon: const Icon(Icons.tune),
              label: const Text('警報設定'),
            ),
          ],
          const SizedBox(height: 20),
          _EnergyHero(window: detail.todayEnergy),
          const SizedBox(height: 16),
          Row(children: [
            Expanded(child: _PowerCard(power: detail.currentPower)),
            const SizedBox(width: 12),
            Expanded(
                child: _EnergyCard(title: '本月用電', window: detail.monthEnergy)),
          ]),
          _WatermarkLabel(
              label: '今日完整至', value: detail.todayEnergy.completeThrough),
          _WatermarkLabel(
              label: '本月完整至', value: detail.monthEnergy.completeThrough),
          const SizedBox(height: 16),
          _CurrentDevice(device: detail.currentDevice),
          if (detail.technicalInfo != null)
            _TechnicalInfo(info: detail.technicalInfo!),
          const SizedBox(height: 20),
          const Text('歷史設備紀錄',
              style: TextStyle(fontSize: 18, fontWeight: FontWeight.bold)),
          const SizedBox(height: 8),
          if (detail.assignmentHistory.isEmpty)
            const Text('尚無設備紀錄')
          else
            ...detail.assignmentHistory.map((row) => _HistoryCard(row: row)),
        ],
      );
}

class _Status extends StatelessWidget {
  const _Status({required this.status});
  final String status;
  @override
  Widget build(BuildContext context) {
    final label =
        switch (status) { 'online' => '線上', 'offline' => '離線', _ => '未綁定' };
    final color = status == 'online'
        ? Colors.green
        : status == 'offline'
            ? Colors.orange
            : Colors.grey;
    return Row(children: [
      Icon(Icons.circle, size: 12, color: color),
      const SizedBox(width: 6),
      Text(label)
    ]);
  }
}

class _EnergyHero extends StatelessWidget {
  const _EnergyHero({required this.window});
  final MeasurementPointEnergyWindow window;
  @override
  Widget build(BuildContext context) => Card(
        color: AppTheme.primaryColor,
        child: Padding(
          padding: const EdgeInsets.all(22),
          child:
              Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
            const Text('今日用電',
                style: TextStyle(color: Colors.white70, fontSize: 16)),
            const SizedBox(height: 8),
            Text(_energy(window.kwh),
                style: const TextStyle(
                    color: Colors.white,
                    fontSize: 34,
                    fontWeight: FontWeight.bold)),
          ]),
        ),
      );
}

class _PowerCard extends StatelessWidget {
  const _PowerCard({required this.power});
  final MeasurementPointCurrentPower power;
  @override
  Widget build(BuildContext context) => _SmallCard(
      title: '即時功率',
      value: power.watts == null ? '無資料' : '${_number(power.watts!)} W');
}

class _EnergyCard extends StatelessWidget {
  const _EnergyCard({required this.title, required this.window});
  final String title;
  final MeasurementPointEnergyWindow window;
  @override
  Widget build(BuildContext context) =>
      _SmallCard(title: title, value: _energy(window.kwh));
}

class _SmallCard extends StatelessWidget {
  const _SmallCard({required this.title, required this.value});
  final String title;
  final String value;
  @override
  Widget build(BuildContext context) => Card(
      child: Padding(
          padding: const EdgeInsets.all(14),
          child:
              Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
            Text(title, style: const TextStyle(fontWeight: FontWeight.bold)),
            const SizedBox(height: 8),
            Text(value)
          ])));
}

class _WatermarkLabel extends StatelessWidget {
  const _WatermarkLabel({required this.label, required this.value});
  final String label;
  final DateTime? value;
  @override
  Widget build(BuildContext context) => Padding(
      padding: const EdgeInsets.only(top: 6),
      child: Text('$label：${value == null ? '無資料' : _date(value!)}',
          style: const TextStyle(fontSize: 12, color: Colors.grey)));
}

class _CurrentDevice extends StatelessWidget {
  const _CurrentDevice({required this.device});
  final MeasurementPointDetailDevice? device;
  @override
  Widget build(BuildContext context) => Card(
      child: Padding(
          padding: const EdgeInsets.all(16),
          child: device == null
              ? const Text('目前未綁定設備')
              : Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
                  const Text('目前設備',
                      style: TextStyle(fontWeight: FontWeight.bold)),
                  const SizedBox(height: 8),
                  Text(device!.displayName),
                  Text('MAC：${device!.mac}'),
                  Text(
                      '最後上線：${device!.lastSeen == null ? '未知' : _date(device!.lastSeen!)}')
                ])));
}

class _TechnicalInfo extends StatelessWidget {
  const _TechnicalInfo({required this.info});
  final MeasurementPointTechnicalInfo info;
  @override
  Widget build(BuildContext context) => Card(
      child: Padding(
          padding: const EdgeInsets.all(16),
          child:
              Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
            const Text('技術資訊', style: TextStyle(fontWeight: FontWeight.bold)),
            Text('Measurement Point ID：${info.measurementPointId}'),
            Text('Backend Device ID：${info.deviceId ?? '無'}')
          ])));
}

class _HistoryCard extends StatelessWidget {
  const _HistoryCard({required this.row});
  final MeasurementPointAssignment row;
  @override
  Widget build(BuildContext context) => Card(
      child: ListTile(
          leading: const Icon(Icons.history),
          title: Text(row.displayName),
          subtitle: Text(
              '${row.mac}\n${_date(row.validFrom)} → ${row.validTo == null ? '目前' : _date(row.validTo!)}')));
}

class _Message extends StatelessWidget {
  const _Message(this.message);
  final String message;
  @override
  Widget build(BuildContext context) => Center(child: Text(message));
}

class _RetryMessage extends StatelessWidget {
  const _RetryMessage({required this.onRetry});
  final VoidCallback onRetry;
  @override
  Widget build(BuildContext context) => Center(
          child: Column(mainAxisSize: MainAxisSize.min, children: [
        const Text('量測點資料暫時無法取得'),
        TextButton(onPressed: onRetry, child: const Text('重試'))
      ]));
}

String _energy(double? value) =>
    value == null ? '無資料' : '${_number(value)} kWh';
String _number(double value) => value == value.roundToDouble()
    ? value.toInt().toString()
    : value.toStringAsFixed(3);
String _date(DateTime value) => value.toLocal().toIso8601String();
