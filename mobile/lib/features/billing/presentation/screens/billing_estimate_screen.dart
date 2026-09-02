import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:power_iot_app/config/theme.dart';
import 'package:power_iot_app/features/billing/domain/models/billing_estimate.dart';
import 'package:power_iot_app/features/billing/presentation/providers/billing_estimate_provider.dart';
import 'package:power_iot_app/features/shops/providers/remote_shop_provider.dart';

class BillingEstimateScreen extends ConsumerStatefulWidget {
  const BillingEstimateScreen({super.key, this.initialMonth});
  final DateTime? initialMonth;

  @override
  ConsumerState<BillingEstimateScreen> createState() =>
      _BillingEstimateScreenState();
}

class _BillingEstimateScreenState extends ConsumerState<BillingEstimateScreen> {
  late DateTime _month;

  @override
  void initState() {
    super.initState();
    final value = widget.initialMonth ?? DateTime.now();
    _month = DateTime(value.year, value.month);
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
    final shopId = authorizedShopId(shops);
    if (shopId == null || shopId.isEmpty) {
      return const Scaffold(
        body: SafeArea(child: Center(child: Text('尚未選擇店家'))),
      );
    }
    final estimate = ref.watch(
      billingEstimateProvider((shopId: shopId, month: _monthKey)),
    );
    return Scaffold(
      appBar: AppBar(title: const Text('電費估算')),
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
              child: estimate.when(
                loading: () => const Center(child: CircularProgressIndicator()),
                error: (error, _) => _EstimateMessage(
                  '目前無法取得電費估算',
                  onRetry: () => ref.invalidate(
                    billingEstimateProvider((shopId: shopId, month: _monthKey)),
                  ),
                ),
                data: (value) => _EstimateBody(estimate: value),
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
  Widget build(BuildContext context) {
    return Padding(
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
}

class _EstimateBody extends StatelessWidget {
  const _EstimateBody({required this.estimate});
  final BillingEstimate estimate;

  @override
  Widget build(BuildContext context) {
    if (estimate.status == BillingEstimateStatus.noData) {
      return const _EstimateMessage('無可用監測資料', footer: _Disclaimer());
    }
    if (estimate.status == BillingEstimateStatus.configurationRequired) {
      return _EstimateMessage(
        '尚未設定電費計算方案',
        onAction: () => context.push('/shops'),
        actionLabel: '前往電費方案設定',
      );
    }
    if (estimate.status == BillingEstimateStatus.unsupportedTariff) {
      return const _EstimateMessage('目前尚未支援此電價類型的電費估算');
    }
    if (estimate.status == BillingEstimateStatus.unsupportedPeriod) {
      return const _EstimateMessage('此月份目前無法估算');
    }
    if (estimate.status == BillingEstimateStatus.rateNotFound) {
      return const _EstimateMessage('此月份找不到適用的費率版本');
    }
    final charges = estimate.charges;
    if (charges == null) return const _EstimateMessage('目前無法取得電費估算');
    final partial =
        estimate.status == BillingEstimateStatus.partialDataEstimate;
    return SingleChildScrollView(
      padding: const EdgeInsets.fromLTRB(20, 8, 20, 24),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          _AmountCard(amount: charges.estimatedTotal, partial: partial),
          const SizedBox(height: 16),
          _FactCard(estimate: estimate),
          if (partial) ...[
            const SizedBox(height: 12),
            const _WarningBox('部分期間缺少監測資料，預估金額可能偏低。'),
          ],
          if (charges.minimumChargeAdjustment != '0' &&
              charges.minimumChargeAdjustment != '0.0') ...[
            const SizedBox(height: 12),
            _ChargeCard(charges: charges),
          ],
          if (estimate.tiers.isNotEmpty) ...[
            const SizedBox(height: 12),
            _TierCard(tiers: estimate.tiers),
          ],
          if (estimate.warnings.any(
            (warning) => warning == 'BOTTOM_DEGREE_NOT_MODELED',
          )) ...[
            const SizedBox(height: 12),
            const _WarningBox('本估算未納入底度及契約容量等資料。'),
          ],
          const SizedBox(height: 16),
          const _Disclaimer(),
        ],
      ),
    );
  }
}

class _AmountCard extends StatelessWidget {
  const _AmountCard({required this.amount, required this.partial});
  final String amount;
  final bool partial;

  @override
  Widget build(BuildContext context) => Card(
        child: Padding(
          padding: const EdgeInsets.all(22),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(
                partial ? '部分資料預估電費' : '預估電費',
                style: const TextStyle(color: AppTheme.textSecondary),
              ),
              const SizedBox(height: 8),
              Text(
                'NT\$$amount',
                style: const TextStyle(
                  fontSize: 36,
                  fontWeight: FontWeight.bold,
                  color: AppTheme.primaryColor,
                ),
              ),
              const SizedBox(height: 4),
              const Text('估算結果，非正式台電帳單'),
            ],
          ),
        ),
      );
}

class _FactCard extends StatelessWidget {
  const _FactCard({required this.estimate});
  final BillingEstimate estimate;

  @override
  Widget build(BuildContext context) {
    final coverage = estimate.energy.coverage;
    final percent = coverage == null ? '無法取得' : '${_percentage(coverage)}%';
    return Card(
      child: Padding(
        padding: const EdgeInsets.all(18),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            const Text(
              '用電與費率',
              style: TextStyle(fontSize: 18, fontWeight: FontWeight.bold),
            ),
            const SizedBox(height: 12),
            _FactRow('用電量', '${estimate.energy.usageKwh ?? '無資料'} kWh'),
            _FactRow('監測資料完整度', percent),
            _FactRow('電價方案', estimate.tariff.planCode),
            _FactRow('季別', estimate.tariff.season == 'SUMMER' ? '夏月' : '非夏月'),
            _FactRow('費率版本', estimate.rateSet.version),
          ],
        ),
      ),
    );
  }
}

class _ChargeCard extends StatelessWidget {
  const _ChargeCard({required this.charges});
  final BillingEstimateCharges charges;

  @override
  Widget build(BuildContext context) => Card(
        child: Padding(
          padding: const EdgeInsets.all(18),
          child: Column(
            children: [
              _FactRow('用電流動電費', 'NT\$${charges.energyCharge}'),
              _FactRow('最低月費調整', 'NT\$${charges.minimumChargeAdjustment}'),
            ],
          ),
        ),
      );
}

class _TierCard extends StatelessWidget {
  const _TierCard({required this.tiers});
  final List<BillingEstimateTier> tiers;

  @override
  Widget build(BuildContext context) => Card(
        child: Padding(
          padding: const EdgeInsets.all(18),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              const Text(
                '級距明細',
                style: TextStyle(fontSize: 18, fontWeight: FontWeight.bold),
              ),
              const SizedBox(height: 8),
              ...tiers.map(
                (tier) => Padding(
                  padding: const EdgeInsets.symmetric(vertical: 6),
                  child: _FactRow(
                    '${tier.fromKwh}–${tier.toKwh ?? '以上'} kWh',
                    '${tier.usageKwh} × NT\$${tier.ratePerKwh} = NT\$${tier.subtotal}',
                  ),
                ),
              ),
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
        padding: const EdgeInsets.symmetric(vertical: 4),
        child: Row(
          mainAxisAlignment: MainAxisAlignment.spaceBetween,
          children: [
            Expanded(
              child: Text(
                label,
                style: const TextStyle(color: AppTheme.textSecondary),
              ),
            ),
            const SizedBox(width: 12),
            Flexible(child: Text(value, textAlign: TextAlign.right)),
          ],
        ),
      );
}

class _WarningBox extends StatelessWidget {
  const _WarningBox(this.message);
  final String message;

  @override
  Widget build(BuildContext context) => Container(
        width: double.infinity,
        padding: const EdgeInsets.all(14),
        decoration: BoxDecoration(
          color: Colors.orange.shade50,
          borderRadius: BorderRadius.circular(14),
        ),
        child: Text(message, style: TextStyle(color: Colors.orange.shade900)),
      );
}

class _Disclaimer extends StatelessWidget {
  const _Disclaimer();

  @override
  Widget build(BuildContext context) => Text(
        '此金額依 Power-IoT 已監測到的用電資料與目前系統內的台電費率估算，實際應繳金額仍以台電帳單為準。',
        style: TextStyle(color: Colors.grey.shade700, fontSize: 12),
      );
}

class _EstimateMessage extends StatelessWidget {
  const _EstimateMessage(this.message,
      {this.onRetry, this.footer, this.onAction, this.actionLabel});
  final String message;
  final VoidCallback? onRetry;
  final Widget? footer;
  final VoidCallback? onAction;
  final String? actionLabel;

  @override
  Widget build(BuildContext context) => Center(
        child: Padding(
          padding: const EdgeInsets.all(24),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              Text(message, textAlign: TextAlign.center),
              if (onRetry != null)
                TextButton(onPressed: onRetry, child: const Text('重試')),
              if (onAction != null)
                TextButton(
                  onPressed: onAction,
                  child: Text(actionLabel ?? '前往設定'),
                ),
              if (footer != null) ...[const SizedBox(height: 16), footer!],
            ],
          ),
        ),
      );
}

String _percentage(String ratio) {
  final parts = ratio.trim().split('.');
  if (parts.length > 2) return ratio;
  try {
    final whole = BigInt.parse(parts.first.isEmpty ? '0' : parts.first);
    final fraction = parts.length == 2 ? parts[1] : '';
    final denominator = BigInt.from(10).pow(fraction.length);
    final numerator = whole * denominator +
        (fraction.isEmpty ? BigInt.zero : BigInt.parse(fraction));
    final scaledTenths =
        (numerator * BigInt.from(1000) + denominator ~/ BigInt.from(2)) ~/
            denominator;
    final integer = scaledTenths ~/ BigInt.from(10);
    final tenth = scaledTenths.remainder(BigInt.from(10));
    return '$integer.$tenth';
  } on FormatException {
    return ratio;
  }
}
