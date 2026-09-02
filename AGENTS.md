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

## Orchestration, Skills, and Model References

Use applicable installed/global Skills when they materially improve correctness,
decomposition, review independence, or efficiency, including Skills installed
from `mattpocock/skills`. Installed definitions are authoritative; historical
names do not establish that a Skill exists. Keep detailed Skill policies in the
Skills, not here.

Subagents are optional: simple/trivial/mechanical/low-risk tasks may be done
directly; medium tasks use Skills/subagents only when decomposition helps; large,
cross-domain, or high-risk tasks normally use orchestration/subagents.
Substantial/high-risk schema, security, concurrency, attribution, identity,
data-integrity, or cross-domain contract work normally receives independent
review; small deterministic changes may use parent self-review.

Roles: `scout` = fast reconnaissance; `researcher` = deeper contract/architecture/
governance investigation; `worker` = bounded implementation; `delegate` = bounded
parallel work; `reviewer` = independent review; `oracle` = exceptional escalation
for unresolved high-risk architecture, root-cause, authority-conflict, or
correctness decisions. Do not require every role or invoke Oracle routinely.

The parent decides whether decomposition is worthwhile, selects an applicable
profile, evaluates findings, resolves authority conflicts, makes final
acceptance/rejection decisions, and writes the final user-facing report.

Supported Orchestration Profiles are A, C, and D; explicit user profile selection
is authoritative. Session topology, responsibility decomposition, and model
family/effort/escalation remain owned by the global Orchestration Profiles,
Agent Orchestration, and Model Escalation Skills. Profile B is not supported;
model/effort configuration inherits project/global defaults unless overridden.

## Repository Safety

Keep changes within the task's declared scope. Before and after editing, check
the branch, accepted checkpoint, and worktree status. Preserve unrelated user
changes; do not reset, stash, or discard them. For PostgreSQL integration work,
follow the applicable global Skill and task-specific safety gates. Report the
actual validation evidence and any checks not run.
