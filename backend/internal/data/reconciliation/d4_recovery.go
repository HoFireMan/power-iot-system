package reconciliation

import (
	"context"
	"errors"
	"fmt"
)

// D4RecoveryResolver is the owner-mediated resolution seam. It returns a safe
// semantic disposition; it cannot return D007, D010, a session, or a retry
// instruction.
type D4RecoveryResolver interface {
	ResolveD4(context.Context, D4Record) (D4SafeResult, D4RecoveryClass, error)
}

type D4RecoveryAction string

const (
	D4RecoveryProjectionOnly D4RecoveryAction = "PROJECTION_ONLY"
	D4RecoveryQueued         D4RecoveryAction = "QUEUED"
	D4RecoveryAwaitingEvent  D4RecoveryAction = "AWAITING_OWNER_EVENT"
)

type D4RecoveryClassification struct {
	Tuple  D4OwnerTuple
	State  D4State
	Action D4RecoveryAction
}

// D4RecoveryManager performs conservative restart classification. It never
// invokes D3 and never turns an expired claim into authority.
type D4RecoveryManager struct {
	Ledger     D4Ledger
	Authorizer D4OwnerEventAuthorizer
	Resolver   D4RecoveryResolver
}

func NewD4RecoveryManager(ledger D4Ledger, authorizer D4OwnerEventAuthorizer, resolver D4RecoveryResolver) *D4RecoveryManager {
	return &D4RecoveryManager{Ledger: ledger, Authorizer: authorizer, Resolver: resolver}
}

func (m *D4RecoveryManager) ClassifyStartup(ctx context.Context, records []D4Record) ([]D4RecoveryClassification, error) {
	if m == nil || m.Ledger == nil || m.Authorizer == nil {
		return nil, errors.New("D4 recovery dependencies are incomplete")
	}
	out := make([]D4RecoveryClassification, 0, len(records))
	for _, record := range records {
		if !validD4State(record.State) || !record.Tuple.Valid() || !validD4RecoveryClass(record.Recovery) {
			return nil, fmt.Errorf("invalid durable D4 record vocabulary")
		}
		if record.Result != nil {
			if err := record.Result.ValidateFor(record.Tuple); err != nil {
				return nil, fmt.Errorf("invalid durable D4 result: %w", err)
			}
		}
		action := D4RecoveryAwaitingEvent
		switch record.State {
		case D4Terminal, D4ContinuationConsumed:
			action = D4RecoveryProjectionOnly
		case D4Executing, D4ContinuationPending:
			if err := m.requireRecovery(ctx, record); err != nil {
				return nil, err
			}
			action = D4RecoveryQueued
		case D4RecoveryRequired:
			action = D4RecoveryQueued
		case D4Received, D4Admitted, D4ResultRecorded, D4WaitingForMapping:
			// Revalidation/new approved input is required; startup does not
			// advance any of these states and cannot replay a physical call.
		}
		out = append(out, D4RecoveryClassification{Tuple: record.Tuple, State: record.State, Action: action})
	}
	return out, nil
}

func (m *D4RecoveryManager) requireRecovery(ctx context.Context, record D4Record) error {
	approval, err := m.Authorizer.ApproveD4(ctx, record.Tuple, D4EventRequireRecovery)
	if err != nil {
		return err
	}
	if !approval.approved || approval.kind != D4EventRequireRecovery || !approval.tuple.Equal(record.Tuple) {
		return errors.New("owner returned invalid recovery approval")
	}
	_, err = m.Ledger.Transition(ctx, D4TransitionRequest{Tuple: record.Tuple, Expected: record.State, ClaimID: record.ClaimID, Event: D4OwnerEvent{Kind: D4EventRequireRecovery, Tuple: record.Tuple, Approval: approval, Recovery: D4RecoveryUnknown}})
	return err
}

// Resolve is the only path from RECOVERY_REQUIRED to TERMINAL. Owner
// resolution must provide a complete safe result; unresolved UNKNOWN remains
// queued and is never retried by this method.
func (m *D4RecoveryManager) Resolve(ctx context.Context, record D4Record) (D4Record, error) {
	if m == nil || m.Ledger == nil || m.Authorizer == nil || m.Resolver == nil {
		return D4Record{}, errors.New("D4 recovery resolver is unavailable")
	}
	if record.State != D4RecoveryRequired {
		return D4Record{}, fmt.Errorf("record is %s, not RECOVERY_REQUIRED", record.State)
	}
	result, recovery, err := m.Resolver.ResolveD4(ctx, record)
	if err != nil {
		return D4Record{}, err
	}
	if err := result.ValidateFor(record.Tuple); err != nil {
		return D4Record{}, err
	}
	if result.Unknown || result.RecoveryRequired || result.Disposition != D4NonSuccess && recovery != "" {
		return D4Record{}, errors.New("owner resolution is not a safe terminal disposition")
	}
	approval, err := m.Authorizer.ApproveD4(ctx, record.Tuple, D4EventResolveRecovery)
	if err != nil {
		return D4Record{}, err
	}
	if !approval.approved || approval.kind != D4EventResolveRecovery || !approval.tuple.Equal(record.Tuple) {
		return D4Record{}, errors.New("owner returned invalid resolution approval")
	}
	return m.Ledger.Transition(ctx, D4TransitionRequest{Tuple: record.Tuple, Expected: D4RecoveryRequired, ClaimID: record.ClaimID, Event: D4OwnerEvent{Kind: D4EventResolveRecovery, Tuple: record.Tuple, Approval: approval, Result: &result, Recovery: recovery}})
}

// RecoveryRequiredFromReport is the conservative mapping used at process
// boundaries: any unproven physical or cleanup outcome is queued.
func RecoveryRequiredFromReport(report ExecutionReport) bool {
	return report.Outcome == ExecutionCommitOutcomeUnknown || report.Outcome == ExecutionCommittedPostVerifyFailed || report.CleanupError != ""
}
