import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../providers/alert_providers.dart';

class AlertHistoryScreen extends ConsumerWidget {
  const AlertHistoryScreen({required this.shopId, super.key});
  final String shopId;
  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final state = ref.watch(alertHistoryProvider(shopId));
    return Scaffold(
      appBar: AppBar(title: const Text('警報紀錄')),
      body: state.when(
        loading: () => const Center(child: CircularProgressIndicator()),
        error: (error, _) => Center(
          child: Text(
            '警報紀錄暫時無法取得',
            style: Theme.of(context).textTheme.bodyLarge,
          ),
        ),
        data: (page) => RefreshIndicator(
          onRefresh: () => ref.refresh(alertHistoryProvider(shopId).future),
          child: ListView(
            children: [
              if (page.items.isEmpty)
                const Padding(
                  padding: EdgeInsets.all(24),
                  child: Center(child: Text('尚無警報紀錄')),
                ),
              ...page.items.map(
                (alert) => ListTile(
                  leading: Icon(
                    alert.isRead
                        ? Icons.notifications_none
                        : Icons.notifications_active,
                    color: alert.isRead ? Colors.grey : Colors.orange,
                  ),
                  title: Text(
                    alert.measurementPointName.isEmpty
                        ? alert.type
                        : alert.measurementPointName,
                  ),
                  subtitle: Text(
                    '${alert.message}\n${alert.recordedAt.toLocal()}',
                  ),
                  isThreeLine: true,
                  trailing: Text('${alert.power.toStringAsFixed(1)} W'),
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }
}
