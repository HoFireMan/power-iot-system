package migrations

import (
	"errors"
	"fmt"
)

// WriterFenceDecisionStatus is deliberately limited to the decision state that
// this repository can prove today. No protected migration/reconciliation path
// is exposed until an approved enforcement mechanism exists.
type WriterFenceDecisionStatus string

const WriterFenceDecisionRequired WriterFenceDecisionStatus = "WRITER_FENCE_DECISION_REQUIRED"

// WriterFenceReasonCode identifies why the current runtime cannot safely admit
// protected work. It is not a transient observation of current activity.
type WriterFenceReasonCode string

const WriterFenceNoApprovedEnforceableWriterAdmission WriterFenceReasonCode = "NO_APPROVED_ENFORCEABLE_WRITER_ADMISSION"

// WriterFenceGate identifies the first required property that current runtime
// evidence cannot prove.
type WriterFenceGate string

const WriterFenceGateNewWriterExclusion WriterFenceGate = "TEST_D_NEW_WRITER_EXCLUSION"

// WriterFenceMechanism names architecture choices that require an explicit
// operator decision before implementation. They are not implemented locks.
type WriterFenceMechanism string

const (
	WriterFenceManagedDatabaseAdmission WriterFenceMechanism = "MANAGED_DATABASE_ADMISSION"
	WriterFenceDeploymentOrchestration  WriterFenceMechanism = "DEPLOYMENT_ORCHESTRATION"
	WriterFenceApplicationCooperation   WriterFenceMechanism = "APPLICATION_COOPERATION"
)

var ErrWriterFenceDecisionRequired = errors.New("security schema writer fence decision is required")

// WriterFenceDecision is a fail-closed boundary result, not a lock handle or
// an assertion that current writers are absent. Protected work must not start
// while Status is WriterFenceDecisionRequired.
type WriterFenceDecision struct {
	Status                           WriterFenceDecisionStatus `json:"status"`
	ReasonCode                       WriterFenceReasonCode     `json:"reason_code"`
	Message                          string                    `json:"message"`
	FailedGate                       WriterFenceGate           `json:"failed_gate"`
	StageAAdditiveFoundationSafe     bool                      `json:"stage_a_additive_foundation_safe"`
	ProtectedWorkAllowed             bool                      `json:"protected_work_allowed"`
	RequiresExplicitOperatorDecision bool                      `json:"requires_explicit_operator_decision"`
	ApplicationCooperationCrossLane  bool                      `json:"application_cooperation_cross_lane"`
	RequiredMechanismDecisions       []WriterFenceMechanism    `json:"required_mechanism_decisions"`
}

// AssessSecuritySchemaWriterFence records the current repository/runtime
// decision. Advisory locks, READ ONLY transactions, pg_stat_activity checks,
// and a no-writer observation are intentionally not treated as enforcement.
func AssessSecuritySchemaWriterFence() WriterFenceDecision {
	return WriterFenceDecision{
		Status:                           WriterFenceDecisionRequired,
		ReasonCode:                       WriterFenceNoApprovedEnforceableWriterAdmission,
		Message:                          "No approved mechanism can close admission to new ordinary database writers, drain existing writers, and preserve fence ownership across the protected sequence on the current single-superuser, unmanaged-Backend topology.",
		FailedGate:                       WriterFenceGateNewWriterExclusion,
		StageAAdditiveFoundationSafe:     false,
		ProtectedWorkAllowed:             false,
		RequiresExplicitOperatorDecision: true,
		ApplicationCooperationCrossLane:  true,
		RequiredMechanismDecisions: []WriterFenceMechanism{
			WriterFenceManagedDatabaseAdmission,
			WriterFenceDeploymentOrchestration,
			WriterFenceApplicationCooperation,
		},
	}
}

// RequireProtectedWork is the only protected-work gate currently available.
// It always refuses until a future, explicitly approved mechanism replaces this
// decision result with an enforceable implementation.
func (d WriterFenceDecision) RequireProtectedWork() error {
	// No approved implementation exists in this lane. Keep this unconditional
	// so a caller cannot fabricate approval by constructing the public report.
	return fmt.Errorf("%w: %s", ErrWriterFenceDecisionRequired, d.Message)
}
