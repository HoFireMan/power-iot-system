# CONTEXT.md

Current Power-IoT mainline state and accepted checkpoints. Durable working
rules are in `AGENTS.md`; public/product documentation remains in `README.md`.

## Mainline State

Canonical repository:

`/home/admin-195/code/power-iot-system`

Current accepted main HEAD:

`05d9ccd7caaa8015943eea8dd9564e3942bef83f`

I3:

**COMPLETE / ACCEPTED / MERGED TO MAIN**

PR #7:

**MERGED**

PR #7 merge commit:

`459f0f3b121aefe7793d943e9609ac4570d144f2`

Last completed feature:

**POST-I3-F1 — Dashboard Auto Refresh**

Last feature worktree:

`/home/admin-195/code/power-iot-system-wt-dashboard-auto-refresh`

Last feature branch:

`work/post-i3-dashboard-auto-refresh`

POST-I3-F1 status:

**COMPLETE / ACCEPTED / MERGED TO MAIN**

PR #8:

**MERGED**

PR #8 merge commit:

`05d9ccd7caaa8015943eea8dd9564e3942bef83f`

GIT INTEGRATION:

**COMPLETE**

Next active feature:

**NONE / NOT YET SELECTED**

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

## Accepted POST-I3-F1 Behavior

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

## GitHub Integration

- PR #6 security reconciliation: historical phase, no longer the active phase.
- PR #7 frontend/backend integration: **MERGED** as the I3 merge commit above.
- PR #8 dashboard auto refresh: **MERGED** to `main` with merge commit
  `05d9ccd7caaa8015943eea8dd9564e3942bef83f`.

## Documentation and Scope Boundary

The dashboard polling contract is documented in `README.md`. This context file
records internal mainline progress and agent execution policy; it does not
expand the accepted feature or authorize production activity.

Never read or stage:

`infrastructure/firmware/certs/ota.key`
