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

---

# **Power IoT System \- 專案需求與規格書 (PRD)**

**版本:** V1.0

**日期:** 2025-11-25

**專案目標:** 建構一套支援本地分散式部署的物聯網電力監控系統，提供多店管理、即時能耗監控、異常警報與碳排放計算功能。

## **1\. 系統架構與技術選型 (System Architecture)**

本專案採用 **Monorepo** 結構，強調高效能、輕量化與容器化部署，以適應本地伺服器環境。

### **1.1 技術堆疊 (Tech Stack)**

| 層級 | 技術 | 說明 |
| :---- | :---- | :---- |
| **Mobile App** | **Flutter** (Dart) | 支援 iOS/Android 雙平台，高效能原生編譯。 |
| **Backend API** | **Go** (Golang) | 使用 Gin 框架。單一執行檔，高併發，極低資源佔用。 |
| **Database** | **PostgreSQL** | 關聯式資料庫，未來可搭配 TimescaleDB 處理時序數據。 |
| **IoT Broker** | **Mosquitto** (MQTT) | 負責接收硬體傳送的電力數據。 |
| **Infrastructure** | **Docker** (Compose) | 容器化管理 DB 與 MQTT，實現「一鍵部署」。 |
| **IDE** | **VS Code** | 統一開發環境。 |

### **1.2 部署架構**

* **本地分散式部署 (Local Deployment):** 後端、資料庫與 MQTT Broker 部署於客戶現場的伺服器或微型電腦 (如 Raspberry Pi / Industrial PC)。  
* **App 連線:** 手機 App 在區網內或透過固定 IP/VPN 連線至該本地伺服器。

## **2\. 角色權限與使用者故事 (Roles & Permissions)**

系統分為兩個主要角色，透過 users 表與 user\_shop\_relations 表進行權限控制。

### **2.1 平台管理員 (Super Admin)**

* **權限範圍:** 跨店家，擁有最高權限。  
* **核心功能:**  
  * 查看所有「客戶別列表」、「店家列表」、「使用者列表」。  
  * **綁定使用者店家:** 手動將使用者加入特定店家。  
  * **綁定感測器:** 輸入 MAC Address 將設備註冊到特定店家。  
  * 查看所有感測器列表與狀態。

### **2.2 一般用戶 / 店長 (User / Shop Manager)**

* **權限範圍:** 僅限於自己被授權的店家。  
* **核心功能:**  
  * **註冊與綁定:** 透過掃描店家 QR Code 註冊，自動綁定該店家。  
  * **多店切換:** 若擁有多家店權限，可在 App 內切換視角。  
  * **店務管理:** 編輯店家資訊 (如電話、地址)。  
  * **設備監控:** 查看即時用電、設定警報。

## **3\. 功能模組規格 (Functional Specifications)**

### **3.1 組織與身份模組 (Identity Context)**

* **登入/註冊:**  
  * 支援帳號密碼登入。  
  * **QR Code 邀請註冊機制:**  
    * 管理員產生含 invite\_uuid 的店家 QR Code。  
    * 新用戶掃描 \-\> App 解析 UUID \-\> 後端自動建立 User 並寫入 user\_shop\_relations。  
* **多店管理:**  
  * 使用者可查看自己權限下的所有店家。  
  * 記錄 current\_shop\_id，下次開啟 App 自動載入上次瀏覽的店家。

### **3.2 店家與設備管理模組 (Shop & Device Context)**

* **店家資訊:**  
  * 欄位包含：名稱、電話、地址、備註、是否為總部 (IsHead)、店家編碼 (Code)。  
* **設備列表:**  
  * 顯示設備名稱、類別 (冷氣、冰箱等)、即時狀態 (在線/離線)。  
  * 支援關鍵字搜尋、分類篩選。  
  * **離線處理:** 當設備斷線時，UI 顯示灰色並提示檢查網路。

### **3.3 數據與儀表板模組 (Telemetry & Dashboard)**

* **首頁儀表板:**  
  * **即時功率 (W):** 顯示當前總用電負載。  
  * **本日用電量 (kWh):** 累計至目前的度數。  
  * **碳排放量 (kg):**  
    * 公式: 用電量 (kWh) \* 0.474 (係數存於 system\_configs 可調整)。  
* **歷史報表:**  
  * 查看每日/每月的用電趨勢。

### **3.4 警報與監控模組 (Alerts & Monitoring)**

* **警報設定 (針對單一設備):**  
  1. **用量限制:** 設定每日或每月用電上限 (kWh)，超過即推播。  
  2. **非營業時間偵測 (Curfew):** 設定起始與結束時間 (e.g., 22:00 \- 06:00)，若期間內設備功率 \> 0 (運轉中) 則發送警報。  
* **警報紀錄:**  
  * 紀錄警報類型、訊息、發生時間。  
  * **快照 (Snapshot):** 紀錄異常發生當下的電壓、電流、功率數值 (對應舊版 warn 表)。

## **4\. 資料庫設計 (Database Schema V2.1)**

本設計整合了舊有 basic.\* 表格欄位，並進行了關聯式正規化 (Normalization)。

### **4.1 核心資料表**

| 表格名稱 | 用途 | 舊版對應 | 重點欄位 |
| :---- | :---- | :---- | :---- |
| **system\_configs** | 全域設定 | \- | key (carbon\_factor), value (0.474) |
| **clients** | 客戶/商圈 | \- | code, name |
| **shops** | 店家 | basic.branch | invite\_uuid, code, is\_head, memo |
| **users** | 使用者 | basic.employee | account, password, is\_admin, current\_shop\_id |
| **user\_shop\_relations** | 用戶-店家關聯 | \- | user\_id, shop\_id, shop\_role |
| **device\_types** | 設備類別 | basic.systag | name, icon\_key, code |
| **devices** | 設備主檔 | basic.device | mac\_address, location, is\_online, memo |
| **device\_alert\_settings** | 警報設定 | \- | daily\_limit, non\_usage\_start/end\_time |
| **power\_readings** | 電力數據 (Raw) | \- | voltage, current, power, kwh\_total |
| **alert\_logs** | 警報紀錄 | basic.warn | type, message, **快照(V/A/W)** |
| **daily\_usages** | 每日統計 | basic.report | date, kwh\_usage, carbon\_kg |

## **5\. API 介面規劃 (API Endpoint Plan)**

### **Auth**

* POST /api/auth/login: 登入，回傳 JWT Token。  
* POST /api/auth/register: 註冊 (需帶入 invite\_uuid)。

### **Shops**

* GET /api/shops/my: 取得我能看的所有店家。  
* POST /api/shops/switch: 切換當前店家 (更新 current\_shop\_id)。  
* GET /api/shops/:id: 取得店家詳情 (含 QR Code 字串)。

### **Devices**

* GET /api/shops/:id/devices: 取得某店家的設備列表 (含即時狀態)。  
* POST /api/devices: (Admin) 綁定新設備。  
* PUT /api/devices/:id/alert-settings: 更新警報設定。

### **Dashboard**

* GET /api/dashboard/summary: 取得首頁數據 (今日用電、碳排、即時功率)。  
* GET /api/dashboard/chart: 取得圖表數據。

## **6\. 非功能性需求 (NFR)**

1. **資料生命週期 (Data Retention):**  
   * power\_readings (Raw Data): 保留 3 個月 (可透過排程清除)。  
   * daily\_usages (統計數據): 保留 5 年以上。  
   * alert\_logs: 保留 1 年。  
2. **離線容錯:** 若網路中斷，App 應顯示快取數據或明確的離線提示，不可崩潰。  
3. **時間同步:** 後端接收 IoT 數據時，若發現時間戳異常 (如 1970 年)，需強制使用 Server Time 校正。  
4. **Log 管理:** Docker 容器需設定 Log Rotation，避免佔滿磁碟空間。