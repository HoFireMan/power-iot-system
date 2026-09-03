# CONTEXT.md

Current Power-IoT mainline state and accepted checkpoints. Durable working rules
are in `AGENTS.md`; public/product documentation remains in `README.md`.

## Mainline State

Canonical repository:

`/home/admin-195/code/power-iot-system`

Live canonical Git HEAD is authoritative in Git. Obtain it from the canonical
repository with `git rev-parse main`.

DOCUMENTATION_RECONCILED_THROUGH = **PR #52**

Current documentation status: **ACCEPTED / MERGED / DOCS_RECONCILED / DEVICE_RUNTIME_PENDING**

The accepted mainline through PR #52 includes the earlier Admin Binding,
IDENT-002, Alerts V1, Assignment History, Audit History, BUG-006, and BUG-004
checkpoints recorded below. No source, product, protocol, schema, migration,
MQTT topic, Flutter route, or HTTP route change is authorized by this context
reconciliation.

## Accepted Local Runtime Operator

STATUS = **ACCEPTED / MERGED / READY_FOR_USE**

PR = **25**

MERGE_COMMIT = `43fc892478b44f898e8e3386650976aa69bfae06`

PRIMARY_OPERATOR = `scripts/local-runtime.sh`

WINDOWS_WRAPPER = `tools/windows/power-iot-local.bat`

CANONICAL_BACKEND_RUNTIME = **main worktree**

BACKEND_DB_TARGET = **canonical UI DB**

LEGACY_DB = **PRESERVE_ONLY**

PROCESS_OWNERSHIP_GUARD = **ENABLED**

SIMULATOR_APPLICATION_ACK_HEALTH = **ENABLED**

SIMULATOR_BOOT_IDENTITY = **persistent monotonic local boot counter**

Normal local lifecycle operations must use the Operator rather than invented
manual lifecycle commands. The Backend baseline was already running and was
left untouched during this documentation task.

## Accepted Checkpoints Since the Previous Baseline

### IDENT-002 — MeasurementPoint-centered derived identity

- PR #36 implementation: `49529edf06b2d6d063b260d61cc9a88becaaf60d`
- PR #37 documentation reconciliation: merged

Migration `000010_measurement_point_identity` established the accepted
MeasurementPoint-centered identity boundary. AlertLog rows are backfilled only
when an exact historical DeviceAssignment match exists at `CreatedAt`; legacy
rows that are unattributable or ambiguous remain explicitly unresolved. The
DailyUsage writer/reader was proven inactive, so legacy device-day rows remain
quarantined evidence and future authoritative uniqueness is
`(date, measurement_point_id)`. Replacement, relocation, half-open interval,
rollback, replay, and admission-repair evidence passed. `Device.ShopID` and MAC
remain non-authoritative.

**IDENT-002 is CLOSED.** This closes the identity-resolution debt; it does not
claim that every future derived-data/reporting capability exists.

### Alerts V1

- PR #39 implementation: `2e6e542437087da027661da72a6512cec6a559a9`
- PR #40 documentation: `79e0c610cba3e65aedb03ec6c6075f99acddfcee`
- PR #41 recovery acceptance: `2daa0823690ad88849110fbef669a50d68c0ac7b`
- PR #42–#43 acceptance/documentation governance: merged before the later
  Admin Binding history sequence

Alerts V1 is accepted for development/local system integration. It includes
MeasurementPoint-centered settings and durable edge-triggered `CURFEW_USAGE`
lifecycle, authenticated Shop-scoped read history, MeasurementPoint filtering,
newest-first cursor pagination, replacement/relocation behavior, out-of-order
handling, PostgreSQL concurrency deduplication, and disposable PostgreSQL plus
isolated Backend runtime evidence. Settings GET is Shop-member readable; PUT
requires the existing scoped-admin capability plus Shop membership. Quiet hours
use `Asia/Taipei` and `[start, end)` semantics; the validated per-MP threshold
has a 10 W default.

The Flutter Alert History and Alert Settings sources are present with focused
model/UI integration tests. Device-level Flutter/runtime acceptance remains
pending an operator-owned emulator. V1 does not include notification delivery,
mark-read/read-acknowledgement semantics, daily/monthly kWh evaluators,
per-Shop timezone configuration, or production hardening.

### Admin Assignment History and Audit History

Admin Assignment History remains an accepted development capability with
isolated Backend and Flutter evidence. It provides the authorized
Device-to-MeasurementPoint interval timeline, current human-readable
resolution, Active/Ended and MP/Device filters, and safe Shop transitions.
Device-level runtime acceptance remains pending an operator-owned emulator.

Admin Binding Audit History was accepted through:

- PR #44 read slice: `8f7a36f27380a1143ddd0da14799878eb59f4931`
- PR #45 documentation reconciliation: merged
- PR #46 same-timestamp cursor evidence: merged
- PR #47 UI recovery: `dc752dde12885e4dfc0c8b5776ac32a71436028f`
- PR #48 documentation reconciliation: merged

The read-only audit view covers the five persisted operations:
`create_measurement_point`, `bind`, `replace`, `relocate`, and `unbind`. It is
scoped by authenticated scoped-admin capability and active Shop authorization,
supports Action/MeasurementPoint/Device filters, and uses stable
`(occurred_at, id)` cursor pagination. Historical IDs and serial/MAC snapshots
are authoritative; current Actor, Device, and MeasurementPoint names are
nullable enrichment. Relocate rows require current authorization to both source
and target Shops and are excluded when provenance cannot be verified.

Flutter filter text is draft-only until Apply. Apply promotes normalized values,
resets pagination, and generation-scoped loading/results prevent stale pages
from repopulating a newer Shop or filter view. No audit mutation, actor filter,
schema migration, or audit-writer change is included in this read/UI slice.

### BUG-006 — diagnostics command parity

- PR #49 implementation: `3b9f1aade94a3662b08f852261b0aaa05c578575`
- PR #50 documentation reconciliation: `2c8ae7f42f5e406d4519dc6cdb82fc062192776a`

`diagnostics` is the canonical Device Protocol v1 command action and
`report_diagnostics` is an exact compatibility alias. Both use the existing
CommandEnvelope, command topic, and generic command acknowledgement. Backend,
simulator, and `iotctl` validation agree. No diagnostic-report topic, payload,
persistence, consumer, report-specific identity, authorization, or sensitive
data transport exists. BUG-006 is **CLOSED**; real firmware behavior is not
claimed. Future diagnostic reporting remains separate scope:
`DEVICE_DIAGNOSTIC_REPORT_V1`.

### BUG-004 — unsafe legacy MQTT constructor

- PR #51 implementation: `a54cb09e1f6d781e33967ca7bd6f4c8961713571`
- PR #52 documentation reconciliation: merged

The unused `NewMqttService(brokerURL, db)` constructor was removed. All
repository Backend/tool construction remains:

`LoadMqttConfigFromEnv → NewMqttServiceWithConfig`

This preserves TLS, CA validation, credentials, reconnect, client ID, and D6
ingestion-mode authority. No replacement brokerURL constructor or protocol,
topic, schema, migration, Flutter, or HTTP route change was introduced.
BUG-004 is **CLOSED**. Real-device and production validation are not claimed.

## Current Accepted Development Capability Summary

- Auth/session/JWT integration, authenticated API flow, `/me`, and `/shops`.
- Shop-scoped Dashboard current power, daily/month energy, Carbon projection,
tariff/Billing configuration, historical energy/coverage, and Billing V1
estimate semantics.
- Authenticated Shop-scoped monthly MeasurementPoint historical energy report
with real Flutter integration.
- MeasurementPoint-centered historical assignment attribution across Device
replacement and relocation.
- Authenticated scoped-admin Admin Device Binding HTTP lifecycle: Create
Measurement Point, Bind, Replace, Relocate, and Unbind.
- Real Flutter Admin Binding integration with authoritative refresh, retry-safe
request identity, and double-submit serialization.
- Admin Assignment History and read-only Admin Binding Audit History UI slices,
including their focused Flutter acceptance tests.
- Alerts V1 Backend/system integration plus Flutter Alert History and Settings
sources/tests; device-level runtime remains pending.
- Dashboard-only durable last-successful snapshot cache V1. It is scoped by
authenticated User and authorized Shop, supports transient stale-read fallback,
and does not provide offline login, offline authorization, mutation queues, or
broader product caching.
- Local Runtime Operator accepted and ready for development use.

## Current Boundaries and Next Legal Work

- Production deployment and hardening remain pending.
- Physical ESP8266/fleet validation remains pending and belongs to the external
firmware/hardware acceptance process.
- Device-level Flutter acceptance remains pending an operator-owned emulator;
the connected `emulator-5554` is not operator-owned and must not be used as
acceptance evidence.
- Broader historical reports, exports, arbitrary date ranges, and production
reporting hardening remain incomplete.
- Broader Alerts remain incomplete for daily/monthly kWh thresholds,
read/acknowledgement semantics, notification delivery, per-Shop timezones,
retention policy, and production hardening.
- General Admin inventory/history and broader audit domains remain incomplete
beyond the accepted bounded slices. No actor directory/filter or audit mutation
is implied.
- BLE/QR provisioning, broader offline/product caching, and physical recovery
remain incomplete or not frozen.
- Global-admin semantics, complete tenant/role/device scope, and production
credential/certificate lifecycle remain incomplete.

Dashboard refresh contract:

- **NORMAL PRODUCT DEFAULT = 300 seconds**
- **DEVELOPMENT / E2E OVERRIDE = 10 seconds**
- **10 seconds is test acceleration only; it is not the product default.**

Actual production/device MQTT telemetry interval: **UNCONFIRMED**. Do not claim
that device telemetry is 300 seconds unless separately established.

Backend current-power freshness remains **120 seconds**.

## Production and Safety Boundary

- `PRODUCTION_EXECUTION_AUTHORIZED = NO`
- `PRODUCTION_TCRFID01_MUTATED = NO`
- `PRODUCTION_DB_MUTATED = NO`
- `PRODUCTION_CUTOVER = NOT_STARTED`

No production, TCRFID01, private firmware, OTA key, or canonical database access
occurred. PostgreSQL validation, where required by accepted feature work, used
disposable test databases only. Preserve unrelated untracked/private files,
including:

- `ADMIN_BINDING_AUDIT_HISTORY_REBASE_01.txt`
- `infrastructure/firmware/`
- `infrastructure/mosquitto/config/acl`

## Engineering Policy

Power-IoT feature-development default:

- **PROFILE C — Single-Subagent Sequential**
- **VERTICAL SLICE** where implementation is requested
- **TDD**
- **DEEP MODULES**

Current documentation work is docs-only and must not alter source behavior.
Profile A and D are not normal defaults. Preserve canonical contracts, use
focused validation, keep verified/planned/production maturity labels distinct,
and stop at the declared milestone boundary.

The technical debt register remains canonical for debt status. At this
checkpoint BUG-004 and BUG-006 are CLOSED, IDENT-002 is CLOSED, and unresolved
production/physical-device gates remain deferred rather than silently closed.

## Repository Preservation Boundary

Before and after documentation edits, preserve unrelated user changes and avoid
destructive history operations. Do not read or stage:

`infrastructure/firmware/certs/ota.key`
