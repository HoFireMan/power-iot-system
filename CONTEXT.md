# CONTEXT.md

Current Power-IoT mainline state and accepted checkpoints. Durable working
rules are in `AGENTS.md`; public/product documentation remains in `README.md`.

## Mainline State

- Repository worktree: `/home/admin-195/code/power-iot-system-wt-security-reconciliation`
- Branch: `work/security-schema-reconciliation`
- Current accepted HEAD: `64bce24b72f2976e9333e9d853c0c4c78efd139b`

A3 status:

- **A3-P1 — Final Enforcement Plan:** FROZEN
- **A3-D1 — Writer/Startup Gates:** ACCEPTED
- **A3-D2 — Lifecycle/Readiness Seams:** ACCEPTED
- **A3-D3 — Dedicated Runner/Recovery:** ACCEPTED after GT reconciliation repair
- **A3-D4 — Protected Continuous Orchestration:** NEXT
- **A3-D5 — 000006 + Integration:** NOT STARTED
- **A3-D6 — Deployment/Bootstrap/Final Cutover:** NOT STARTED

Dependency: **D1 + D2 + D3 → D4 → D5 → D6**

## D2 Delivery Summary

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

Preserved boundaries:

- D1 external admission/protected capability;
- D3 metadata transition, lock, recovery, and UNKNOWN-COMMIT authority;
- no D4 continuous orchestration;
- no D5 / 000006;
- no D6 deployment/cutover;
- no DML/backfill; and
- no ownership/Authz redesign.

Remaining later-phase work is D4 wiring/integration of these readiness seams
into protected continuous orchestration, followed by later final v6
semantic-verifier integration where assigned by the frozen phase plan. This is
not an unresolved D2 blocker.

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
