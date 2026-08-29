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
| Measurement Point Detail read path | ✅ Development implemented / verified |
| Dashboard daily/monthly energy | ✅ Development implemented / verified |
| Dashboard Carbon summary | ✅ Development implemented / verified |
| Shop tariff classification | ✅ Development implemented / verified |
| Billing V1 configuration | ✅ Development implemented / verified |
| Billing Energy / coverage | ✅ Development implemented / verified |
| Billing estimate | ✅ Development implemented / verified |
| Admin Binding transaction/concurrency | ✅ Backend implemented / verified |
| User authentication/session/JWT | ✅ Development implemented / verified |
| Admin mutation authorization | 🕒 Planned |
| Public Admin HTTP API | 🕒 Planned |
| Flutter real Backend integration | ✅ Development/runtime/E2E verified |
| Local Runtime Operator | ✅ Accepted / merged / ready for use |
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

   Server 會連線資料庫、執行 embedded versioned SQL migrations，再啟動 MQTT client 與 HTTP server。根路徑 health endpoint 為 `GET /`；目前沒有公開 Admin Device Binding API。已註冊的 read 與 scoped-admin routes 見 Backend Interface Inventory。

For an already-provisioned local workstation, normal Power-IoT lifecycle
operations should prefer `./scripts/local-runtime.sh`. The manual Docker,
Backend, and Simulator startup steps in this README remain useful for
bootstrap, troubleshooting, or explicit verification.

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

Telemetry availability is independent from Carbon availability and Billing estimate availability. Successful telemetry does not create a carbon factor or a billing tariff/configuration; Carbon factors and Billing configuration/estimate data may remain unconfigured while telemetry is healthy.

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

## Backend Interface Inventory

The current server composition exposes 12 HTTP routes: `GET /` is public liveness/readiness (including database and MQTT state); public authentication routes are `POST /api/v1/auth/login` and `POST /api/v1/auth/refresh`; authenticated routes are logout, profile, shop listing, dashboard, measurement-point detail, billing configuration read, and billing estimate; scoped-admin mutations are `PATCH /api/v1/shops/:shopId` and `PUT /api/v1/shops/:shopId/billing/configuration`. Shop reads require an active Shop and an explicit `UserShopRelation`. Admin status does not bypass Shop scope, and `CurrentShopID` is only current-view preference state.

MQTT requires TLS (`tls://`, TLS 1.2 minimum). The Backend subscribes to `device/upload/data` and `device/+/status`, publishes telemetry application ACKs to `device/{MAC}/telemetry/ack`, and distinguishes those ACKs from MQTT transport QoS. Device identity is the normalized uppercase MAC; protocol v1 uses `boot_counter` plus `seq` for identity/deduplication and may carry coverage interval fields. The canonical local simulator is `tools/device-simulator` and requires a successful application ACK, not only broker connectivity.

## Local Runtime Asset Classes

The local operator recognizes only evidence-backed `ACTIVE_CANONICAL` assets. Current workstation evidence identifies the UI PostgreSQL/TimescaleDB container on port `55435`, the repository Mosquitto container on TLS port `8883`, the Backend, and the canonical simulator process; these port values are local observations, not universal product constants. The database on port `5432` is `PRESERVE_LEGACY`, and repository dedicated PostgreSQL test infrastructure on port `55434` is not runtime-owned. Historical, test, stale, unrelated, or insufficiently proven assets are reported but ignored by lifecycle commands; cleanup is a separate reviewed task.

## Power-IoT Local Runtime Operator

The reusable local lifecycle entry point is:

- **WSL / Linux source of truth:** `./scripts/local-runtime.sh`
- **Windows wrapper:** `tools/windows/power-iot-local.bat`

The Bash script owns runtime logic. The BAT file only forwards the command and
arguments into WSL. The Operator manages only verified `ACTIVE_CANONICAL`
Power-IoT assets; it is not a general workstation process or container manager.

### Quick start

From the repository root:

```bash
./scripts/local-runtime.sh status
./scripts/local-runtime.sh start core
./scripts/local-runtime.sh start telemetry
./scripts/local-runtime.sh start ui
```

Use `status` first to inspect the non-secret inventory. `core` starts the local
UI database, MQTT, and Backend. `telemetry` adds the Device Simulator. `ui`
adds Flutter using the canonical UI startup path; an existing approved Android
Emulator may be detected and reused, but the emulator is not normal
Operator-owned shutdown state.

### Runtime profiles

| Profile | Starts | Intended use |
|---|---|---|
| `core` | UI DB + MQTT + Backend | Backend-only development |
| `telemetry` | `core` + Device Simulator | Telemetry development and ACK verification |
| `ui` | `core` + Flutter | Flutter integration against the canonical Backend endpoint |

The Operator does not start an Android Emulator; it reuses an approved
connected emulator when `ui` needs one.

### Stop, restart, and logs

Stop only the named canonical process, or use the full local runtime stop:

```bash
./scripts/local-runtime.sh stop simulator
./scripts/local-runtime.sh stop backend
./scripts/local-runtime.sh stop ui
./scripts/local-runtime.sh stop runtime
```

`stop runtime` stops Flutter, the Simulator, the Backend, and canonical MQTT.
It does **not** destroy database data: the canonical UI DB, legacy DB, database
volumes, telemetry history, fixtures, Carbon/Billing configuration, and
existing development configuration are preserved. The Operator performs no database reset, reseed, or purge.

For a targeted restart or recent logs:

```bash
./scripts/local-runtime.sh restart backend
./scripts/local-runtime.sh restart simulator
./scripts/local-runtime.sh logs backend
./scripts/local-runtime.sh logs simulator
```

For the complete command surface:

```bash
./scripts/local-runtime.sh help
```

### Recommended daily workflow

- **Backend-only:** `status` → `start core`
- **Telemetry:** `status` → `start telemetry`
- **Flutter integration:** `status` → `start ui`
- **End of work:** `stop runtime`

### Safety and ownership

Lifecycle control applies only to verified `ACTIVE_CANONICAL` Power-IoT
assets. It does not manage the legacy DB, repository dedicated PostgreSQL test
infrastructure, unrelated IoT Backends, unrelated Flutter applications,
unrelated Docker containers, GitHub MCP, or other projects sharing the
workstation.

On this workstation, the known roles are:

| Endpoint | Role |
|---|---|
| `127.0.0.1:5432` | `PRESERVE_LEGACY` |
| `127.0.0.1:55435` | Current canonical local UI DB |
| `127.0.0.1:55434` | Repository dedicated PostgreSQL test infrastructure |

These port values describe the current workstation, not universal product
defaults. In particular, `55435` is a local UI DB endpoint and `55434` is not
a runtime DB or an Operator-managed asset. Keep repository contracts distinct
from current workstation state.

Before lifecycle actions, the Operator verifies process and container
ownership, repository paths, and protected configuration. If ownership or the
Backend database target cannot be proven, it fails closed with states such as
`PROCESS_OWNERSHIP_UNVERIFIED` or `BACKEND_DB_TARGET_UNVERIFIED`. This refusal
is intentional; do not bypass it. `status` is read-only and reports non-secret
facts even when a component is degraded or unknown. Secrets remain in
protected local sources and are never printed.

PR #25 is merged and this Operator is accepted for normal local Power-IoT
operation.

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

Flutter UI 已包含 login、dashboard、devices、shops、profile 與 alert 相關畫面，並使用 Riverpod 與 GoRouter。核心 development integration 現在使用真實 Backend：real authentication、refresh/logout、`/me`、`/shops`、remote dashboard and the Measurement Point Detail read path. Device Binding remains a backend transaction foundation rather than a public Admin Device Binding API. Android → HTTP → Go → PostgreSQL E2E 與 real MQTTS → Backend → PostgreSQL → Flutter development proof 均已通過。Current accepted development capabilities include Dashboard daily/monthly energy, Dashboard Carbon summary, Shop tariff classification, Billing V1 configuration, historical energy/coverage, and billing estimates; these are system-integration capabilities, not an official utility bill. This does not imply production deployment/readiness or physical hardware validation. Dashboard 數值現在支援 automatic refresh：產品預設每 300 秒輪詢一次；local development/E2E 可使用 positive-integer `--dart-define=POWER_IOT_DASHBOARD_POLL_SECONDS=<seconds>` 覆寫（10 秒僅供加速驗證，不是產品預設）。輪詢只在 app lifecycle 為 resumed 且 Dashboard route 可見時啟用，route 被覆蓋或 app 離開 resumed 狀態時停止；這不代表 production deployment/readiness。BLE provisioning、QR flow 與離線快取仍未完成。

## Firmware Boundary

ESP8266 / HLW8032 firmware 由獨立專案維護。本 repository 提供 system-side protocol integration、Backend、Infrastructure 與 Simulator；Simulator success 不代表 physical firmware flashing、Wi-Fi recovery、OTA 或 mains-electrical validation 已完成。

## Production Notice

目前適合 development/local system integration，不應直接視為 production deployment。正式環境仍需要：

- production Auth/API security hardening
- credential、MQTT ACL 與 certificate lifecycle
- backup/restore 與 disaster recovery rehearsal
- observability、capacity/load validation
- CI/CD、release hardening 與 operational runbooks
