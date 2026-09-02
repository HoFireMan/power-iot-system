// #C:\Code\PowerWork\power-iot-system\mobile\lib\features\shops\screens\shop_list_screen.dart
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:power_iot_app/config/theme.dart';
import 'package:power_iot_app/features/shops/domain/models/shop.dart' as remote;
import 'package:power_iot_app/features/profile/presentation/providers/profile_provider.dart';
import 'package:power_iot_app/features/shops/providers/remote_shop_provider.dart';
import 'package:power_iot_app/features/shops/domain/repositories/shops_repository.dart';
import 'package:power_iot_app/features/billing/domain/models/billing_configuration.dart';
import 'package:power_iot_app/features/billing/presentation/providers/billing_configuration_provider.dart';

const _commercialTariffs = <MapEntry<String, String>>[
  MapEntry('LIGHTING_COMMERCIAL', '表燈營業'),
  MapEntry('LOW_VOLTAGE', '低壓電力'),
  MapEntry('HIGH_VOLTAGE', '高壓電力'),
  MapEntry('EXTRA_HIGH_VOLTAGE', '特高壓電力'),
];
const _nonCommercialTariffs = <MapEntry<String, String>>[
  MapEntry('LIGHTING_NONCOMMERCIAL', '表燈非營業'),
  MapEntry('PACKAGE_LIGHTING', '包燈'),
];

class ShopListScreen extends ConsumerWidget {
  const ShopListScreen({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final shopState = ref.watch(shopsProvider);
    final snapshot = shopState.data;
    final shops = snapshot?.shops ?? const <remote.Shop>[];
    // Selection is a local/view preference. Every candidate must still be in
    // the server-returned authorized list; CurrentShopID never grants access.
    final currentShopId = authorizedShopId(shopState);
    final profile = ref.watch(profileProvider);
    final isAdmin =
        profile.status == RemoteStatus.success && profile.data?.isAdmin == true;

    return Scaffold(
      appBar: AppBar(
        title: const Text("店家管理"),
        centerTitle: true,
        backgroundColor: Colors.transparent,
        foregroundColor: AppTheme.textPrimary,
        leading: IconButton(
          icon: const Icon(Icons.arrow_back_ios_new_rounded, size: 20),
          onPressed: () => context.pop(),
        ),
      ),
      body: SafeArea(
        child: _buildBody(
          context,
          ref,
          shopState,
          shops,
          currentShopId,
          isAdmin,
        ),
      ),
      // The selection above is intentionally view-only; dashboard data is not
      // loaded or authorized by this screen.
      bottomNavigationBar: _buildBottomNav(context),
    );
  }

  Widget _buildBody(
    BuildContext context,
    WidgetRef ref,
    ShopsState shopState,
    List<remote.Shop> shops,
    String? currentShopId,
    bool isAdmin,
  ) {
    if (shopState.status == RemoteStatus.loading) {
      return const Center(child: CircularProgressIndicator());
    }
    if (shopState.status == RemoteStatus.unauthorized) {
      return const Center(child: Text('登入已失效，請重新登入'));
    }
    if (shopState.status == RemoteStatus.error) {
      return Center(
        child: OutlinedButton(
          onPressed: () => ref.read(shopsProvider.notifier).load(),
          child: const Text('載入店家失敗，重試'),
        ),
      );
    }
    return Column(
      children: [
        Padding(
          padding: const EdgeInsets.symmetric(horizontal: 20, vertical: 12),
          child: Row(
            mainAxisAlignment: MainAxisAlignment.end,
            children: [
              const Icon(
                Icons.star_rounded,
                color: AppTheme.accentColor,
                size: 20,
              ),
              const SizedBox(width: 6),
              Text(
                '目前檢視店家（僅限已授權店家）',
                style: TextStyle(
                  fontSize: 14,
                  color: Colors.grey.shade600,
                  fontWeight: FontWeight.bold,
                ),
              ),
            ],
          ),
        ),
        Expanded(
          child: shops.isEmpty
              ? const Center(child: Text('目前沒有可用店家'))
              : ListView.builder(
                  padding: const EdgeInsets.symmetric(horizontal: 20),
                  itemCount: shops.length,
                  itemBuilder: (context, index) {
                    final shop = shops[index];
                    return _buildShopCard(
                      context,
                      ref,
                      shop,
                      shop.id == currentShopId,
                      isAdmin: isAdmin,
                    );
                  },
                ),
        ),
        Container(
          padding: const EdgeInsets.all(16),
          alignment: Alignment.center,
          child: Text(
            '僅顯示伺服器授權的店家',
            style: TextStyle(fontSize: 12, color: Colors.grey.shade400),
          ),
        ),
      ],
    );
  }

  Widget _buildShopCard(
    BuildContext context,
    WidgetRef ref,
    remote.Shop shop,
    bool isSelected, {
    required bool isAdmin,
  }) {
    return GestureDetector(
      onTap: () {
        ref.read(shopsProvider.notifier).selectShop(shop.id);
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(
            content: Text("已切換至：${shop.name}"),
            duration: const Duration(seconds: 1),
            backgroundColor: AppTheme.primaryColor,
          ),
        );
      },
      child: Container(
        margin: const EdgeInsets.only(bottom: 16),
        decoration: BoxDecoration(
          color: Colors.white,
          borderRadius: BorderRadius.circular(20),
          border: isSelected
              ? Border.all(color: AppTheme.primaryColor, width: 2)
              : null,
          boxShadow: [
            BoxShadow(
              color: isSelected
                  ? AppTheme.primaryColor.withValues(alpha: 0.1)
                  : Colors.black.withValues(alpha: 0.05),
              blurRadius: 10,
              offset: const Offset(0, 4),
            ),
          ],
        ),
        child: Column(
          children: [
            Container(
              padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 12),
              decoration: BoxDecoration(
                color: isSelected ? AppTheme.primaryColor : Colors.grey.shade50,
                borderRadius: const BorderRadius.vertical(
                  top: Radius.circular(18),
                ),
              ),
              child: Row(
                children: [
                  Icon(
                    isSelected ? Icons.star_rounded : Icons.star_border_rounded,
                    color: isSelected
                        ? AppTheme.accentColor
                        : Colors.grey.shade400,
                    size: 24,
                  ),
                  const SizedBox(width: 8),
                  Container(
                    padding: const EdgeInsets.symmetric(
                      horizontal: 8,
                      vertical: 2,
                    ),
                    decoration: BoxDecoration(
                      color: isSelected
                          ? Colors.white.withValues(alpha: 0.2)
                          : Colors.grey.shade200,
                      borderRadius: BorderRadius.circular(4),
                    ),
                    child: Text(
                      shop.isHead ? "總部" : "分店",
                      style: TextStyle(
                        fontSize: 12,
                        color: isSelected ? Colors.white : Colors.grey.shade600,
                      ),
                    ),
                  ),
                ],
              ),
            ),
            Padding(
              padding: const EdgeInsets.all(16),
              child: Row(
                children: [
                  Container(
                    padding: const EdgeInsets.all(12),
                    decoration: BoxDecoration(
                      color: isSelected
                          ? AppTheme.primaryColor.withValues(alpha: 0.1)
                          : AppTheme.backgroundColor,
                      borderRadius: BorderRadius.circular(12),
                    ),
                    child: Icon(
                      Icons.storefront_rounded,
                      size: 32,
                      color: isSelected
                          ? AppTheme.primaryColor
                          : Colors.grey.shade400,
                    ),
                  ),
                  const SizedBox(width: 16),
                  Expanded(
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Text(
                          shop.name,
                          style: const TextStyle(
                            fontSize: 18,
                            fontWeight: FontWeight.bold,
                            color: AppTheme.textPrimary,
                          ),
                        ),
                        const SizedBox(height: 6),
                        Row(
                          children: [
                            Icon(
                              Icons.location_on_outlined,
                              size: 14,
                              color: Colors.grey.shade500,
                            ),
                            const SizedBox(width: 4),
                            Expanded(
                              child: Text(
                                shop.address ?? '未提供地址',
                                style: TextStyle(
                                  fontSize: 13,
                                  color: Colors.grey.shade600,
                                ),
                                maxLines: 1,
                                overflow: TextOverflow.ellipsis,
                              ),
                            ),
                          ],
                        ),
                      ],
                    ),
                  ),
                  IconButton(
                    tooltip: '警報紀錄',
                    icon: const Icon(Icons.notifications_none_rounded),
                    onPressed: () => context.push('/shops/${shop.id}/alerts'),
                  ),
                  IconButton(
                    tooltip: '電費方案設定',
                    icon: const Icon(Icons.receipt_long_rounded),
                    onPressed: () =>
                        _showBillingSettings(context, ref, shop, isAdmin),
                  ),
                  if (isAdmin)
                    IconButton(
                      tooltip: '電價分類設定',
                      icon: const Icon(Icons.tune_rounded),
                      onPressed: () => _showTariffSettings(context, ref, shop),
                    ),
                ],
              ),
            ),
          ],
        ),
      ),
    );
  }

  Future<void> _showBillingSettings(
    BuildContext context,
    WidgetRef ref,
    remote.Shop shop,
    bool editable,
  ) async {
    try {
      final configuration =
          await ref.read(billingConfigurationProvider(shop.id).future);
      if (!context.mounted) return;
      final selected = await showDialog<String>(
        context: context,
        builder: (context) => BillingConfigurationDialog(
            configuration: configuration, editable: editable),
      );
      if (selected == null || !editable || !context.mounted) return;
      await ref
          .read(billingConfigurationRepositoryProvider)
          .setPlan(shop.id, selected);
      ref.invalidate(billingConfigurationProvider(shop.id));
      if (context.mounted) {
        ScaffoldMessenger.of(context)
            .showSnackBar(const SnackBar(content: Text('電費方案已更新')));
      }
    } catch (_) {
      if (context.mounted) {
        ScaffoldMessenger.of(context)
            .showSnackBar(const SnackBar(content: Text('電費方案載入或更新失敗')));
      }
    }
  }

  Future<void> _showTariffSettings(
    BuildContext context,
    WidgetRef ref,
    remote.Shop shop,
  ) async {
    final selected = await showDialog<String>(
      context: context,
      builder: (context) => _TariffDialog(current: shop.tariff),
    );
    if (selected == null || !context.mounted) return;
    try {
      final mutation = ref.read(shopsRepositoryProvider);
      if (mutation is! ShopTariffMutation) {
        throw StateError('shop tariff mutation unavailable');
      }
      await (mutation as ShopTariffMutation).updateTariff(shop.id, selected);
      await ref.read(shopsProvider.notifier).load();
      if (context.mounted) {
        ScaffoldMessenger.of(
          context,
        ).showSnackBar(const SnackBar(content: Text('電價分類已更新')));
      }
    } catch (_) {
      if (context.mounted) {
        ScaffoldMessenger.of(
          context,
        ).showSnackBar(const SnackBar(content: Text('電價分類更新失敗')));
      }
    }
  }

  // --- 統一的導航欄 ---
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
            offset: const Offset(0, 10),
          ),
        ],
      ),
      child: Row(
        mainAxisAlignment: MainAxisAlignment.spaceAround,
        children: [
          // 1. 首頁 (跳轉)
          _NavIcon(
            icon: Icons.home_rounded,
            isSelected: false,
            label: "首頁",
            onTap: () => context.go('/dashboard'),
          ),
          // 2. 設備 (跳轉)
          _NavIcon(
            icon: Icons.electrical_services_rounded,
            isSelected: false,
            label: "設備",
            onTap: () => context.go('/devices'),
          ),
          // 3. 個人 (跳轉)
          _NavIcon(
            icon: Icons.person_rounded,
            isSelected: false,
            label: "個人",
            onTap: () => context.go('/profile'),
          ),
          // 4. 店家 (當前頁，isSelected: true)
          _NavIcon(
            icon: Icons.store_rounded,
            isSelected: true,
            label: "店家",
            onTap: () {}, // 已經在店家頁，不需動作
          ),
        ],
      ),
    );
  }
}

class BillingConfigurationDialog extends StatefulWidget {
  const BillingConfigurationDialog(
      {required this.configuration, required this.editable, super.key});
  final BillingConfiguration configuration;
  final bool editable;

  @override
  State<BillingConfigurationDialog> createState() => _BillingDialogState();
}

class _BillingDialogState extends State<BillingConfigurationDialog> {
  String? selected;

  @override
  void initState() {
    super.initState();
    selected = widget.configuration.scheduledAssignment?.planCode ??
        widget.configuration.currentAssignment?.planCode ??
        (widget.configuration.plans.isEmpty
            ? null
            : widget.configuration.plans.first.code);
  }

  @override
  Widget build(BuildContext context) {
    final configuration = widget.configuration;
    if (!configuration.supported) {
      return AlertDialog(
        title: const Text('電費方案設定'),
        content: const Text('目前尚未支援此電價類型的電費估算'),
        actions: [
          TextButton(
              onPressed: () => Navigator.pop(context), child: const Text('關閉'))
        ],
      );
    }
    return AlertDialog(
      title: const Text('電費方案設定'),
      content: SizedBox(
        width: double.maxFinite,
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(configuration.currentAssignment == null
                ? '尚未設定計費方案'
                : '目前方案：${_planLabel(configuration.currentAssignment!.planCode)}'),
            Text(configuration.currentAssignment == null
                ? '本月起生效'
                : '本月起生效：${configuration.currentAssignment!.validFrom}'),
            if (configuration.scheduledAssignment != null)
              Text(
                  '下月起生效：${_planLabel(configuration.scheduledAssignment!.planCode)}'),
            const SizedBox(height: 8),
            ...configuration.plans.map((plan) => RadioListTile<String>(
                  value: plan.code,
                  // ignore: deprecated_member_use
                  groupValue: selected,
                  title: Text(_planLabel(plan.code)),
                  subtitle: plan.usageClass == null
                      ? null
                      : Text(_usageClassLabel(plan.usageClass!)),
                  // ignore: deprecated_member_use
                  onChanged: widget.editable
                      ? (value) => setState(() => selected = value)
                      : null,
                )),
            if (!widget.editable) const Text('僅限店家管理員修改'),
          ],
        ),
      ),
      actions: [
        TextButton(
            onPressed: () => Navigator.pop(context), child: const Text('關閉')),
        if (widget.editable)
          FilledButton(
            onPressed: selected == null
                ? null
                : () => Navigator.pop(context, selected),
            child: const Text('儲存'),
          ),
      ],
    );
  }

  String _planLabel(String code) => switch (code) {
        'LIGHTING_COMMERCIAL_NON_TOU' => '一般電價（非時間電價）',
        'LIGHTING_NONCOMMERCIAL_RESIDENTIAL_NON_TOU' => '住宅用',
        'LIGHTING_NONCOMMERCIAL_NONRESIDENTIAL_NON_TOU' => '住宅以外非營業用',
        _ => code,
      };

  String _usageClassLabel(String value) =>
      value == 'RESIDENTIAL' ? '住宅用' : '住宅以外非營業用';
}

// 支援文字標籤的 Icon 元件
class _TariffDialog extends StatefulWidget {
  const _TariffDialog({required this.current});
  final String? current;

  @override
  State<_TariffDialog> createState() => _TariffDialogState();
}

class _TariffDialogState extends State<_TariffDialog> {
  late String? selected = widget.current;

  @override
  Widget build(BuildContext context) => AlertDialog(
        title: const Text('電價分類設定'),
        content: SizedBox(
          width: double.maxFinite,
          child: ListView(
            shrinkWrap: true,
            children: [
              const ListTile(title: Text('營業／電力用戶')),
              ..._options(_commercialTariffs),
              const ListTile(title: Text('非營業／包燈用戶')),
              ..._options(_nonCommercialTariffs),
            ],
          ),
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(context),
            child: const Text('取消'),
          ),
          FilledButton(
            onPressed: selected == null
                ? null
                : () => Navigator.pop(context, selected),
            child: const Text('儲存'),
          ),
        ],
      );

  List<Widget> _options(List<MapEntry<String, String>> values) => values
      .map(
        (entry) => RadioListTile<String>(
          value: entry.key,
          // ignore: deprecated_member_use
          groupValue: selected,
          title: Text(entry.value),
          // ignore: deprecated_member_use
          onChanged: (value) => setState(() => selected = value),
        ),
      )
      .toList();
}

class _NavIcon extends StatelessWidget {
  final IconData icon;
  final String label;
  final bool isSelected;
  final VoidCallback onTap;

  const _NavIcon({
    required this.icon,
    required this.label,
    required this.isSelected,
    required this.onTap,
  });

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
