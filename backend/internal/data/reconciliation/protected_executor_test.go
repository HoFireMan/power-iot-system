package reconciliation

import (
	"errors"
	"testing"

	"github.com/google/uuid"
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
