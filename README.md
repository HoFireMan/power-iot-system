# Power IoT System

Provider-hosted 電力 IoT 監控系統，整合 Go Backend、PostgreSQL/TimescaleDB、MQTTS、Flutter 與 Device Simulator。

## Overview

Power IoT System 是集中部署方向的電力 IoT 平台，負責接收遠端電力遙測、保存歷史資料，並逐步支援多 Client、Shop 與 Device 管理。Repository 內含系統端 Backend、Mobile UI、MQTT/資料庫基礎設施與 Simulator；ESP8266/HLW8032 firmware 維護於獨立的外部工作區。

主要資料流是：Device 或 Device Simulator 透過已驗證的 MQTTS 將 telemetry 發送至 Mosquitto，Go Backend 驗證並寫入 PostgreSQL/TimescaleDB；Flutter App 則位於 Backend API 邊界，提供跨平台操作介面。

## Current Status

| Capability | Status |
|---|---|
| Backend foundation | ✅ Implemented |
| PostgreSQL + TimescaleDB | ✅ Development verified |
| MQTTS telemetry | ✅ Development/runtime verified |
| Telemetry deduplication / ACK | ✅ Verified |
| MeasurementPoint / DeviceAssignment | ✅ Implemented |
| Admin Binding transaction/concurrency | ✅ Backend implemented / verified |
| Admin Auth/JWT | 🕒 Planned |
| Public Admin HTTP API | 🕒 Planned |
| Flutter real Backend integration | ⚠️ Partial |
| Physical ESP8266 / fleet validation | ⚠️ External / pending |
| Production hardening | 🕒 Planned |

目前的驗證成熟度是 development/local system integration；上述完成項目不代表 production-ready 或 physical hardware validation complete。

## Architecture

```text
Physical Device / Device Simulator
                ↓ MQTTS
             Mosquitto
                ↓
             Go Backend
                ↓
      PostgreSQL + TimescaleDB

Flutter App
     ↓ planned/current HTTPS API boundary
  Go Backend
```

核心識別與歷史歸屬關係為：

```text
Device
  → historical assignment
  → MeasurementPoint
  → Shop / Site
```

MeasurementPoint 是持續存在的邏輯量測位置；Device 可被替換、搬遷或解除綁定，而歷史 assignment 仍可用於遙測歸屬。

## Key Capabilities

- authenticated MQTTS telemetry ingestion
- MAC normalization 與 Device resolution
- `recorded_at` / `received_at` 時間語意
- PostgreSQL deduplication 與 TimescaleDB telemetry storage
- persistence 成功後的 Stored / Duplicate ACK
- 依歷史 MeasurementPoint attribution 處理延遲 telemetry
- Admin Device binding、replacement、relocation、unbind 的 Backend transaction foundation
- Device Simulator，支援無實體設備時的系統端驗證

## Technology Stack

- **Backend:** Go, Gin, GORM
- **Database:** PostgreSQL, TimescaleDB
- **MQTT:** Mosquitto, Eclipse Paho MQTT Go
- **Mobile:** Flutter/Dart, Riverpod, GoRouter
- **Infrastructure:** Docker Compose
- **Testing:** Go tests、real PostgreSQL integration tests、race/static/build validation

## Repository Structure

```text
backend/                 Go service and domain/application code
mobile/                  Flutter UI scaffold
infrastructure/          PostgreSQL, Mosquitto and local TLS setup
tools/device-simulator/  Go-based Device Simulator
.env.example             development environment template
README.md                public project introduction
```

## Getting Started

需求：Git、Docker Engine with Compose v2、Go 1.25.4，以及需要執行 Mobile 時的 Flutter/Dart toolchain。

1. 建立本機環境檔：

   ```bash
   cp .env.example .env
   ```

   將 placeholder 值替換為本機 development credentials；不要使用於 production。

2. 產生 development TLS certificates，並建立本機 Mosquitto user：

   ```bash
   ./infrastructure/mosquitto/scripts/generate-dev-certs.sh <LAN-IP> [DNS-NAME]
   ./infrastructure/mosquitto/scripts/create-dev-user.sh dev-user
   ```

3. 啟動 PostgreSQL/TimescaleDB 與 Mosquitto：

   ```bash
   docker compose -f infrastructure/docker-compose.yml up -d
   docker compose -f infrastructure/docker-compose.yml ps
   ```

4. 啟動 Backend：

   ```bash
   cd backend
   set -a
   . ../.env
   set +a
   MQTT_CA_FILE=../infrastructure/mosquitto/certs/ca.crt go run ./cmd/server
   ```

   Server 會連線資料庫、執行 embedded versioned SQL migrations，再啟動 MQTT client 與 HTTP server。根路徑 health endpoint 為 `GET /`；目前沒有公開 Admin API。

## Backend

Backend 入口是 `backend/cmd/server`。資料庫 schema 由 embedded versioned SQL migrations 管理，另有 `backend/cmd/migrate` 可執行 migration action；GORM models 僅作為 persistence representation。`backend/cmd/devseed` 可明確註冊 development device，未知 MAC 不會由 Server 自動註冊：

```bash
cd backend
set -a
. ../.env
set +a
MQTT_CA_FILE=../infrastructure/mosquitto/certs/ca.crt \
  go run ./cmd/devseed --device-mac AABBCCDDEEFF --device-name test-meter-01
```

## Device Simulator

Simulator source 位於 `tools/device-simulator/`，用於在沒有實體設備時產生 telemetry 並等待 application ACK。支援的主要模式包括 `once`、`continuous`、`duplicate`、`invalid`、`offline-replay` 與 `reconnect`。

從 Simulator module 執行最小的 MQTTS invocation：

```bash
cd tools/device-simulator
go run . \
  --mode once \
  --mqtt-broker-url "$MQTT_BROKER_URL" \
  --mqtt-username "$MQTT_USERNAME" \
  --mqtt-password "$MQTT_PASSWORD" \
  --mqtt-ca-file ../../infrastructure/mosquitto/certs/ca.crt \
  --device-mac AABBCCDDEEFF
```

Simulator 通過不等於 ESP8266 firmware、mains electrical measurement 或 physical hardware runtime 已驗證。

## Testing

Backend：

```bash
cd backend
go test ./...
go vet ./...
go build ./...
```

Simulator：

```bash
cd tools/device-simulator
go test ./...
go vet ./...
go build ./...
```

Backend 中 database-sensitive concurrency tests 需要 disposable PostgreSQL/TimescaleDB test environment；可透過測試環境變數提供連線，不應使用 production database。Flutter UI 可執行：

```bash
cd mobile
flutter analyze
flutter test
dart format --output=none --set-exit-if-changed .
```

## Mobile

Flutter UI/scaffold 已包含 login、dashboard、devices、shops、profile 與 alert 相關畫面，並使用 Riverpod 與 GoRouter。目前主要資料仍是 mock；真實 Backend、Auth/token、離線快取、BLE provisioning 與 QR flow 尚未完成。因此 Mobile 與 Backend 的 end-to-end integration 仍屬 partial。

## Firmware Boundary

ESP8266 / HLW8032 firmware 由獨立專案維護。本 repository 提供 system-side protocol integration、Backend、Infrastructure 與 Simulator；Simulator success 不代表 physical firmware flashing、Wi-Fi recovery、OTA 或 mains-electrical validation 已完成。

## Production Notice

目前適合 development/local system integration，不應直接視為 production deployment。正式環境仍需要：

- Auth/API security boundary
- credential、MQTT ACL 與 certificate lifecycle
- backup/restore 與 disaster recovery rehearsal
- observability、capacity/load validation
- CI/CD、release hardening 與 operational runbooks
