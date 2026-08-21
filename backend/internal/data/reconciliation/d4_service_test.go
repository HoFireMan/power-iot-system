package reconciliation

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

type d4ServiceInvoker struct {
	mu      sync.Mutex
	calls   int
	results []D4CompositeResult
	err     error
}

func (i *d4ServiceInvoker) InvokeD4(context.Context, D4OwnerTuple) (D4CompositeResult, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.calls++
	if len(i.results) == 0 {
		return D4CompositeResult{}, i.err
	}
	result := i.results[0]
	i.results = i.results[1:]
	return result, i.err
}
func (i *d4ServiceInvoker) Calls() int { i.mu.Lock(); defer i.mu.Unlock(); return i.calls }

type d4RecoveryResolver struct {
	result   D4SafeResult
	recovery D4RecoveryClass
}

func (r d4RecoveryResolver) ResolveD4(context.Context, D4Record) (D4SafeResult, D4RecoveryClass, error) {
	return r.result, r.recovery, nil
}

func d4TestAuthorizer() D4OwnerEventAuthorizer {
	return D4OwnerEventAuthorizerFunc(func(_ context.Context, tuple D4OwnerTuple, kind D4EventKind) (D4OwnerApproval, error) {
		return NewD4OwnerApproval(tuple, kind, time.Now().UTC())
	})
}
func d4AdmitEvent(t *testing.T, tuple D4OwnerTuple) D4OwnerEvent {
	t.Helper()
	return D4OwnerEvent{Kind: D4EventAdmit, Tuple: tuple, Approval: d4Approval(t, tuple, D4EventAdmit)}
}

func TestD4ServiceSingleWinnerTerminalAndDuplicateProjection(t *testing.T) {
	ledger := NewInMemoryD4Ledger()
	tuple := d4TestTuple(t)
	invoker := &d4ServiceInvoker{results: []D4CompositeResult{{Safe: d4TestResult(tuple)}}}
	service := NewD4Service(ledger, d4TestAuthorizer(), invoker)
	first, err := service.Process(context.Background(), d4AdmitEvent(t, tuple))
	if err != nil {
		t.Fatal(err)
	}
	if !first.Won || !first.CalledD3 || first.Record.State != D4Terminal {
		t.Fatalf("first=%+v", first)
	}
	second, err := service.Process(context.Background(), d4AdmitEvent(t, tuple))
	if err != nil {
		t.Fatal(err)
	}
	if second.CalledD3 || second.Record.State != D4Terminal || invoker.Calls() != 1 {
		t.Fatalf("duplicate=%+v calls=%d", second, invoker.Calls())
	}
}

func TestD4ServiceMappingResumeRequiresFreshOwnerRevalidation(t *testing.T) {
	ledger := NewInMemoryD4Ledger()
	tuple := d4TestTuple(t)
	waiting := d4TestResult(tuple)
	waiting.Disposition = D4NonSuccess
	invoker := &d4ServiceInvoker{results: []D4CompositeResult{{Safe: waiting, MappingWait: true}, {Safe: d4TestResult(tuple)}}}
	revalidator := &d4Revalidator{}
	service := NewD4Service(ledger, d4TestAuthorizer(), invoker)
	service.Revalidator = revalidator
	first, err := service.Process(context.Background(), d4AdmitEvent(t, tuple))
	if err != nil {
		t.Fatal(err)
	}
	if first.Record.State != D4WaitingForMapping {
		t.Fatalf("waiting state=%s", first.Record.State)
	}
	resume := D4OwnerEvent{Kind: D4EventResumeMapping, Tuple: tuple, Approval: d4Approval(t, tuple, D4EventResumeMapping)}
	second, err := service.ResumeMapping(context.Background(), resume)
	if err != nil {
		t.Fatal(err)
	}
	if second.Record.State != D4Terminal || revalidator.calls != 1 || invoker.Calls() != 2 {
		t.Fatalf("resume=%+v revalidation=%d calls=%d", second, revalidator.calls, invoker.Calls())
	}
}

func TestD4ServiceRejectsContinuationOnNonSuccess(t *testing.T) {
	ledger := NewInMemoryD4Ledger()
	tuple := d4TestTuple(t)
	waiting := d4TestResult(tuple)
	waiting.Disposition = D4NonSuccess
	consumed := 0
	invoker := &d4ServiceInvoker{results: []D4CompositeResult{{Safe: waiting, continuation: func() error { consumed++; return nil }}}}
	service := NewD4Service(ledger, d4TestAuthorizer(), invoker)
	result, err := service.Process(context.Background(), d4AdmitEvent(t, tuple))
	if err == nil || result.Record.State != D4RecoveryRequired || consumed != 0 {
		t.Fatalf("result=%+v err=%v consumed=%d", result, err, consumed)
	}
}

func TestD4ServicePreClaimDispositionDoesNotSynthesizeClaimOrCallD3(t *testing.T) {
	for _, kind := range []D4EventKind{D4EventTerminal, D4EventRequireRecovery} {
		ledger := NewInMemoryD4Ledger()
		tuple := d4TestTuple(t)
		invoker := &d4ServiceInvoker{}
		service := NewD4Service(ledger, d4TestAuthorizer(), invoker)
		event := D4OwnerEvent{Kind: kind, Tuple: tuple, Approval: d4Approval(t, tuple, kind)}
		if kind == D4EventTerminal {
			result := d4TestResult(tuple)
			result.Disposition = D4NonSuccess
			result.CommitStatus = D4CommitNotCommitted
			event.Result = &result
		} else {
			event.Recovery = D4RecoveryUnknown
		}
		got, err := service.FinalizePreClaim(context.Background(), event)
		if err != nil || got.Record.State != map[D4EventKind]D4State{D4EventTerminal: D4Terminal, D4EventRequireRecovery: D4RecoveryRequired}[kind] || got.Record.ClaimID != uuid.Nil || got.CalledD3 || invoker.Calls() != 0 {
			t.Fatalf("kind=%s got=%+v err=%v calls=%d", kind, got, err, invoker.Calls())
		}
	}
}

func TestD4ServiceAmbiguousD3OutcomeQueuesRecovery(t *testing.T) {
	ledger := NewInMemoryD4Ledger()
	tuple := d4TestTuple(t)
	invoker := &d4ServiceInvoker{err: errors.New("connection lost")}
	service := NewD4Service(ledger, d4TestAuthorizer(), invoker)
	result, err := service.Process(context.Background(), d4AdmitEvent(t, tuple))
	if err == nil || result.Record.State != D4RecoveryRequired {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	entries, err := ledger.ListRecovery(context.Background())
	if err != nil || len(entries) != 1 {
		t.Fatalf("recovery entries=%v err=%v", entries, err)
	}
}

func TestD4RecoveryClassifiesAndOwnerResolvesOnlyToTerminal(t *testing.T) {
	ledger := NewInMemoryD4Ledger()
	tuple := d4TestTuple(t)
	if _, err := ledger.Receive(context.Background(), D4ReceiveRequest{Tuple: tuple, ReceivedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	claim, err := ledger.Claim(context.Background(), D4ClaimRequest{Tuple: tuple, Approval: d4Approval(t, tuple, D4EventAdmit), WorkerID: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	auth := d4TestAuthorizer()
	manager := NewD4RecoveryManager(ledger, auth, nil)
	begin, _ := NewD4OwnerApproval(tuple, D4EventBeginExecution, time.Now())
	if _, err := ledger.Transition(context.Background(), D4TransitionRequest{Tuple: tuple, Expected: D4Admitted, ClaimID: claim.ClaimID, Event: D4OwnerEvent{Kind: D4EventBeginExecution, Tuple: tuple, Approval: begin}}); err != nil {
		t.Fatal(err)
	}
	record, _ := ledger.Get(context.Background(), tuple)
	classified, err := manager.ClassifyStartup(context.Background(), []D4Record{record})
	if err != nil {
		t.Fatal(err)
	}
	if classified[0].Action != D4RecoveryQueued {
		t.Fatalf("classification=%+v", classified)
	}
	record, _ = ledger.Get(context.Background(), tuple)
	known := d4TestResult(tuple)
	known.Disposition = D4NonSuccess
	manager.Resolver = d4RecoveryResolver{result: known, recovery: D4RecoveryStale}
	resolved, err := manager.Resolve(context.Background(), record)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.State != D4Terminal {
		t.Fatalf("resolved=%+v", resolved)
	}
}

func TestD4JournalBundleAndSafeProjectionBoundary(t *testing.T) {
	tuple := d4TestTuple(t)
	result := d4TestResult(tuple)
	event := D4JournalEvent{EventID: uuid.New(), Version: 1, Tuple: tuple, From: D4ResultRecorded, To: D4Terminal, Result: &result, OccurredAt: time.Now().UTC()}
	journal := NewInMemoryD4Journal()
	if err := journal.Append(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if err := journal.Append(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	seen := 0
	if err := journal.Replay(context.Background(), func(got D4JournalEvent) error {
		seen++
		if got.To != D4Terminal {
			t.Fatal(got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if seen != 1 {
		t.Fatalf("replay count=%d", seen)
	}
	record := D4Record{Tuple: tuple, State: D4Terminal, Result: &result, UpdatedAt: time.Now().UTC()}
	bundle, err := NewD4ToD5Bundle(record)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := bundle.MarshalSafe()
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateSafeProjectionBytes(encoded); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSafeProjectionBytes([]byte(`{"d010":"bearer"}`)); err == nil {
		t.Fatal("forbidden projection accepted")
	}
	if err := ValidateSafeProjectionBytes([]byte(`{"arbitrary_authority_field":"value"}`)); err == nil {
		t.Fatal("unknown projection field accepted")
	}
}

func TestD4BundleV2CorrelationAndExactD018Validation(t *testing.T) {
	tuple := d4TestTuple(t)
	result := d4TestResult(tuple)
	record := D4Record{Tuple: tuple, State: D4Terminal, Result: &result, UpdatedAt: time.Now().UTC()}
	correlation := D4SafeCorrelation{FactsDigest: strings.Repeat("a", 64), ProofDigest: strings.Repeat("b", 64), PredicateIdentity: "predicate-v1", PredicateVersion: "1", ProvenanceDigest: strings.Repeat("c", 64)}
	bundle, err := NewD4ToD5BundleV2(record, correlation)
	if err != nil {
		t.Fatal(err)
	}
	if err := bundle.ValidateForD5(); err != nil {
		t.Fatal(err)
	}
	if _, err := bundle.MarshalSafeForD5(); err != nil {
		t.Fatal(err)
	}
	bundle.D018[0].Seam = "forged"
	if err := bundle.ValidateForD5(); err == nil {
		t.Fatal("forged D018 seam accepted")
	}
}

func TestD4JournalRejectsConflictingExactDuplicate(t *testing.T) {
	tuple := d4TestTuple(t)
	result := d4TestResult(tuple)
	event := D4JournalEvent{EventID: uuid.New(), Version: 1, Tuple: tuple, From: D4ResultRecorded, To: D4Terminal, Result: &result, OccurredAt: time.Now().UTC()}
	journal := NewInMemoryD4Journal()
	if err := journal.Append(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	conflict := event
	conflict.Correlation = "different"
	if err := journal.Append(context.Background(), conflict); err == nil {
		t.Fatal("conflicting journal duplicate accepted")
	}
}

func TestD4ContinuationStatesRequirePrivateProof(t *testing.T) {
	tuple := d4TestTuple(t)
	fsm, err := NewD4FSM(tuple)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []D4EventKind{D4EventAdmit, D4EventBeginExecution} {
		if err := fsm.Apply(context.Background(), D4OwnerEvent{Kind: kind, Tuple: tuple, Approval: d4Approval(t, tuple, kind)}, nil); err != nil {
			t.Fatal(err)
		}
	}
	result := d4TestResult(tuple)
	if err := fsm.Apply(context.Background(), D4OwnerEvent{Kind: D4EventRecordResult, Tuple: tuple, Approval: d4Approval(t, tuple, D4EventRecordResult), Result: &result}, nil); err != nil {
		t.Fatal(err)
	}
	if err := fsm.Apply(context.Background(), D4OwnerEvent{Kind: D4EventRequestContinuation, Tuple: tuple, Approval: d4Approval(t, tuple, D4EventRequestContinuation)}, nil); err == nil {
		t.Fatal("synthetic continuation accepted")
	}
}

func TestD4LedgerClaimInvariantAllowsOnlyPreClaimTerminalRecovery(t *testing.T) {
	for _, kind := range []D4EventKind{D4EventTerminal, D4EventRequireRecovery} {
		ledger := NewInMemoryD4Ledger()
		tuple := d4TestTuple(t)
		if _, err := ledger.Receive(context.Background(), D4ReceiveRequest{Tuple: tuple, ReceivedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		event := D4OwnerEvent{Kind: kind, Tuple: tuple, Approval: d4Approval(t, tuple, kind)}
		if kind == D4EventTerminal {
			result := d4TestResult(tuple)
			result.Disposition = D4NonSuccess
			result.CommitStatus = D4CommitNotCommitted
			event.Result = &result
		} else {
			event.Recovery = D4RecoveryUnknown
		}
		record, err := ledger.Transition(context.Background(), D4TransitionRequest{Tuple: tuple, Expected: D4Received, ClaimID: uuid.Nil, Event: event})
		if err != nil || record.ClaimID != uuid.Nil {
			t.Fatalf("kind=%s record=%+v err=%v", kind, record, err)
		}
	}
	ledger := NewInMemoryD4Ledger()
	tuple := d4TestTuple(t)
	_, _ = ledger.Receive(context.Background(), D4ReceiveRequest{Tuple: tuple, ReceivedAt: time.Now()})
	approval := d4Approval(t, tuple, D4EventAdmit)
	if _, err := ledger.Transition(context.Background(), D4TransitionRequest{Tuple: tuple, Expected: D4Received, ClaimID: uuid.Nil, Event: D4OwnerEvent{Kind: D4EventAdmit, Tuple: tuple, Approval: approval}}); err == nil {
		t.Fatal("claimless admission accepted")
	}
}

func TestD4LedgerPostClaimTerminalRetainsClaim(t *testing.T) {
	ledger := NewInMemoryD4Ledger()
	tuple := d4TestTuple(t)
	_, _ = ledger.Receive(context.Background(), D4ReceiveRequest{Tuple: tuple, ReceivedAt: time.Now()})
	claim, err := ledger.Claim(context.Background(), D4ClaimRequest{Tuple: tuple, Approval: d4Approval(t, tuple, D4EventAdmit), WorkerID: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	begin := D4OwnerEvent{Kind: D4EventBeginExecution, Tuple: tuple, Approval: d4Approval(t, tuple, D4EventBeginExecution)}
	if _, err = ledger.Transition(context.Background(), D4TransitionRequest{Tuple: tuple, Expected: D4Admitted, ClaimID: claim.ClaimID, Event: begin}); err != nil {
		t.Fatal(err)
	}
	result := d4TestResult(tuple)
	terminal := D4OwnerEvent{Kind: D4EventTerminal, Tuple: tuple, Approval: d4Approval(t, tuple, D4EventTerminal), Result: &result}
	record, err := ledger.Transition(context.Background(), D4TransitionRequest{Tuple: tuple, Expected: D4Executing, ClaimID: claim.ClaimID, Event: terminal})
	if err != nil || record.ClaimID != claim.ClaimID {
		t.Fatalf("record=%+v err=%v", record, err)
	}
}

func TestD4LedgerRejectsStaleWorkerWrite(t *testing.T) {
	ledger := NewInMemoryD4Ledger()
	tuple := d4TestTuple(t)
	_, _ = ledger.Receive(context.Background(), D4ReceiveRequest{Tuple: tuple, ReceivedAt: time.Now()})
	claim, err := ledger.Claim(context.Background(), D4ClaimRequest{Tuple: tuple, Approval: d4Approval(t, tuple, D4EventAdmit), WorkerID: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	approval := d4Approval(t, tuple, D4EventBeginExecution)
	_, err = ledger.Transition(context.Background(), D4TransitionRequest{Tuple: tuple, Expected: D4Admitted, ClaimID: uuid.Nil, Event: D4OwnerEvent{Kind: D4EventBeginExecution, Tuple: tuple, Approval: approval}})
	var d4err *D4Error
	if !errors.As(err, &d4err) || d4err.Class != D4ErrorCASConflict {
		t.Fatalf("stale write err=%v", err)
	}
	_ = claim
}
