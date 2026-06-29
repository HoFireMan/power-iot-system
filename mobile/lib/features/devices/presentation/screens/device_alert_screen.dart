// #C:\Code\PowerWork\power-iot-system\mobile\lib\features\devices\screens\device_alert_screen.dart
import 'package:flutter/material.dart';
import 'package:go_router/go_router.dart';
import 'package:power_iot_app/config/theme.dart';

class DeviceAlertScreen extends StatefulWidget {
  final String deviceId;
  const DeviceAlertScreen({super.key, required this.deviceId});

  @override
  State<DeviceAlertScreen> createState() => _DeviceAlertScreenState();
}

class _DeviceAlertScreenState extends State<DeviceAlertScreen> {
  final TextEditingController _monthLimitController = TextEditingController();
  final TextEditingController _dayLimitController = TextEditingController();

  TimeOfDay startTime = const TimeOfDay(hour: 0, minute: 0);
  TimeOfDay endTime = const TimeOfDay(hour: 0, minute: 0);

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(
        title: const Text("提醒設定"),
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
        child: Column(
          children: [
            Expanded(
              child: SingleChildScrollView(
                padding: const EdgeInsets.all(20),
                child: Column(
                  children: [
                    // 1. 設備名稱卡片
                    Container(
                      width: double.infinity,
                      padding: const EdgeInsets.symmetric(vertical: 16),
                      decoration: BoxDecoration(
                        color: Colors.white,
                        borderRadius: BorderRadius.circular(16),
                        boxShadow: [
                          BoxShadow(
                            color: Colors.black.withOpacity(0.05),
                            blurRadius: 10,
                            offset: const Offset(0, 4),
                          ),
                        ],
                      ),
                      child: const Center(
                        child: Text(
                          "所有電器",
                          style: TextStyle(
                              fontSize: 18,
                              fontWeight: FontWeight.bold,
                              color: AppTheme.textPrimary),
                        ),
                      ),
                    ),
                    const SizedBox(height: 24),

                    // 2. 設定表單區域
                    Container(
                      padding: const EdgeInsets.all(24),
                      decoration: BoxDecoration(
                        color: Colors.white,
                        borderRadius: BorderRadius.circular(24),
                        boxShadow: [
                          BoxShadow(
                            color: Colors.black.withOpacity(0.05),
                            blurRadius: 10,
                            offset: const Offset(0, 4),
                          ),
                        ],
                      ),
                      child: Column(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          _buildInputField(
                              "每月用電上限", _monthLimitController, "度", "0"),
                          const SizedBox(height: 24),
                          _buildInputField(
                              "每日用電上限", _dayLimitController, "度", "1"),
                          const SizedBox(height: 32),
                          const Text(
                            "非用電時間",
                            style: TextStyle(
                                fontSize: 16,
                                fontWeight: FontWeight.bold,
                                color: AppTheme.textPrimary),
                          ),
                          const SizedBox(height: 16),
                          Row(
                            mainAxisAlignment: MainAxisAlignment.center,
                            children: [
                              _buildTimeCard("起始", startTime,
                                  (time) => setState(() => startTime = time)),
                              const Padding(
                                padding: EdgeInsets.symmetric(horizontal: 16),
                                child: Text("到",
                                    style: TextStyle(
                                        fontSize: 16, color: Colors.grey)),
                              ),
                              _buildTimeCard("結束", endTime,
                                  (time) => setState(() => endTime = time)),
                            ],
                          ),
                          const SizedBox(height: 24),
                          const Center(
                            child: Text(
                              "非用電時間提醒",
                              style: TextStyle(
                                  fontSize: 14,
                                  fontWeight: FontWeight.bold,
                                  color: AppTheme.primaryColor),
                            ),
                          ),
                          const SizedBox(height: 40),
                          Text(
                            "※ 預設值 0 為不提醒\n※ 用電時間為 24 小時制",
                            style: TextStyle(
                              fontSize: 12,
                              color: Colors.grey.shade400,
                              height: 1.5,
                            ),
                          ),
                        ],
                      ),
                    ),
                  ],
                ),
              ),
            ),

            // 3. 底部按鈕
            Container(
              padding: const EdgeInsets.all(20),
              decoration: BoxDecoration(
                color: Colors.white,
                boxShadow: [
                  BoxShadow(
                    color: Colors.black.withOpacity(0.05),
                    blurRadius: 20,
                    offset: const Offset(0, -5),
                  ),
                ],
              ),
              child: SizedBox(
                width: double.infinity,
                child: ElevatedButton(
                  onPressed: () {
                    ScaffoldMessenger.of(context).showSnackBar(
                      const SnackBar(content: Text('設定已更新')),
                    );
                    context.pop();
                  },
                  child: const Text("修改"),
                ),
              ),
            ),
          ],
        ),
      ),
    );
  }

  // --- UI 元件 ---

  Widget _buildInputField(String label, TextEditingController controller,
      String suffix, String placeholder) {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(label,
            style: const TextStyle(
                fontSize: 14,
                fontWeight: FontWeight.bold,
                color: AppTheme.textSecondary)),
        const SizedBox(height: 8),
        TextField(
          controller: controller,
          keyboardType: TextInputType.number,
          textAlign: TextAlign.end,
          style: const TextStyle(
              fontSize: 18,
              fontWeight: FontWeight.bold,
              color: AppTheme.primaryColor),
          decoration: InputDecoration(
            hintText: placeholder,
            suffixText: "  $suffix",
            suffixStyle: const TextStyle(
                fontSize: 14,
                color: AppTheme.textPrimary,
                fontWeight: FontWeight.bold),
            filled: true,
            fillColor: AppTheme.backgroundColor,
            border: OutlineInputBorder(
              borderRadius: BorderRadius.circular(12),
              borderSide: BorderSide.none,
            ),
            contentPadding:
                const EdgeInsets.symmetric(horizontal: 16, vertical: 14),
          ),
        ),
      ],
    );
  }

  Widget _buildTimeCard(
      String label, TimeOfDay time, Function(TimeOfDay) onSelect) {
    return GestureDetector(
      onTap: () async {
        final TimeOfDay? picked = await showTimePicker(
          context: context,
          initialTime: time,
        );
        if (picked != null) onSelect(picked);
      },
      child: Column(
        children: [
          Container(
            padding: const EdgeInsets.symmetric(horizontal: 24, vertical: 16),
            decoration: BoxDecoration(
              color: AppTheme.backgroundColor,
              borderRadius: BorderRadius.circular(16),
              border: Border.all(color: Colors.transparent),
            ),
            child: Text(
              time.hour.toString(),
              style: const TextStyle(
                  fontSize: 32,
                  fontWeight: FontWeight.bold,
                  color: AppTheme.textPrimary),
            ),
          ),
          const SizedBox(height: 8),
          Text(label,
              style: TextStyle(fontSize: 12, color: Colors.grey.shade500)),
        ],
      ),
    );
  }
}
