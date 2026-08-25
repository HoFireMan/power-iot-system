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
| User authentication/session/JWT | ✅ Development implemented / verified |
| Admin mutation authorization | 🕒 Planned |
| Public Admin HTTP API | 🕒 Planned |
| Flutter real Backend integration | ✅ Development/runtime/E2E verified |
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
     ↓ current HTTP API boundary
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
APP_ENV=development \
DEVSEED_ENABLE=true \
DEVSEED_PASSWORD='<redacted/runtime input>' \
MQTT_CA_FILE=../infrastructure/mosquitto/certs/ca.crt \
  go run ./cmd/devseed --device-mac AABBCCDDEEFF --device-name test-meter-01
```

`DEVSEED_PASSWORD` must be supplied as a runtime secret. Do not place a real password, password hash, or token in tracked files.

For local/test B-02 coverage ingestion, the optional `--coverage-max-interval-ms` flag (or `DEVSEED_COVERAGE_MAX_INTERVAL_MS`) explicitly bootstraps `system_configs.key=coverage.max_interval_ms`. There is no implicit default; values must be at least `1000` milliseconds. A missing key is created, an identical value is idempotent, and a conflicting existing value fails closed. For the local simulator's five-second coverage intervals, pass `--coverage-max-interval-ms 5000`; this is local test configuration only and is not a production default.

For an explicit local/test scoped-admin fixture, add `--admin-fixture` and supply the separate runtime secret `DEVSEED_ADMIN_PASSWORD`. The fixture creates `devseed-admin` with `is_admin=true` and a relation only to the development Shop; it is never created by default. Do not promote `devseed` or reuse `DEVSEED_PASSWORD` implicitly. This fixture does not configure Carbon or Billing.

### Trusted proxy client-IP configuration

The backend reads the optional `TRUSTED_PROXY_CIDRS` environment variable as a comma-separated list of exact trusted reverse-proxy CIDRs. When it is empty or unset, the server uses direct-peer-only semantics: `X-Forwarded-For` and `Forwarded` are ignored from untrusted peers. Reverse-proxy operators must explicitly set the exact CIDR(s) for their deployment; malformed or trust-all CIDRs fail startup closed. No production CIDR value is supplied by this repository or this change.

## Local Development Runbook

This runbook describes the reproducible local system-side path. It is not a production deployment procedure and does not define a hardware telemetry cadence.

### Canonical local data path

```text
PostgreSQL / TimescaleDB
  → schema admission (serving state: CLEAN_B02)
  → canonical development seed and coverage configuration
  → Mosquitto (MQTTS)
  → Go Backend
  → tools/device-simulator
  → MQTT application ACK
  → power_readings
  → Backend Dashboard API / Flutter Dashboard
```

Each component has a separate responsibility:

- PostgreSQL/TimescaleDB stores the protected application catalog and telemetry.
- Schema admission proves that the database is eligible to serve; a normal serving database is `CLEAN_B02`.
- `backend/cmd/devseed` creates or verifies only the development identity and fixture: user/shop, Device, MeasurementPoint, and current DeviceAssignment.
- The devseed coverage option writes the runtime-owned `system_configs` entry needed by B-02 coverage ingestion.
- Mosquitto terminates local TLS and carries device telemetry and application acknowledgements.
- The Backend validates telemetry, resolves the current assignment, persists accepted readings, and sends the application ACK.
- The simulator generates protocol telemetry; a publish without a successful application ACK is not an ingestion proof.
- The Dashboard API and Flutter client expose the resulting current power and energy projections.

### Database and schema admission

Use a local isolated PostgreSQL/TimescaleDB database and set the Backend `DATABASE_URL` to that intended local database. Do not treat any particular workstation port as a repository or product default. Before serving, use the supported migration/admission operators and verify the protected serving state is `CLEAN_B02`.

Protected migration transitions must use their supported operators. Do not use `migrate force`, manually edit migration metadata, apply raw schema SQL, recreate a protected database, or mutate migration state by hand. A schema that is not admitted as `CLEAN_B02` must be investigated or repaired through the supported operator boundary before telemetry verification continues.

### Development seed and coverage configuration

Run the canonical command from `backend/`; do not manually insert fixture rows or configuration:

```bash
cd backend
set -a
. ../.env
set +a
APP_ENV=development \
DEVSEED_ENABLE=true \
DATABASE_URL="${LOCAL_DATABASE_URL:?set the intended local database URL}" \
DEVSEED_PASSWORD="${DEVSEED_PASSWORD:?supply the runtime secret}" \
go run ./cmd/devseed \
  --device-mac "${DEV_DEVICE_MAC:?set a registered development MAC}" \
  --measurement-point-name "UI Test Meter" \
  --coverage-max-interval-ms 5000
```

`APP_ENV=development` and `DEVSEED_ENABLE=true` are explicit admission guards. `DEVSEED_PASSWORD` is a runtime secret and must come from an existing protected secret mechanism; never put it in this README, a tracked environment file, command history, or runtime-state record. The command is idempotent for the existing development user/shop, Device, MeasurementPoint, and valid current DeviceAssignment. The Device MAC must belong to the intended local development fixture.

`--coverage-max-interval-ms` is optional for non-coverage fixtures. When supplied, it configures `system_configs.key=coverage.max_interval_ms`. The value is an integer number of milliseconds, must be at least `1000`, and has no implicit default. A missing value is fail-closed at coverage ingest; an existing conflicting value must not be overwritten. For the five-second local simulator example, use `5000` explicitly. This is a **LOCAL DEVELOPMENT / TEST VALUE ONLY**; it is not a production default, hardware contract, or production telemetry cadence.

### Backend and Mosquitto verification

Start Mosquitto with the local TLS configuration and start the Backend only after the intended local database is admitted and the development fixture is available. Keep database, MQTT, JWT, and devseed credentials in protected runtime configuration; do not copy them into commands committed to documentation.

Verify the local Backend health endpoint:

```bash
curl -fsS http://localhost:8080/
```

A healthy local response includes:

```text
db = connected
mqtt_ready = true
mqtt_ingestion_blocked = false
```

The Backend must point at the intended local database and the local MQTTS broker. A healthy process that started before a coverage key was created need not be restarted when its ingestor reads `coverage.max_interval_ms` from `system_configs` for each coverage ingest transaction; verify the source/runtime contract before restarting a service.

### Canonical simulator and ACK proof

The canonical simulator is `tools/device-simulator`. Use a development Device with a valid current DeviceAssignment and local TLS credentials supplied through protected runtime configuration. A five-second local coverage example is:

```bash
cd tools/device-simulator
# Supply these from protected local runtime configuration; never echo them.
: "${MQTT_BROKER_URL:?set the local tls:// broker URL}"
: "${MQTT_USERNAME:?set the local MQTT username}"
: "${MQTT_PASSWORD:?supply the local MQTT password at runtime}"
: "${MQTT_CA_FILE:?set the local public CA path}"
go run . \
  --mode continuous \
  --device-mac "${DEV_DEVICE_MAC:?set a registered development MAC}" \
  --publish-interval 5s \
  --coverage-profile \
  --clock-synchronized=true
```

The simulator reads the broker URL, username, password, and CA path from these runtime environment variables; they may instead be passed as equivalent flags from a protected launcher. Do not place their values in this README or in the runtime-state file.

The five-second interval is only a local development/test choice. Do not infer a production or physical-device cadence from it. The successful application path is:

```text
MQTT publish
  → Backend validation and assignment resolution
  → PostgreSQL persistence
  → application ACK (stored or duplicate)
```

A simulator log that says only `PUBLISHED` is insufficient; the corresponding application ACK must succeed.

### Telemetry verification sequence

After multiple simulator publishes, verify in this order:

1. The simulator has an established MQTTS connection.
2. Each accepted publish receives a successful application ACK.
3. The `power_readings` count increases.
4. The latest `received_at` is recent.
5. Coverage rows have `coverage_version=1`, non-null `interval_start`/`interval_end`, non-null `energy_delta_kwh`, and `recorded_at=interval_start`.
6. Dashboard `currentPowerW` is non-null.
7. Dashboard `dailyKwh` is non-null.
8. Dashboard `monthlyKwh` is non-null.
9. Flutter Dashboard displays the telemetry instead of its no-data state.

Telemetry availability is independent from Carbon availability and Billing estimate availability. Successful telemetry does not create a carbon factor or a billing tariff/configuration; Carbon and Billing may remain unavailable while telemetry is healthy.

### Configuration catalog: `coverage.max_interval_ms`

| Field | Value |
|---|---|
| Purpose | Maximum accepted interval for B-02 coverage ingestion |
| Type | Base-10 integer |
| Unit | Milliseconds |
| Minimum | `1000` |
| Authority | `system_configs.key=coverage.max_interval_ms` |
| Missing behavior | Fail closed; no implicit runtime default |
| Implicit default | None |
| Local example | `5000`, local development/test only |
| Production value status | `UNRESOLVED / NOT ESTABLISHED` |

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

Flutter UI 已包含 login、dashboard、devices、shops、profile 與 alert 相關畫面，並使用 Riverpod 與 GoRouter。核心 development integration 現在使用真實 Backend：real authentication、refresh/logout、`/me`、`/shops`、remote dashboard，以及由 dashboard 資料驅動的 Device Management。Android → HTTP → Go → PostgreSQL E2E 與 real MQTTS → Backend → PostgreSQL → Flutter development proof 均已通過。這不代表 production deployment/readiness、physical hardware validation，或 I1-B daily/monthly energy/carbon aggregation 已完成。Dashboard 數值現在支援 automatic refresh：產品預設每 300 秒輪詢一次；local development/E2E 可使用 positive-integer `--dart-define=POWER_IOT_DASHBOARD_POLL_SECONDS=<seconds>` 覆寫（10 秒僅供加速驗證，不是產品預設）。輪詢只在 app lifecycle 為 resumed 且 Dashboard route 可見時啟用，route 被覆蓋或 app 離開 resumed 狀態時停止；這不代表 production deployment/readiness。BLE provisioning、QR flow 與離線快取仍未完成。

## Firmware Boundary

ESP8266 / HLW8032 firmware 由獨立專案維護。本 repository 提供 system-side protocol integration、Backend、Infrastructure 與 Simulator；Simulator success 不代表 physical firmware flashing、Wi-Fi recovery、OTA 或 mains-electrical validation 已完成。

## Production Notice

目前適合 development/local system integration，不應直接視為 production deployment。正式環境仍需要：

- production Auth/API security hardening
- credential、MQTT ACL 與 certificate lifecycle
- backup/restore 與 disaster recovery rehearsal
- observability、capacity/load validation
- CI/CD、release hardening 與 operational runbooks
