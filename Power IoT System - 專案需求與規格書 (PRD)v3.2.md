# **Power IoT System \- 專案需求與規格書 (PRD)**

**版本:** V3.2 (完整功能細節版)

**日期:** 2025-11-27

**專案目標:** 建構一套支援**營運端集中部署**的物聯網電力監控系統，提供多店管理、即時能耗監控、異常警報與碳排放計算功能。

## **1\. 系統架構與技術選型 (System Architecture)**

本專案採用 **Monorepo** 結構，部署於營運商 (Provider) 的私有伺服器。

### **1.1 技術堆疊 (Tech Stack)**

| 層級 | 技術 | 說明 |
| :---- | :---- | :---- |
| **Mobile App** | **Flutter** (Dart) | 負責 UI、藍牙配網。透過 Internet 連線後端 API。 |
| **Backend API** | **Go** (Golang) | Gin 框架。部署於營運端，處理請求、MQTT 訂閱、警報計算。 |
| **Database** | **PostgreSQL** | 集中式資料庫，儲存所有客戶資料與電力數據。 |
| **IoT Broker** | **Mosquitto** (MQTT) | 部署於營運端，接收來自各地設備的數據。 |
| **Infrastructure** | **Docker** (Compose) | 容器化管理，便於擴展或遷移。 |
| **IDE** | **VS Code** | 統一開發環境 (建議安裝 Go, Flutter, Docker 擴充套件)。 |

### **1.2 物理部署架構 (Deployment Topology)**

* **伺服器端 (Server Side):**  
  * 位置：**開發/營運團隊機房**。  
  * 條件：需具備 **固定公網 IP**。  
  * 防火牆：開放 MQTT (1883) 與 API (8080) 端口。  
* **客戶端 (Client Side):**  
  * 硬體：僅需安裝 IoT 設備。  
  * 網路：需具備可連上 Internet 的 Wi-Fi。

### **1.3 數據流架構 (Data Flow) \- 方案 B**

1. **上行:** 硬體 \-\> Internet \-\> MQTT Broker \-\> Go Backend \-\> DB。  
2. **下行:** Flutter App \-\> Internet \-\> Go API \-\> DB (輪詢 Polling)。

## **2\. 角色權限與使用者故事 (Roles & Permissions)**

系統分為兩個主要角色，透過 users 表與 user\_shop\_relations 表進行多租戶權限控制。

### **2.1 平台管理員 (Super Admin)**

* **權限範圍:** 跨店家/跨客戶，擁有最高權限。  
* **核心功能:**  
  * **全域檢視:** 查看所有「客戶別列表」、「店家列表」、「使用者列表」。  
  * **綁定使用者店家:** 手動將特定使用者加入特定店家 (維護 user\_shop\_relations)。  
  * **綁定感測器:** 輸入 MAC Address 將設備註冊到特定店家 (維護 devices 表)。  
  * **感測器管理:** 查看所有感測器列表與連線狀態。

### **2.2 一般用戶 / 店長 (User / Shop Manager)**

* **權限範圍:** 僅限於自己被授權的店家。  
* **核心功能:**  
  * **註冊與綁定:** 透過掃描店家 QR Code 註冊，自動綁定該店家。  
  * **多店切換:** 若擁有多家店權限，可在 App 內切換視角 (切換 current\_shop\_id)。  
  * **店務管理:** 編輯店家資訊 (如電話、地址)。  
  * **設備監控:** 查看即時用電、設定警報。

## **3\. 功能模組規格 (Functional Specifications)**

### **3.1 組織與身份模組 (Identity Context)**

* **登入/註冊:**  
  * 支援帳號密碼登入。  
  * **QR Code 邀請註冊機制:**  
    * 管理員產生含 invite\_uuid 的店家 QR Code。  
    * 新用戶掃描 \-\> App 解析 UUID \-\> 後端 API 自動建立 User 並寫入 user\_shop\_relations。  
* **多店管理:**  
  * 使用者可查看自己權限下的所有店家。  
  * 系統記錄 current\_shop\_id，下次開啟 App 自動載入上次瀏覽的店家。

### **3.2 店家與設備管理模組 (Shop & Device Context)**

* **店家資訊:**  
  * 欄位包含：名稱、電話、地址、備註、是否為總部 (IsHead)、店家編碼 (Code)。  
* **設備列表:**  
  * 顯示設備名稱、類別 (冷氣、冰箱等)、即時狀態 (在線/離線)。  
  * 支援 **關鍵字搜尋** (依名稱或 MAC)、**分類篩選**。  
  * **離線處理:** 當設備斷線時 (超過 3-5 分鐘無數據)，UI 顯示灰色並提示檢查網路。  
* **BLE 配網 (補充):**  
  * App 透過藍牙寫入 Wi-Fi 帳密與 MQTT Server IP。

### **3.3 數據與儀表板模組 (Telemetry & Dashboard)**

* **首頁儀表板:**  
  * **即時功率 (W):** 顯示當前總用電負載。  
  * **本日用電量 (kWh):** 累計至目前的度數。  
  * **碳排放量 (kg):**  
    * 公式: 用電量 (kWh) \* 0.474。  
    * *注意:* 係數 0.474 必須讀取自 system\_configs 表 (key=carbon\_factor)，不可寫死。  
* **歷史報表:**  
  * 查看每日/每月的用電趨勢圖表。

### **3.4 警報與監控模組 (Alerts & Monitoring)**

* **警報設定 (針對單一設備):**  
  1. **用量限制:** 設定每日或每月用電上限 (kWh)，超過即產生警報。  
  2. **非營業時間偵測 (Curfew):** 設定起始與結束時間 (e.g., 22:00 \- 06:00)，若期間內設備功率 \> 0 (運轉中) 則發送警報。  
* **警報紀錄:**  
  * 紀錄警報類型、訊息、發生時間。  
  * **快照 (Snapshot):** 紀錄異常發生當下的 **電壓 (Voltage)、電流 (Current)、功率 (Power)** 數值 (對應舊版 warn 表)，以便事後追查。

## **4\. 資料庫設計 (Database Schema V2.1)**

本設計已整合舊有 basic.\* 表格欄位，並完成正規化。

### **4.1 核心資料表參考**

| 表格名稱 (New) | 用途 | 舊版對應 | 整合與新增欄位說明 |
| :---- | :---- | :---- | :---- |
| **system\_configs** | 全域設定 | \- | key (carbon\_factor), value (0.474) |
| **clients** | 客戶/商圈 | \- | code, name |
| **shops** | 店家 | basic.branch | invite\_uuid (新增), code, is\_head, memo |
| **users** | 使用者 | basic.employee | account, password, is\_admin, current\_shop\_id |
| **user\_shop\_relations** | 用戶-店家關聯 | \- | user\_id, shop\_id, shop\_role |
| **device\_types** | 設備類別 | basic.systag | name, icon\_key, code |
| **devices** | 設備主檔 | basic.device | mac\_address, location, is\_online, memo |
| **device\_alert\_settings** | 警報設定 | \- | daily\_limit, non\_usage\_start/end\_time |
| **power\_readings** | 電力數據 (Raw) | \- | voltage, current, power, kwh\_total |
| **alert\_logs** | 警報紀錄 | basic.warn | type, message, **快照(V/A/W)** |
| **daily\_usages** | 每日統計 | basic.report | date, kwh\_usage, carbon\_kg |

## **5\. API 介面規劃 (API Endpoint Plan)**

所有 API 均以 /api 為前綴，並需在 Header 帶入 JWT Token。

### **5.1 Auth**

* POST /api/auth/login: 登入。  
* POST /api/auth/register: 註冊 (Payload 需包含 invite\_uuid)。

### **5.2 Shops**

* GET /api/shops/my: 取得我能看的所有店家。  
* POST /api/shops/switch: 切換當前店家 (更新 current\_shop\_id)。  
* GET /api/shops/:id: 取得店家詳情 (含 QR Code 字串)。

### **5.3 Devices**

* GET /api/shops/:id/devices: 取得某店家的設備列表 (含 is\_online)。  
* POST /api/devices: (Admin) 綁定新設備。  
* PUT /api/devices/:id/alert-settings: 更新警報設定。

### **5.4 Dashboard**

* GET /api/dashboard/summary: 取得首頁數據 (今日用電、碳排、即時功率)。  
* GET /api/dashboard/chart: 取得圖表數據。

## **6\. 非功能性需求 (NFR)**

### **6.1 資料生命週期 (Data Retention)**

* **power\_readings (Raw Data):** 保留 3 個月 (可透過排程清除，CronJob 每日清除)。  
* **daily\_usages (統計數據):** 保留 5 年以上。  
* **alert\_logs:** 保留 1 年。
* **離線容錯:** 若網路中斷，App 應顯示快取數據或明確的離線提示，不可崩潰。  
* **Docker Log:** 設定 Log Rotation (單檔 100MB, 保留 5 份)。


### **6.2 時間同步與校正 (Time Synchronization)**

* **規則:** 若時間戳小於 2020 年，**必須強制使用 Server Time** 覆寫，確保時序正確。

### **6.3 安全性 (Security)**

* **MQTT:** 啟用帳號密碼認證。  
* **API:** 配置 SSL/HTTPS。

## **7\. 開發規範與版本控制 (Development & Version Control)**

### **7.1 版本控制策略 (Git Workflow)**

本專案採用 **Gitflow** 簡化版策略：

* **分支命名規範:**  
  * main: **正式發佈分支**。隨時保持可部署狀態 (Production Ready)。禁止直接 Push，需透過 Pull Request (PR) 合併。  
  * develop: **開發主分支**。所有新功能開發完成後合併至此。  
  * feature/\<feature-name\>: **功能分支**。從 develop 切出，開發特定功能 (e.g., feature/mqtt-service, feature/auth-login)。  
  * fix/\<bug-name\>: **修復分支**。修復 Bug 使用。  
* **Commit Message 規範 (Conventional Commits):**  
  * 格式: \<type\>(\<scope\>): \<description\>  
  * 範例: feat(backend): add mqtt service  
  * 類型 (Type):  
    * feat: 新功能  
    * fix: 修復 Bug  
    * docs: 文件修改  
    * chore: 建置過程或輔助工具的變動 (如 Dockerfile)

### **7.2 Monorepo 管理**

由於後端 (Backend)、App (Mobile) 與基礎設施 (Infra) 在同一倉庫：

* **提交原則:** 盡量保持一次 Commit 只修改一個模組 (Backend 或 Mobile)，避免混雜。  
* **忽略檔案:** 必須嚴格設定 .gitignore，排除 node\_modules, build/, .env, mosquitto/data 等敏感或暫存檔案。