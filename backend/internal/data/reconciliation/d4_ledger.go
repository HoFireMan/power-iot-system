package reconciliation

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// D4ReceiveRequest contains only safe correlation identity. A ledger is not a
// source-facts store and must never accept physical handles or owner bearers.
type D4ReceiveRequest struct {
	Tuple      D4OwnerTuple
	ReceivedAt time.Time
}

type D4ClaimRequest struct {
	Tuple    D4OwnerTuple
	Approval D4OwnerApproval
	// WorkerID is safe correlation metadata. It is required by the service so
	// stale workers cannot progress a record after losing its claim.
	WorkerID uuid.UUID
}

type D4ClaimResult struct {
	Record  D4Record
	Won     bool
	ClaimID uuid.UUID
}

type D4TransitionRequest struct {
	Tuple       D4OwnerTuple
	Expected    D4State
	Event       D4OwnerEvent
	Revalidator D4OwnerRevalidator
	ClaimID     uuid.UUID
}

// D4Ledger is the logical persistence contract. Implementations must provide
// conditional, tuple-bound state changes; Claim is scheduling coordination,
// never D1/D2/D3/A2 authority.
type D4Ledger interface {
	Receive(context.Context, D4ReceiveRequest) (D4Record, error)
	Claim(context.Context, D4ClaimRequest) (D4ClaimResult, error)
	Transition(context.Context, D4TransitionRequest) (D4Record, error)
	RecordSafeResult(context.Context, D4OwnerTuple, D4State, uuid.UUID, D4SafeResult) (D4Record, error)
	MarkRecovery(context.Context, D4OwnerTuple, D4State, uuid.UUID, D4RecoveryClass) (D4Record, error)
	Get(context.Context, D4OwnerTuple) (D4Record, error)
	ListRecovery(context.Context) ([]D4Record, error)
}

type d4LedgerKey struct {
	operationID uuidKey
	attemptID   uuidKey
	target      [32]byte
	generation  uint64
}
type uuidKey [16]byte

func makeD4LedgerKey(tuple D4OwnerTuple) d4LedgerKey {
	var operation, attempt uuidKey
	copy(operation[:], tuple.operationID[:])
	copy(attempt[:], tuple.attemptID[:])
	return d4LedgerKey{operationID: operation, attemptID: attempt, target: tuple.targetFingerprint, generation: tuple.generation}
}

type d4InvocationKey struct{ operationID, attemptID uuidKey }

func makeD4InvocationKey(tuple D4OwnerTuple) d4InvocationKey {
	key := makeD4LedgerKey(tuple)
	return d4InvocationKey{operationID: key.operationID, attemptID: key.attemptID}
}

// InMemoryD4Ledger is a deterministic reference implementation. The mutex is
// an implementation detail only; correctness is expressed as one conditional
// critical section and is intentionally mirrored by the PostgreSQL harness.
type InMemoryD4Ledger struct {
	mu          sync.Mutex
	records     map[d4LedgerKey]D4Record
	invocations map[d4InvocationKey]d4LedgerKey
}

func NewInMemoryD4Ledger() *InMemoryD4Ledger {
	return &InMemoryD4Ledger{records: make(map[d4LedgerKey]D4Record), invocations: make(map[d4InvocationKey]d4LedgerKey)}
}

func (l *InMemoryD4Ledger) Receive(ctx context.Context, request D4ReceiveRequest) (D4Record, error) {
	if err := contextErr(ctx); err != nil {
		return D4Record{}, err
	}
	if l == nil {
		return D4Record{}, errors.New("D4 ledger is unavailable")
	}
	if !request.Tuple.Valid() || request.ReceivedAt.IsZero() {
		return D4Record{}, &D4Error{Class: D4ErrorMalformed, Cause: errors.New("receive requires tuple and time")}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.initLocked()
	invocation := makeD4InvocationKey(request.Tuple)
	if existing, ok := l.invocations[invocation]; ok {
		record := l.records[existing]
		if !record.Tuple.Equal(request.Tuple) {
			return D4Record{}, tupleMismatchError(record.Tuple, request.Tuple)
		}
		return cloneD4Record(record), nil
	}
	record := D4Record{Tuple: request.Tuple, State: D4Received, UpdatedAt: request.ReceivedAt.UTC()}
	key := makeD4LedgerKey(request.Tuple)
	l.records[key] = record
	l.invocations[invocation] = key
	return cloneD4Record(record), nil
}

func (l *InMemoryD4Ledger) Claim(ctx context.Context, request D4ClaimRequest) (D4ClaimResult, error) {
	if err := contextErr(ctx); err != nil {
		return D4ClaimResult{}, err
	}
	if l == nil {
		return D4ClaimResult{}, errors.New("D4 ledger is unavailable")
	}
	if !request.Tuple.Valid() {
		return D4ClaimResult{}, &D4Error{Class: D4ErrorMalformed, Cause: errors.New("claim requires a valid tuple")}
	}
	if request.WorkerID == uuid.Nil {
		return D4ClaimResult{}, &D4Error{Class: D4ErrorMalformed, Cause: errors.New("claim worker identity is required")}
	}
	if !request.Approval.approved || request.Approval.kind != D4EventAdmit || !request.Approval.tuple.Equal(request.Tuple) {
		return D4ClaimResult{}, &D4Error{Class: D4ErrorNonOwner, Cause: errors.New("claim requires owner admission approval")}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.initLocked()
	key := makeD4LedgerKey(request.Tuple)
	record, ok := l.records[key]
	if !ok {
		return D4ClaimResult{}, &D4Error{Class: D4ErrorStale, Cause: errors.New("claim record does not exist")}
	}
	if record.State != D4Received {
		// A duplicate delivery converges on a safe existing record. It is not a
		// second winner and never authorizes a second physical call. A malformed
		// claimed record cannot silently manufacture a zero claim identity.
		if record.ClaimID == uuid.Nil {
			return D4ClaimResult{}, &D4Error{Class: D4ErrorCASConflict, Cause: errors.New("existing D4 record has no durable claim identity")}
		}
		return D4ClaimResult{Record: cloneD4Record(record), Won: false, ClaimID: record.ClaimID}, nil
	}
	record.State = D4Admitted
	record.ClaimID = request.WorkerID
	record.UpdatedAt = time.Now().UTC()
	l.records[key] = record
	return D4ClaimResult{Record: cloneD4Record(record), Won: true, ClaimID: request.WorkerID}, nil
}

func (l *InMemoryD4Ledger) Transition(ctx context.Context, request D4TransitionRequest) (D4Record, error) {
	if err := contextErr(ctx); err != nil {
		return D4Record{}, err
	}
	if l == nil {
		return D4Record{}, errors.New("D4 ledger is unavailable")
	}
	if !request.Tuple.Valid() || !validD4State(request.Expected) {
		return D4Record{}, &D4Error{Class: D4ErrorMalformed, Cause: errors.New("transition binding is incomplete")}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.initLocked()
	key := makeD4LedgerKey(request.Tuple)
	record, ok := l.records[key]
	if !ok {
		return D4Record{}, &D4Error{Class: D4ErrorStale, Cause: errors.New("transition record does not exist")}
	}
	if !record.Tuple.Equal(request.Event.Tuple) {
		return D4Record{}, tupleMismatchError(record.Tuple, request.Event.Tuple)
	}
	if request.Tuple.Equal(request.Event.Tuple) == false {
		return D4Record{}, tupleMismatchError(record.Tuple, request.Event.Tuple)
	}
	if record.State != request.Expected {
		return D4Record{}, &D4Error{Class: D4ErrorCASConflict, Cause: errors.New("expected state no longer matches")}
	}
	if request.ClaimID == uuid.Nil {
		preClaim := record.ClaimID == uuid.Nil && ((record.State == D4Received && (request.Event.Kind == D4EventTerminal || request.Event.Kind == D4EventRequireRecovery)) || (record.State == D4RecoveryRequired && request.Event.Kind == D4EventResolveRecovery))
		if !preClaim {
			return D4Record{}, &D4Error{Class: D4ErrorCASConflict, Cause: errors.New("claim is required for this progression")}
		}
	} else if record.ClaimID == uuid.Nil || request.ClaimID != record.ClaimID {
		return D4Record{}, &D4Error{Class: D4ErrorCASConflict, Cause: errors.New("worker claim is no longer current")}
	}
	if requiresD4Claim(record.State) && record.ClaimID == uuid.Nil {
		return D4Record{}, &D4Error{Class: D4ErrorCASConflict, Cause: errors.New("post-claim state has no durable claim")}
	}
	if err := validateD4EventForRecord(record, request.Event, request.Revalidator); err != nil {
		return D4Record{}, err
	}
	record.State = d4TransitionTable[record.State][request.Event.Kind]
	if request.Event.Result != nil {
		result := *request.Event.Result
		record.Result = &result
	}
	if request.Event.Recovery != "" {
		record.Recovery = request.Event.Recovery
	}
	record.UpdatedAt = time.Now().UTC()
	l.records[key] = record
	return cloneD4Record(record), nil
}

func requiresD4Claim(state D4State) bool {
	switch state {
	case D4Admitted, D4Executing, D4ResultRecorded, D4ContinuationPending, D4ContinuationConsumed, D4WaitingForMapping:
		return true
	default:
		return false
	}
}

func validateD4EventForRecord(record D4Record, event D4OwnerEvent, revalidator D4OwnerRevalidator) error {
	if record.State == D4Terminal {
		return &D4Error{Class: D4ErrorTerminalImmutable, Cause: errors.New("terminal record is immutable")}
	}
	if !event.Tuple.Valid() {
		return &D4Error{Class: D4ErrorMalformed, Cause: errors.New("event tuple is invalid")}
	}
	if !event.Approval.approved || !event.Approval.tuple.Equal(event.Tuple) || event.Approval.kind != event.Kind {
		return &D4Error{Class: D4ErrorForged, Cause: errors.New("event lacks matching owner approval")}
	}
	next, ok := d4TransitionTable[record.State][event.Kind]
	if !ok {
		return &D4Error{Class: D4ErrorIllegalTransition, Cause: errors.New("event is not legal from expected state")}
	}
	if event.Kind == D4EventResumeMapping {
		if revalidator == nil {
			return &D4Error{Class: D4ErrorNonOwner, Cause: errors.New("mapping resumption requires owner revalidation")}
		}
		if err := revalidator.RevalidateD4(context.Background(), event.Tuple); err != nil {
			return &D4Error{Class: D4ErrorStale, Cause: err}
		}
	}
	if (event.Kind == D4EventRequestContinuation || event.Kind == D4EventConsumeContinuation) && (record.Result == nil || record.Result.Disposition != D4Success || record.Result.CommitStatus != D4CommitCommitted || record.Result.PostVerificationStatus != D4PostVerified || record.Result.Certainty != D4Known || record.Result.Unknown || record.Result.RecoveryRequired) {
		return &D4Error{Class: D4ErrorNonOwner, Cause: errors.New("continuation requires a known successful D3 result")}
	}
	if (event.Kind == D4EventRequestContinuation || event.Kind == D4EventConsumeContinuation) && !event.continuationProof {
		return &D4Error{Class: D4ErrorNonOwner, Cause: errors.New("private D3 continuation proof is required")}
	}
	if event.Kind == D4EventRecordResult || event.Kind == D4EventTerminal || event.Kind == D4EventResolveRecovery {
		if event.Result == nil {
			return &D4Error{Class: D4ErrorMalformed, Cause: errors.New("result is required")}
		}
		if err := event.Result.ValidateFor(event.Tuple); err != nil {
			return err
		}
	}
	if !validD4RecoveryClass(event.Recovery) {
		return &D4Error{Class: D4ErrorMalformed, Cause: errors.New("recovery class is invalid")}
	}
	if (event.Kind == D4EventRequireRecovery || event.Kind == D4EventResolveRecovery) && event.Recovery == "" {
		return &D4Error{Class: D4ErrorMalformed, Cause: errors.New("recovery class is required")}
	}
	_ = next
	return nil
}

func (l *InMemoryD4Ledger) RecordSafeResult(ctx context.Context, tuple D4OwnerTuple, expected D4State, claimID uuid.UUID, result D4SafeResult) (D4Record, error) {
	approval, err := NewD4OwnerApproval(tuple, D4EventRecordResult, time.Now().UTC())
	if err != nil {
		return D4Record{}, err
	}
	return l.Transition(ctx, D4TransitionRequest{Tuple: tuple, Expected: expected, ClaimID: claimID, Event: D4OwnerEvent{Kind: D4EventRecordResult, Tuple: tuple, Approval: approval, Result: &result}})
}

func (l *InMemoryD4Ledger) MarkRecovery(ctx context.Context, tuple D4OwnerTuple, expected D4State, claimID uuid.UUID, recovery D4RecoveryClass) (D4Record, error) {
	approval, err := NewD4OwnerApproval(tuple, D4EventRequireRecovery, time.Now().UTC())
	if err != nil {
		return D4Record{}, err
	}
	return l.Transition(ctx, D4TransitionRequest{Tuple: tuple, Expected: expected, ClaimID: claimID, Event: D4OwnerEvent{Kind: D4EventRequireRecovery, Tuple: tuple, Approval: approval, Recovery: recovery}})
}

func (l *InMemoryD4Ledger) Get(ctx context.Context, tuple D4OwnerTuple) (D4Record, error) {
	if err := contextErr(ctx); err != nil {
		return D4Record{}, err
	}
	if l == nil {
		return D4Record{}, errors.New("D4 ledger is unavailable")
	}
	if !tuple.Valid() {
		return D4Record{}, &D4Error{Class: D4ErrorMalformed, Cause: errors.New("get requires a valid tuple")}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.initLocked()
	record, ok := l.records[makeD4LedgerKey(tuple)]
	if !ok {
		return D4Record{}, &D4Error{Class: D4ErrorStale, Cause: errors.New("ledger record does not exist")}
	}
	return cloneD4Record(record), nil
}

func (l *InMemoryD4Ledger) ListRecovery(ctx context.Context) ([]D4Record, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if l == nil {
		return nil, errors.New("D4 ledger is unavailable")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.initLocked()
	out := make([]D4Record, 0)
	for _, record := range l.records {
		if record.State == D4RecoveryRequired {
			out = append(out, cloneD4Record(record))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tuple.OperationID().String() < out[j].Tuple.OperationID().String() })
	return out, nil
}

func (l *InMemoryD4Ledger) initLocked() {
	if l.records == nil {
		l.records = make(map[d4LedgerKey]D4Record)
	}
	if l.invocations == nil {
		l.invocations = make(map[d4InvocationKey]d4LedgerKey)
	}
}
func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
