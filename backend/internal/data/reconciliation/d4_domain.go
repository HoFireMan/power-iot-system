package reconciliation

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// D4State is the closed-world durable D4 state machine. Do not add states
// without changing the frozen D4 contract and its transition matrix.
type D4State string

const (
	D4Received             D4State = "RECEIVED"
	D4Admitted             D4State = "ADMITTED"
	D4Executing            D4State = "EXECUTING"
	D4ResultRecorded       D4State = "RESULT_RECORDED"
	D4ContinuationPending  D4State = "CONTINUATION_PENDING"
	D4ContinuationConsumed D4State = "CONTINUATION_CONSUMED"
	D4WaitingForMapping    D4State = "WAITING_FOR_MAPPING"
	D4Terminal             D4State = "TERMINAL"
	D4RecoveryRequired     D4State = "RECOVERY_REQUIRED"
)

var d4States = [...]D4State{
	D4Received, D4Admitted, D4Executing, D4ResultRecorded,
	D4ContinuationPending, D4ContinuationConsumed, D4WaitingForMapping,
	D4Terminal, D4RecoveryRequired,
}

// D4States returns the exact frozen state vocabulary.
func D4States() []D4State { return append([]D4State(nil), d4States[:]...) }

func validD4State(state D4State) bool {
	for _, candidate := range d4States {
		if state == candidate {
			return true
		}
	}
	return false
}

// D4OwnerTuple is the immutable owner binding for one operation attempt. The
// fields are private by design: callers can retain or compare this value, but
// cannot alter one component in place or use a partial tuple as authority.
type D4OwnerTuple struct {
	operationID       uuid.UUID
	attemptID         uuid.UUID
	targetFingerprint [32]byte
	generation        uint64
}

// OwnerTuple is the short public name used by the ledger contract.
type OwnerTuple = D4OwnerTuple

func NewD4OwnerTuple(operationID, attemptID uuid.UUID, targetFingerprint []byte, generation uint64) (D4OwnerTuple, error) {
	if operationID == uuid.Nil || attemptID == uuid.Nil {
		return D4OwnerTuple{}, &D4Error{Class: D4ErrorMalformed, Cause: errors.New("operation and attempt identities are required")}
	}
	if len(targetFingerprint) != 32 {
		return D4OwnerTuple{}, &D4Error{Class: D4ErrorMalformed, Cause: errors.New("target fingerprint must be 32 bytes")}
	}
	if generation == 0 {
		return D4OwnerTuple{}, &D4Error{Class: D4ErrorMalformed, Cause: errors.New("generation must be positive")}
	}
	var target [32]byte
	copy(target[:], targetFingerprint)
	return D4OwnerTuple{operationID: operationID, attemptID: attemptID, targetFingerprint: target, generation: generation}, nil
}

func (t D4OwnerTuple) Valid() bool {
	return t.operationID != uuid.Nil && t.attemptID != uuid.Nil && t.generation > 0
}
func (t D4OwnerTuple) OperationID() uuid.UUID      { return t.operationID }
func (t D4OwnerTuple) AttemptID() uuid.UUID        { return t.attemptID }
func (t D4OwnerTuple) TargetFingerprint() [32]byte { return t.targetFingerprint }
func (t D4OwnerTuple) Generation() uint64          { return t.generation }
func (t D4OwnerTuple) TargetFingerprintHex() string {
	return hex.EncodeToString(t.targetFingerprint[:])
}

func (t D4OwnerTuple) Equal(other D4OwnerTuple) bool {
	return t.operationID == other.operationID && t.attemptID == other.attemptID &&
		t.generation == other.generation && subtle.ConstantTimeCompare(t.targetFingerprint[:], other.targetFingerprint[:]) == 1
}

func (t D4OwnerTuple) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		OperationID       uuid.UUID `json:"operation_id"`
		AttemptID         uuid.UUID `json:"attempt_id"`
		TargetFingerprint string    `json:"target_fingerprint_digest"`
		Generation        uint64    `json:"generation"`
	}{t.operationID, t.attemptID, t.TargetFingerprintHex(), t.generation})
}

func (t *D4OwnerTuple) UnmarshalJSON(encoded []byte) error {
	var wire struct {
		OperationID       uuid.UUID `json:"operation_id"`
		AttemptID         uuid.UUID `json:"attempt_id"`
		TargetFingerprint string    `json:"target_fingerprint_digest"`
		Generation        uint64    `json:"generation"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		return err
	}
	decoded, err := hex.DecodeString(wire.TargetFingerprint)
	if err != nil {
		return fmt.Errorf("decode owner tuple target fingerprint: %w", err)
	}
	parsed, err := NewD4OwnerTuple(wire.OperationID, wire.AttemptID, decoded, wire.Generation)
	if err != nil {
		return err
	}
	*t = parsed
	return nil
}

// D4OwnerApproval is created only at the owner-approved event seam. Its tuple
// is private, so a caller cannot forge an approval by editing a binding field.
type D4OwnerApproval struct {
	tuple    D4OwnerTuple
	kind     D4EventKind
	issuedAt time.Time
	approved bool
}

func NewD4OwnerApproval(tuple D4OwnerTuple, kind D4EventKind, issuedAt time.Time) (D4OwnerApproval, error) {
	if !tuple.Valid() {
		return D4OwnerApproval{}, &D4Error{Class: D4ErrorMalformed, Cause: errors.New("owner approval requires a valid tuple")}
	}
	if !validD4Event(kind) {
		return D4OwnerApproval{}, &D4Error{Class: D4ErrorMalformed, Cause: errors.New("owner approval event is invalid")}
	}
	if issuedAt.IsZero() {
		return D4OwnerApproval{}, &D4Error{Class: D4ErrorMalformed, Cause: errors.New("owner approval time is required")}
	}
	return D4OwnerApproval{tuple: tuple, kind: kind, issuedAt: issuedAt.UTC(), approved: true}, nil
}

// D4OwnerRevalidator is intentionally narrow. It is required when resuming a
// mapping wait; a state observation, timer, source change, or caller key is not
// a substitute for this owner check.
type D4OwnerRevalidator interface {
	RevalidateD4(context.Context, D4OwnerTuple) error
}

// D4EventKind identifies an owner-approved semantic event, not a persistence
// operation. The mapping-resume event is the only event that can leave a wait.
type D4EventKind string

const (
	D4EventAdmit               D4EventKind = "ADMIT"
	D4EventBeginExecution      D4EventKind = "BEGIN_EXECUTION"
	D4EventRecordResult        D4EventKind = "RECORD_RESULT"
	D4EventRequestContinuation D4EventKind = "REQUEST_CONTINUATION"
	D4EventConsumeContinuation D4EventKind = "CONSUME_CONTINUATION"
	D4EventWaitForMapping      D4EventKind = "WAIT_FOR_MAPPING"
	D4EventResumeMapping       D4EventKind = "RESUME_MAPPING"
	D4EventTerminal            D4EventKind = "TERMINAL"
	D4EventRequireRecovery     D4EventKind = "REQUIRE_RECOVERY"
	D4EventResolveRecovery     D4EventKind = "RESOLVE_RECOVERY"
)

// D4OwnerEvent carries no physical authority or source evidence.
type D4OwnerEvent struct {
	Kind     D4EventKind
	Tuple    D4OwnerTuple
	Approval D4OwnerApproval
	Result   *D4SafeResult
	Recovery D4RecoveryClass
	// continuationProof is private: only the trusted D3 composition service
	// can authorize the two immediate continuation transitions.
	continuationProof bool
}

func validD4Event(kind D4EventKind) bool {
	switch kind {
	case D4EventAdmit, D4EventBeginExecution, D4EventRecordResult,
		D4EventRequestContinuation, D4EventConsumeContinuation,
		D4EventWaitForMapping, D4EventResumeMapping, D4EventTerminal,
		D4EventRequireRecovery, D4EventResolveRecovery:
		return true
	default:
		return false
	}
}

// D4ErrorClass keeps malformed, stale, forged, and binding failures distinct
// for safe recovery and review without exposing owner or physical details.
type D4ErrorClass string

const (
	D4ErrorMalformed         D4ErrorClass = "MALFORMED"
	D4ErrorStale             D4ErrorClass = "STALE"
	D4ErrorForged            D4ErrorClass = "FORGED"
	D4ErrorWrongOperation    D4ErrorClass = "WRONG_OPERATION"
	D4ErrorWrongAttempt      D4ErrorClass = "WRONG_ATTEMPT"
	D4ErrorWrongTarget       D4ErrorClass = "WRONG_TARGET"
	D4ErrorWrongGeneration   D4ErrorClass = "WRONG_GENERATION"
	D4ErrorNonOwner          D4ErrorClass = "NON_OWNER"
	D4ErrorIllegalTransition D4ErrorClass = "ILLEGAL_TRANSITION"
	D4ErrorTerminalImmutable D4ErrorClass = "TERMINAL_IMMUTABLE"
	D4ErrorCASConflict       D4ErrorClass = "CAS_CONFLICT"
)

type D4Error struct {
	Class D4ErrorClass
	Cause error
}

func (e *D4Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Cause == nil {
		return string(e.Class)
	}
	return fmt.Sprintf("D4 %s: %v", e.Class, e.Cause)
}
func (e *D4Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// Safe result dimensions deliberately contain semantic statuses only. No D1,
// D2, D3, D007, D010, bearer, transaction, session, or source evidence fits
// in this projection.
type D4CommitStatus string
type D4PostVerificationStatus string
type D4CleanupStatus string
type D4Certainty string
type D4Disposition string
type D4ReplayDisposition string
type D4RecoveryClass string

const (
	D4CommitNotCommitted D4CommitStatus           = "NOT_COMMITTED"
	D4CommitCommitted    D4CommitStatus           = "COMMITTED"
	D4CommitUnknown      D4CommitStatus           = "COMMIT_UNKNOWN"
	D4PostNotVerified    D4PostVerificationStatus = "NOT_VERIFIED"
	D4PostVerified       D4PostVerificationStatus = "VERIFIED"
	D4PostFailed         D4PostVerificationStatus = "FAILED"
	D4CleanupConfirmed   D4CleanupStatus          = "CONFIRMED"
	D4CleanupUncertain   D4CleanupStatus          = "UNCERTAIN"
	D4Known              D4Certainty              = "KNOWN"
	D4Unknown            D4Certainty              = "UNKNOWN"
	D4Success            D4Disposition            = "SUCCESS"
	D4NonSuccess         D4Disposition            = "NON_SUCCESS"
	D4ReplayForbidden    D4ReplayDisposition      = "NOT_REPLAYABLE"
	D4RetryOwnerOnly     D4ReplayDisposition      = "RETRY_ONLY_IF_OWNER_PERMITS"
	D4RecoveryUnknown    D4RecoveryClass          = "UNKNOWN_COMMIT_OR_CLEANUP"
	D4RecoveryPostVerify D4RecoveryClass          = "COMMITTED_POSTVERIFY_FAILED"
	D4RecoveryStale      D4RecoveryClass          = "STALE_OR_REVALIDATION_REQUIRED"
)

func validD4CommitStatus(value D4CommitStatus) bool {
	switch value {
	case D4CommitNotCommitted, D4CommitCommitted, D4CommitUnknown:
		return true
	}
	return false
}
func validD4PostStatus(value D4PostVerificationStatus) bool {
	switch value {
	case D4PostNotVerified, D4PostVerified, D4PostFailed:
		return true
	}
	return false
}
func validD4CleanupStatus(value D4CleanupStatus) bool {
	switch value {
	case D4CleanupConfirmed, D4CleanupUncertain:
		return true
	}
	return false
}
func validD4Certainty(value D4Certainty) bool {
	switch value {
	case D4Known, D4Unknown:
		return true
	}
	return false
}
func validD4Disposition(value D4Disposition) bool {
	switch value {
	case D4Success, D4NonSuccess:
		return true
	}
	return false
}
func validD4ReplayDisposition(value D4ReplayDisposition) bool {
	switch value {
	case D4ReplayForbidden, D4RetryOwnerOnly:
		return true
	}
	return false
}
func validD4RecoveryClass(value D4RecoveryClass) bool {
	switch value {
	case "", D4RecoveryUnknown, D4RecoveryPostVerify, D4RecoveryStale:
		return true
	}
	return false
}

// D4SafeResult is an allow-listed semantic projection. It is intentionally
// incapable of carrying a capability, source proof, or physical handle.
type D4SafeResult struct {
	OperationID            string                   `json:"operation_id"`
	AttemptID              uuid.UUID                `json:"attempt_id"`
	TargetFingerprint      string                   `json:"target_fingerprint_digest"`
	Generation             uint64                   `json:"generation"`
	Disposition            D4Disposition            `json:"disposition"`
	CommitStatus           D4CommitStatus           `json:"commit_status"`
	PostVerificationStatus D4PostVerificationStatus `json:"post_verification_status"`
	CleanupStatus          D4CleanupStatus          `json:"cleanup_status"`
	Certainty              D4Certainty              `json:"certainty"`
	Unknown                bool                     `json:"unknown"`
	RecoveryRequired       bool                     `json:"recovery_required"`
	RecoveryClass          D4RecoveryClass          `json:"recovery_class,omitempty"`
	ReplayDisposition      D4ReplayDisposition      `json:"replay_disposition"`
}

func (r D4SafeResult) ValidateFor(tuple D4OwnerTuple) error {
	if !tuple.Valid() || r.OperationID != tuple.OperationID().String() || r.AttemptID != tuple.AttemptID() || r.Generation != tuple.Generation() || r.TargetFingerprint != tuple.TargetFingerprintHex() {
		return &D4Error{Class: D4ErrorForged, Cause: errors.New("safe result does not match owner tuple")}
	}
	if !validD4CommitStatus(r.CommitStatus) || !validD4PostStatus(r.PostVerificationStatus) || !validD4CleanupStatus(r.CleanupStatus) || !validD4Certainty(r.Certainty) || !validD4Disposition(r.Disposition) || !validD4ReplayDisposition(r.ReplayDisposition) || !validD4RecoveryClass(r.RecoveryClass) {
		return &D4Error{Class: D4ErrorMalformed, Cause: errors.New("safe result contains an unknown semantic value")}
	}
	if r.Unknown != (r.CommitStatus == D4CommitUnknown || r.CleanupStatus == D4CleanupUncertain) || r.RecoveryRequired != (r.Unknown || r.PostVerificationStatus == D4PostFailed) {
		return &D4Error{Class: D4ErrorMalformed, Cause: errors.New("safe result dimensions are inconsistent")}
	}
	if r.Disposition == D4Success && (r.CommitStatus != D4CommitCommitted || r.PostVerificationStatus != D4PostVerified || r.CleanupStatus != D4CleanupConfirmed || r.Certainty != D4Known || r.Unknown || r.RecoveryRequired || r.RecoveryClass != "") {
		return &D4Error{Class: D4ErrorMalformed, Cause: errors.New("successful safe result is not fully committed, verified, and certain")}
	}
	if r.CommitStatus == D4CommitUnknown && r.Disposition == D4Success {
		return &D4Error{Class: D4ErrorMalformed, Cause: errors.New("unknown commit cannot be successful")}
	}
	if r.PostVerificationStatus == D4PostFailed && r.RecoveryClass != D4RecoveryPostVerify {
		return &D4Error{Class: D4ErrorMalformed, Cause: errors.New("post-verification failure requires its recovery class")}
	}
	if (r.CommitStatus == D4CommitUnknown || r.CleanupStatus == D4CleanupUncertain) && r.RecoveryClass != D4RecoveryUnknown {
		return &D4Error{Class: D4ErrorMalformed, Cause: errors.New("unknown commit or cleanup requires its recovery class")}
	}
	return nil
}

// D4Record is the logical safe ledger record. Identity is immutable for the
// lifetime of a record; Snapshot returns copies of mutable result pointers.
type D4Record struct {
	Tuple     D4OwnerTuple    `json:"tuple"`
	State     D4State         `json:"state"`
	ClaimID   uuid.UUID       `json:"claim_id,omitempty"`
	Result    *D4SafeResult   `json:"result,omitempty"`
	Recovery  D4RecoveryClass `json:"recovery_class,omitempty"`
	UpdatedAt time.Time       `json:"updated_at"`
}

func cloneD4Record(in D4Record) D4Record {
	out := in
	if in.Result != nil {
		result := *in.Result
		out.Result = &result
	}
	return out
}

// D4FSM validates state transitions independently of persistence. Every
// unspecified edge, including every edge out of TERMINAL, is rejected.
type D4FSM struct {
	mu     sync.Mutex
	record D4Record
}

func NewD4FSM(tuple D4OwnerTuple) (*D4FSM, error) {
	if !tuple.Valid() {
		return nil, &D4Error{Class: D4ErrorMalformed, Cause: errors.New("valid owner tuple is required")}
	}
	return &D4FSM{record: D4Record{Tuple: tuple, State: D4Received, UpdatedAt: time.Now().UTC()}}, nil
}
func (f *D4FSM) Snapshot() (D4Record, error) {
	if f == nil {
		return D4Record{}, errors.New("D4 FSM is unavailable")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return cloneD4Record(f.record), nil
}

var d4TransitionTable = map[D4State]map[D4EventKind]D4State{
	D4Received:             {D4EventAdmit: D4Admitted, D4EventTerminal: D4Terminal, D4EventRequireRecovery: D4RecoveryRequired},
	D4Admitted:             {D4EventBeginExecution: D4Executing, D4EventWaitForMapping: D4WaitingForMapping, D4EventTerminal: D4Terminal, D4EventRequireRecovery: D4RecoveryRequired},
	D4Executing:            {D4EventRecordResult: D4ResultRecorded, D4EventRequestContinuation: D4ContinuationPending, D4EventWaitForMapping: D4WaitingForMapping, D4EventTerminal: D4Terminal, D4EventRequireRecovery: D4RecoveryRequired},
	D4ResultRecorded:       {D4EventRequestContinuation: D4ContinuationPending, D4EventWaitForMapping: D4WaitingForMapping, D4EventTerminal: D4Terminal, D4EventRequireRecovery: D4RecoveryRequired},
	D4ContinuationPending:  {D4EventConsumeContinuation: D4ContinuationConsumed, D4EventTerminal: D4Terminal, D4EventRequireRecovery: D4RecoveryRequired},
	D4ContinuationConsumed: {D4EventTerminal: D4Terminal, D4EventRequireRecovery: D4RecoveryRequired},
	D4WaitingForMapping:    {D4EventResumeMapping: D4Admitted, D4EventTerminal: D4Terminal, D4EventRequireRecovery: D4RecoveryRequired},
	D4RecoveryRequired:     {D4EventResolveRecovery: D4Terminal},
	D4Terminal:             {},
}

func (f *D4FSM) Apply(ctx context.Context, event D4OwnerEvent, revalidator D4OwnerRevalidator) error {
	if f == nil {
		return &D4Error{Class: D4ErrorMalformed, Cause: errors.New("D4 FSM is unavailable")}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !validD4Event(event.Kind) {
		return &D4Error{Class: D4ErrorMalformed, Cause: errors.New("unknown D4 event")}
	}
	if !event.Tuple.Valid() {
		return &D4Error{Class: D4ErrorMalformed, Cause: errors.New("event tuple is invalid")}
	}
	if !event.Tuple.Equal(f.record.Tuple) {
		return tupleMismatchError(f.record.Tuple, event.Tuple)
	}
	if f.record.State == D4Terminal {
		return &D4Error{Class: D4ErrorTerminalImmutable, Cause: errors.New("terminal state cannot change")}
	}
	if !event.Approval.approved || !event.Approval.tuple.Equal(event.Tuple) || event.Approval.kind != event.Kind || event.Approval.issuedAt.IsZero() {
		return &D4Error{Class: D4ErrorForged, Cause: errors.New("event lacks matching owner approval")}
	}
	if event.Kind == D4EventResumeMapping {
		if revalidator == nil {
			return &D4Error{Class: D4ErrorNonOwner, Cause: errors.New("mapping resumption requires owner revalidation")}
		}
		if err := revalidator.RevalidateD4(ctx, event.Tuple); err != nil {
			return &D4Error{Class: D4ErrorStale, Cause: err}
		}
	}
	next, ok := d4TransitionTable[f.record.State][event.Kind]
	if !ok {
		return &D4Error{Class: D4ErrorIllegalTransition, Cause: fmt.Errorf("%s cannot accept %s", f.record.State, event.Kind)}
	}
	if (event.Kind == D4EventRequestContinuation || event.Kind == D4EventConsumeContinuation) && (f.record.Result == nil || f.record.Result.Disposition != D4Success || f.record.Result.CommitStatus != D4CommitCommitted || f.record.Result.PostVerificationStatus != D4PostVerified || f.record.Result.Certainty != D4Known || f.record.Result.Unknown || f.record.Result.RecoveryRequired) {
		return &D4Error{Class: D4ErrorNonOwner, Cause: errors.New("continuation requires a known successful D3 result")}
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
	if (event.Kind == D4EventRequestContinuation || event.Kind == D4EventConsumeContinuation) && !event.continuationProof {
		return &D4Error{Class: D4ErrorNonOwner, Cause: errors.New("private D3 continuation proof is required")}
	}
	f.record.State = next
	if event.Result != nil {
		copyResult := *event.Result
		f.record.Result = &copyResult
	}
	if event.Recovery != "" {
		f.record.Recovery = event.Recovery
	}
	f.record.UpdatedAt = time.Now().UTC()
	return nil
}

func tupleMismatchError(expected, got D4OwnerTuple) error {
	if expected.operationID != got.operationID {
		return &D4Error{Class: D4ErrorWrongOperation, Cause: errors.New("operation binding mismatch")}
	}
	if expected.attemptID != got.attemptID {
		return &D4Error{Class: D4ErrorWrongAttempt, Cause: errors.New("attempt binding mismatch")}
	}
	if subtle.ConstantTimeCompare(expected.targetFingerprint[:], got.targetFingerprint[:]) != 1 {
		return &D4Error{Class: D4ErrorWrongTarget, Cause: errors.New("target binding mismatch")}
	}
	return &D4Error{Class: D4ErrorWrongGeneration, Cause: errors.New("generation binding mismatch")}
}
