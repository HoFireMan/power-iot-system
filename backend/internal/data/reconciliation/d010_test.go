package reconciliation

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"power-iot-backend/internal/data/private_migrations"
)

func d010Fixture(t *testing.T, issuer *D010HandoffIssuer) (D010Handoff, D010HandoffContext, D007TerminalEvidence, D009Evidence) {
	t.Helper()
	facts := FactSet{SchemaVersion: SchemaVersion, AsOf: time.Now().UTC()}
	request := cleanV5ReadinessRequest()
	request.Facts = facts
	bindReadinessEvidence(&request)
	decision := EvaluateReadiness(request)
	if !decision.Ready {
		t.Fatalf("readiness: %+v", decision)
	}
	target := [32]byte{7, 8, 9}
	binding := D007Binding{AttemptID: uuid.New(), TargetFingerprint: target, Generation: 12}
	live, err := issuerD007ForD010(decision, facts, binding)
	if err != nil {
		t.Fatal(err)
	}
	_, digestBytes, _ := CanonicalSourceFacts(facts)
	var digest [32]byte
	copy(digest[:], digestBytes)
	expected := D007CapabilityBinding{D007Binding: binding, FactsDigest: digest, ProofDigest: d007ProofDigest(decision, digest), FreshUntil: d007FreshUntil(facts), PredicateVersion: D007PredicateVersion}
	terminal, err := ConsumeLiveD007Capability(live, expected, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	operationID := uuid.New()
	lease := &migrations.D1LLeaseIdentity{OperationID: operationID, AttemptID: binding.AttemptID, Generation: binding.Generation, TargetFingerprint: target[:]}
	report := ExecutionReport{Outcome: ExecutionCommittedAndVerified, Committed: true, PostCommitVerified: true,
		OperationID: operationID,
		PlanID:      uuid.New(), PlanDigest: "plan", PostCommitFactsDigest: terminal.FactsDigest, PostCommitFactsAsOf: terminal.FreshUntil.Add(-d007FreshnessWindow)}
	// Use the actual D007 facts digest and a fresh as-of for the sealed D009
	// fixture; the report helper models the post-TX2 owner seam.
	report.PostCommitFactsAsOf = facts.AsOf
	report.d009Seal = makeD009ExecutionSeal(report, lease, target)
	if report.d009Seal == nil {
		t.Fatal("failed to make D009 fixture")
	}
	d009, err := D009EvidenceFromReport(report)
	if err != nil {
		t.Fatal(err)
	}
	handoff, err := issuer.Issue(terminal, d009)
	if err != nil {
		t.Fatal(err)
	}
	if d009.OperationID != operationID {
		t.Fatalf("D009 operation=%s, want owner operation %s", d009.OperationID, operationID)
	}
	return handoff, D010HandoffContext{OperationID: operationID, AttemptID: binding.AttemptID, TargetFingerprint: target, Generation: binding.Generation}, terminal, d009
}

func issuerD007ForD010(decision ReadinessDecision, facts FactSet, binding D007Binding) (LiveD007Capability, error) {
	issuer := NewD007CapabilityIssuer()
	return issuer.issue(decision, facts, binding)
}

func TestD010IssueVerifyConsumeIsOpaqueAndOneShot(t *testing.T) {
	issuer := NewD010HandoffIssuer()
	handoff, context, _, _ := d010Fixture(t, issuer)
	if handoff.String() != "D010_HANDOFF[opaque]" {
		t.Fatalf("unexpected handoff presentation: %s", handoff)
	}
	encoded, err := json.Marshal(handoff)
	if err != nil || string(encoded) != "null" {
		t.Fatalf("handoff serialized: %s (%v)", encoded, err)
	}
	if err := VerifyD010Handoff(handoff, context); err != nil {
		t.Fatal(err)
	}
	if err := ConsumeD010Handoff(handoff, context); err != nil {
		t.Fatal(err)
	}
	if err := ConsumeD010Handoff(handoff, context); !errors.Is(err, ErrD010HandoffReused) {
		t.Fatalf("replay err=%v", err)
	}
}

func TestD010RejectsForgedCopiedEvidenceAndMismatchedContext(t *testing.T) {
	issuer := NewD010HandoffIssuer()
	handoff, context, terminal, d009 := d010Fixture(t, issuer)
	forgedTerminal := terminal
	forgedTerminal.ProofDigest = "forged"
	if _, err := issuer.Issue(forgedTerminal, d009); !errors.Is(err, ErrD010HandoffInvalid) {
		t.Fatalf("forged terminal err=%v", err)
	}
	copiedD009 := d009
	copiedD009.seal = nil
	if _, err := issuer.Issue(terminal, copiedD009); !errors.Is(err, ErrD010HandoffInvalid) {
		t.Fatalf("reconstructed D009 err=%v", err)
	}
	wrong := context
	wrong.OperationID = uuid.New()
	if err := VerifyD010Handoff(handoff, wrong); !errors.Is(err, ErrD010HandoffInvalid) {
		t.Fatalf("operation mismatch err=%v", err)
	}

	consumeHandoff, consumeContext, _, _ := d010Fixture(t, issuer)
	consumeWrong := consumeContext
	consumeWrong.OperationID = uuid.New()
	if err := ConsumeD010Handoff(consumeHandoff, consumeWrong); !errors.Is(err, ErrD010HandoffInvalid) {
		t.Fatalf("consume operation mismatch err=%v", err)
	}
}

func TestD010RejectsStaleHandoff(t *testing.T) {
	issuer := NewD010HandoffIssuer()
	handoff, context, _, _ := d010Fixture(t, issuer)
	handoff.state.binding.FreshUntil = time.Now().UTC().Add(-time.Second)
	if err := VerifyD010Handoff(handoff, context); !errors.Is(err, ErrD010HandoffInvalid) && !errors.Is(err, ErrD010HandoffExpired) {
		t.Fatalf("stale err=%v", err)
	}
}

func TestD010RejectsLiveCapabilitySafeTerminalAndStateLoss(t *testing.T) {
	issuer := NewD010HandoffIssuer()
	handoff, context, terminal, d009 := d010Fixture(t, issuer)
	live := LiveD007Capability{}
	// A live capability has a different Go type and cannot be accepted as a
	// D010 input; a safe/reconstructed terminal projection has no private seal.
	_ = live
	safe := terminal
	safe.state = nil
	if _, err := issuer.Issue(safe, d009); !errors.Is(err, ErrD010HandoffInvalid) {
		t.Fatalf("safe terminal err=%v", err)
	}
	if err := VerifyD010Handoff(handoff, context); err != nil {
		t.Fatal(err)
	}
	issuer.states = nil
	if err := VerifyD010Handoff(handoff, context); !errors.Is(err, ErrD010HandoffUnknown) {
		t.Fatalf("state loss err=%v", err)
	}
}
