# **Power IoT System \- 軟體開發生命週期 (SDLC)** 

**版本:**V3.2

**日期:** 2025-11-27

**模式:** 迭代式開發 (Iterative) / 敏捷 (Agile)

**部署情境:** 營運端集中部署 (Provider-Hosted)

## **階段一：探索與定義 (Discovery)**

| 任務 | 描述 | 狀態 |
| :---- | :---- | :---- |
| **1.1 硬體規格確認** | 確認 MQTT Topic。**確認韌體是否寫死 Server IP 或需透過 BLE 寫入**。 | 需確認 |
| **1.2 領域建模** | 確認 User-Shop 多對多關係、Alert 快照邏輯、**碳排計算公式**。 | ✅ 已完成 |

## **階段二：設計與架構 (Design)**

| 任務 | 描述 | 狀態 |
| :---- | :---- | :---- |
| **2.1 網路架構設計** | 確認營運端伺服器的 **固定 IP** 與 **Port Forwarding** 設定。 | 待執行 |
| **2.2 安全性設計** | 規劃 Mosquitto 帳號密碼機制、API SSL/TLS (HTTPS) 配置方案。 | 待執行 |
| **2.3 資料庫 Schema** | V2.1 Schema (PostgreSQL) 定版 (含 Index 優化)。 | ✅ 已完成 |
| **2.4 版本控制初始化** | 建立 Git Repository，設定 .gitignore，建立 develop 分支。 | 待執行 |

## **階段三：開發與實作 (Development)**

**開發順序:** 基礎設施 \-\> 後端核心 \-\> App 功能

| 順序 | 模組 | 任務描述 |
| :---- | :---- | :---- |
| **3.1** | **Infra** | 在**營運端伺服器** (或本機開發環境) 啟動 Docker (DB \+ MQTT)。 |
| **3.2** | **Backend (IoT)** | 實作 MqttService： 1\. 訂閱 Broker。 2\. 實作時間校正邏輯。 3\. 實作警報判斷與快照寫入。 |
| **3.3** | **Backend (API)** | 實作 API 模組：Auth, Shops, Devices, Dashboard。 |
| **3.4** | **Backend (NFR)** | 實作資料清理排程 (CronJob)。 |
| **3.5** | **Mobile** | 實作 BLE 配網：傳送 Wi-Fi 帳密 \+ MQTT Server IP。 |
| **3.6** | **Mobile** | 實作 UI：登入、儀表板、警報設定。 |

## **階段四：測試與整合 (Testing)**

| 測試類型 | 描述 |
| :---- | :---- |
| **4.1 公網連線測試** | 將後端部署在伺服器，使用手機 4G 網路測試 API 連線。 |
| **4.2 模擬硬體測試** | 使用 MQTT Explorer (從外部網路) 連線至 Broker，發送數據，驗證防火牆設定。 |
| **4.3 邏輯驗證** | 1\. **時間校正測試**: 發送 1970 年的數據，確認 DB 寫入當下時間。 2\. **警報快照測試**: 發送異常數據，確認 alert\_logs 紀錄了 V/A/P 數值。 |
| **4.4 NFR 驗證** | 修改系統時間或手動觸發 CronJob，確認過期資料是否被刪除。 |

## **階段五：發佈與部署 (Deployment)**

**目標:** 營運端伺服器上線

| 任務 | 描述 |
| :---- | :---- |
| **5.1 伺服器環境準備** | 設定固定 IP、防火牆、安裝 Docker。 |
| **5.2 安全性配置** | 設定 Nginx 反向代理與 Let's Encrypt SSL 憑證。 |
| **5.3 Git 部署流水線** | 設定簡單的 CI/CD 或 Git Hook，當 main 分支更新時自動部署後端。 |
| **5.4 後端部署** | 將 Go 編譯檔與 docker-compose.yml 上傳並啟動。 |
| **5.5 App 發佈** | 上架 Google Play / App Store。 |

## **階段六：維護與監控 (Maintenance)**

| 任務 | 描述 |
| :---- | :---- |
| **6.1 集中監控** | 監控營運伺服器的 CPU、RAM、Disk、網路流量。 |
| **6.2 數據備份** | 定期備份 PostgreSQL 資料庫 (因為所有客戶資料都在這)。 |
| **6.3 異常追蹤** | 定期檢查 alert\_logs 與 system\_logs，優化警報閥值。 |

