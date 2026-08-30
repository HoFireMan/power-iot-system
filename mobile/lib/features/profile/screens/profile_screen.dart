// #C:\Code\PowerWork\power-iot-system\mobile\lib\features\profile\screens\profile_screen.dart
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:power_iot_app/config/theme.dart';
import 'package:power_iot_app/features/auth/auth_controller.dart';
import 'package:power_iot_app/features/profile/domain/models/user_profile.dart';
import 'package:power_iot_app/features/profile/presentation/providers/profile_provider.dart';

class ProfileScreen extends ConsumerStatefulWidget {
  const ProfileScreen({super.key});

  @override
  ConsumerState<ProfileScreen> createState() => _ProfileScreenState();
}

class _ProfileScreenState extends ConsumerState<ProfileScreen> {
  @override
  Widget build(BuildContext context) {
    final remoteState = ref.watch(profileProvider);
    if (remoteState.status == RemoteStatus.loading) {
      return const Scaffold(body: Center(child: CircularProgressIndicator()));
    }
    if (remoteState.status == RemoteStatus.unauthorized) {
      return const Scaffold(body: Center(child: Text('登入已失效，請重新登入')));
    }
    if (remoteState.status == RemoteStatus.error || remoteState.data == null) {
      return Scaffold(
        body: Center(
          child: OutlinedButton(
            onPressed: () => ref.read(profileProvider.notifier).load(),
            child: const Text('載入個人資料失敗，重試'),
          ),
        ),
      );
    }
    final profile = remoteState.data!;
    return Scaffold(
      backgroundColor: AppTheme.backgroundColor,
      body: SingleChildScrollView(
        child: Column(
          children: [
            // 1. 頂部個人資訊卡片 (已修正：補回手機資訊、登出樣式)
            _buildProfileHeader(profile),

            // 2. 功能選單列表
            Padding(
              padding: const EdgeInsets.symmetric(horizontal: 20, vertical: 24),
              child: Column(
                children: [
                  _buildSectionContainer(
                    children: [
                      _buildMenuItem(
                        icon: Icons.person_outline_rounded,
                        title: "編輯個人資訊",
                        onTap: () {
                          ScaffoldMessenger.of(context).showSnackBar(
                            const SnackBar(content: Text('功能開發中: 編輯個人資訊')),
                          );
                        },
                      ),
                      _buildDivider(),
                      _buildMenuItem(
                        icon: Icons.storefront_rounded,
                        title: "設定主要店家",
                        onTap: () {},
                      ),
                      _buildDivider(),
                      _buildMenuItem(
                        icon: Icons.notifications_active_outlined,
                        title: "用電提醒設定",
                        onTap: () {},
                      ),
                    ],
                  ),
                  if (profile.isAdmin) ...[
                    const SizedBox(height: 24),
                    _buildSectionLabel("系統管理"),
                    const SizedBox(height: 8),
                    _buildSectionContainer(
                      children: [
                        _buildMenuItem(
                            icon: Icons.folder_shared_outlined,
                            title: "客戶別列表",
                            onTap: () {}),
                        _buildDivider(),
                        _buildMenuItem(
                            icon: Icons.store_mall_directory_outlined,
                            title: "店家列表",
                            onTap: () => context.go('/shops')),
                        _buildDivider(),
                        _buildMenuItem(
                            icon: Icons.manage_accounts_outlined,
                            title: "使用者列表",
                            onTap: () {}),
                        _buildDivider(),
                        _buildMenuItem(
                            icon: Icons.sensors, title: "感測器列表", onTap: () {}),
                        _buildDivider(),
                        _buildMenuItem(
                            icon: Icons.link,
                            title: "綁定感測器",
                            onTap: () => context.go('/admin')),
                        _buildDivider(),
                        _buildMenuItem(
                            icon: Icons.add_business_outlined,
                            title: "綁定使用者店家",
                            onTap: () {}),
                      ],
                    ),
                  ],
                  const SizedBox(height: 40),
                  if (!profile.isAdmin)
                    SizedBox(
                      width: double.infinity,
                      child: OutlinedButton.icon(
                        onPressed: () async {
                          await ref.read(authControllerProvider).logout();
                          if (!context.mounted) return;
                          context.go('/login');
                        },
                        icon: const Icon(Icons.logout),
                        label: const Text("登出帳號"),
                        style: OutlinedButton.styleFrom(
                          foregroundColor: Colors.grey,
                          side: const BorderSide(color: Colors.grey),
                          padding: const EdgeInsets.symmetric(vertical: 16),
                          shape: RoundedRectangleBorder(
                            borderRadius: BorderRadius.circular(12),
                          ),
                        ),
                      ),
                    ),
                  const SizedBox(height: 40),
                ],
              ),
            ),
          ],
        ),
      ),
      bottomNavigationBar: _buildBottomNav(context),
    );
  }

  // --- UI 元件區 ---

  Widget _buildProfileHeader(UserProfile profile) {
    return Container(
      padding: const EdgeInsets.fromLTRB(24, 60, 24, 40),
      decoration: const BoxDecoration(
        gradient: AppTheme.primaryGradient,
        borderRadius: BorderRadius.vertical(bottom: Radius.circular(32)),
        boxShadow: [
          BoxShadow(
              color: Colors.black12, blurRadius: 10, offset: Offset(0, 5)),
        ],
      ),
      child: Column(
        children: [
          Row(
            crossAxisAlignment: CrossAxisAlignment.center,
            children: [
              // 頭像 (修正：改回品牌 Leaf 圖示)
              Container(
                padding: const EdgeInsets.all(4),
                decoration: BoxDecoration(
                  color: Colors.white.withValues(alpha: 0.2),
                  shape: BoxShape.circle,
                ),
                child: const CircleAvatar(
                  radius: 36,
                  backgroundColor: Colors.white,
                  child: Icon(Icons.eco,
                      size: 40, color: AppTheme.primaryColor), // 改回 eco
                ),
              ),
              const SizedBox(width: 20),

              // 資訊區 (修正：補回手機欄位)
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(
                      'Hi! ${profile.name}',
                      style: const TextStyle(
                          fontSize: 24,
                          fontWeight: FontWeight.bold,
                          color: Colors.white,
                          letterSpacing: 0.5),
                    ),
                    const SizedBox(height: 8),
                    // 店家名稱
                    Row(
                      children: [
                        const Icon(Icons.store_rounded,
                            color: Colors.white70, size: 16),
                        const SizedBox(width: 6),
                        Flexible(
                          child: Text(
                            profile.currentShopId == null
                                ? '尚未設定主要店家'
                                : '店家 ${profile.currentShopId}',
                            style: const TextStyle(
                                color: Colors.white,
                                fontSize: 14,
                                fontWeight: FontWeight.w500),
                            overflow: TextOverflow.ellipsis,
                          ),
                        ),
                      ],
                    ),
                    const SizedBox(height: 4),
                    Row(
                      children: [
                        const Icon(Icons.email_outlined,
                            color: Colors.white60, size: 16),
                        const SizedBox(width: 6),
                        Text(profile.email ?? '尚未設定',
                            style: const TextStyle(
                                color: Colors.white60, fontSize: 13)),
                      ],
                    ),
                    const SizedBox(height: 2),
                    Row(
                      children: [
                        const Icon(Icons.phone_iphone_rounded,
                            color: Colors.white60, size: 16),
                        const SizedBox(width: 6),
                        Text(profile.phone ?? '尚未設定',
                            style: const TextStyle(
                                color: Colors.white60, fontSize: 13)),
                      ],
                    ),
                  ],
                ),
              ),

              // 登出按鈕 (修正：改成 文字+圖示 樣式)
              GestureDetector(
                onTap: () async {
                  await ref.read(authControllerProvider).logout();
                  if (!mounted) return;
                  context.go('/login');
                },
                child: Container(
                  padding:
                      const EdgeInsets.symmetric(horizontal: 12, vertical: 6),
                  decoration: BoxDecoration(
                    color: Colors.white.withValues(alpha: 0.2),
                    borderRadius: BorderRadius.circular(20),
                  ),
                  child: const Row(
                    mainAxisSize: MainAxisSize.min,
                    children: [
                      Text("登出",
                          style: TextStyle(
                              color: Colors.white,
                              fontSize: 13,
                              fontWeight: FontWeight.w600)),
                      SizedBox(width: 4),
                      Icon(Icons.logout, color: Colors.white, size: 16),
                    ],
                  ),
                ),
              ),
            ],
          ),
        ],
      ),
    );
  }

  Widget _buildSectionContainer({required List<Widget> children}) {
    return Container(
      decoration: BoxDecoration(
        color: Colors.white,
        borderRadius: BorderRadius.circular(20),
        boxShadow: [
          BoxShadow(
              color: AppTheme.primaryColor.withValues(alpha: 0.08),
              blurRadius: 20,
              offset: const Offset(0, 5)),
        ],
      ),
      child: Column(children: children),
    );
  }

  Widget _buildMenuItem(
      {required IconData icon,
      required String title,
      required VoidCallback onTap}) {
    return Material(
      color: Colors.transparent,
      child: InkWell(
        onTap: onTap,
        borderRadius: BorderRadius.circular(20),
        child: Padding(
          padding: const EdgeInsets.symmetric(horizontal: 20, vertical: 16),
          child: Row(
            children: [
              Container(
                padding: const EdgeInsets.all(10),
                decoration: const BoxDecoration(
                    color: AppTheme.backgroundColor, shape: BoxShape.circle),
                child: Icon(icon, color: AppTheme.primaryColor, size: 22),
              ),
              const SizedBox(width: 16),
              Expanded(
                child: Text(title,
                    style: const TextStyle(
                        fontSize: 16,
                        fontWeight: FontWeight.w600,
                        color: AppTheme.textPrimary)),
              ),
              Icon(Icons.arrow_forward_ios_rounded,
                  size: 16, color: Colors.grey.shade300),
            ],
          ),
        ),
      ),
    );
  }

  Widget _buildDivider() {
    return Padding(
      padding: const EdgeInsets.symmetric(horizontal: 20),
      child: Divider(height: 1, color: Colors.grey.shade100),
    );
  }

  Widget _buildSectionLabel(String text) {
    return Padding(
      padding: const EdgeInsets.only(left: 8, bottom: 8),
      child: Row(
        children: [
          Container(
            width: 4,
            height: 16,
            decoration: BoxDecoration(
                color: AppTheme.secondaryColor,
                borderRadius: BorderRadius.circular(2)),
          ),
          const SizedBox(width: 8),
          Text(text,
              style: TextStyle(
                  fontSize: 14,
                  fontWeight: FontWeight.bold,
                  color: Colors.grey.shade600,
                  letterSpacing: 0.5)),
        ],
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
            offset: const Offset(0, 10),
          ),
        ],
      ),
      child: Row(
        mainAxisAlignment: MainAxisAlignment.spaceAround,
        children: [
          _NavIcon(
              icon: Icons.home_rounded,
              isSelected: false,
              label: "首頁",
              onTap: () => context.go('/dashboard')),
          _NavIcon(
              icon: Icons.electrical_services_rounded,
              isSelected: false,
              label: "設備",
              onTap: () => context.go('/devices')),
          _NavIcon(
              icon: Icons.person_rounded,
              isSelected: true,
              label: "個人",
              onTap: () {}),
          _NavIcon(
              icon: Icons.store_rounded,
              isSelected: false,
              label: "店家",
              onTap: () => context.go('/shops')),
        ],
      ),
    );
  }
}

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
                  color: isSelected
                      ? AppTheme.primaryColor
                      : Colors.grey.shade400)),
        ],
      ),
    );
  }
}
