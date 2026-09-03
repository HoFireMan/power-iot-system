package reconciliation

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"power-iot-backend/internal/data/private_migrations"
)

func TestExecutableItemsUseDeterministicWriteOrder(t *testing.T) {
	client := uint(10)
	items := []PlanItem{
		{StableID: StableID("test", "audit"), Kind: PlanItemAdmin, OperationID: uuid.MustParse("00000000-0000-0000-0000-000000000002"), AuditID: uuid.MustParse("00000000-0000-0000-0000-000000000003"), SetAdminClient: true, IntendedClientID: &client, ExpectedAffectedCount: 1},
		{StableID: StableID("test", "shop"), Kind: PlanItemShop, ShopID: 2, SetShopClient: true, IntendedClientID: &client, ExpectedAffectedCount: 1},
		{StableID: StableID("test", "device"), Kind: PlanItemDevice, DeviceID: 1, SetInventoryOwner: true, IntendedOwnerClientID: &client, IntendedClientID: &client, ExpectedAffectedCount: 1},
		{StableID: StableID("test", "operation"), Kind: PlanItemAdmin, OperationID: uuid.MustParse("00000000-0000-0000-0000-000000000002"), SetAdminClient: true, IntendedClientID: &client, ExpectedAffectedCount: 1},
	}
	got := executableItems(items)
	if len(got) != 4 {
		t.Fatalf("executable items=%d", len(got))
	}
	if !got[0].SetInventoryOwner || !got[1].SetShopClient || got[2].AuditID != uuid.Nil || got[3].AuditID == uuid.Nil {
		t.Fatalf("unexpected deterministic order: %+v", got)
	}
}

func TestApplyExpectedFactsChangesOnlyPermittedFields(t *testing.T) {
	facts := testFacts()
	client := uint(20)
	shopID := facts.Shops[0].ID
	deviceID := facts.Devices[0].ID
	opID := uuid.MustParse("20000000-0000-0000-0000-000000000021")
	auditID := uuid.MustParse("30000000-0000-0000-0000-000000000021")
	facts.Users = []UserFact{{ID: 7, AuthEnabled: true}}
	facts.AdminOperations = []AdminOperationFact{{OperationID: opID, Operation: "bind", ActorID: 7}}
	facts.AdminAudits = []AdminAuditFact{{ID: auditID, OperationID: opID, Action: "bind", ActorID: 7}}
	items := []PlanItem{
		{Kind: PlanItemShop, ShopID: shopID, SetShopClient: true, IntendedClientID: &client},
		{Kind: PlanItemDevice, DeviceID: deviceID, SetInventoryOwner: true, IntendedClientID: &client},
		{Kind: PlanItemAdmin, OperationID: opID, SetAdminClient: true, IntendedClientID: &client},
		{Kind: PlanItemAdmin, OperationID: opID, AuditID: auditID, SetAdminClient: true, IntendedClientID: &client},
	}
	updated := applyExpectedFacts(facts, items)
	if updated.Shops[0].ClientID == nil || *updated.Shops[0].ClientID != client {
		t.Fatalf("shop was not updated: %+v", updated.Shops[0])
	}
	if updated.Devices[0].InventoryOwnerClientID == nil || *updated.Devices[0].InventoryOwnerClientID != client {
		t.Fatalf("device was not updated: %+v", updated.Devices[0])
	}
	if updated.Users[0].AuthEnabled != facts.Users[0].AuthEnabled {
		t.Fatal("applyExpectedFacts changed auth evidence")
	}
	if err := equalCanonicalFacts(updated, updated); err != nil {
		t.Fatal(err)
	}
}

func TestCompareCountsRejectsMissingOrUnexpectedMutation(t *testing.T) {
	if err := compareCounts(map[string]int{ExpectedCountShopClientUpdates: 1}, map[string]int{ExpectedCountShopClientUpdates: 0}); err == nil {
		t.Fatal("accepted mismatched affected count")
	}
	if err := compareCounts(map[string]int{ExpectedCountShopClientUpdates: 1}, map[string]int{ExpectedCountShopClientUpdates: 1, "unexpected": 0}); err == nil {
		t.Fatal("accepted unexpected affected-count key")
	}
}

func TestPostCommitFactsAsOfMustMatchTX2Snapshot(t *testing.T) {
	facts := testFacts()
	if err := validatePostCommitFactsAsOf(facts, facts.AsOf); err != nil {
		t.Fatalf("matching collector timestamp rejected: %v", err)
	}
	facts.AsOf = facts.AsOf.Add(time.Nanosecond)
	if err := validatePostCommitFactsAsOf(facts, facts.AsOf.Add(-time.Nanosecond)); err == nil {
		t.Fatal("collector timestamp mismatch accepted")
	}
}

func TestProtectedExecutorSuccessfulTerminalIssuesD010(t *testing.T) {
	issuer := NewD010HandoffIssuer()
	terminal, context := freshD007TerminalForD010Test(t)
	target := context.TargetFingerprint
	lease := &migrations.D1LLeaseIdentity{OperationID: context.OperationID, AttemptID: context.AttemptID, Generation: context.Generation, TargetFingerprint: target[:]}
	report := ExecutionReport{Outcome: ExecutionCommittedAndVerified, Committed: true, PostCommitVerified: true,
		OperationID: context.OperationID, PlanID: uuid.New(), PlanDigest: "protected-plan", PostCommitFactsDigest: terminal.FactsDigest, PostCommitFactsAsOf: time.Now().UTC()}
	report.D007Terminal = terminal
	executor := &ProtectedExecutor{}
	if err := issueProtectedD010(&report, lease, target, issuer, executor.installD010Handoff); err != nil {
		t.Fatal(err)
	}
	if report.d009Seal != nil {
		t.Fatal("D009 seal escaped the owner-local issuance conversion")
	}
	if err := executor.ContinueD3Protected(context); err != nil {
		t.Fatal(err)
	}
}

func freshD007TerminalForD010Test(t *testing.T) (D007TerminalEvidence, D010HandoffContext) {
	t.Helper()
	facts := FactSet{SchemaVersion: SchemaVersion, AsOf: time.Now().UTC()}
	request := cleanV5ReadinessRequest()
	request.Facts = facts
	bindReadinessEvidence(&request)
	decision := EvaluateReadiness(request)
	if !decision.Ready {
		t.Fatalf("readiness: %+v", decision)
	}
	target := [32]byte{0x31, 0x32, 0x33}
	operationID := uuid.New()
	binding := D007Binding{AttemptID: uuid.New(), TargetFingerprint: target, Generation: 4}
	live, err := NewD007CapabilityIssuer().issue(decision, facts, binding)
	if err != nil {
		t.Fatal(err)
	}
	_, digestBytes, err := CanonicalSourceFacts(facts)
	if err != nil {
		t.Fatal(err)
	}
	var digest [32]byte
	copy(digest[:], digestBytes)
	expected := D007CapabilityBinding{D007Binding: binding, FactsDigest: digest, ProofDigest: d007ProofDigest(decision, digest), FreshUntil: d007FreshUntil(facts), PredicateVersion: D007PredicateVersion}
	terminal, err := ConsumeLiveD007Capability(live, expected, facts.AsOf)
	if err != nil {
		t.Fatal(err)
	}
	return terminal, D010HandoffContext{OperationID: operationID, AttemptID: binding.AttemptID, TargetFingerprint: target, Generation: binding.Generation}
}

func TestProtectedExecutorD3ContinuationIsPrivateAndOneShot(t *testing.T) {
	issuer := NewD010HandoffIssuer()
	handoff, context, _, _ := d010Fixture(t, issuer)
	executor := &ProtectedExecutor{D010: issuer}
	if err := executor.installD010Handoff(handoff); err != nil {
		t.Fatal(err)
	}
	if err := executor.ContinueD3Protected(context); err != nil {
		t.Fatal(err)
	}
	if err := executor.ContinueD3Protected(context); !errors.Is(err, ErrD010HandoffInvalid) {
		t.Fatalf("continuation replay err=%v", err)
	}

	report := ExecutionReport{Outcome: ExecutionCommittedAndVerified}
	encoded, err := json.Marshal(report)
	if err != nil || string(encoded) == "" || containsAny(string(encoded), "D010", "d009Seal") {
		t.Fatalf("private continuation appeared in JSON: %s (%v)", encoded, err)
	}
	if diagnostic := fmt.Sprintf("%+v", report); containsAny(diagnostic, "D010", "d009Seal") {
		t.Fatalf("private continuation appeared in diagnostic: %s", diagnostic)
	}
}

func containsAny(value string, terms ...string) bool {
	for _, term := range terms {
		if strings.Contains(value, term) {
			return true
		}
	}
	return false
}

func TestExecutionErrorPreservesOutcomeAndCause(t *testing.T) {
	cause := errors.New("commit transport failure")
	err := &ExecutionError{Outcome: ExecutionCommitOutcomeUnknown, Phase: PhaseCommit, Cause: cause}
	if !errors.Is(err, cause) {
		t.Fatal("execution error did not unwrap cause")
	}
	if err.Outcome != ExecutionCommitOutcomeUnknown || err.Phase != PhaseCommit {
		t.Fatalf("execution error=%+v", err)
	}
}
