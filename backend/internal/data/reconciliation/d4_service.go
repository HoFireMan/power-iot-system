package reconciliation

import (
	"context"
	"crypto/subtle"
	"errors"
	"time"

	"github.com/google/uuid"
)

// D4OwnerEventAuthorizer is the trusted owner seam for progression events.
// D4 never manufactures authority from a caller key, timer, or ledger state.
type D4OwnerEventAuthorizer interface {
	ApproveD4(context.Context, D4OwnerTuple, D4EventKind) (D4OwnerApproval, error)
}

// D4OwnerEventAuthorizerFunc adapts an owner implementation without exposing
// any physical authority to the orchestration service.
type D4OwnerEventAuthorizerFunc func(context.Context, D4OwnerTuple, D4EventKind) (D4OwnerApproval, error)

func (f D4OwnerEventAuthorizerFunc) ApproveD4(ctx context.Context, tuple D4OwnerTuple, kind D4EventKind) (D4OwnerApproval, error) {
	if f == nil {
		return D4OwnerApproval{}, errors.New("D4 owner event authorizer is unavailable")
	}
	return f(ctx, tuple, kind)
}

// D4CompositeResult is the only result crossing the D3/D4 composition seam.
// continuation is deliberately private and can only be consumed by this
// trusted service call; it is never copied into a ledger, event, or bundle.
type D4CompositeResult struct {
	Safe         D4SafeResult
	Correlation  *D4SafeCorrelation
	MappingWait  bool
	continuation func() error
}

func (r D4CompositeResult) HasContinuation() bool { return r.continuation != nil }

// NewD5Bundle is the safe versioned handoff constructor. It refuses to build a
// D5 bundle when the PR1 correlation seam was not supplied.
func (r D4CompositeResult) NewD5Bundle(record D4Record) (D4ToD5Bundle, error) {
	if r.Correlation == nil {
		return D4ToD5Bundle{}, errors.New("complete D4-to-D5 safe correlation is unavailable")
	}
	return NewD4ToD5BundleV2(record, *r.Correlation)
}

// D4D3Composite is a single owner-mediated physical invocation. Implementors
// must return semantic facts only; D4 does not coordinate D3 phases.
type D4D3Composite interface {
	InvokeD4(context.Context, D4OwnerTuple) (D4CompositeResult, error)
}

// D4Service composes explicit owner events, logical CAS state, and one D3
// composite call. It has no retry loop and never treats an ambiguous outcome
// as permission to invoke D3 again.
type D4Service struct {
	Ledger      D4Ledger
	Authorizer  D4OwnerEventAuthorizer
	Revalidator D4OwnerRevalidator
	Invoker     D4D3Composite
	Clock       func() time.Time
}

func NewD4Service(ledger D4Ledger, authorizer D4OwnerEventAuthorizer, invoker D4D3Composite) *D4Service {
	return &D4Service{Ledger: ledger, Authorizer: authorizer, Invoker: invoker, Clock: func() time.Time { return time.Now().UTC() }}
}

type D4ProcessResult struct {
	Record      D4Record
	Won         bool
	CalledD3    bool
	Correlation *D4SafeCorrelation
}

func (r D4ProcessResult) NewD5Bundle() (D4ToD5Bundle, error) {
	if r.Correlation == nil {
		return D4ToD5Bundle{}, errors.New("D4 process result has no safe D5 correlation")
	}
	return NewD4ToD5BundleV2(r.Record, *r.Correlation)
}

// Process accepts one explicit owner-approved admission event. Duplicate
// delivery returns the existing safe record and performs no physical call.
func (s *D4Service) Process(ctx context.Context, event D4OwnerEvent) (D4ProcessResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || s.Ledger == nil || s.Authorizer == nil || s.Invoker == nil {
		return D4ProcessResult{}, errors.New("D4 service dependencies are incomplete")
	}
	if event.Kind != D4EventAdmit || !event.Tuple.Valid() || !event.Approval.approved || !event.Approval.tuple.Equal(event.Tuple) || event.Approval.kind != D4EventAdmit {
		return D4ProcessResult{}, &D4Error{Class: D4ErrorForged, Cause: errors.New("D4 requires an explicit owner-approved admission event")}
	}
	// The caller's envelope is only a correlation carrier. The owner seam is
	// invoked again and its fresh approval is the authority used for Receive and
	// Claim; a caller-created approval cannot admit work by itself.
	ownerApproval, err := s.approve(ctx, event.Tuple, D4EventAdmit)
	if err != nil {
		return D4ProcessResult{}, err
	}
	now := time.Now().UTC()
	if s.Clock != nil {
		now = s.Clock().UTC()
	}
	record, err := s.Ledger.Receive(ctx, D4ReceiveRequest{Tuple: event.Tuple, ReceivedAt: now})
	if err != nil {
		return D4ProcessResult{}, err
	}
	if record.State != D4Received {
		return D4ProcessResult{Record: record}, nil
	}
	worker := uuid.New()
	claim, err := s.Ledger.Claim(ctx, D4ClaimRequest{Tuple: event.Tuple, Approval: ownerApproval, WorkerID: worker})
	if err != nil {
		return D4ProcessResult{Record: record}, err
	}
	if !claim.Won {
		return D4ProcessResult{Record: claim.Record}, nil
	}
	result := D4ProcessResult{Record: claim.Record, Won: true}
	claimID := claim.ClaimID
	if claimID == uuid.Nil {
		return result, &D4Error{Class: D4ErrorCASConflict, Cause: errors.New("ledger did not return durable claim ownership")}
	}
	return s.processClaimed(ctx, event.Tuple, claimID, result)
}

// ResumeMapping is the only mapping-wait continuation. It needs a fresh
// owner-approved event and revalidation; a timer or source change cannot call
// D3 through this method.
func (s *D4Service) ResumeMapping(ctx context.Context, event D4OwnerEvent) (D4ProcessResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || s.Ledger == nil || s.Authorizer == nil || s.Invoker == nil {
		return D4ProcessResult{}, errors.New("D4 service dependencies are incomplete")
	}
	if event.Kind != D4EventResumeMapping || !event.Tuple.Valid() || !event.Approval.approved || !event.Approval.tuple.Equal(event.Tuple) || event.Approval.kind != D4EventResumeMapping {
		return D4ProcessResult{}, &D4Error{Class: D4ErrorForged, Cause: errors.New("mapping resumption requires an owner-approved event")}
	}
	ownerApproval, err := s.approve(ctx, event.Tuple, D4EventResumeMapping)
	if err != nil {
		return D4ProcessResult{}, err
	}
	event.Approval = ownerApproval
	record, err := s.Ledger.Get(ctx, event.Tuple)
	if err != nil {
		return D4ProcessResult{}, err
	}
	if record.State != D4WaitingForMapping {
		return D4ProcessResult{Record: record}, &D4Error{Class: D4ErrorIllegalTransition, Cause: errors.New("record is not waiting for mapping")}
	}
	if _, err := s.Ledger.Transition(ctx, D4TransitionRequest{Tuple: event.Tuple, Expected: D4WaitingForMapping, ClaimID: record.ClaimID, Revalidator: s, Event: event}); err != nil {
		return D4ProcessResult{Record: record}, err
	}
	return s.processClaimed(ctx, event.Tuple, record.ClaimID, D4ProcessResult{Record: record, Won: true})
}

// FinalizePreClaim records an owner-authorized rejection or recovery outcome
// before any worker claim exists. It never invokes D3 and never synthesizes a
// scheduling claim merely to satisfy persistence constraints.
func (s *D4Service) FinalizePreClaim(ctx context.Context, event D4OwnerEvent) (D4ProcessResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || s.Ledger == nil || s.Authorizer == nil {
		return D4ProcessResult{}, errors.New("D4 pre-claim finalizer dependencies are incomplete")
	}
	if (event.Kind != D4EventTerminal && event.Kind != D4EventRequireRecovery) || !event.Tuple.Valid() || !event.Approval.approved || !event.Approval.tuple.Equal(event.Tuple) || event.Approval.kind != event.Kind {
		return D4ProcessResult{}, &D4Error{Class: D4ErrorForged, Cause: errors.New("pre-claim disposition requires an owner-approved terminal or recovery event")}
	}
	approval, err := s.approve(ctx, event.Tuple, event.Kind)
	if err != nil {
		return D4ProcessResult{}, err
	}
	now := time.Now().UTC()
	if s.Clock != nil {
		now = s.Clock().UTC()
	}
	record, err := s.Ledger.Receive(ctx, D4ReceiveRequest{Tuple: event.Tuple, ReceivedAt: now})
	if err != nil {
		return D4ProcessResult{}, err
	}
	if record.State != D4Received {
		return D4ProcessResult{Record: record}, nil
	}
	event.Approval = approval
	record, err = s.Ledger.Transition(ctx, D4TransitionRequest{Tuple: event.Tuple, Expected: D4Received, ClaimID: uuid.Nil, Revalidator: s.Revalidator, Event: event})
	return D4ProcessResult{Record: record}, err
}

func (s *D4Service) processClaimed(ctx context.Context, tuple D4OwnerTuple, claimID uuid.UUID, result D4ProcessResult) (D4ProcessResult, error) {
	if err := s.transition(ctx, tuple, claimID, D4Admitted, D4EventBeginExecution, nil, ""); err != nil {
		return result, err
	}
	result.CalledD3 = true
	composite, invokeErr := s.Invoker.InvokeD4(ctx, tuple)
	if invokeErr != nil && composite.Safe.OperationID == "" {
		_ = s.markRecovery(ctx, tuple, claimID, D4Executing, D4RecoveryUnknown)
		result.Record, _ = s.Ledger.Get(ctx, tuple)
		return result, invokeErr
	}
	if err := composite.Safe.ValidateFor(tuple); err != nil {
		_ = s.markRecovery(ctx, tuple, claimID, D4Executing, D4RecoveryUnknown)
		result.Record, _ = s.Ledger.Get(ctx, tuple)
		return result, err
	}
	result.Correlation = composite.Correlation
	if err := s.transition(ctx, tuple, claimID, D4Executing, D4EventRecordResult, &composite.Safe, ""); err != nil {
		return result, err
	}
	if composite.MappingWait {
		if err := s.transition(ctx, tuple, claimID, D4ResultRecorded, D4EventWaitForMapping, nil, ""); err != nil {
			return result, err
		}
		result.Record, _ = s.Ledger.Get(ctx, tuple)
		return result, invokeErr
	}
	if composite.HasContinuation() {
		if composite.Safe.Disposition != D4Success || composite.Safe.CommitStatus != D4CommitCommitted || composite.Safe.PostVerificationStatus != D4PostVerified || composite.Safe.Certainty != D4Known || composite.Safe.RecoveryRequired || composite.Safe.Unknown {
			_ = s.markRecovery(ctx, tuple, claimID, D4ResultRecorded, D4RecoveryUnknown)
			result.Record, _ = s.Ledger.Get(ctx, tuple)
			return result, errors.New("D3 continuation accompanied a non-success safe result")
		}
		if err := s.transition(ctx, tuple, claimID, D4ResultRecorded, D4EventRequestContinuation, nil, ""); err != nil {
			return result, err
		}
		if err := composite.continuation(); err != nil {
			_ = s.markRecovery(ctx, tuple, claimID, D4ContinuationPending, D4RecoveryUnknown)
			result.Record, _ = s.Ledger.Get(ctx, tuple)
			return result, err
		}
		if err := s.transition(ctx, tuple, claimID, D4ContinuationPending, D4EventConsumeContinuation, nil, ""); err != nil {
			return result, err
		}
	}
	if composite.Safe.RecoveryRequired || composite.Safe.Unknown {
		_ = s.markRecovery(ctx, tuple, claimID, D4ContinuationConsumedOrResult(composite.HasContinuation()), D4RecoveryUnknown)
	} else {
		state := D4ResultRecorded
		if composite.HasContinuation() {
			state = D4ContinuationConsumed
		}
		if err := s.transition(ctx, tuple, claimID, state, D4EventTerminal, &composite.Safe, ""); err != nil {
			return result, err
		}
	}
	result.Record, _ = s.Ledger.Get(ctx, tuple)
	return result, invokeErr
}

func D4ContinuationConsumedOrResult(consumed bool) D4State {
	if consumed {
		return D4ContinuationConsumed
	}
	return D4ResultRecorded
}

func (s *D4Service) approve(ctx context.Context, tuple D4OwnerTuple, kind D4EventKind) (D4OwnerApproval, error) {
	approval, err := s.Authorizer.ApproveD4(ctx, tuple, kind)
	if err != nil {
		return D4OwnerApproval{}, err
	}
	if !approval.approved || approval.kind != kind || !approval.tuple.Equal(tuple) {
		return D4OwnerApproval{}, &D4Error{Class: D4ErrorForged, Cause: errors.New("owner returned a mismatched D4 approval")}
	}
	return approval, nil
}

func (s *D4Service) transition(ctx context.Context, tuple D4OwnerTuple, claimID uuid.UUID, expected D4State, kind D4EventKind, result *D4SafeResult, recovery D4RecoveryClass) error {
	approval, err := s.approve(ctx, tuple, kind)
	if err != nil {
		return err
	}
	event := D4OwnerEvent{Kind: kind, Tuple: tuple, Approval: approval, Result: result, Recovery: recovery}
	if kind == D4EventRequestContinuation || kind == D4EventConsumeContinuation {
		event.continuationProof = true
	}
	_, err = s.Ledger.Transition(ctx, D4TransitionRequest{Tuple: tuple, Expected: expected, ClaimID: claimID, Revalidator: s.Revalidator, Event: event})
	return err
}

func (s *D4Service) markRecovery(ctx context.Context, tuple D4OwnerTuple, claimID uuid.UUID, expected D4State, recovery D4RecoveryClass) error {
	return s.transition(ctx, tuple, claimID, expected, D4EventRequireRecovery, nil, recovery)
}

// RevalidateD4 makes recovery/mapping hooks explicit. A service does not have
// an independent authority source, so owners should wrap this service or use
// the separate recovery resolver below for real revalidation.
func (s *D4Service) RevalidateD4(ctx context.Context, tuple D4OwnerTuple) error {
	if s == nil || s.Revalidator == nil {
		return errors.New("D4 owner revalidation is unavailable")
	}
	return s.Revalidator.RevalidateD4(ctx, tuple)
}

// ProtectedExecutorD4Composite adapts the existing D3-owned executor. The
// executor remains responsible for the fence, D2 frozen-point work, D010,
// cleanup, and physical UNKNOWN; only its safe report reaches D4.
// D3ProtectedReportInvoker keeps physical configuration on the D3 owner
// side. D4 receives only a callback and the resulting safe report.
type D3ProtectedReportInvoker func(context.Context) (ExecutionReport, error)

type ProtectedExecutorD4Composite struct {
	Executor     *ProtectedExecutor
	InvokeReport D3ProtectedReportInvoker
}

func (a ProtectedExecutorD4Composite) InvokeD4(ctx context.Context, tuple D4OwnerTuple) (D4CompositeResult, error) {
	if a.Executor == nil || a.InvokeReport == nil {
		return D4CompositeResult{}, errors.New("D3 owner invocation is required")
	}
	target := tuple.TargetFingerprint()
	if a.Executor.Lease == nil || a.Executor.Lease.OperationID != tuple.OperationID() || a.Executor.Lease.AttemptID != tuple.AttemptID() || a.Executor.Lease.Generation != int64(tuple.Generation()) || len(a.Executor.Lease.TargetFingerprint) != 32 || subtle.ConstantTimeCompare(a.Executor.Lease.TargetFingerprint, target[:]) != 1 {
		return D4CompositeResult{}, errors.New("D3 invocation tuple does not match owner lease")
	}
	report, err := a.InvokeReport(ctx)
	if report.OperationID != uuid.Nil && report.OperationID != tuple.OperationID() {
		return D4CompositeResult{}, errors.New("D3 report operation does not match owner tuple")
	}
	projection, projectionErr := NewPR1ToD4Result(report), error(nil)
	if report.Outcome == ExecutionCommittedAndVerified && a.Executor.D1 != nil {
		projection, projectionErr = a.Executor.PR1ToD4Result(report, true)
	}
	correlation := D4SafeCorrelation{
		FactsDigest:           projection.Semantic.FactsDigest,
		ProofDigest:           projection.Semantic.ProofDigest,
		PredicateIdentity:     projection.Semantic.PredicateIdentity,
		PredicateVersion:      projection.Semantic.PredicateVersion,
		ProvenanceDigest:      projection.Semantic.RecoveryEvidence.CorrelationDigest,
		PostCommitFactsDigest: projection.Semantic.PostCommitFactsDigest,
		PostCommitFactsAsOf:   projection.Semantic.PostCommitFactsAsOf,
	}
	result := D4CompositeResult{Safe: d4SafeResultFromReport(tuple, report), Correlation: &correlation}
	if projectionErr != nil {
		result.Safe.Unknown = true
		result.Safe.Certainty = D4Unknown
		result.Safe.RecoveryRequired = true
		result.Safe.ReplayDisposition = D4ReplayForbidden
		return result, projectionErr
	}
	if projection.HasD3Continuation() {
		// This closure is the immediate same-owner handoff. It is not a D4
		// record and cannot be serialized or reconstructed after process loss.
		expected := D010HandoffContext{OperationID: tuple.OperationID(), AttemptID: tuple.AttemptID(), Generation: int64(tuple.Generation())}
		copyTarget := tuple.TargetFingerprint()
		expected.TargetFingerprint = copyTarget
		result.continuation = func() error { return projection.ContinueD3Protected(expected) }
	}
	return result, err
}

func d4SafeResultFromReport(tuple D4OwnerTuple, report ExecutionReport) D4SafeResult {
	result := D4SafeResult{OperationID: tuple.OperationID().String(), AttemptID: tuple.AttemptID(), TargetFingerprint: tuple.TargetFingerprintHex(), Generation: tuple.Generation(), CommitStatus: D4CommitNotCommitted, PostVerificationStatus: D4PostNotVerified, CleanupStatus: D4CleanupConfirmed, Certainty: D4Known, Disposition: D4NonSuccess, ReplayDisposition: D4RetryOwnerOnly}
	switch report.Outcome {
	case ExecutionCommittedAndVerified:
		result.CommitStatus, result.PostVerificationStatus, result.Disposition = D4CommitCommitted, D4PostVerified, D4Success
	case ExecutionCommittedPostVerifyFailed:
		result.CommitStatus, result.PostVerificationStatus = D4CommitCommitted, D4PostFailed
	case ExecutionCommitOutcomeUnknown:
		result.CommitStatus, result.Certainty, result.Unknown = D4CommitUnknown, D4Unknown, true
	}
	if report.CleanupError != "" {
		result.CleanupStatus, result.Unknown = D4CleanupUncertain, true
		result.Certainty = D4Unknown
	}
	result.RecoveryRequired = result.Unknown || result.PostVerificationStatus == D4PostFailed
	if result.RecoveryRequired {
		result.ReplayDisposition = D4ReplayForbidden
	}
	return result
}
