# Power IoT System 
## 專案結構
```plaintext
C:\Code\PowerWork\power-iot-system
│
├── 📄 README.md                 # 專案說明檔
│
├── 📂 backend/                  # [後端] Go (Golang)
│   ├── 📄 go.mod                # Go 套件依賴管理檔 (類似 package.json)
│   ├── 📄 go.sum                # Go 套件版本鎖定檔
│   ├── 📂 cmd/
│   │   └── 📂 server/
│   │       └── 📄 main.go       # ★ 程式入口 (我們剛寫的，含自動遷移邏輯)
│   ├── 📂 config/               # 存放設定檔 (目前是空的)
│   ├── 📂 internal/             # 核心邏輯 (不對外公開)
│   │   ├── 📂 api/              # API 層
│   │   │   ├── 📂 handlers      # (預留) 處理 HTTP 請求
│   │   │   └── 📂 middleware    # (預留) 權限驗證
│   │   ├── 📂 core/             # 業務邏輯層
│   │   │   ├── 📂 identity      # (預留) 用戶與店家邏輯
│   │   │   ├── 📂 iot           # (預留) 設備與 MQTT 邏輯
│   │   │   └── 📂 telemetry     # (預留) 電力數據計算
│   │   ├── 📂 data/             # 資料庫層
│   │   │   ├── 📂 migrations    # (預留) SQL 腳本
│   │   │   └── 📂 models/
│   │   │       └── 📄 schema.go # ★ 資料庫模型 (定義 V2.1 所有表格結構)
│   │   └── 📂 utils/            # (預留) 工具函式
│   └── 📄 Dockerfile            # (預留) 未來部署後端用的映像檔設定
│
├── 📂 infrastructure/           # [基礎設施] Docker 環境
│   ├── 🐳 docker-compose.yml    # ★ 啟動資料庫與 MQTT 的設定檔
│   └── 📂 mosquitto/            # MQTT Broker 設定目錄
│       ├── 📂 config
│       ├── 📂 data
│       └── 📂 log
│
└── 📂 mobile/                   # [App 端] Flutter
    ├── 📂 android/              # Android 原生專案檔
    ├── 📂 ios/                  # iOS 原生專案檔
    ├── 📂 lib/                  # ★ Dart 程式碼都在這裡
    │   ├── 📄 main.dart         # Flutter App 入口 (目前是預設計數器 App)
    │   ├── 📂 config            # (預留) App 設定
    │   ├── 📂 core/             # (預留) 共用元件
    │   │   ├── 📂 constants
    │   │   ├── 📂 utils
    │   │   └── 📂 widgets
    │   ├── 📂 features/         # (預留) 功能模組
    │   │   ├── 📂 auth          # 登入註冊
    │   │   ├── 📂 dashboard     # 首頁儀表板
    │   │   ├── 📂 devices       # 設備管理
    │   │   └── 📂 shops         # 店家管理
    │   ├── 📂 models            # (預留) 資料模型
    │   ├── 📂 providers         # (預留) 狀態管理
    │   └── 📂 services          # (預留) API 呼叫服務
    ├── 📄 pubspec.yaml          # Flutter 套件管理檔
    └── 📂 assets/               # 圖片資源目錄
        ├── 📂 images
        └── 📂 icons
```
---

## 專案說明
這是一個用於管理電力物聯網系統的專案，包含後端服務、基礎設施設定以及移動端應用程式。專案採用 Go 語言開發後端，使用 Docker 來管理基礎設施，並使用 Flutter 開發跨平台的移動應用程式。
專案是採用 Declarative Programming (宣告式) 的 UI 框架 (Flutter)，搭配 Reactive Programming (響應式) 的 狀態管理 (Riverpod)。

0. backend 重構為 正式的 Clean Architecture Package Layout。
1. **後端架構**：採用 Clean Architecture + Hexagonal（Ports & Adapters） 設計模式，將業務邏輯與技術實現分離，提高系統的可維護性和擴展性。
    ✔ 你可以換資料庫
    ✔ 你可以換 API 格式
    ✔ 你可以換 MQTT broker
    ✔ 你可以換 UI（Flutter / Web）

2. **基礎設施**：使用 Docker Compose 管理後端服務，包括資料庫、消息代理等，確保環境的一致性和可移植性。
3. **移動端應用**：採用 Flutter 開發跨平台應用程式，確保在 iOS 和 Android 上的原生體驗。
4. **版本控制**：使用 Git 進行版本控制，並遵循 Git Flow 工作流程，確保專案的穩定性和協作效率。

---

## 版本控制

# 1. 初始化 Git
git init

# 2. 加入所有檔案 (會自動參考 .gitignore 排除垃圾檔)
git add .

# 3. 第一次提交
git commit -m "feat(init): project structure initialization with v2.1 schema"

# 4. 建立開發分支
git checkout -b develop

現在，您的專案已經正式納入版本控制了！未來每次完成一個功能 (例如寫完 MqttService)，記得執行 `git add .` 和 `git commit`。

---
# 啟動前端介面查看

#進入專案目錄
cd mobile

#執行 App (Windows 模式)
flutter run -d windows