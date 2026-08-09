// #C:\Code\PowerWork\power-iot-system\mobile\lib\features\shops\providers\shop_provider.dart
import 'package:flutter_riverpod/flutter_riverpod.dart';

// 1. 定義店家資料模型
class Shop {
  final String id;
  final String name;
  final String address;
  final bool isHead; // 是否為總部

  Shop({
    required this.id,
    required this.name,
    required this.address,
    this.isHead = false,
  });
}

// 2. 定義狀態：目前選中的店家
class ShopState {
  final Shop currentShop;
  final List<Shop> availableShops;
  final bool isAdmin; // 模擬權限

  ShopState({
    required this.currentShop,
    required this.availableShops,
    required this.isAdmin,
  });
}

// 3. 建立 Notifier (邏輯核心)
class ShopNotifier extends StateNotifier<ShopState> {
  ShopNotifier() : super(_initialState());

  // 模擬初始化數據 (備註：未來這裡會改成從 API 抓取)
  static ShopState _initialState() {
    // 模擬數據庫中的店家列表
    final allShops = [
      Shop(id: 's1', name: '維野納複合式餐飲', address: '雲林縣虎尾鎮...'),
      Shop(id: 's2', name: '張淑芬住宅', address: '雲林縣虎尾鎮...'), // 模擬一般住家
      Shop(id: 's3', name: '洪房東公共區域', address: '雲林縣斗六市...', isHead: true),
      Shop(id: 's4', name: '林翊榆住宅', address: '雲林縣古坑鄉...'),
    ];

    // [備註] 權限邏輯模擬：
    // true  = 管理員 (可以看到 allShops)
    // false = 一般使用者 (只能看到 subset，例如 s1 和 s2)
    const bool isSimulatedAdmin = true;

    // Keep the future non-admin mock branch while admin simulation is fixed.
    final visibleShops =
        // ignore: dead_code
        isSimulatedAdmin ? allShops : [allShops[0], allShops[1]]; // 一般人只看得到前兩家

    return ShopState(
      currentShop: visibleShops[0], // 預設選中第一家
      availableShops: visibleShops,
      isAdmin: isSimulatedAdmin,
    );
  }

  // 切換店家動作
  void selectShop(String shopId) {
    final selected = state.availableShops.firstWhere((s) => s.id == shopId);
    state = ShopState(
      currentShop: selected,
      availableShops: state.availableShops,
      isAdmin: state.isAdmin,
    );
  }
}

// 4. 建立 Provider (讓 UI 可以存取)
final shopProvider = StateNotifierProvider<ShopNotifier, ShopState>((ref) {
  return ShopNotifier();
});
