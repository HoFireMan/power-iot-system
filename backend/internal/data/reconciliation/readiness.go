package reconciliation

import (
	"errors"
	"fmt"
	"strings"

	"power-iot-backend/internal/data/migrations"
)

// LifecycleState is the D2-only lifecycle boundary around protected
// reconciliation and readiness. It is intentionally not a migration authority:
// it cannot write migration metadata, run DDL, or advance v5 to v6.
type LifecycleState string

const (
	LifecycleBlocked                 LifecycleState = "BLOCKED"
	LifecycleCleanV5                 LifecycleState = "CLEAN_V5"
	LifecycleProtectedReconciliation LifecycleState = "PROTECTED_RECONCILIATION"
	LifecycleReadinessCheck          LifecycleState = "READINESS_CHECK"
	LifecycleReadinessFailed         LifecycleState = "READINESS_FAILED"
	LifecycleCutoverReadyV5          LifecycleState = "CUTOVER_READY_V5"
	LifecycleCleanV6                 LifecycleState = "CLEAN_V6"
)

// WriterAdmission describes the only writer lane exposed by this lifecycle
// seam. Ordinary v5 writers are deliberately not admitted here; D1 owns the
// separate pre-v6 runtime contract and D4 owns the protected composition.
type WriterAdmission string

const (
	WriterAdmissionDenied      WriterAdmission = "DENIED"
	WriterAdmissionProtectedA2 WriterAdmission = "PROTECTED_A2"
)

var (
	ErrLifecycleBlocked      = errors.New("lifecycle is fail-closed")
	ErrLifecycleTransition   = errors.New("invalid lifecycle transition")
	ErrLifecycleWriterDenied = errors.New("writer admission is denied by lifecycle state")
	ErrReadinessNotMet       = errors.New("readiness predicates did not pass")
	ErrReadinessEvidence     = errors.New("reconciliation evidence is not committed and post-verified")
)

// Lifecycle is a small, in-memory state machine. A fresh value must be created
// from a fresh D3 protected-state observation for every protected attempt. No
// reset operation exists, so a failed readiness check cannot be reused for a
// later writer or cutover attempt.
type Lifecycle struct {
	state LifecycleState
}

// NewLifecycleFromProtectedState maps the D3 observation into the D2 lifecycle
// seam. Only exact clean v5 and clean v6 observations are admitted as useful
// states. Dirty, ambiguous, missing, duplicate, unreadable, bootstrap, and
// future observations all become BLOCKED and cannot start writers.
func NewLifecycleFromProtectedState(state migrations.ProtectedMigrationState) Lifecycle {
	switch state {
	case migrations.ProtectedStateCleanV5:
		return Lifecycle{state: LifecycleCleanV5}
	case migrations.ProtectedStateCleanV6:
		return Lifecycle{state: LifecycleCleanV6}
	default:
		return Lifecycle{state: LifecycleBlocked}
	}
}

func (l Lifecycle) State() LifecycleState {
	return l.state
}

// WriterAdmission returns a descriptive lane only. Call RequireWriterAdmission
// to enforce the lane and, for A2, prove live protected-fence ownership.
func (l Lifecycle) WriterAdmission() WriterAdmission {
	switch l.state {
	case LifecycleProtectedReconciliation:
		return WriterAdmissionProtectedA2
	default:
		return WriterAdmissionDenied
	}
}

// AdmitProtectedReconciliation enters the only D2 state that may host A2
// writes. Both the D1 external-writer evidence and the D3 protected-fence
// capability are required; a clean metadata/catalog observation alone never
// starts a writer.
func (l *Lifecycle) AdmitProtectedReconciliation(capability migrations.ProtectedWorkCapability, admission migrations.ExternalWriterAdmission) error {
	if l == nil || l.state != LifecycleCleanV5 {
		return lifecycleTransitionError(l, LifecycleProtectedReconciliation)
	}
	if err := migrations.RequireExternalWriterAdmission(admission); err != nil {
		return fmt.Errorf("%w: %v", ErrLifecycleWriterDenied, err)
	}
	if err := migrations.RequireProtectedWork(capability); err != nil {
		return fmt.Errorf("%w: %v", ErrLifecycleWriterDenied, err)
	}
	l.state = LifecycleProtectedReconciliation
	return nil
}

// BeginReadiness closes the A2 writer lane before the fresh readiness check.
// This is a state transition only; it does not release or acquire a fence and
// does not invoke a later orchestration phase.
func (l *Lifecycle) BeginReadiness() error {
	if l == nil || l.state != LifecycleProtectedReconciliation {
		return lifecycleTransitionError(l, LifecycleReadinessCheck)
	}
	l.state = LifecycleReadinessCheck
	return nil
}

// ApplyReadiness records the result of the fresh D2 predicate evaluation. A
// failed result permanently denies writers for this lifecycle instance. A
// successful v5 result only makes the protected cutover eligible; it does not
// imply D4, migration ownership, or permission to execute DDL.
func (l *Lifecycle) ApplyReadiness(decision ReadinessDecision) error {
	if l == nil || l.state != LifecycleReadinessCheck {
		return lifecycleTransitionError(l, LifecycleReadinessCheck)
	}
	if decision.proof == nil || decision.proof.Target != decision.Target || decision.proof.Ready != decision.Ready {
		l.state = LifecycleReadinessFailed
		return fmt.Errorf("%w: readiness decision was not produced by EvaluateReadiness", ErrReadinessNotMet)
	}
	if !decision.Ready {
		l.state = LifecycleReadinessFailed
		return readinessError(decision)
	}
	if decision.Target != ReadinessForCutover {
		l.state = LifecycleReadinessFailed
		return fmt.Errorf("%w: target=%s is not the protected v5 cutover target", ErrReadinessNotMet, decision.Target)
	}
	l.state = LifecycleCutoverReadyV5
	return nil
}

// RequireWriterAdmission enforces the state-specific writer lane. D2 never
// admits writes in blocked, clean-v5, readiness, failed, cutover-ready, or
// clean-v6 states. D1/D6 must perform the separate v6-compatible runtime
// admission; this seam cannot be used to bypass that deployment boundary.
func (l Lifecycle) RequireWriterAdmission(admission WriterAdmission, capability migrations.ProtectedWorkCapability) error {
	if admission == WriterAdmissionProtectedA2 && l.state == LifecycleProtectedReconciliation {
		if err := migrations.RequireProtectedWork(capability); err != nil {
			return fmt.Errorf("%w: %v", ErrLifecycleWriterDenied, err)
		}
		return nil
	}
	return fmt.Errorf("%w: state=%s admission=%s", ErrLifecycleWriterDenied, l.state, admission)
}

func lifecycleTransitionError(l *Lifecycle, target LifecycleState) error {
	state := LifecycleBlocked
	if l != nil {
		state = l.state
	}
	if state == LifecycleBlocked || state == LifecycleReadinessFailed {
		return fmt.Errorf("%w: state=%s target=%s", ErrLifecycleBlocked, state, target)
	}
	return fmt.Errorf("%w: state=%s target=%s", ErrLifecycleTransition, state, target)
}

// ReadinessTarget identifies a D2 decision's purpose. Neither target starts a
// writer or implies the D4 continuous orchestration phase.
type ReadinessTarget string

const (
	ReadinessForCutover ReadinessTarget = "V5_CUTOVER"
	ReadinessForServing ReadinessTarget = "V6_SERVING"
)

// ReconciliationEvidence is the narrow handoff from the protected A2 writer.
// Fresh facts and deterministic planning remain authoritative; these fields
// only ensure that D2 cannot certify a readiness check before A2's commit and
// durable post-commit verification have completed.
type ReconciliationEvidence struct {
	Outcome            ExecutionOutcome
	Committed          bool
	PostCommitVerified bool
	PlanID             string
	PlanDigest         string
	SourceFactsDigest  string
	MappingDigest      string
}

// ReconciliationEvidenceFromReport preserves the A2 evidence identifiers
// while keeping the readiness check anchored to a fresh fact snapshot.
func ReconciliationEvidenceFromReport(report ExecutionReport) ReconciliationEvidence {
	return ReconciliationEvidence{
		Outcome:            report.Outcome,
		Committed:          report.Committed,
		PostCommitVerified: report.PostCommitVerified,
		PlanID:             report.PlanID.String(),
		PlanDigest:         report.PlanDigest,
		SourceFactsDigest:  report.SourceFactsDigest,
		MappingDigest:      report.MappingDigest,
	}
}

func (e ReconciliationEvidence) valid() bool {
	return e.Outcome == ExecutionCommittedAndVerified && e.Committed && e.PostCommitVerified &&
		e.PlanID != "" && e.PlanDigest != "" && e.SourceFactsDigest != ""
}

// ReadinessRequest contains only observations. EvaluateReadiness performs no
// I/O and has no write capability. For v5 cutover, Facts must be the fresh
// post-A2 snapshot collected under the already-owned protected session. For v6
// serving, D3 supplies the exact clean-v6 state and semantic proof marker.
type ReadinessRequest struct {
	Target                      ReadinessTarget
	ProtectedState              migrations.ProtectedMigrationState
	Facts                       FactSet
	Reconciliation              ReconciliationEvidence
	PostCutoverSemanticVerified bool
}

type ReadinessCheck struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Reason string `json:"reason,omitempty"`
}

// ReadinessDecision is evidence, not a capability. In particular, Ready=true
// never grants a fence, migration lock, DDL permission, or D4 orchestration.
// proof is deliberately unexported so a caller cannot manufacture a successful
// decision for Lifecycle.ApplyReadiness by filling exported fields.
type ReadinessDecision struct {
	Target ReadinessTarget  `json:"target"`
	Ready  bool             `json:"ready"`
	Checks []ReadinessCheck `json:"checks"`
	proof  *readinessProof
}

type readinessProof struct {
	Target ReadinessTarget
	Ready  bool
}

func (d ReadinessDecision) FailedPredicates() []string {
	failed := make([]string, 0)
	for _, check := range d.Checks {
		if !check.Passed {
			failed = append(failed, check.Name)
		}
	}
	return failed
}

// RequireReadiness is the fail-closed convenience seam for future protected
// orchestration. It still returns only D2 evidence; it does not acquire locks,
// start writers, or invoke a migration authority.
func RequireReadiness(request ReadinessRequest) (ReadinessDecision, error) {
	decision := EvaluateReadiness(request)
	if !decision.Ready {
		return decision, readinessError(decision)
	}
	return decision, nil
}

func readinessError(decision ReadinessDecision) error {
	failedPredicates := decision.FailedPredicates()
	failed := strings.Join(failedPredicates, ",")
	if failed == "" {
		failed = "unspecified"
	}
	cause := error(ErrReadinessNotMet)
	for _, predicate := range failedPredicates {
		if predicate == "reconciliation-committed-and-post-verified" {
			cause = errors.Join(cause, ErrReadinessEvidence)
			break
		}
	}
	return fmt.Errorf("%w: target=%s failed=%s", cause, decision.Target, failed)
}

// EvaluateReadiness evaluates the exact D2 semantic boundary. A non-clean D3
// state is always false, including dirty/ambiguous/missing/duplicate/unreadable
// and unsupported-future observations represented by D3 as non-clean states.
func EvaluateReadiness(request ReadinessRequest) ReadinessDecision {
	decision := ReadinessDecision{Target: request.Target, Checks: make([]ReadinessCheck, 0, 8)}
	add := func(name string, passed bool, reason string) {
		decision.Checks = append(decision.Checks, ReadinessCheck{Name: name, Passed: passed, Reason: reason})
		if !passed {
			decision.Ready = false
		}
	}

	switch request.Target {
	case ReadinessForCutover:
		add("protected-state-clean-v5", request.ProtectedState == migrations.ProtectedStateCleanV5,
			protectedStateReason(request.ProtectedState, migrations.ProtectedStateCleanV5))
		add("reconciliation-committed-and-post-verified", request.Reconciliation.valid(),
			protectedEvidenceReason(request.Reconciliation))
		add("fresh-facts-v5", request.Facts.SchemaVersion == SchemaVersion && !request.Facts.AsOf.IsZero(),
			factsVersionReason(request.Facts))
		if request.Facts.SchemaVersion != SchemaVersion || request.Facts.AsOf.IsZero() {
			add("semantic-data-predicates", false, "facts are not a complete v5 snapshot")
			add("deterministic-post-a2-plan", false, "facts cannot produce a deterministic v5 plan")
			break
		}

		classes, classErr := ClassifyFacts(request.Facts)
		semanticOK := classErr == nil
		semanticReason := "all v5 fact classifications are terminal"
		if classErr != nil {
			semanticReason = classErr.Error()
		} else {
			for _, class := range classes {
				if class.Classification != AlreadyConsistent {
					semanticOK = false
					semanticReason = fmt.Sprintf("%s:%s:%s", class.Kind, class.StableID, class.Classification)
					break
				}
			}
		}
		add("semantic-data-predicates", semanticOK, semanticReason)

		plan, planErr := BuildPlan(request.Facts, nil)
		planOK := planErr == nil && plan.SchemaVersion == SchemaVersion && plan.AsOf.Equal(request.Facts.AsOf.UTC()) && len(plan.Blockers) == 0 && len(plan.RequiredExplicitMappings) == 0
		planReason := "fresh facts produce a blocker-free plan without required mappings"
		if planErr != nil {
			planReason = planErr.Error()
		} else if !planOK {
			planReason = fmt.Sprintf("plan blockers=%d required_mappings=%d", len(plan.Blockers), len(plan.RequiredExplicitMappings))
		}
		add("deterministic-post-a2-plan", planOK, planReason)
	case ReadinessForServing:
		add("protected-state-clean-v6", request.ProtectedState == migrations.ProtectedStateCleanV6,
			protectedStateReason(request.ProtectedState, migrations.ProtectedStateCleanV6))
		add("post-cutover-semantic-proof", request.PostCutoverSemanticVerified,
			"D3/D5 final semantic verifier did not provide proof")
	default:
		add("known-readiness-target", false, fmt.Sprintf("unsupported target %q", request.Target))
	}

	decision.Ready = len(decision.FailedPredicates()) == 0
	decision.proof = &readinessProof{Target: decision.Target, Ready: decision.Ready}
	return decision
}

func protectedStateReason(got, want migrations.ProtectedMigrationState) string {
	if got == want {
		return "exact protected state observed"
	}
	return fmt.Sprintf("got protected state %s, want %s", got, want)
}

func protectedEvidenceReason(evidence ReconciliationEvidence) string {
	if evidence.valid() {
		return "A2 commit and durable post-commit verification are present"
	}
	return fmt.Sprintf("outcome=%s committed=%t post_commit_verified=%t", evidence.Outcome, evidence.Committed, evidence.PostCommitVerified)
}

func factsVersionReason(facts FactSet) string {
	if facts.SchemaVersion == SchemaVersion && !facts.AsOf.IsZero() {
		return "fresh v5 facts snapshot has schema version and timestamp"
	}
	return fmt.Sprintf("schema_version=%q as_of_zero=%t", facts.SchemaVersion, facts.AsOf.IsZero())
}
