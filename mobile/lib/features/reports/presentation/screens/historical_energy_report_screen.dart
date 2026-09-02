import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:power_iot_app/features/admin/presentation/providers/admin_overview_provider.dart';
import 'package:power_iot_app/core/network/remote_error.dart';
import 'package:power_iot_app/features/reports/domain/models/historical_energy_report.dart';
import 'package:power_iot_app/features/reports/presentation/providers/historical_energy_provider.dart';
import 'package:power_iot_app/features/profile/presentation/providers/profile_provider.dart';
import 'package:power_iot_app/features/shops/providers/remote_shop_provider.dart';

class HistoricalEnergyReportScreen extends ConsumerStatefulWidget {
  const HistoricalEnergyReportScreen({super.key, this.initialMonth});

  final DateTime? initialMonth;

  @override
  ConsumerState<HistoricalEnergyReportScreen> createState() =>
      _HistoricalEnergyReportScreenState();
}

class _HistoricalEnergyReportScreenState
    extends ConsumerState<HistoricalEnergyReportScreen> {
  late DateTime _month;

  @override
  void initState() {
    super.initState();
    final now = DateTime.now();
    final current = DateTime(now.year, now.month);
    final value = widget.initialMonth ?? current;
    final requested = DateTime(value.year, value.month);
    _month = requested.isAfter(current) ? current : requested;
  }

  String get _monthKey =>
      '${_month.year.toString().padLeft(4, '0')}-${_month.month.toString().padLeft(2, '0')}';

  void _changeMonth(int delta) {
    final candidate = DateTime(_month.year, _month.month + delta);
    final now = DateTime.now();
    final current = DateTime(now.year, now.month);
    if (candidate.isAfter(current)) return;
    setState(() => _month = candidate);
  }

  @override
  Widget build(BuildContext context) {
    final shops = ref.watch(shopsProvider);
    final shop = selectedAdminShop(shops);
    if (shops.status == RemoteStatus.loading) {
      return const Scaffold(body: Center(child: CircularProgressIndicator()));
    }
    if (shops.status == RemoteStatus.unauthorized) {
      return const Scaffold(body: Center(child: Text('請重新登入')));
    }
    if (shops.status == RemoteStatus.error) {
      return Scaffold(
        body: _ReportMessage(
          '目前無法取得店家資料',
          onRetry: () => ref.read(shopsProvider.notifier).load(),
        ),
      );
    }
    if (shop == null) {
      return const Scaffold(body: Center(child: Text('尚未選擇店家')));
    }

    final report = ref.watch(
      historicalEnergyProvider((shopId: shop.id, month: _monthKey)),
    );
    return Scaffold(
      appBar: AppBar(title: const Text('歷史用電報表')),
      body: SafeArea(
        child: Column(
          children: [
            _MonthSelector(
              month: _month,
              onPrevious: () => _changeMonth(-1),
              onNext: () => _changeMonth(1),
              nextDisabled: _isCurrentMonth,
            ),
            Expanded(
              child: report.when(
                loading: () => const Center(child: CircularProgressIndicator()),
                error: (error, _) => _ReportMessage(
                  isUnauthorizedError(error) ? '請重新登入' : '目前無法取得歷史用電報表',
                  onRetry: () => ref.invalidate(
                    historicalEnergyProvider(
                      (shopId: shop.id, month: _monthKey),
                    ),
                  ),
                ),
                data: (value) => _ReportBody(report: value),
              ),
            ),
          ],
        ),
      ),
    );
  }

  bool get _isCurrentMonth {
    final now = DateTime.now();
    return _month.year == now.year && _month.month == now.month;
  }
}

class _MonthSelector extends StatelessWidget {
  const _MonthSelector({
    required this.month,
    required this.onPrevious,
    required this.onNext,
    required this.nextDisabled,
  });

  final DateTime month;
  final VoidCallback onPrevious;
  final VoidCallback onNext;
  final bool nextDisabled;

  @override
  Widget build(BuildContext context) => Padding(
        padding: const EdgeInsets.fromLTRB(20, 12, 20, 8),
        child: Row(
          mainAxisAlignment: MainAxisAlignment.spaceBetween,
          children: [
            IconButton(
              onPressed: onPrevious,
              icon: const Icon(Icons.chevron_left),
            ),
            Text(
              '${month.year} 年 ${month.month.toString().padLeft(2, '0')} 月',
              style: const TextStyle(fontSize: 18, fontWeight: FontWeight.bold),
            ),
            IconButton(
              onPressed: nextDisabled ? null : onNext,
              icon: const Icon(Icons.chevron_right),
            ),
          ],
        ),
      );
}

class _ReportBody extends StatelessWidget {
  const _ReportBody({required this.report});

  final HistoricalEnergyReport report;

  @override
  Widget build(BuildContext context) {
    return ListView(
      padding: const EdgeInsets.fromLTRB(20, 8, 20, 24),
      children: [
        Text(report.timezone, style: const TextStyle(color: Colors.grey)),
        const SizedBox(height: 8),
        _FactsCard(title: '本月總用電', facts: report.summary),
        if (report.warnings.isNotEmpty) ...[
          const SizedBox(height: 12),
          _WarningCard(warnings: report.warnings),
        ],
        const SizedBox(height: 20),
        const Text(
          'Measurement Points',
          style: TextStyle(fontSize: 18, fontWeight: FontWeight.bold),
        ),
        const SizedBox(height: 8),
        if (report.measurementPoints.isEmpty)
          const Text('此店家沒有 Measurement Point')
        else
          ...report.measurementPoints.map(
            (point) => Padding(
              padding: const EdgeInsets.only(bottom: 12),
              child: _PointCard(point: point),
            ),
          ),
        const SizedBox(height: 8),
        const Text(
          '此報表為監測用電資料，不是正式電費帳單。',
          style: TextStyle(color: Colors.grey),
        ),
      ],
    );
  }
}

class _FactsCard extends StatelessWidget {
  const _FactsCard({required this.title, required this.facts});

  final String title;
  final HistoricalEnergyFacts facts;

  @override
  Widget build(BuildContext context) => Card(
        child: Padding(
          padding: const EdgeInsets.all(18),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(title,
                  style: const TextStyle(
                      fontSize: 18, fontWeight: FontWeight.bold)),
              const SizedBox(height: 12),
              _FactRow('狀態', _statusLabel(facts.status)),
              _FactRow('用電量', '${facts.usageKwh ?? '無資料'} kWh'),
              _FactRow('覆蓋率', facts.coverage ?? '無資料'),
              _FactRow(
                  '預期監測時間', _formatDuration(facts.expectedDurationSeconds)),
              _FactRow(
                  '實際監測時間', _formatDuration(facts.observedDurationSeconds)),
            ],
          ),
        ),
      );
}

class _PointCard extends StatelessWidget {
  const _PointCard({required this.point});

  final HistoricalEnergyPoint point;

  @override
  Widget build(BuildContext context) => Card(
        child: Padding(
          padding: const EdgeInsets.all(16),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(point.measurementPointId,
                  style: const TextStyle(fontWeight: FontWeight.bold)),
              const SizedBox(height: 8),
              _FactRow('狀態', _statusLabel(point.status)),
              _FactRow('用電量', '${point.usageKwh ?? '無資料'} kWh'),
              _FactRow('覆蓋率', point.coverage ?? '無資料'),
              _FactRow(
                  '預期監測時間', _formatDuration(point.expectedDurationSeconds)),
              _FactRow(
                  '實際監測時間', _formatDuration(point.observedDurationSeconds)),
              if (point.warnings.isNotEmpty) ...[
                const SizedBox(height: 8),
                _WarningCard(warnings: point.warnings),
              ],
            ],
          ),
        ),
      );
}

class _FactRow extends StatelessWidget {
  const _FactRow(this.label, this.value);

  final String label;
  final String value;

  @override
  Widget build(BuildContext context) => Padding(
        padding: const EdgeInsets.symmetric(vertical: 3),
        child: Row(
          mainAxisAlignment: MainAxisAlignment.spaceBetween,
          children: [
            Text(label, style: const TextStyle(color: Colors.grey)),
            const SizedBox(width: 12),
            Flexible(child: Text(value, textAlign: TextAlign.right)),
          ],
        ),
      );
}

class _WarningCard extends StatelessWidget {
  const _WarningCard({required this.warnings});

  final List<String> warnings;

  @override
  Widget build(BuildContext context) => Container(
        width: double.infinity,
        padding: const EdgeInsets.all(12),
        decoration: BoxDecoration(
          color: Colors.orange.shade50,
          borderRadius: BorderRadius.circular(12),
        ),
        child: Text(warnings.join(', '),
            style: TextStyle(color: Colors.orange.shade900)),
      );
}

class _ReportMessage extends StatelessWidget {
  const _ReportMessage(this.message, {this.onRetry});

  final String message;
  final VoidCallback? onRetry;

  @override
  Widget build(BuildContext context) => Center(
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            Text(message),
            if (onRetry != null) ...[
              const SizedBox(height: 12),
              OutlinedButton(onPressed: onRetry, child: const Text('重試')),
            ],
          ],
        ),
      );
}

String _statusLabel(HistoricalEnergyStatus status) => switch (status) {
      HistoricalEnergyStatus.complete => '完整',
      HistoricalEnergyStatus.partial => '部分資料',
      HistoricalEnergyStatus.noData => '無資料',
      HistoricalEnergyStatus.noExpectedWindow => '無預期監測區間',
    };

String _formatDuration(int seconds) {
  if (seconds < 60) return '$seconds 秒';
  final minutes = seconds ~/ 60;
  if (minutes < 60) return '$minutes 分鐘';
  final hours = minutes ~/ 60;
  final remaining = minutes % 60;
  return remaining == 0 ? '$hours 小時' : '$hours 小時 $remaining 分鐘';
}
