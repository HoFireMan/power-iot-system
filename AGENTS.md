# AGENTS.md

This file defines durable agent-working rules for the Power-IoT repository.
Current project state belongs in `CONTEXT.md`; public/product documentation
belongs in `README.md`.

## Mainline Task Prompt Structure

For substantial Power-IoT implementation, repair, verification, or review
tasks, prompts should use two layers.

### Immutable / Stable Contract

The first section contains stable task-execution rules such as:

- authority and frozen-source precedence;
- selected Orchestration Profile;
- Model Escalation authority;
- repository/worktree safety;
- PostgreSQL integration-test safety;
- mutation and phase boundaries;
- Task Report requirements; and
- final evidence/output requirements.

Detailed behavior remains owned by the relevant global Skills. Do not duplicate
their full policies in this file.

### Dynamic Task Suffix

The second section contains task-specific state such as:

- current accepted branch/checkpoint;
- task objective;
- frozen-artifact-derived requirements;
- current requirement ledger;
- implementation phases;
- task-specific adversarial checks;
- tests required by the actual changed surface; and
- acceptance/checkpoint conditions.

The Dynamic Task Suffix must never override the Immutable / Stable Contract.

### Task Report Preservation

For substantial mainline tasks, Task Report output is part of the acceptance
evidence, not optional diagnostic decoration.

Preserve, when emitted:

- TIMING;
- AGENT METRICS;
- TOKEN METRICS;
- TASK TOTAL; and
- CACHE STATUS.

Do not manually reconstruct Task Report metrics. Detailed metric semantics
remain owned by the global Task Report Skill.

## Orchestration and Model References

Supported Orchestration Profiles are A, C, and D. Explicit user profile
selection is authoritative. Session topology is owned by the global
Orchestration Profiles Skill; logical responsibility decomposition is owned by
Agent Orchestration; and model family, effort, and escalation are owned by
Model Escalation. Profile B is not supported. Trivial tasks do not require
unnecessary profile selection.

## Repository Safety

Keep changes within the task's declared scope. Before and after editing, check
the branch, accepted checkpoint, and worktree status. Preserve unrelated user
changes; do not reset, stash, or discard them. For PostgreSQL integration work,
follow the applicable global Skill and task-specific safety gates. Report the
actual validation evidence and any checks not run.
