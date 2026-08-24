import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:power_iot_app/config/theme.dart';
import 'package:power_iot_app/features/dashboard/domain/models/dashboard.dart';
import 'package:power_iot_app/features/dashboard/presentation/providers/dashboard_provider.dart';
import 'package:power_iot_app/features/profile/presentation/providers/profile_provider.dart';
import 'package:power_iot_app/features/shops/providers/remote_shop_provider.dart';

/// Device Management renders the device projection already authorized by B7.
/// It deliberately has no device-specific repository or local fixture source.
class DeviceListScreen extends ConsumerWidget {
  const DeviceListScreen({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final shopsState = ref.watch(shopsProvider);
    final snapshot = shopsState.data;
    final shopId = shopsState.selectedShopId ?? snapshot?.currentShopId;

    Widget body;
    if (shopsState.status == RemoteStatus.loading) {
      body = const Center(child: CircularProgressIndicator());
    } else if (shopsState.status == RemoteStatus.unauthorized) {
      body = const _Message('登入狀態已失效，請重新登入');
    } else if (shopsState.status == RemoteStatus.error) {
      body = _RetryMessage(
        message: '目前無法取得店家資料',
        onRetry: () => ref.read(shopsProvider.notifier).load(),
      );
    } else if (shopId == null || shopId.trim().isEmpty) {
      body = const _Message('尚未選擇店家');
    } else {
      final dashboardState = ref.watch(dashboardProvider(shopId));
      body = _dashboardBody(context, dashboardState, shopId, ref);
    }

    return Scaffold(
      appBar: AppBar(
        title: const Text('設備管理'),
        centerTitle: true,
        backgroundColor: Colors.transparent,
        foregroundColor: AppTheme.textPrimary,
        elevation: 0,
      ),
      body: SafeArea(child: body),
      bottomNavigationBar: _buildBottomNav(context),
    );
  }

  Widget _dashboardBody(
    BuildContext context,
    DashboardState state,
    String shopId,
    WidgetRef ref,
  ) {
    switch (state.status) {
      case DashboardStatus.loading:
        return const Center(child: CircularProgressIndicator());
      case DashboardStatus.unauthorized:
        return const _Message('登入狀態已失效，請重新登入');
      case DashboardStatus.notFound:
        return const _Message('目前無法取得此店家的設備資料');
      case DashboardStatus.error:
        return _RetryMessage(
          message: '目前無法取得設備資料',
          onRetry: () => ref.read(dashboardProvider(shopId).notifier).load(),
        );
      case DashboardStatus.success:
        final dashboard = state.data;
        if (dashboard == null || dashboard.devices.isEmpty) {
          return const _Message('目前沒有設備');
        }
        return _deviceList(context, shopId, dashboard.devices);
    }
  }

  Widget _deviceList(
      BuildContext context, String shopId, List<DashboardDevice> devices) {
    final online = devices.where((device) => device.isOnline).toList();
    final offline = devices.where((device) => !device.isOnline).toList();
    return ListView(
      padding: const EdgeInsets.fromLTRB(20, 4, 20, 24),
      children: [
        _allDevicesCard(devices.length),
        if (online.isNotEmpty) ...[
          const SizedBox(height: 24),
          _sectionTitle('已連線', Colors.green),
          const SizedBox(height: 12),
          ...online.map((device) => _deviceCard(context, shopId, device)),
        ],
        if (offline.isNotEmpty) ...[
          const SizedBox(height: 24),
          _sectionTitle('連線中斷', AppTheme.errorColor),
          const SizedBox(height: 12),
          ...offline.map((device) => _deviceCard(context, shopId, device)),
        ],
      ],
    );
  }

  Widget _allDevicesCard(int count) {
    return Container(
      padding: const EdgeInsets.all(20),
      decoration: BoxDecoration(
        color: Colors.white,
        borderRadius: BorderRadius.circular(20),
        boxShadow: [
          BoxShadow(
            color: AppTheme.primaryColor.withValues(alpha: 0.1),
            blurRadius: 10,
            offset: const Offset(0, 4),
          ),
        ],
      ),
      child: Row(
        children: [
          Container(
            padding: const EdgeInsets.all(12),
            decoration: BoxDecoration(
              color: AppTheme.primaryColor.withValues(alpha: 0.1),
              shape: BoxShape.circle,
            ),
            child:
                const Icon(Icons.power, color: AppTheme.primaryColor, size: 28),
          ),
          const SizedBox(width: 16),
          const Text(
            '所有設備',
            style: TextStyle(
              fontSize: 18,
              fontWeight: FontWeight.bold,
              color: AppTheme.textPrimary,
            ),
          ),
          const Spacer(),
          Text('$count', style: const TextStyle(color: AppTheme.textSecondary)),
        ],
      ),
    );
  }

  Widget _sectionTitle(String title, Color color) {
    return Row(
      children: [
        Container(
            width: 10,
            height: 10,
            decoration: BoxDecoration(color: color, shape: BoxShape.circle)),
        const SizedBox(width: 8),
        Text(title,
            style: const TextStyle(
                fontSize: 16,
                fontWeight: FontWeight.bold,
                color: AppTheme.textSecondary)),
      ],
    );
  }

  Widget _deviceCard(
      BuildContext context, String shopId, DashboardDevice device) {
    final canOpenDetail = device.measurementPointRef.trim().isNotEmpty;
    return InkWell(
      borderRadius: BorderRadius.circular(20),
      onTap: canOpenDetail
          ? () => context.push(
                '/shops/${Uri.encodeComponent(shopId)}/measurement-points/${Uri.encodeComponent(device.measurementPointRef)}',
              )
          : null,
      child: Container(
        margin: const EdgeInsets.only(bottom: 12),
        padding: const EdgeInsets.all(16),
        decoration: BoxDecoration(
          color: Colors.white,
          borderRadius: BorderRadius.circular(20),
          border:
              device.isOnline ? null : Border.all(color: Colors.grey.shade200),
          boxShadow: [
            BoxShadow(
                color: Colors.black.withValues(alpha: 0.05),
                blurRadius: 10,
                offset: const Offset(0, 4)),
          ],
        ),
        child: Row(
          children: [
            Container(
              padding: const EdgeInsets.all(12),
              decoration: BoxDecoration(
                color: device.isOnline
                    ? AppTheme.secondaryColor.withValues(alpha: 0.15)
                    : Colors.grey.shade100,
                shape: BoxShape.circle,
              ),
              child: Icon(Icons.power_rounded,
                  color: device.isOnline ? AppTheme.primaryColor : Colors.grey,
                  size: 24),
            ),
            const SizedBox(width: 16),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(device.name,
                      style: TextStyle(
                          fontSize: 16,
                          fontWeight: FontWeight.bold,
                          color: device.isOnline
                              ? AppTheme.textPrimary
                              : Colors.grey)),
                  const SizedBox(height: 4),
                  Text(
                    device.lastSeen == null
                        ? '最後上線時間未知'
                        : '最後上線：${device.lastSeen!.toLocal().toIso8601String()}',
                    style: TextStyle(fontSize: 12, color: Colors.grey.shade600),
                  ),
                ],
              ),
            ),
            if (canOpenDetail) const Icon(Icons.chevron_right_rounded),
          ],
        ),
      ),
    );
  }

  Widget _buildBottomNav(BuildContext context) {
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
              offset: const Offset(0, 10))
        ],
      ),
      child: Row(
        mainAxisAlignment: MainAxisAlignment.spaceAround,
        children: [
          _NavIcon(
              icon: Icons.home_rounded,
              isSelected: false,
              label: '首頁',
              onTap: () => context.go('/dashboard')),
          _NavIcon(
              icon: Icons.electrical_services_rounded,
              isSelected: true,
              label: '設備',
              onTap: () {}),
          _NavIcon(
              icon: Icons.person_rounded,
              isSelected: false,
              label: '個人',
              onTap: () => context.go('/profile')),
          _NavIcon(
              icon: Icons.store_rounded,
              isSelected: false,
              label: '店家',
              onTap: () => context.go('/shops')),
        ],
      ),
    );
  }
}

class _Message extends StatelessWidget {
  const _Message(this.text);
  final String text;
  @override
  Widget build(BuildContext context) => Center(child: Text(text));
}

class _RetryMessage extends StatelessWidget {
  const _RetryMessage({required this.message, required this.onRetry});
  final String message;
  final VoidCallback onRetry;
  @override
  Widget build(BuildContext context) => Center(
        child: Column(mainAxisSize: MainAxisSize.min, children: [
          Text(message),
          const SizedBox(height: 12),
          OutlinedButton(onPressed: onRetry, child: const Text('重試')),
        ]),
      );
}

class _NavIcon extends StatelessWidget {
  const _NavIcon(
      {required this.icon,
      required this.label,
      required this.isSelected,
      required this.onTap});
  final IconData icon;
  final String label;
  final bool isSelected;
  final VoidCallback onTap;
  @override
  Widget build(BuildContext context) => GestureDetector(
        onTap: onTap,
        behavior: HitTestBehavior.opaque,
        child: Column(mainAxisAlignment: MainAxisAlignment.center, children: [
          Icon(icon,
              color: isSelected ? AppTheme.primaryColor : Colors.grey.shade400,
              size: 26),
          const SizedBox(height: 4),
          Text(label,
              style: TextStyle(
                  fontSize: 10,
                  fontWeight: FontWeight.w600,
                  color: isSelected
                      ? AppTheme.primaryColor
                      : Colors.grey.shade400)),
        ]),
      );
}
