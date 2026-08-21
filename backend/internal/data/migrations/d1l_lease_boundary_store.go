package migrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// The D1-L lease/boundary writer is deliberately private.  It is a create-only
// seam for the control store; activation, terminalization, renewal, and
// cleanup remain separate lifecycle authorities and are not exposed here.
type d1LLeaseBoundaryStore struct {
	db *sql.DB
}

// d1LLeaseCreateInput contains only values that are immutable at lease issue.
// issued_at and generation are database/store-owned and are not caller input.
type d1LLeaseCreateInput struct {
	LeaseID                  uuid.UUID
	OperationID              uuid.UUID
	AttemptID                uuid.UUID
	TargetFingerprint        []byte
	EvidenceDigest           []byte
	CapabilityVerifierDigest []byte
	ExpiresAt                time.Time
}

type d1LLease struct {
	LeaseID                  uuid.UUID
	OperationID              uuid.UUID
	AttemptID                uuid.UUID
	Generation               int64
	TargetFingerprint        []byte
	EvidenceDigest           []byte
	CapabilityVerifierDigest []byte
	Status                   string
	IssuedAt                 time.Time
	ExpiresAt                time.Time
	// activation is owner-private one-shot material. It is never persisted,
	// serialized, or included in safe lease projections.
	activation []byte
}

// d1LBoundaryCreateInput identifies the immutable grant and its parent.  The
// parent expiry is intentionally absent: it is read from the locked persisted
// lease row by CreateBoundary.
type d1LBoundaryCreateInput struct {
	BoundaryID    uuid.UUID
	LeaseID       uuid.UUID
	AttemptID     uuid.UUID
	Generation    int64
	BoundaryNonce uuid.UUID
	BoundaryName  string
	StartedAt     time.Time
	ExpiresAt     time.Time
}

type d1LBoundary struct {
	BoundaryID    uuid.UUID
	LeaseID       uuid.UUID
	AttemptID     uuid.UUID
	Generation    int64
	BoundaryNonce uuid.UUID
	BoundaryName  string
	Status        string
	StartedAt     time.Time
	ExpiresAt     time.Time
}

var (
	ErrD1LLeaseStoreUnavailable  = errors.New("D1-L lease store is unavailable")
	ErrD1LLeaseInput             = errors.New("D1-L lease input is invalid")
	ErrD1LLeaseExpiry            = errors.New("D1-L lease expiry is not in the future")
	ErrD1LBoundaryInput          = errors.New("D1-L boundary input is invalid")
	ErrD1LBoundaryParentMissing  = errors.New("D1-L boundary parent lease is missing")
	ErrD1LBoundaryParentInactive = errors.New("D1-L boundary parent lease is not active")
	ErrD1LBoundaryParentExpired  = errors.New("D1-L boundary parent lease is expired")
	ErrD1LBoundaryExpiry         = errors.New("D1-L boundary expiry exceeds persisted parent expiry")
	ErrD1LBoundaryNotStarted     = errors.New("D1-L boundary start is not yet valid")
)

const (
	d1LLeaseStatusIssued  = "ISSUED"
	d1LLeaseStatusActive  = "ACTIVE"
	d1LBoundaryStatusOpen = "OPEN"
)

var d1LBoundaryNames = map[string]struct{}{
	"A2_COMMIT":                {},
	"HANDOFF":                  {},
	"DIRTY_MARKER_COMMIT":      {},
	"DDL_COMMIT":               {},
	"FINAL_VERIFY":             {},
	"FINAL_METADATA_COMMIT":    {},
	"RECOVERY_METADATA_COMMIT": {},
}

func newD1LLeaseBoundaryStore(db *sql.DB) (*d1LLeaseBoundaryStore, error) {
	if db == nil {
		return nil, ErrD1LLeaseStoreUnavailable
	}
	return &d1LLeaseBoundaryStore{db: db}, nil
}

func (s *d1LLeaseBoundaryStore) CreateLease(ctx context.Context, input d1LLeaseCreateInput) (d1LLease, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateD1LLeaseCreateInput(input); err != nil {
		return d1LLease{}, err
	}
	if s == nil || s.db == nil {
		return d1LLease{}, ErrD1LLeaseStoreUnavailable
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return d1LLease{}, fmt.Errorf("begin D1-L lease transaction: %w", err)
	}
	rollback := func(cause error) (d1LLease, error) {
		_ = tx.Rollback()
		return d1LLease{}, cause
	}

	var lease d1LLease
	var target, evidence, verifier []byte
	err = tx.QueryRowContext(ctx, d1LCreateLeaseSQL,
		input.LeaseID,
		input.OperationID,
		input.AttemptID,
		input.TargetFingerprint,
		input.EvidenceDigest,
		input.CapabilityVerifierDigest,
		input.ExpiresAt,
	).Scan(
		&lease.LeaseID,
		&lease.OperationID,
		&lease.AttemptID,
		&lease.Generation,
		&target,
		&evidence,
		&verifier,
		&lease.Status,
		&lease.IssuedAt,
		&lease.ExpiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return rollback(ErrD1LLeaseExpiry)
	}
	if err != nil {
		return rollback(fmt.Errorf("insert D1-L lease: %w", err))
	}
	lease.TargetFingerprint = append([]byte(nil), target...)
	lease.EvidenceDigest = append([]byte(nil), evidence...)
	lease.CapabilityVerifierDigest = append([]byte(nil), verifier...)
	if err := tx.Commit(); err != nil {
		return d1LLease{}, fmt.Errorf("commit D1-L lease: %w", err)
	}
	return lease, nil
}

// CreateBoundary locks the exact composite parent identity before evaluating
// its status/expiry and inserting the child. No caller-provided parent expiry
// is accepted or consulted; the database clock is read only after the lock.
func (s *d1LLeaseBoundaryStore) CreateBoundary(ctx context.Context, input d1LBoundaryCreateInput) (d1LBoundary, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateD1LBoundaryCreateInput(input); err != nil {
		return d1LBoundary{}, err
	}
	if s == nil || s.db == nil {
		return d1LBoundary{}, ErrD1LLeaseStoreUnavailable
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return d1LBoundary{}, fmt.Errorf("begin D1-L boundary transaction: %w", err)
	}
	rollback := func(cause error) (d1LBoundary, error) {
		_ = tx.Rollback()
		return d1LBoundary{}, cause
	}

	// Lock the parent before sampling the database clock. A clock value
	// obtained before this FOR UPDATE could authorize a child after a
	// concurrently-held parent lock has carried the lease past expiry.
	var parentStatus string
	var parentExpiry time.Time
	err = tx.QueryRowContext(ctx, `
SELECT status, expires_at
FROM security_control.admission_leases
WHERE lease_id = $1 AND attempt_id = $2 AND generation = $3
FOR UPDATE`, input.LeaseID, input.AttemptID, input.Generation).Scan(&parentStatus, &parentExpiry)
	if errors.Is(err, sql.ErrNoRows) {
		return rollback(ErrD1LBoundaryParentMissing)
	}
	if err != nil {
		return rollback(fmt.Errorf("lock D1-L boundary parent: %w", err))
	}
	var dbNow time.Time
	if err := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&dbNow); err != nil {
		return rollback(fmt.Errorf("read D1-L boundary clock: %w", err))
	}
	if parentStatus != d1LLeaseStatusActive {
		return rollback(ErrD1LBoundaryParentInactive)
	}
	if !parentExpiry.After(dbNow) {
		return rollback(ErrD1LBoundaryParentExpired)
	}
	if input.StartedAt.After(dbNow) {
		return rollback(ErrD1LBoundaryNotStarted)
	}
	if input.ExpiresAt.After(parentExpiry) {
		return rollback(ErrD1LBoundaryExpiry)
	}

	var boundary d1LBoundary
	err = tx.QueryRowContext(ctx, `
INSERT INTO security_control.admission_boundaries (
    boundary_id, lease_id, attempt_id, generation, boundary_nonce,
    boundary_name, status, started_at, expires_at
)
VALUES ($1, $2, $3, $4, $5, $6, 'OPEN', $7, $8)
RETURNING boundary_id, lease_id, attempt_id, generation, boundary_nonce,
          boundary_name, status, started_at, expires_at`,
		input.BoundaryID, input.LeaseID, input.AttemptID, input.Generation,
		input.BoundaryNonce, input.BoundaryName, input.StartedAt, input.ExpiresAt,
	).Scan(
		&boundary.BoundaryID, &boundary.LeaseID, &boundary.AttemptID,
		&boundary.Generation, &boundary.BoundaryNonce, &boundary.BoundaryName,
		&boundary.Status, &boundary.StartedAt, &boundary.ExpiresAt,
	)
	if err != nil {
		return rollback(fmt.Errorf("insert D1-L boundary: %w", err))
	}
	if err := tx.Commit(); err != nil {
		return d1LBoundary{}, fmt.Errorf("commit D1-L boundary: %w", err)
	}
	return boundary, nil
}

func validateD1LLeaseCreateInput(input d1LLeaseCreateInput) error {
	if input.LeaseID == uuid.Nil || input.OperationID == uuid.Nil || input.AttemptID == uuid.Nil {
		return fmt.Errorf("%w: lease, operation, and attempt identities are required", ErrD1LLeaseInput)
	}
	if len(input.TargetFingerprint) != 32 || len(input.EvidenceDigest) != 32 || len(input.CapabilityVerifierDigest) != 32 {
		return fmt.Errorf("%w: lease digests must each contain 32 bytes", ErrD1LLeaseInput)
	}
	if input.ExpiresAt.IsZero() {
		return fmt.Errorf("%w: lease expiry is required", ErrD1LLeaseInput)
	}
	return nil
}

func validateD1LBoundaryCreateInput(input d1LBoundaryCreateInput) error {
	if input.BoundaryID == uuid.Nil || input.LeaseID == uuid.Nil || input.AttemptID == uuid.Nil || input.BoundaryNonce == uuid.Nil {
		return fmt.Errorf("%w: boundary and parent identities are required", ErrD1LBoundaryInput)
	}
	if input.Generation <= 0 {
		return fmt.Errorf("%w: parent generation must be positive", ErrD1LBoundaryInput)
	}
	if _, ok := d1LBoundaryNames[input.BoundaryName]; !ok {
		return fmt.Errorf("%w: unsupported boundary name %q", ErrD1LBoundaryInput, input.BoundaryName)
	}
	if input.StartedAt.IsZero() || input.ExpiresAt.IsZero() || !input.ExpiresAt.After(input.StartedAt) {
		return fmt.Errorf("%w: boundary expiry must be after its start", ErrD1LBoundaryInput)
	}
	return nil
}

const d1LCreateLeaseSQL = `
WITH d1l_clock AS MATERIALIZED (
    SELECT clock_timestamp() AS now
)
INSERT INTO security_control.admission_leases (
    lease_id,
    operation_id,
    attempt_id,
    target_fingerprint,
    evidence_digest,
    capability_verifier_digest,
    status,
    issued_at,
    expires_at
)
SELECT
    $1::uuid,
    $2::uuid,
    $3::uuid,
    $4::bytea,
    $5::bytea,
    $6::bytea,
    'ISSUED'::text,
    d1l_clock.now,
    $7::timestamptz
FROM d1l_clock
WHERE $7::timestamptz > d1l_clock.now
RETURNING
    lease_id,
    operation_id,
    attempt_id,
    generation,
    target_fingerprint,
    evidence_digest,
    capability_verifier_digest,
    status,
    issued_at,
    expires_at`
