package reconciliation

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestPR1ToD4SafeProjectionSeparatesAuthorityAndSerializesSafely(t *testing.T) {
	handoff, context, terminal, _ := d010Fixture(t, NewD010HandoffIssuer())
	_ = handoff
	report := ExecutionReport{Outcome: ExecutionCommittedAndVerified, Phase: PhasePostVerify,
		OperationID: context.OperationID, PlanID: uuid.New(), Committed: true, PostCommitVerified: true, D007Terminal: terminal}
	result := NewPR1ToD4Result(report)
	if result.HasD3Continuation() {
		t.Fatal("plain projection unexpectedly carries continuation")
	}
	if result.Semantic.OperationID != context.OperationID.String() {
		t.Fatalf("owner operation was not projected exactly: %+v", result.Semantic)
	}
	if result.Semantic.AttemptID != context.AttemptID || result.Semantic.Generation != context.Generation ||
		result.Semantic.TargetFingerprint == "" || result.Semantic.ProofDigest == "" {
		t.Fatalf("binding was not projected safely: %+v", result.Semantic)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	output := string(encoded)
	if strings.Contains(output, "D010") || strings.Contains(output, "LIVE_D007") || strings.Contains(output, "BackendPID") {
		t.Fatalf("authority or physical handle leaked: %s", output)
	}
	if strings.Contains(result.String(), "continuation") {
		t.Fatalf("continuation appeared in String: %s", result.String())
	}
}

func TestPR1ToD4ContinuationIsConditionalAndD3Verified(t *testing.T) {
	issuer := NewD010HandoffIssuer()
	handoff, context, terminal, d009 := d010Fixture(t, issuer)
	executor := &ProtectedExecutor{D010: issuer}
	if err := executor.installD010Handoff(handoff); err != nil {
		t.Fatal(err)
	}
	report := ExecutionReport{Outcome: ExecutionCommittedAndVerified, Phase: PhasePostVerify, OperationID: context.OperationID,
		PlanID: uuid.New(), PlanDigest: "plan", Committed: true, PostCommitVerified: true,
		PostCommitFactsDigest: terminal.FactsDigest, PostCommitFactsAsOf: d009.PostCommitAsOf, D007Terminal: terminal}
	without, err := executor.PR1ToD4Result(report, false)
	if err != nil || without.HasD3Continuation() {
		t.Fatalf("optional result=%+v err=%v", without, err)
	}
	with, err := executor.PR1ToD4Result(report, true)
	if err != nil || !with.HasD3Continuation() {
		t.Fatalf("required result=%+v err=%v", with, err)
	}
	wrong := context
	wrong.Generation++
	if err := with.ContinueD3Protected(wrong); !errors.Is(err, ErrD010HandoffInvalid) {
		t.Fatalf("wrong binding err=%v", err)
	}
	if err := with.ContinueD3Protected(context); !errors.Is(err, ErrD3ContinuationAbsent) {
		t.Fatalf("recovery after rejected continuation err=%v", err)
	}

	missing := &ProtectedExecutor{}
	if _, err := missing.PR1ToD4Result(report, true); !errors.Is(err, ErrD3ContinuationRequired) {
		t.Fatalf("missing continuation err=%v", err)
	}
}

func TestPR1ToD4RejectsMismatchedSuccessfulReportBeforeAttachingContinuation(t *testing.T) {
	issuer := NewD010HandoffIssuer()
	handoff, _, terminal, d009 := d010Fixture(t, issuer)
	executor := &ProtectedExecutor{D010: issuer}
	if err := executor.installD010Handoff(handoff); err != nil {
		t.Fatal(err)
	}
	forged := ExecutionReport{Outcome: ExecutionCommittedAndVerified, Phase: PhasePostVerify,
		OperationID: uuid.New(), PlanID: uuid.New(), PlanDigest: "plan", Committed: true,
		PostCommitVerified: true, PostCommitFactsDigest: terminal.FactsDigest,
		PostCommitFactsAsOf: d009.PostCommitAsOf, D007Terminal: terminal}
	if _, err := executor.PR1ToD4Result(forged, true); !errors.Is(err, ErrD3ContinuationRequired) {
		t.Fatalf("mismatched successful report received continuation: %v", err)
	}
}

func TestSafeResultPreservesUnknownAndRecovery(t *testing.T) {
	report := ExecutionReport{Outcome: ExecutionCommitOutcomeUnknown, Phase: PhaseCommit, CleanupError: "hidden"}
	result := NewPR1ToD4Result(report).Semantic
	if !result.Unknown || result.UnknownReason != "commit-outcome-unknown" || !result.RecoveryRequired ||
		result.CommitStatus != "COMMIT_UNKNOWN" || result.RetryDisposition != "DO_NOT_RETRY" {
		t.Fatalf("unknown was erased: %+v", result)
	}
	if result.RecoveryEvidence.CorrelationDigest == "" || !result.RecoveryEvidence.CleanupUncertain {
		t.Fatalf("recovery evidence missing: %+v", result.RecoveryEvidence)
	}

	postVerify := NewPR1ToD4Result(ExecutionReport{Outcome: ExecutionCommittedPostVerifyFailed, Phase: PhasePostVerify}).Semantic
	if postVerify.CommitStatus != "COMMITTED" || postVerify.PostVerificationStatus != "FAILED" || !postVerify.RecoveryRequired {
		t.Fatalf("postverify recovery was not preserved: %+v", postVerify)
	}
}

func TestD018InventoryIsStaticEvidenceOnly(t *testing.T) {
	inventory := D018SeamInventory()
	if len(inventory) != 7 {
		t.Fatalf("inventory entries=%d", len(inventory))
	}
	for _, entry := range inventory {
		if entry.Implementation != "inventory-only" || entry.Owner == "" || entry.FutureD5Proof == "" {
			t.Fatalf("incomplete inventory entry: %+v", entry)
		}
		if strings.Contains(strings.ToLower(entry.EvidenceInPR1), "migration") {
			t.Fatalf("inventory leaks implementation scope: %+v", entry)
		}
	}
	inventory[0].Implementation = "mutated"
	if D018SeamInventory()[0].Implementation != "inventory-only" {
		t.Fatal("inventory was mutable")
	}
}
