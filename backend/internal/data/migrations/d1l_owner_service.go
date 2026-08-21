package migrations

import (
	"context"
	"power-iot-backend/internal/data/reconciliation/upstream"
	"time"
)

// D1LLeaseIssueResult is the only lease issue projection exposed by the D1
// owner service. It intentionally excludes the activation presentation and
// capability verifier so neither can enter safe results, logs, or callers'
// durable rows.
type D1LLeaseIssueResult struct {
	Identity                          D1LLeaseIdentity
	TargetFingerprint, EvidenceDigest []byte
	Status                            string
	IssuedAt, ExpiresAt, ActivatedAt  time.Time
}

// D1LOwnerService is the private D1 owner acceptance seam. It accepts only a
// sealed upstream provenance value, performs Record -> Reserve -> Complete as
// the provenance lifecycle, and keeps activation material inside migrations.
type D1LOwnerService struct {
	ledger *D1LProvenanceLedger
}

func NewD1LOwnerService(ledger *D1LProvenanceLedger) (*D1LOwnerService, error) {
	if ledger == nil || ledger.db == nil {
		return nil, ErrD1LLeaseStoreUnavailable
	}
	return &D1LOwnerService{ledger: ledger}, nil
}

// Issue performs the sealed T0/T1 issue path and returns only a safe ISSUED
// projection. The private lease retains one-shot activation material until the
// owner invokes IssueAndActivate.
func (s *D1LOwnerService) Issue(ctx context.Context, p upstream.Provenance) (D1LLeaseIssueResult, error) {
	lease, err := s.issue(ctx, p)
	if err != nil {
		return D1LLeaseIssueResult{}, err
	}
	result := safeD1LLeaseIssueResult(lease)
	zeroD1L(lease.activation)
	return result, nil
}

// IssueAndActivate is the owner-mediated acceptance path. The presentation is
// generated from and consumed against the exact persisted lease tuple exactly
// once; no raw presentation crosses this exported service boundary.
func (s *D1LOwnerService) IssueAndActivate(ctx context.Context, p upstream.Provenance) (D1LLeaseIssueResult, error) {
	lease, err := s.issue(ctx, p)
	if err != nil {
		return D1LLeaseIssueResult{}, err
	}
	if err := s.ledger.activateIssuedLease(ctx, &lease); err != nil {
		return D1LLeaseIssueResult{}, err
	}
	inspection, err := s.ledger.InspectLease(ctx, D1LLeaseIdentity{
		LeaseID: lease.LeaseID, OperationID: lease.OperationID, AttemptID: lease.AttemptID,
		Generation: lease.Generation, TargetFingerprint: lease.TargetFingerprint,
		EvidenceDigest: lease.EvidenceDigest,
	})
	if err != nil {
		return D1LLeaseIssueResult{}, err
	}
	return D1LLeaseIssueResult{Identity: inspection.Identity, TargetFingerprint: append([]byte(nil), inspection.TargetFingerprint...), EvidenceDigest: append([]byte(nil), inspection.EvidenceDigest...), Status: inspection.Status, IssuedAt: inspection.IssuedAt, ExpiresAt: inspection.ExpiresAt, ActivatedAt: inspection.ActivatedAt}, nil
}

func (s *D1LOwnerService) issue(ctx context.Context, p upstream.Provenance) (d1LLease, error) {
	if s == nil || s.ledger == nil {
		return d1LLease{}, ErrD1LLeaseStoreUnavailable
	}
	record, err := s.ledger.Record(ctx, p)
	if err != nil {
		return d1LLease{}, err
	}
	reservation, err := s.ledger.Reserve(ctx, record)
	if err != nil {
		return d1LLease{}, err
	}
	return s.ledger.completeIssue(ctx, reservation)
}

// Inspect returns the existing safe lease projection and performs owner-clock
// expiry terminalization under the row lock.
func (s *D1LOwnerService) Inspect(ctx context.Context, id D1LLeaseIdentity) (D1LLeaseInspection, error) {
	if s == nil || s.ledger == nil {
		return D1LLeaseInspection{}, ErrD1LLeaseStoreUnavailable
	}
	return s.ledger.InspectLease(ctx, id)
}

// ConsumeLease is the owner-mediated execution gate. The identity is passed
// unchanged to the ledger, which validates the complete binding and performs
// the one-shot ACTIVE -> CONSUMED transition under its row lock.
func (s *D1LOwnerService) ConsumeLease(ctx context.Context, id D1LLeaseIdentity) error {
	if s == nil || s.ledger == nil {
		return ErrD1LLeaseStoreUnavailable
	}
	return s.ledger.ConsumeLease(ctx, id)
}

func safeD1LLeaseIssueResult(lease d1LLease) D1LLeaseIssueResult {
	return D1LLeaseIssueResult{
		Identity: D1LLeaseIdentity{
			LeaseID: lease.LeaseID, OperationID: lease.OperationID, AttemptID: lease.AttemptID,
			Generation: lease.Generation, TargetFingerprint: append([]byte(nil), lease.TargetFingerprint...),
			EvidenceDigest: append([]byte(nil), lease.EvidenceDigest...),
		},
		TargetFingerprint: append([]byte(nil), lease.TargetFingerprint...),
		EvidenceDigest:    append([]byte(nil), lease.EvidenceDigest...),
		Status:            lease.Status, IssuedAt: lease.IssuedAt, ExpiresAt: lease.ExpiresAt,
	}
}
