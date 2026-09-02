import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:power_iot_app/features/dashboard/dashboard_route_observer.dart';
import 'package:power_iot_app/features/billing/domain/models/billing_estimate.dart';
import 'package:power_iot_app/features/billing/presentation/providers/billing_estimate_provider.dart';
import 'package:power_iot_app/config/theme.dart';
import 'package:power_iot_app/features/dashboard/domain/models/dashboard.dart';
import 'package:power_iot_app/features/dashboard/presentation/providers/dashboard_provider.dart';
import 'package:power_iot_app/features/shops/providers/remote_shop_provider.dart';
import 'package:power_iot_app/features/profile/presentation/providers/profile_provider.dart';

class DashboardScreen extends ConsumerStatefulWidget {
  const DashboardScreen({super.key});

  @override
  ConsumerState<DashboardScreen> createState() => _DashboardScreenState();
}

class _DashboardScreenState extends ConsumerState<DashboardScreen>
    with WidgetsBindingObserver, RouteAware {
  ModalRoute<dynamic>? _route;
  DashboardNotifier? _notifier;
  var _routeVisible = true;
  var _lifecycleState =
      WidgetsBinding.instance.lifecycleState ?? AppLifecycleState.resumed;

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
  }

  @override
  void didChangeDependencies() {
    super.didChangeDependencies();
    final route = ModalRoute.of(context);
    if (route != null && route != _route) {
      if (_route != null) dashboardRouteObserver.unsubscribe(this);
      _route = route;
      dashboardRouteObserver.subscribe(this, route);
      // A new route starts visible. Do not reset this on every dependency
      // change: RouteAware.didPushNext must keep covered dashboards stopped.
      _routeVisible = true;
    }
  }

  void _syncNotifier() {
    final notifier = _notifier;
    if (notifier == null) return;
    notifier.setRouteVisible(_routeVisible);
    notifier.setAppLifecycleState(_lifecycleState);
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    _lifecycleState = state;
    _syncNotifier();
  }

  @override
  void didPush() {
    _routeVisible = true;
    _syncNotifier();
  }

  @override
  void didPopNext() {
    _routeVisible = true;
    _syncNotifier();
  }

  @override
  void didPushNext() {
    _routeVisible = false;
    _syncNotifier();
  }

  @override
  void didPop() {
    _routeVisible = false;
    _syncNotifier();
  }

  @override
  void dispose() {
    _routeVisible = false;
    _syncNotifier();
    if (_route != null) dashboardRouteObserver.unsubscribe(this);
    WidgetsBinding.instance.removeObserver(this);
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final shopsState = ref.watch(shopsProvider);
    final shopId = authorizedShopId(shopsState);

    if (shopsState.status == RemoteStatus.loading) {
      return _scaffold(
        context,
        const Center(child: CircularProgressIndicator()),
      );
    }
    if (shopsState.status == RemoteStatus.unauthorized) {
      return _scaffold(context, const _Message('登入狀態已失效'));
    }
    if (shopsState.status == RemoteStatus.error) {
      return _scaffold(context, const _Message('目前無法取得店家資料'));
    }
    if (shopId == null || shopId.trim().isEmpty) {
      _notifier?.setRouteVisible(false);
      _notifier = null;
      return _scaffold(context, const _Message('尚未選擇店家'));
    }

    final notifier = ref.read(dashboardProvider(shopId).notifier);
    if (!identical(_notifier, notifier)) {
      _notifier?.setRouteVisible(false);
      _notifier = notifier;
    }
    _syncNotifier();
    final dashboardState = ref.watch(dashboardProvider(shopId));
    return _scaffold(
      context,
      _dashboardBody(context, ref, dashboardState, shopId),
    );
  }

  Widget _dashboardBody(
    BuildContext context,
    WidgetRef ref,
    DashboardState state,
    String shopId,
  ) {
    switch (state.status) {
      case DashboardStatus.loading:
        return const Center(child: CircularProgressIndicator());
      case DashboardStatus.unauthorized:
        return const _Message('登入狀態已失效');
      case DashboardStatus.notFound:
        // Do not expose whether the shop is missing, inactive, or unauthorized.
        return const _Message('目前無法取得此店家的儀表板資料');
      case DashboardStatus.error:
        return Center(
          child: _Message(
            '儀表板資料暫時無法取得',
            action: TextButton(
              onPressed: () =>
                  ref.read(dashboardProvider(shopId).notifier).load(),
              child: const Text('重試'),
            ),
          ),
        );
      case DashboardStatus.success:
        final dashboard = state.data!;
        return _DashboardContent(
          dashboard: dashboard,
          isCached: state.isDurableCache,
          cachedAt: state.cachedAt,
          onSwitchShop: () => context.push('/shops'),
        );
    }
  }

  Widget _scaffold(BuildContext context, Widget body) {
    return Scaffold(
      body: SafeArea(child: body),
      bottomNavigationBar: _BottomNav(context: context),
    );
  }
}

class _DashboardContent extends StatelessWidget {
  const _DashboardContent({
    required this.dashboard,
    required this.isCached,
    required this.cachedAt,
    required this.onSwitchShop,
  });

  final Dashboard dashboard;
  final bool isCached;
  final DateTime? cachedAt;
  final VoidCallback onSwitchShop;

  @override
  Widget build(BuildContext context) {
    return SingleChildScrollView(
      padding: const EdgeInsets.all(20),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            mainAxisAlignment: MainAxisAlignment.spaceBetween,
            children: [
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    const Text(
                      '儀表板',
                      style: TextStyle(
                        fontSize: 28,
                        fontWeight: FontWeight.bold,
                        color: AppTheme.textPrimary,
                      ),
                    ),
                    const SizedBox(height: 4),
                    Text(
                      dashboard.shop.name,
                      style: const TextStyle(
                        fontSize: 16,
                        color: AppTheme.textSecondary,
                        fontWeight: FontWeight.w500,
                      ),
                    ),
                  ],
                ),
              ),
              Container(
                decoration: BoxDecoration(
                  color: Colors.white,
                  shape: BoxShape.circle,
                  boxShadow: [
                    BoxShadow(
                      color: Colors.black.withValues(alpha: 0.05),
                      blurRadius: 10,
                      offset: const Offset(0, 4),
                    ),
                  ],
                ),
                child: IconButton(
                  icon: const Icon(Icons.swap_horiz_rounded),
                  color: AppTheme.primaryColor,
                  onPressed: onSwitchShop,
                ),
              ),
            ],
          ),
          if (isCached && cachedAt != null) ...[
            _StaleDashboardBanner(cachedAt: cachedAt!),
            const SizedBox(height: 16),
          ],
          const SizedBox(height: 24),
          _PowerCard(powerW: dashboard.currentPowerW),
          const SizedBox(height: 16),
          _EnergySummary(
            dailyKwh: dashboard.dailyKwh,
            monthlyKwh: dashboard.monthlyKwh,
            dailyKg: dashboard.dailyKg,
            monthlyKg: dashboard.monthlyKg,
          ),
          const SizedBox(height: 12),
          _BillingEstimateEntry(shopId: dashboard.shop.id),
          const SizedBox(height: 12),
          const _HistoricalEnergyReportEntry(),
          const SizedBox(height: 24),
          const Text(
            '設備狀態',
            style: TextStyle(fontSize: 18, fontWeight: FontWeight.bold),
          ),
          const SizedBox(height: 12),
          if (dashboard.devices.isEmpty)
            const _Message('目前沒有設備')
          else
            _DeviceGrid(shopId: dashboard.shop.id, devices: dashboard.devices),
        ],
      ),
    );
  }
}

class _StaleDashboardBanner extends StatelessWidget {
  const _StaleDashboardBanner({required this.cachedAt});

  final DateTime cachedAt;

  @override
  Widget build(BuildContext context) {
    return Semantics(
      label: 'Saved data. Last updated ${_formatDate(cachedAt)}. '
          'Live refresh unavailable.',
      child: Container(
        width: double.infinity,
        padding: const EdgeInsets.all(12),
        decoration: BoxDecoration(
          color: Colors.amber.shade50,
          border: Border.all(color: Colors.amber.shade700),
          borderRadius: BorderRadius.circular(12),
        ),
        child: Text(
          '已儲存資料｜最後更新 ${_formatDate(cachedAt)}｜即時更新暫時無法取得',
          style: TextStyle(
            color: Colors.amber.shade900,
            fontWeight: FontWeight.w600,
          ),
        ),
      ),
    );
  }
}

class _BillingEstimateEntry extends ConsumerWidget {
  const _BillingEstimateEntry({required this.shopId});
  final String shopId;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final now = DateTime.now();
    final month =
        '${now.year.toString().padLeft(4, '0')}-${now.month.toString().padLeft(2, '0')}';
    final state = ref.watch(
      billingEstimateProvider((shopId: shopId, month: month)),
    );
    final label = state.when(
      loading: () => '查看本月預估電費',
      error: (_, __) => '查看本月預估電費',
      data: (estimate) {
        final charges = estimate.charges;
        if (charges == null || !estimate.hasAmount) return '本月預估電費（無資料）';
        final suffix =
            estimate.status == BillingEstimateStatus.partialDataEstimate
                ? '（部分資料）'
                : '';
        return '本月預估電費  NT\$${charges.estimatedTotal}$suffix';
      },
    );
    return Card(
      child: ListTile(
        leading: const Icon(Icons.receipt_long_rounded),
        title: Text(label),
        subtitle: const Text('監測資料完整度與級距明細'),
        trailing: const Icon(Icons.chevron_right),
        onTap: () => context.push('/billing/estimate'),
      ),
    );
  }
}

class _HistoricalEnergyReportEntry extends StatelessWidget {
  const _HistoricalEnergyReportEntry();

  @override
  Widget build(BuildContext context) => Card(
        child: ListTile(
          leading: const Icon(Icons.bar_chart_rounded),
          title: const Text('查看歷史用電報表'),
          subtitle: const Text('依 Measurement Point 檢視每月用電與完整度'),
          trailing: const Icon(Icons.chevron_right),
          onTap: () => context.push('/reports/energy'),
        ),
      );
}

class _PowerCard extends StatelessWidget {
  const _PowerCard({required this.powerW});

  final double? powerW;

  @override
  Widget build(BuildContext context) {
    final value = powerW == null ? '無資料' : '${_formatNumber(powerW!)} W';
    return Container(
      width: double.infinity,
      padding: const EdgeInsets.all(24),
      decoration: BoxDecoration(
        gradient: const LinearGradient(
          colors: [Color(0xFF1B5E20), Color(0xFF43A047)],
          begin: Alignment.topLeft,
          end: Alignment.bottomRight,
        ),
        borderRadius: BorderRadius.circular(24),
        boxShadow: [
          BoxShadow(
            color: const Color(0xFF43A047).withValues(alpha: 0.4),
            blurRadius: 20,
            offset: const Offset(0, 10),
          ),
        ],
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          const Text(
            '即時功率',
            style: TextStyle(color: Colors.white70, fontSize: 16),
          ),
          const SizedBox(height: 12),
          Text(
            value,
            style: const TextStyle(
              color: Colors.white,
              fontSize: 40,
              fontWeight: FontWeight.bold,
            ),
          ),
          const SizedBox(height: 12),
          Text(
            powerW == null ? '目前沒有可用的量測資料' : '來自伺服器的即時量測',
            style: const TextStyle(color: Colors.white70),
          ),
        ],
      ),
    );
  }
}

class _EnergySummary extends StatelessWidget {
  const _EnergySummary({
    required this.dailyKwh,
    required this.monthlyKwh,
    required this.dailyKg,
    required this.monthlyKg,
  });

  final double? dailyKwh;
  final double? monthlyKwh;
  final double? dailyKg;
  final double? monthlyKg;

  @override
  Widget build(BuildContext context) {
    return Column(
      children: [
        Row(
          children: [
            Expanded(
              child: _EnergyCard(label: '本日用電量', kwh: dailyKwh),
            ),
            const SizedBox(width: 12),
            Expanded(
              child: _EnergyCard(label: '本月用電量', kwh: monthlyKwh),
            ),
          ],
        ),
        const SizedBox(height: 12),
        Row(
          children: [
            Expanded(
              child: _CarbonCard(label: '本日碳排放量', kg: dailyKg),
            ),
            const SizedBox(width: 12),
            Expanded(
              child: _CarbonCard(label: '本月碳排放量', kg: monthlyKg),
            ),
          ],
        ),
      ],
    );
  }
}

class _CarbonCard extends StatelessWidget {
  const _CarbonCard({required this.label, required this.kg});
  final String label;
  final double? kg;

  @override
  Widget build(BuildContext context) => _EnergyCard(
        label: label,
        kwh: kg,
        unit: 'kgCO₂e',
        fixedTwoDecimals: true,
        zeroAsInteger: true,
      );
}

class _EnergyCard extends StatelessWidget {
  const _EnergyCard({
    required this.label,
    required this.kwh,
    this.unit = 'kWh',
    this.fixedTwoDecimals = false,
    this.zeroAsInteger = false,
  });

  final String label;
  final double? kwh;
  final String unit;
  final bool fixedTwoDecimals;
  final bool zeroAsInteger;

  @override
  Widget build(BuildContext context) {
    final value = kwh == null
        ? '無資料'
        : '${fixedTwoDecimals && !(zeroAsInteger && kwh == 0) ? kwh!.toStringAsFixed(2) : _formatNumber(kwh!)} $unit';
    return Container(
      padding: const EdgeInsets.all(18),
      decoration: BoxDecoration(
        color: Colors.white,
        borderRadius: BorderRadius.circular(20),
        boxShadow: [
          BoxShadow(
            color: Colors.black.withValues(alpha: 0.05),
            blurRadius: 10,
            offset: const Offset(0, 4),
          ),
        ],
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(
            label,
            style: const TextStyle(color: AppTheme.textSecondary, fontSize: 14),
          ),
          const SizedBox(height: 10),
          Text(
            value,
            style: const TextStyle(
              color: AppTheme.textPrimary,
              fontSize: 22,
              fontWeight: FontWeight.bold,
            ),
          ),
        ],
      ),
    );
  }
}

class _DeviceGrid extends StatelessWidget {
  const _DeviceGrid({required this.shopId, required this.devices});

  final String shopId;
  final List<DashboardDevice> devices;

  @override
  Widget build(BuildContext context) {
    return GridView.builder(
      shrinkWrap: true,
      physics: const NeverScrollableScrollPhysics(),
      gridDelegate: const SliverGridDelegateWithFixedCrossAxisCount(
        crossAxisCount: 2,
        crossAxisSpacing: 16,
        mainAxisSpacing: 16,
        childAspectRatio: 1.05,
      ),
      itemCount: devices.length,
      itemBuilder: (context, index) =>
          _DeviceCard(shopId: shopId, device: devices[index]),
    );
  }
}

class _DeviceCard extends StatelessWidget {
  const _DeviceCard({required this.shopId, required this.device});

  final String shopId;
  final DashboardDevice device;

  @override
  Widget build(BuildContext context) {
    final online = device.isOnline;
    return InkWell(
      borderRadius: BorderRadius.circular(20),
      onTap: device.measurementPointRef.isEmpty
          ? null
          : () => context.push(
                '/shops/${Uri.encodeComponent(shopId)}/measurement-points/${Uri.encodeComponent(device.measurementPointRef)}',
              ),
      child: Container(
        decoration: BoxDecoration(
          color: Colors.white,
          borderRadius: BorderRadius.circular(20),
          border: online ? null : Border.all(color: Colors.grey.shade200),
          boxShadow: [
            BoxShadow(
              color: Colors.black.withValues(alpha: 0.05),
              blurRadius: 10,
              offset: const Offset(0, 4),
            ),
          ],
        ),
        padding: const EdgeInsets.all(16),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          mainAxisAlignment: MainAxisAlignment.spaceBetween,
          children: [
            Row(
              mainAxisAlignment: MainAxisAlignment.spaceBetween,
              children: [
                Icon(
                  Icons.electrical_services_rounded,
                  color: online ? AppTheme.primaryColor : Colors.grey,
                  size: 26,
                ),
                Container(
                  width: 8,
                  height: 8,
                  decoration: BoxDecoration(
                    color: online ? Colors.green : Colors.red,
                    shape: BoxShape.circle,
                  ),
                ),
              ],
            ),
            Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(
                  device.name,
                  maxLines: 2,
                  overflow: TextOverflow.ellipsis,
                  style: TextStyle(
                    fontWeight: FontWeight.bold,
                    fontSize: 16,
                    color: online ? Colors.black87 : Colors.grey,
                  ),
                ),
                const SizedBox(height: 4),
                Text(
                  online ? '運轉中' : '已離線',
                  style: TextStyle(
                    fontSize: 12,
                    color: online ? AppTheme.secondaryColor : Colors.grey,
                  ),
                ),
                const SizedBox(height: 2),
                Text(
                  device.lastSeen == null
                      ? '最後連線未知'
                      : '最後連線 ${_formatDate(device.lastSeen!)}',
                  style: const TextStyle(fontSize: 11, color: Colors.grey),
                ),
              ],
            ),
          ],
        ),
      ),
    );
  }
}

class _Message extends StatelessWidget {
  const _Message(this.message, {this.action});

  final String message;
  final Widget? action;

  @override
  Widget build(BuildContext context) {
    return Center(
      child: Column(
        mainAxisSize: MainAxisSize.min,
        children: [
          Text(message, textAlign: TextAlign.center),
          if (action != null) action!,
        ],
      ),
    );
  }
}

class _BottomNav extends StatelessWidget {
  const _BottomNav({required this.context});

  final BuildContext context;

  @override
  Widget build(BuildContext context) {
    return Container(
      margin: const EdgeInsets.only(left: 20, right: 20, bottom: 20),
      height: 70,
      decoration: BoxDecoration(
        color: Colors.white,
        borderRadius: BorderRadius.circular(35),
        boxShadow: [
          BoxShadow(
            color: Colors.black.withValues(alpha: 0.1),
            blurRadius: 20,
            offset: const Offset(0, 10),
          ),
        ],
      ),
      child: Row(
        mainAxisAlignment: MainAxisAlignment.spaceAround,
        children: [
          _NavIcon(
            icon: Icons.home_rounded,
            isSelected: true,
            label: '首頁',
            onTap: () {},
          ),
          _NavIcon(
            icon: Icons.electrical_services_rounded,
            isSelected: false,
            label: '設備',
            onTap: () => this.context.go('/devices'),
          ),
          _NavIcon(
            icon: Icons.person_rounded,
            isSelected: false,
            label: '個人',
            onTap: () => this.context.go('/profile'),
          ),
          _NavIcon(
            icon: Icons.store_rounded,
            isSelected: false,
            label: '店家',
            onTap: () => this.context.go('/shops'),
          ),
        ],
      ),
    );
  }
}

class _NavIcon extends StatelessWidget {
  const _NavIcon({
    required this.icon,
    required this.label,
    required this.isSelected,
    required this.onTap,
  });

  final IconData icon;
  final String label;
  final bool isSelected;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    return GestureDetector(
      onTap: onTap,
      behavior: HitTestBehavior.opaque,
      child: Column(
        mainAxisAlignment: MainAxisAlignment.center,
        children: [
          Icon(
            icon,
            color: isSelected ? AppTheme.primaryColor : Colors.grey.shade400,
            size: 26,
          ),
          const SizedBox(height: 4),
          Text(
            label,
            style: TextStyle(
              fontSize: 10,
              fontWeight: FontWeight.w600,
              color: isSelected ? AppTheme.primaryColor : Colors.grey.shade400,
            ),
          ),
        ],
      ),
    );
  }
}

String _formatNumber(double value) {
  if (value == value.roundToDouble()) return value.toInt().toString();
  return value.toStringAsFixed(2);
}

String _formatDate(DateTime value) {
  final local = value.toLocal();
  final minute = local.minute.toString().padLeft(2, '0');
  return '${local.month}/${local.day} ${local.hour}:$minute';
}
