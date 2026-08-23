import 'package:flutter/material.dart';
import 'package:go_router/go_router.dart';
import 'package:power_iot_app/config/theme.dart';

/// The legacy reminder editor has no B7 write authority. Keep the route as a
/// safe read-only boundary for existing deep links, without fabricating data or
/// claiming that an update was persisted.
class DeviceAlertScreen extends StatelessWidget {
  const DeviceAlertScreen({super.key, required this.deviceId});

  final String deviceId;

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(
        title: const Text('設備詳情'),
        centerTitle: true,
        backgroundColor: Colors.transparent,
        foregroundColor: AppTheme.textPrimary,
        elevation: 0,
        leading: IconButton(
          icon: const Icon(Icons.arrow_back_ios_new_rounded, size: 20),
          onPressed: () => context.pop(),
        ),
      ),
      body: SafeArea(
        child: Center(
          child: Padding(
            padding: const EdgeInsets.all(24),
            child: Column(
              mainAxisSize: MainAxisSize.min,
              children: [
                const Icon(Icons.info_outline_rounded,
                    size: 48, color: AppTheme.primaryColor),
                const SizedBox(height: 16),
                const Text('目前沒有可用的提醒設定', textAlign: TextAlign.center),
                const SizedBox(height: 8),
                Text('設備：$deviceId',
                    style: const TextStyle(color: AppTheme.textSecondary)),
                const SizedBox(height: 24),
                OutlinedButton(
                    onPressed: () => context.pop(), child: const Text('返回設備')),
              ],
            ),
          ),
        ),
      ),
    );
  }
}
