package reconciliation

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func d4TestTuple(t *testing.T) D4OwnerTuple {
	t.Helper()
	tuple, err := NewD4OwnerTuple(uuid.New(), uuid.New(), make([]byte, 32), 7)
	if err != nil {
		t.Fatal(err)
	}
	return tuple
}

func d4TestResult(tuple D4OwnerTuple) D4SafeResult {
	return D4SafeResult{OperationID: tuple.OperationID().String(), AttemptID: tuple.AttemptID(), TargetFingerprint: tuple.TargetFingerprintHex(), Generation: tuple.Generation(), Disposition: D4Success, CommitStatus: D4CommitCommitted, PostVerificationStatus: D4PostVerified, CleanupStatus: D4CleanupConfirmed, Certainty: D4Known, ReplayDisposition: D4ReplayForbidden}
}
func d4Approval(t *testing.T, tuple D4OwnerTuple, kind D4EventKind) D4OwnerApproval {
	t.Helper()
	approval, err := NewD4OwnerApproval(tuple, kind, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return approval
}

type d4Revalidator struct {
	err   error
	calls int
}

func (r *d4Revalidator) RevalidateD4(context.Context, D4OwnerTuple) error { r.calls++; return r.err }

func TestD4FrozenStatesAndClosedTransitionMatrix(t *testing.T) {
	states := D4States()
	if len(states) != 9 {
		t.Fatalf("states=%v", states)
	}
	seen := map[D4State]bool{}
	for _, state := range states {
		if seen[state] || !validD4State(state) {
			t.Fatalf("invalid/duplicate state %q", state)
		}
		seen[state] = true
	}
	fsm, err := NewD4FSM(d4TestTuple(t))
	if err != nil {
		t.Fatal(err)
	}
	record, _ := fsm.Snapshot()
	if err := fsm.Apply(context.Background(), D4OwnerEvent{Kind: D4EventBeginExecution, Tuple: record.Tuple, Approval: d4Approval(t, record.Tuple, D4EventBeginExecution)}, nil); err == nil {
		t.Fatal("RECEIVED accepted BEGIN_EXECUTION")
	}
}

func TestD4IdentityBindingAndTerminalImmutability(t *testing.T) {
	tuple := d4TestTuple(t)
	fsm, err := NewD4FSM(tuple)
	if err != nil {
		t.Fatal(err)
	}
	admit := D4OwnerEvent{Kind: D4EventAdmit, Tuple: tuple, Approval: d4Approval(t, tuple, D4EventAdmit)}
	if err := fsm.Apply(context.Background(), admit, nil); err != nil {
		t.Fatal(err)
	}
	wrong := tuple
	wrong.operationID = uuid.New()
	err = fsm.Apply(context.Background(), D4OwnerEvent{Kind: D4EventBeginExecution, Tuple: wrong, Approval: d4Approval(t, wrong, D4EventBeginExecution)}, nil)
	var d4err *D4Error
	if !errors.As(err, &d4err) || d4err.Class != D4ErrorWrongOperation {
		t.Fatalf("wrong operation err=%v", err)
	}
	if err := fsm.Apply(context.Background(), D4OwnerEvent{Kind: D4EventBeginExecution, Tuple: tuple, Approval: d4Approval(t, tuple, D4EventBeginExecution)}, nil); err != nil {
		t.Fatal(err)
	}
	result := d4TestResult(tuple)
	if err := fsm.Apply(context.Background(), D4OwnerEvent{Kind: D4EventRecordResult, Tuple: tuple, Approval: d4Approval(t, tuple, D4EventRecordResult), Result: &result}, nil); err != nil {
		t.Fatal(err)
	}
	if err := fsm.Apply(context.Background(), D4OwnerEvent{Kind: D4EventTerminal, Tuple: tuple, Approval: d4Approval(t, tuple, D4EventTerminal), Result: &result}, nil); err != nil {
		t.Fatal(err)
	}
	if err := fsm.Apply(context.Background(), D4OwnerEvent{Kind: D4EventRequireRecovery, Tuple: tuple, Approval: d4Approval(t, tuple, D4EventRequireRecovery), Recovery: D4RecoveryPostVerify}, nil); err == nil {
		t.Fatal("terminal state changed")
	} else if !errors.As(err, &d4err) || d4err.Class != D4ErrorTerminalImmutable {
		t.Fatalf("terminal error=%v", err)
	}
}

func TestD4MappingResumeRequiresFreshOwnerRevalidation(t *testing.T) {
	tuple := d4TestTuple(t)
	fsm, _ := NewD4FSM(tuple)
	for _, event := range []D4EventKind{D4EventAdmit, D4EventWaitForMapping} {
		if err := fsm.Apply(context.Background(), D4OwnerEvent{Kind: event, Tuple: tuple, Approval: d4Approval(t, tuple, event)}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := fsm.Apply(context.Background(), D4OwnerEvent{Kind: D4EventResumeMapping, Tuple: tuple, Approval: d4Approval(t, tuple, D4EventResumeMapping)}, nil); err == nil {
		t.Fatal("mapping resumed without owner")
	}
	revalidator := &d4Revalidator{err: errors.New("stale")}
	err := fsm.Apply(context.Background(), D4OwnerEvent{Kind: D4EventResumeMapping, Tuple: tuple, Approval: d4Approval(t, tuple, D4EventResumeMapping)}, revalidator)
	var d4err *D4Error
	if !errors.As(err, &d4err) || d4err.Class != D4ErrorStale || revalidator.calls != 1 {
		t.Fatalf("stale resume err=%v calls=%d", err, revalidator.calls)
	}
	revalidator.err = nil
	if err := fsm.Apply(context.Background(), D4OwnerEvent{Kind: D4EventResumeMapping, Tuple: tuple, Approval: d4Approval(t, tuple, D4EventResumeMapping)}, revalidator); err != nil {
		t.Fatal(err)
	}
	record, _ := fsm.Snapshot()
	if record.State != D4Admitted {
		t.Fatalf("resume state=%s", record.State)
	}
}

func TestInMemoryD4LedgerClaimCASAndDuplicateConvergence(t *testing.T) {
	ledger := NewInMemoryD4Ledger()
	tuple := d4TestTuple(t)
	if _, err := ledger.Receive(context.Background(), D4ReceiveRequest{Tuple: tuple, ReceivedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	const workers = 32
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	failures := 0
	approval := d4Approval(t, tuple, D4EventAdmit)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claim, err := ledger.Claim(context.Background(), D4ClaimRequest{Tuple: tuple, Approval: approval, WorkerID: uuid.New()})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failures++
			}
			if claim.Won {
				winners++
			}
		}()
	}
	wg.Wait()
	if winners != 1 || failures != 0 {
		t.Fatalf("winners=%d failures=%d", winners, failures)
	}
	record, _ := ledger.Get(context.Background(), tuple)
	if record.State != D4Admitted || record.ClaimID == uuid.Nil {
		t.Fatalf("record=%+v", record)
	}
	duplicate, err := ledger.Claim(context.Background(), D4ClaimRequest{Tuple: tuple, Approval: approval, WorkerID: uuid.New()})
	if err != nil || duplicate.ClaimID == uuid.Nil || duplicate.Won {
		t.Fatalf("duplicate=%+v err=%v", duplicate, err)
	}
	wrong := tuple
	wrong.generation++
	if _, err := ledger.Transition(context.Background(), D4TransitionRequest{Tuple: tuple, Expected: D4Received, Event: D4OwnerEvent{Kind: D4EventBeginExecution, Tuple: wrong}}); err == nil {
		t.Fatal("stale expected state accepted")
	}
}

func TestD4SafeResultPreservesIndependentDimensions(t *testing.T) {
	tuple := d4TestTuple(t)
	result := d4TestResult(tuple)
	result.Disposition = D4NonSuccess
	result.CommitStatus = D4CommitUnknown
	result.Unknown = true
	result.RecoveryRequired = true
	result.RecoveryClass = D4RecoveryUnknown
	result.CleanupStatus = D4CleanupConfirmed
	result.PostVerificationStatus = D4PostNotVerified
	if err := result.ValidateFor(tuple); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) == 0 {
		t.Fatalf("empty safe result: %v", err)
	}
}
