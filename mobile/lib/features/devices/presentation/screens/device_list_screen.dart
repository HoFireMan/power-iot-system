// #C:\Code\PowerWork\power-iot-system\mobile\lib\features\devices\screens\device_list_screen.dart
import 'package:flutter/material.dart';
import 'package:go_router/go_router.dart';
import 'package:power_iot_app/config/theme.dart';

class DeviceListScreen extends StatelessWidget {
  const DeviceListScreen({super.key});

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(
        title: const Text("設備管理"),
        centerTitle: true,
        backgroundColor: Colors.transparent,
        foregroundColor: AppTheme.textPrimary,
        elevation: 0,
      ),
      body: SafeArea(
        child: SingleChildScrollView(
          padding: const EdgeInsets.all(20.0),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              _buildAllDevicesCard(),
              const SizedBox(height: 24),
              _buildSectionTitle("已連線", Colors.green),
              const SizedBox(height: 12),
              _buildDeviceCard(
                context,
                name: "其他 4AAB 插座",
                type: "plug",
                isOnline: true,
                deviceId: "dev_001",
              ),
              const SizedBox(height: 24),
              _buildSectionTitle("連線中斷", AppTheme.errorColor),
              const SizedBox(height: 12),
              _buildDeviceCard(
                context,
                name: "冷氣 4682",
                type: "ac",
                isOnline: false,
                deviceId: "dev_002",
              ),
              const SizedBox(height: 12),
              _buildDeviceCard(
                context,
                name: "冷氣 610B",
                type: "ac",
                isOnline: false,
                deviceId: "dev_003",
              ),
              const SizedBox(height: 12),
              _buildDeviceCard(
                context,
                name: "冷氣 A946",
                type: "ac",
                isOnline: false,
                deviceId: "dev_004",
              ),
            ],
          ),
        ),
      ),
      bottomNavigationBar: _buildBottomNav(context),
    );
  }

  // --- UI 元件區 ---

  Widget _buildAllDevicesCard() {
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
            "所有電器",
            style: TextStyle(
              fontSize: 18,
              fontWeight: FontWeight.bold,
              color: AppTheme.textPrimary,
            ),
          ),
          const Spacer(),
          _buildSettingButton(() {}),
        ],
      ),
    );
  }

  Widget _buildSectionTitle(String title, Color color) {
    return Row(
      children: [
        Container(
          width: 10,
          height: 10,
          decoration: BoxDecoration(
            color: color,
            shape: BoxShape.circle,
          ),
        ),
        const SizedBox(width: 8),
        Text(
          title,
          style: const TextStyle(
            fontSize: 16,
            fontWeight: FontWeight.bold,
            color: AppTheme.textSecondary,
          ),
        ),
      ],
    );
  }

  Widget _buildDeviceCard(
    BuildContext context, {
    required String name,
    required String type,
    required bool isOnline,
    required String deviceId,
  }) {
    return Container(
      padding: const EdgeInsets.all(16),
      decoration: BoxDecoration(
        color: Colors.white,
        borderRadius: BorderRadius.circular(20),
        border: isOnline ? null : Border.all(color: Colors.grey.shade200),
        boxShadow: [
          BoxShadow(
            color: Colors.black.withValues(alpha: 0.05),
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
              color: isOnline
                  ? AppTheme.secondaryColor.withValues(alpha: 0.15)
                  : Colors.grey.shade100,
              shape: BoxShape.circle,
            ),
            child: Icon(
              _getIcon(type),
              color: isOnline ? AppTheme.primaryColor : Colors.grey,
              size: 24,
            ),
          ),
          const SizedBox(width: 16),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(
                  name,
                  style: TextStyle(
                    fontSize: 16,
                    fontWeight: FontWeight.bold,
                    color: isOnline ? AppTheme.textPrimary : Colors.grey,
                  ),
                ),
                if (!isOnline)
                  const Padding(
                    padding: EdgeInsets.only(top: 4),
                    child: Text(
                      "請檢查電源",
                      style:
                          TextStyle(fontSize: 12, color: AppTheme.errorColor),
                    ),
                  ),
              ],
            ),
          ),
          _buildSettingButton(() {
            context.push('/devices/$deviceId/alert');
          }),
        ],
      ),
    );
  }

  Widget _buildSettingButton(VoidCallback onTap) {
    return InkWell(
      onTap: onTap,
      borderRadius: BorderRadius.circular(30),
      child: const Padding(
        padding: EdgeInsets.all(8.0),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            Icon(Icons.access_time_filled_rounded,
                size: 16, color: AppTheme.textSecondary),
            SizedBox(width: 4),
            Text(
              "提醒設定",
              style: TextStyle(
                fontSize: 12,
                color: AppTheme.textSecondary,
                fontWeight: FontWeight.w500,
              ),
            ),
          ],
        ),
      ),
    );
  }

  // --- 關鍵修正：底部導航欄與 Dashboard 一致 ---
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

          // 2. 設備 (當前頁 isSelected: true)
          _NavIcon(
              icon: Icons.electrical_services_rounded,
              isSelected: true,
              label: "設備",
              onTap: () {}),

          // 3. 個人 (跳轉)
          _NavIcon(
              icon: Icons.person_rounded,
              isSelected: false,
              label: "個人",
              onTap: () => context.go('/profile')),

          // 4. 店家 (跳轉) - 加入了這個跳轉逻辑
          _NavIcon(
              icon: Icons.store_rounded,
              isSelected: false,
              label: "店家",
              onTap: () => context.go('/shops')),
        ],
      ),
    );
  }

  IconData _getIcon(String type) {
    switch (type) {
      case 'ac':
        return Icons.ac_unit_rounded;
      case 'plug':
        return Icons.power_rounded;
      default:
        return Icons.lightbulb_rounded;
    }
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
