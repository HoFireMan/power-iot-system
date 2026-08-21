# CONTEXT.md

Current Power-IoT mainline state and accepted checkpoints. Durable working
rules are in `AGENTS.md`; public/product documentation remains in `README.md`.

## Mainline State

- Repository worktree: `/home/admin-195/code/power-iot-system-wt-security-reconciliation`
- Current integration branch: `work/security-schema-reconciliation`
- Last implementation checkpoint: `b454fd8de195687d81f9f95e3924fbedae4acd1f`
- Current integration PR: `#6`

A3 status:

- **A3-P1 — Final Enforcement Plan:** FROZEN
- **A3-D1 — Writer/Startup Gates:** ACCEPTED
- **A3-D2 — Lifecycle/Readiness Seams:** ACCEPTED
- **A3-D3 — Dedicated Runner/Recovery:** ACCEPTED
- **A3-D4 — Protected Continuous Orchestration:** COMPLETE / ACCEPTED
- **A3-D5 — 000006 + Integration:** COMPLETE / ACCEPTED
- **A3-D6 SPEC:** APPROVED / FROZEN
- **A3-D6 PLAN:** APPROVED / FROZEN
- **A3-D6 IMPLEMENTATION:** COMPLETE
- **A3-D6 PRODUCTION-SHAPED REHEARSAL:** PASS
- **A3-D6 PRE-PRODUCTION VALIDATION:** PASS

Migration `000006_d4_reconciliation`: **IMPLEMENTED / VERIFIED / ACCEPTED**.

Production boundary:

- `A3_D6_PRODUCTION_EXECUTION_AUTHORIZED = NO`
- `PRODUCTION_TCRFID01_MUTATED = NO`
- `PRODUCTION_DB_MUTATED = NO`
- `PRODUCTION_000006_EXECUTED = NO`
- `PRODUCTION_CUTOVER = NOT_STARTED`

GitHub integration:

- **PR #6:** OPEN
- **PR #6 MERGED:** NO

Dependency: **D1 + D2 + D3 → D4 → D5 → D6** is complete through D6
pre-production validation; production execution remains separately authorized.

## D2 Delivery Summary (Historical Checkpoint)

Accepted D2 checkpoint: `64bce24b72f2976e9333e9d853c0c4c78efd139b`

D2 changed:

- `backend/internal/data/reconciliation/readiness.go`
- `backend/internal/data/reconciliation/readiness_test.go`

D2 provides:

- explicit fail-closed lifecycle state handling;
- protected A2 readiness/admission boundary;
- v5 cutover readiness predicates;
- v6 serving readiness predicates;
- fresh-fact, semantic, and post-commit evidence checks; and
- denial for dirty, ambiguous, bootstrap, future, missing, and unsupported states.

At the historical D2 checkpoint, the following boundaries were preserved:

- D1 external admission/protected capability;
- D3 metadata transition, lock, recovery, and UNKNOWN-COMMIT authority;
- DML/backfill remained excluded; and
- ownership/Authz redesign remained excluded.

At that checkpoint, later-phase work was D4 wiring/integration of these
readiness seams into protected continuous orchestration, followed by D5 and
D6 delivery under the frozen phase plan. Those later phases are now reflected
in the current Mainline State above and are not unresolved D2 blockers.

## D3 Status

D3-V1 was later reopened by independent ground-truth audit. F003, F005, F007,
and F013 were repaired, and D3 was re-accepted at
`2d1bcf32c04af94e57817e5cf84e47a295feff08`.

## Current Agent Tooling

Global Skills:

- `model-escalation`
- `agent-orchestration`
- `orchestration-profiles`
- `task-report`

Supported Profiles:

- **A — Fresh Bounded Multi-Agent**
- **C — Single-Subagent Sequential**
- **D — Persistent Role Lifecycle**

Profile B is unsupported. Profile selection is user-owned.

Recent mainline execution:

- A3-D3-R2 used Profile C.
- A3-D2 used Profile C.

These historical selections are not a permanent Power-IoT default. Power-IoT
currently has no permanent A/C/D default unless the user explicitly sets one
later.

## Documentation Scope

The current changes concern internal mainline progress and agent execution
policy, not public product usage, installation, or product architecture.
Therefore `README_UPDATE_REQUIRED = NO` for this synchronization.
