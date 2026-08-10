package migrations

import (
	"errors"
	"testing"
)

func TestSecuritySchemaWriterFenceDecisionFailsClosed(t *testing.T) {
	decision := AssessSecuritySchemaWriterFence()

	if decision.Status != WriterFenceDecisionRequired {
		t.Fatalf("status=%q, want %q", decision.Status, WriterFenceDecisionRequired)
	}
	if decision.ReasonCode != WriterFenceNoApprovedEnforceableWriterAdmission {
		t.Fatalf("reason_code=%q, want %q", decision.ReasonCode, WriterFenceNoApprovedEnforceableWriterAdmission)
	}
	if decision.FailedGate != WriterFenceGateNewWriterExclusion {
		t.Fatalf("failed_gate=%q, want %q", decision.FailedGate, WriterFenceGateNewWriterExclusion)
	}
	if decision.StageAAdditiveFoundationSafe || decision.ProtectedWorkAllowed {
		t.Fatalf("decision must fail closed: %+v", decision)
	}
	if !decision.RequiresExplicitOperatorDecision {
		t.Fatal("decision must require an explicit operator/architecture decision")
	}
}

func TestSecuritySchemaWriterFenceProtectedWorkIsRefused(t *testing.T) {
	decision := AssessSecuritySchemaWriterFence()
	if err := decision.RequireProtectedWork(); !errors.Is(err, ErrWriterFenceDecisionRequired) {
		t.Fatalf("RequireProtectedWork()=%v, want ErrWriterFenceDecisionRequired", err)
	}
}

func TestSecuritySchemaWriterFenceCannotBeFabricatedAsApproved(t *testing.T) {
	forged := WriterFenceDecision{ProtectedWorkAllowed: true}
	if err := forged.RequireProtectedWork(); !errors.Is(err, ErrWriterFenceDecisionRequired) {
		t.Fatalf("forged RequireProtectedWork()=%v, want ErrWriterFenceDecisionRequired", err)
	}
}

func TestSecuritySchemaWriterFenceDecisionListsRequiredChoices(t *testing.T) {
	decision := AssessSecuritySchemaWriterFence()
	want := []WriterFenceMechanism{
		WriterFenceManagedDatabaseAdmission,
		WriterFenceDeploymentOrchestration,
		WriterFenceApplicationCooperation,
	}
	if len(decision.RequiredMechanismDecisions) != len(want) {
		t.Fatalf("required mechanism count=%d, want %d", len(decision.RequiredMechanismDecisions), len(want))
	}
	for i := range want {
		if decision.RequiredMechanismDecisions[i] != want[i] {
			t.Fatalf("required mechanism[%d]=%q, want %q", i, decision.RequiredMechanismDecisions[i], want[i])
		}
	}
}
