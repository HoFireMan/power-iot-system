// #C:\Code\PowerWork\power-iot-system\mobile\lib\features\shops\screens\shop_list_screen.dart
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:power_iot_app/config/theme.dart';
import 'package:power_iot_app/features/shops/domain/models/shop.dart' as remote;
import 'package:power_iot_app/features/profile/presentation/providers/profile_provider.dart';
import 'package:power_iot_app/features/shops/providers/remote_shop_provider.dart';

class ShopListScreen extends ConsumerWidget {
  const ShopListScreen({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final shopState = ref.watch(shopsProvider);
    final snapshot = shopState.data;
    final shops = snapshot?.shops ?? const <remote.Shop>[];
    // The server preference is authoritative. A temporary view choice remains
    // separate and is never sent as an authorization signal.
    final currentShopId = shopState.selectedShopId ?? snapshot?.currentShopId;

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
        child: _buildBody(context, ref, shopState, shops, currentShopId),
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
              const Icon(Icons.star_rounded,
                  color: AppTheme.accentColor, size: 20),
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
      BuildContext context, WidgetRef ref, remote.Shop shop, bool isSelected) {
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
                borderRadius:
                    const BorderRadius.vertical(top: Radius.circular(18)),
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
                    padding:
                        const EdgeInsets.symmetric(horizontal: 8, vertical: 2),
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
                            Icon(Icons.location_on_outlined,
                                size: 14, color: Colors.grey.shade500),
                            const SizedBox(width: 4),
                            Expanded(
                              child: Text(
                                shop.address ?? '未提供地址',
                                style: TextStyle(
                                    fontSize: 13, color: Colors.grey.shade600),
                                maxLines: 1,
                                overflow: TextOverflow.ellipsis,
                              ),
                            ),
                          ],
                        ),
                      ],
                    ),
                  ),
                ],
              ),
            ),
          ],
        ),
      ),
    );
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
              onTap: () => context.go('/dashboard')),
          // 2. 設備 (跳轉)
          _NavIcon(
              icon: Icons.electrical_services_rounded,
              isSelected: false,
              label: "設備",
              onTap: () => context.go('/devices')),
          // 3. 個人 (跳轉)
          _NavIcon(
              icon: Icons.person_rounded,
              isSelected: false,
              label: "個人",
              onTap: () => context.go('/profile')),
          // 4. 店家 (當前頁，isSelected: true)
          _NavIcon(
              icon: Icons.store_rounded,
              isSelected: true,
              label: "店家",
              onTap: () {} // 已經在店家頁，不需動作
              ),
        ],
      ),
    );
  }
}

// 支援文字標籤的 Icon 元件
class _NavIcon extends StatelessWidget {
  final IconData icon;
  final String label;
  final bool isSelected;
  final VoidCallback onTap;

  const _NavIcon(
      {required this.icon,
      required this.label,
      required this.isSelected,
      required this.onTap});

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
          Text(label,
              style: TextStyle(
                fontSize: 10,
                fontWeight: FontWeight.w600,
                color:
                    isSelected ? AppTheme.primaryColor : Colors.grey.shade400,
              )),
        ],
      ),
    );
  }
}
