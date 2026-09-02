# CONTEXT.md

Current Power-IoT mainline state and accepted checkpoints. Durable working
rules are in `AGENTS.md`; public/product documentation remains in `README.md`.

## Mainline State

Canonical repository:

`/home/admin-195/code/power-iot-system`

Live canonical Git HEAD is authoritative in Git and is intentionally not
maintained as a hardcoded SHA in this document. Obtain it from the canonical
repository with `git rev-parse main`.

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

For runtime commands and safety semantics, see:

`README.md` → **Power-IoT Local Runtime Operator**

Future agents must prefer the Operator over inventing manual lifecycle commands
for normal local Power-IoT operation.

## Current Accepted Product State

Latest accepted product feature checkpoint:

PR #34 = **READ_ONLY_FLUTTER_CACHE_01**

MERGE_COMMIT = `8df2a67dd41b52c3beb0d84673a1004a930b2968`

Feature parent = `cd503b66f8f52a608f801f453d1da076c5b4a383`

Dashboard-only durable last-successful snapshot cache is implemented and tested
with authenticated User + authorized Shop scoping and transient stale-read
fallback. It does not provide offline login, offline authorization, mutation
queues, or broader product caching. Device persistence runtime remains pending
an operator-owned Android emulator.

Earlier accepted product checkpoint retained for history:

PR #31 = **ADMIN-ASSIGNMENT-HISTORY-01**

MERGE_COMMIT = `a48ed2112166c5b2d26167d9cdcafbdde847cd8c`

Feature parent = `527b9014ff642fafdf92e28a2b3598fe3f642a43`

Earlier accepted product checkpoint retained for history:

PR #29 = **HISTORICAL-MP-ENERGY-REPORT-01**

MERGE_COMMIT = `d9fac309d92347514bdc4e5c6f07d24f2104fdeb`

Feature parent = `271f413292fb1ea98680624bc325593bf9913dba`

PR #29 historical MP report remains an accepted earlier capability.

Earlier accepted product checkpoint retained for history:

PR #27 = **ADMIN-BINDING-HTTP-01**

MERGE_COMMIT = `4c342b3197ace7dc7fcd13d568b984078d6dff33`

Feature parent = `0fc4f2caf94fa7a9f1b1c5eea4fae785640f9a12`

PR #27 Admin Binding remains an accepted earlier capability; PR #31 is the
newer accepted product feature checkpoint.

PR #25 Local Runtime Operator remains the accepted runtime/operator checkpoint:

`43fc892478b44f898e8e3386650976aa69bfae06`

Current accepted development capability summary:

- Auth/session/JWT development integration.
- B-02 coverage foundation and local devseed coverage configuration.
- Measurement Point Detail read path.
- Dashboard daily/monthly energy and Carbon summary.
- Shop tariff classification.
- Billing V1 configuration, historical energy/coverage, and estimate support.
- Authenticated Shop-scoped monthly Measurement Point historical energy report.
- Shop aggregate and per-MeasurementPoint usage/coverage facts.
- Asia/Taipei monthly semantics with accepted cutoff and snapshot boundaries.
- Historical DeviceAssignment attribution, including Device replacement continuity
  under the same MeasurementPoint and relocation attribution by assignment
  interval.
- Report status semantics preserve null versus valid zero and partial versus
  complete data distinctions.
- Real Flutter historical report screen with month navigation.
- Local PostgreSQL and Android development runtime acceptance for the
  historical report.
- Local devseed scoped-admin fixture support.
- Authenticated Shop-scoped Admin Device Binding HTTP API: Create Measurement
  Point, Bind, Replace, Relocate, and Unbind.
- Real Flutter Admin Binding integration with authoritative refresh after
  mutation, retry-safe request identity, and double-submit serialization.
- Local PostgreSQL and Android development runtime verification for Admin
  Binding.
- Authenticated scoped Admin read-only Assignment History view implemented and
  Flutter-tested; isolated Backend runtime verified, while device-level runtime
  verification remains pending an operator-owned Android emulator.
- Device ↔ MeasurementPoint interval timeline from the same authorized
  AdminOverview snapshot with human-readable entity resolution.
- Newest-first Active/Ended, Measurement Point, and Device filters with AND
  semantics and safe selected-Shop transition handling.
- Real Flutter Assignment History route and Admin Overview entry.
- Dashboard-only durable read-only cache V1 using a separate SharedPreferences
  boundary, with strict envelope validation, stale presentation, and
  auth/Shop isolation tests.
- Local Runtime Operator accepted and ready for use.

Current boundaries remain:

- Production deployment and hardening remain pending.
- Physical ESP8266/fleet validation remains pending.
- Monthly Measurement Point historical energy reporting is implemented and
  development/runtime verified.
- Broader historical reporting remains incomplete, including arbitrary date
  ranges, exports, additional report types, and production reporting hardening.
- General Admin inventory/history remains incomplete beyond the bounded
  Assignment History view.
- Assignment History device-level runtime acceptance remains pending because
  the only connected Android emulator is not operator-owned.
- Admin operation audit history, including actor/reason/action events, remains
  not implemented.
- Alerts remain incomplete.
- BLE/QR provisioning remains incomplete.
- Dashboard read-only cache V1 is implemented and tested; broader
  offline/product caching remains incomplete.

Dashboard refresh contract:

- **NORMAL PRODUCT DEFAULT = 300 seconds**
- **DEVELOPMENT / E2E OVERRIDE = 10 seconds**
- **10 seconds is test acceleration only; it is not the product default.**

Actual production/device MQTT telemetry interval:

**UNCONFIRMED**

Do not claim that device telemetry is 300 seconds unless separately established.

Backend current-power freshness remains:

**120 seconds**

Do not alter or reinterpret that contract here.

## Historical PR #8 / POST-I3-F1 Snapshot

I3 and POST-I3-F1 were completed and merged. PR #7 merged as
`459f0f3b121aefe7793d943e9609ac4570d144f2`; PR #8 merged as
`05d9ccd7caaa8015943eea8dd9564e3942bef83f`. The former post-PR #8 snapshot
listed no next active feature; it is historical and is not the current
mainline state.

## Historical POST-I3-F1 Behavior

- Dashboard automatic refresh is implemented.
- Polling runs only while the app is resumed and the Dashboard route is visible.
- Polling stops when the app is backgrounded or the Dashboard route is covered.
- Dashboard requests do not overlap or backlog.
- Transient background refresh failures preserve the last successful UI data.
- Authentication, shop selection, and `null != zero` semantics are preserved.
- No production deployment or production readiness claim is made.

## Production Boundary

- `PRODUCTION_EXECUTION_AUTHORIZED = NO`
- `PRODUCTION_TCRFID01_MUTATED = NO`
- `PRODUCTION_DB_MUTATED = NO`
- `PRODUCTION_CUTOVER = NOT_STARTED`

No production deployment is included in this completed feature.

## Current Agent / Development Policy

Power-IoT feature-development default:

**PROFILE C — Single-Subagent Sequential**

Development style:

**VERTICAL SLICE**

Each normal product feature should be implemented end-to-end through the
relevant layers, typically:

domain / contract
→ backend
→ API
→ Flutter frontend
→ integration
→ real E2E

Engineering method:

**TDD**

Design preference:

**DEEP MODULES**

Frontend and backend should normally be implemented as one bounded feature
slice rather than large frontend-only or backend-only batches.

Profile A and D are **NOT** normal defaults.

Use another profile only when:

- the user explicitly requests it; or
- a materially exceptional task requires it and that exception is made explicit.

Model/effort configuration should inherit project/global defaults and should not
be redundantly repeated in prompts unless the user requests a specific override
or the task materially requires escalation.

## Historical A3/D2/D3 Checkpoints

The following historical delivery information is retained for traceability.

- A3-P1 final enforcement plan: **FROZEN**.
- A3-D1 writer/startup gates: **ACCEPTED**.
- A3-D2 lifecycle/readiness seams: **ACCEPTED**.
- A3-D3 dedicated runner/recovery: **ACCEPTED** after independent ground-truth
  audit repairs.
- A3-D4 protected continuous orchestration: **COMPLETE / ACCEPTED**.
- A3-D5 migration `000006` and integration: **COMPLETE / ACCEPTED**.
- A3-D6 specification and plan: **APPROVED / FROZEN**.
- A3-D6 implementation and production-shaped rehearsal: **COMPLETE / PASS**.
- A3-D6 pre-production validation: **PASS**.

Historical accepted checkpoints:

- D2: `64bce24b72f2976e9333e9d853c0c4c78efd139b`
- D3 re-acceptance: `2d1bcf32c04af94e57817e5cf84e47a295feff08`

D2 preserved the external admission boundary, D3 metadata transition/lock/
recovery authority, and exclusion of DML/backfill and ownership/Authz redesign.
Those historical phases are complete and are not unresolved blockers for
POST-I3-F1.

## Historical GitHub Integration

- PR #6 security reconciliation: historical phase, no longer the active phase.
- PR #7 frontend/backend integration: **MERGED** as the I3 merge commit above.
- PR #8 dashboard auto refresh: **MERGED** to `main` with merge commit
  `05d9ccd7caaa8015943eea8dd9564e3942bef83f`.

## Documentation and Scope Boundary

The dashboard polling contract is documented in `README.md`. Local telemetry
runtime has been proven end-to-end; the repository-wide operational procedure
is documented in the `README.md` Local Development Runbook, while current
workstation state is kept outside Git at
`/home/admin-195/.local/state/power-iot/runbooks/local-runtime-state.md`. This
context file records internal mainline progress and agent execution policy; it
does not expand the accepted feature or authorize production activity.

Never read or stage:

`infrastructure/firmware/certs/ota.key`
