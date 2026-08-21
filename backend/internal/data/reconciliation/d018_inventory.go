package reconciliation

import "fmt"

// D018Seam names the semantic obligations that a future D5 implementation
// must prove. These values are an evidence-only inventory: they do not define
// tables, columns, migrations, foreign keys, or execution authority.
type D018Seam string

const (
	D018ProtectedTargetIdentity D018Seam = "protected-target-identity-and-binding"
	D018D3Continuation          D018Seam = "D3-continuation-and-handoff"
	D018FKValidation            D018Seam = "FK-validation-proof"
	D018NullabilityFinalization D018Seam = "nullability-and-finalization-proof"
	D018FinalVerification       D018Seam = "final-semantic-verification-proof"
	D018UnknownRecovery         D018Seam = "UNKNOWN-and-recovery-semantics"
	D018EvidenceCorrelation     D018Seam = "evidence-and-provenance-correlation"
)

// D018SeamInventoryEntry is traceability metadata only. Status explicitly
// records that no D5 implementation is present in PR1.
type D018SeamInventoryEntry struct {
	Seam            D018Seam `json:"seam"`
	RequiredBinding string   `json:"required_binding"`
	Owner           string   `json:"owner"`
	EvidenceInPR1   string   `json:"evidence_in_pr1"`
	FutureD5Proof   string   `json:"future_d5_proof"`
	Implementation  string   `json:"implementation"`
}

var d018Inventory = []D018SeamInventoryEntry{
	{D018ProtectedTargetIdentity, "target fingerprint + operation/attempt/generation", "PR1/D3", "SafeSemanticResult binding fields", "validate identity before FK/finalization", "inventory-only"},
	{D018D3Continuation, "D3-verified opaque one-shot handoff", "D3", "PR1ToD4Result.ContinueD3Protected", "prove owner verification and one-shot consumption", "inventory-only"},
	{D018FKValidation, "bound target and evidence correlation", "future D5", "D018 inventory; no schema", "prove referenced identity validation", "inventory-only"},
	{D018NullabilityFinalization, "UNKNOWN-safe finalization state", "future D5", "SafeRecoveryEvidence", "prove nullable/finalization semantics", "inventory-only"},
	{D018FinalVerification, "semantic result + post-verification facts", "future D5", "SafeSemanticResult status fields", "prove final semantic verification", "inventory-only"},
	{D018UnknownRecovery, "explicit UNKNOWN and recovery evidence", "D3/PR1", "Unknown, RecoveryRequired, SafeRecoveryEvidence", "prove no blind retry and recovery closure", "inventory-only"},
	{D018EvidenceCorrelation, "safe correlation digests and provenance", "PR1", "OperationID, facts/proof/predicate fields", "correlate evidence without authority", "inventory-only"},
}

// D018SeamInventory returns a copy so callers cannot mutate the static
// traceability record. It is safe to serialize and has no authority-bearing
// values.
func D018SeamInventory() []D018SeamInventoryEntry {
	inventory := make([]D018SeamInventoryEntry, len(d018Inventory))
	copy(inventory, d018Inventory)
	return inventory
}

// ValidateD018SeamInventory accepts only the canonical seven-entry semantic
// inventory. D5 may validate the inventory but cannot treat it as authority.
func ValidateD018SeamInventory(entries []D018SeamInventoryEntry) error {
	if len(entries) != len(d018Inventory) {
		return fmt.Errorf("D018 seam inventory count=%d want=%d", len(entries), len(d018Inventory))
	}
	for index, want := range d018Inventory {
		got := entries[index]
		if got != want {
			return fmt.Errorf("D018 seam inventory entry %d does not match canonical inventory", index)
		}
	}
	return nil
}
