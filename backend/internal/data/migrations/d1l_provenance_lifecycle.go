package migrations

// This file is the D1 owner-side durable boundary. Provenance is recorded by
// the trusted upstream owner and consumed by D1 in the same PostgreSQL
// database as admission_leases; provider authorizations never enter this API.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"power-iot-backend/internal/data/reconciliation/upstream"
)

const (
	d1LProvenanceAvailable   = "AVAILABLE"
	d1LProvenanceReserved    = "RESERVED"
	d1LProvenanceConsumed    = "CONSUMED"
	d1LProvenanceInvalidated = "INVALIDATED"
)

var (
	ErrD1LProvenance          = errors.New("D1-L provenance rejected")
	ErrD1LProvenanceDuplicate = errors.New("D1-L provenance duplicate or already resolved")
	ErrD1LProvenanceUnknown   = errors.New("D1-L provenance outcome is unknown")
	ErrD1LProvenanceStale     = errors.New("D1-L provenance is stale")
)

type D1LProvenanceState string

const (
	D1LProvenanceAvailable   D1LProvenanceState = d1LProvenanceAvailable
	D1LProvenanceReserved    D1LProvenanceState = d1LProvenanceReserved
	D1LProvenanceConsumed    D1LProvenanceState = d1LProvenanceConsumed
	D1LProvenanceInvalidated D1LProvenanceState = d1LProvenanceInvalidated
)

type D1LProvenanceRecord struct {
	ID                                uuid.UUID
	Version                           int64
	OwnerIdentity, OwnerVersion       string
	OperationID, AttemptID            uuid.UUID
	TargetFingerprint                 [32]byte
	EvidenceDigest                    [32]byte
	RouteIntent                       string
	State                             D1LProvenanceState
	IssueID                           uuid.UUID
	LeaseID                           uuid.UUID
	LeaseGeneration                   int64
	CreatedAt, ReservedAt, ResolvedAt time.Time
	TerminalCode                      string
}

type D1LProvenanceLedger struct {
	db       *sql.DB
	lifetime time.Duration
}

// NewD1LProvenanceLedger creates the D1 consumer/resolver. A bounded lifetime
// is owner configuration; no request can choose expiry.
func NewD1LProvenanceLedger(db *sql.DB, lifetime time.Duration) (*D1LProvenanceLedger, error) {
	if db == nil || lifetime <= 0 || lifetime > 24*time.Hour {
		return nil, ErrD1LProvenance
	}
	return &D1LProvenanceLedger{db: db, lifetime: lifetime}, nil
}

// ensureD1LNextLedgerReady is the shared D1-L authority gate. Physical
// admission tables (including admission_leases) are never sufficient: the
// exact next-version manifest and catalog proof must authorize every consumer
// operation.
func ensureD1LNextLedgerReady(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return ErrD1LLeaseStoreUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var target, digest []byte
	if err := db.QueryRowContext(ctx, `SELECT target_fingerprint,installer_digest FROM security_control.control_schema_migrations WHERE control_version=$1 AND dirty=false`, d1LNextControlVersion).Scan(&target, &digest); err != nil {
		return ErrD1LLeaseStoreUnavailable
	}
	obs, err := RecognizeD1LCatalog(ctx, db, target, digest)
	if err != nil || obs.State != D1LValidNextLedgerReady {
		return ErrD1LLeaseStoreUnavailable
	}
	return nil
}

func (l *D1LProvenanceLedger) ensure(ctx context.Context) error {
	if l == nil || l.db == nil {
		return ErrD1LLeaseStoreUnavailable
	}
	return ensureD1LNextLedgerReady(ctx, l.db)
}

// Record persists owner-issued provenance as AVAILABLE. It does not accept a
// tuple or digest from D1; all fields come from the opaque producer output.
func (l *D1LProvenanceLedger) Record(ctx context.Context, p upstream.Provenance) (D1LProvenanceRecord, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	id, version, err := p.Identity()
	if err != nil {
		return D1LProvenanceRecord{}, err
	}
	owner, ownerVersion, err := p.OwnerIdentity()
	if err != nil {
		return D1LProvenanceRecord{}, err
	}
	binding, err := p.Binding()
	if err != nil {
		return D1LProvenanceRecord{}, err
	}
	digest, err := p.EvidenceDigest()
	if err != nil {
		return D1LProvenanceRecord{}, err
	}
	if owner != upstream.D1OwnerIdentity || strings.TrimSpace(ownerVersion) == "" || binding.OperationID == uuid.Nil || binding.AttemptID == uuid.Nil || binding.TargetFingerprint == [32]byte{} || binding.RouteIntent != upstream.D1OwnerIssueRoute || binding.RouteIntent != strings.TrimSpace(binding.RouteIntent) || digest == [32]byte{} {
		return D1LProvenanceRecord{}, ErrD1LProvenance
	}
	observedAt, err := p.ObservedAt()
	if err != nil || observedAt.IsZero() {
		return D1LProvenanceRecord{}, ErrD1LProvenance
	}
	if err := l.ensure(ctx); err != nil {
		return D1LProvenanceRecord{}, err
	}
	var rec D1LProvenanceRecord
	err = l.db.QueryRowContext(ctx, `INSERT INTO security_control.admission_provenance(provenance_id,provenance_version,owner_identity,owner_version,operation_id,attempt_id,target_fingerprint,evidence_digest,route_intent,state) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'AVAILABLE') RETURNING created_at`, id, version, owner, ownerVersion, binding.OperationID, binding.AttemptID, binding.TargetFingerprint[:], digest[:], binding.RouteIntent).Scan(&rec.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return D1LProvenanceRecord{}, ErrD1LProvenanceDuplicate
		}
		return D1LProvenanceRecord{}, err
	}
	rec.ID, rec.Version, rec.OwnerIdentity, rec.OwnerVersion, rec.OperationID, rec.AttemptID, rec.RouteIntent, rec.State = id, version, owner, ownerVersion, binding.OperationID, binding.AttemptID, binding.RouteIntent, D1LProvenanceAvailable
	rec.TargetFingerprint, rec.EvidenceDigest = binding.TargetFingerprint, digest
	return rec, nil
}

type D1LProvenanceReservation struct{ Record D1LProvenanceRecord }

// Resolve is read-only duplicate/UNKNOWN resolution. It never reserves,
// consumes, allocates a generation, changes expiry, or returns activation
// material.
func (l *D1LProvenanceLedger) Resolve(ctx context.Context, id uuid.UUID, version int64, issueID uuid.UUID) (D1LProvenanceRecord, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if id == uuid.Nil || version <= 0 || issueID == uuid.Nil {
		return D1LProvenanceRecord{}, ErrD1LProvenance
	}
	if err := l.ensure(ctx); err != nil {
		return D1LProvenanceRecord{}, err
	}
	var r D1LProvenanceRecord
	var target, evidence []byte
	var issueRaw sql.NullString
	err := l.db.QueryRowContext(ctx, `SELECT provenance_id,provenance_version,owner_identity,owner_version,operation_id,attempt_id,target_fingerprint,evidence_digest,route_intent,state,issue_id,COALESCE(lease_id,'00000000-0000-0000-0000-000000000000'),COALESCE(lease_generation,0),created_at,COALESCE(reserved_at,created_at),COALESCE(resolved_at,created_at),COALESCE(terminal_code,'') FROM security_control.admission_provenance WHERE provenance_id=$1 AND provenance_version=$2 AND issue_id=$3`, id, version, issueID).Scan(&r.ID, &r.Version, &r.OwnerIdentity, &r.OwnerVersion, &r.OperationID, &r.AttemptID, &target, &evidence, &r.RouteIntent, &r.State, &issueRaw, &r.LeaseID, &r.LeaseGeneration, &r.CreatedAt, &r.ReservedAt, &r.ResolvedAt, &r.TerminalCode)
	if errors.Is(err, sql.ErrNoRows) {
		return D1LProvenanceRecord{}, ErrD1LProvenanceUnknown
	}
	if err != nil {
		return D1LProvenanceRecord{}, ErrD1LProvenanceUnknown
	}
	if len(target) != 32 || len(evidence) != 32 {
		return D1LProvenanceRecord{}, ErrD1LProvenanceUnknown
	}
	copy(r.TargetFingerprint[:], target)
	copy(r.EvidenceDigest[:], evidence)
	if err := assignD1LIssueID(&r.IssueID, issueRaw); err != nil {
		return D1LProvenanceRecord{}, ErrD1LProvenanceUnknown
	}
	return r, nil
}

// ResolveByOwnerBinding performs the read-only response-loss lookup. The
// caller supplies the complete owner-issued immutable binding but not an
// issue identity; the durable row (and, once RESERVED, its issue identity) is
// discovered from the unique attempt binding. It never reserves, repairs, or
// mints a lease.
func (l *D1LProvenanceLedger) ResolveByOwnerBinding(ctx context.Context, expected D1LProvenanceRecord) (D1LProvenanceRecord, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !validD1LProvenanceBinding(expected) {
		return D1LProvenanceRecord{}, ErrD1LProvenance
	}
	if err := l.ensure(ctx); err != nil {
		return D1LProvenanceRecord{}, err
	}
	predicate := `WHERE owner_identity=$1 AND owner_version=$2 AND operation_id=$3 AND attempt_id=$4 AND target_fingerprint=$5 AND evidence_digest=$6 AND route_intent=$7`
	args := []any{expected.OwnerIdentity, expected.OwnerVersion, expected.OperationID, expected.AttemptID, expected.TargetFingerprint[:], expected.EvidenceDigest[:], expected.RouteIntent}
	if expected.ID != uuid.Nil && expected.Version > 0 {
		predicate = `WHERE provenance_id=$1 AND provenance_version=$2 AND owner_identity=$3 AND owner_version=$4 AND operation_id=$5 AND attempt_id=$6 AND target_fingerprint=$7 AND evidence_digest=$8 AND route_intent=$9`
		args = []any{expected.ID, expected.Version, expected.OwnerIdentity, expected.OwnerVersion, expected.OperationID, expected.AttemptID, expected.TargetFingerprint[:], expected.EvidenceDigest[:], expected.RouteIntent}
	}
	return l.resolveProvenanceQuery(ctx, predicate, args...)
}

// ResolveExact is the owner-side read-only duplicate resolver. All immutable
// fields, including the durable issue identity, must match; no lifecycle
// transition is performed for any state.
func (l *D1LProvenanceLedger) ResolveExact(ctx context.Context, expected D1LProvenanceRecord) (D1LProvenanceRecord, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !validD1LProvenanceRecord(expected) || expected.IssueID == uuid.Nil {
		return D1LProvenanceRecord{}, ErrD1LProvenance
	}
	if err := l.ensure(ctx); err != nil {
		return D1LProvenanceRecord{}, err
	}
	return l.resolveProvenanceQuery(ctx, `WHERE provenance_id=$1 AND provenance_version=$2 AND owner_identity=$3 AND owner_version=$4 AND operation_id=$5 AND attempt_id=$6 AND target_fingerprint=$7 AND evidence_digest=$8 AND route_intent=$9 AND issue_id=$10`, expected.ID, expected.Version, expected.OwnerIdentity, expected.OwnerVersion, expected.OperationID, expected.AttemptID, expected.TargetFingerprint[:], expected.EvidenceDigest[:], expected.RouteIntent, expected.IssueID)
}

// ResolveProducer reconstructs the exact owner binding from a sealed producer
// value after a Record response is lost, allowing the owner to discover the
// durable row and any issue identity already assigned by T0 without accepting
// caller-controlled identity fields.
func (l *D1LProvenanceLedger) ResolveProducer(ctx context.Context, p upstream.Provenance) (D1LProvenanceRecord, error) {
	id, version, err := p.Identity()
	if err != nil {
		return D1LProvenanceRecord{}, err
	}
	owner, ownerVersion, err := p.OwnerIdentity()
	if err != nil {
		return D1LProvenanceRecord{}, err
	}
	binding, err := p.Binding()
	if err != nil {
		return D1LProvenanceRecord{}, err
	}
	digest, err := p.EvidenceDigest()
	if err != nil {
		return D1LProvenanceRecord{}, err
	}
	return l.ResolveByOwnerBinding(ctx, D1LProvenanceRecord{ID: id, Version: version, OwnerIdentity: owner, OwnerVersion: ownerVersion, OperationID: binding.OperationID, AttemptID: binding.AttemptID, TargetFingerprint: binding.TargetFingerprint, EvidenceDigest: digest, RouteIntent: binding.RouteIntent})
}

func (l *D1LProvenanceLedger) resolveProvenanceQuery(ctx context.Context, predicate string, args ...any) (D1LProvenanceRecord, error) {
	var r D1LProvenanceRecord
	var target, evidence []byte
	var issueRaw sql.NullString
	query := `SELECT provenance_id,provenance_version,owner_identity,owner_version,operation_id,attempt_id,target_fingerprint,evidence_digest,route_intent,state,issue_id,COALESCE(lease_id,'00000000-0000-0000-0000-000000000000'),COALESCE(lease_generation,0),created_at,COALESCE(reserved_at,created_at),COALESCE(resolved_at,created_at),COALESCE(terminal_code,'') FROM security_control.admission_provenance ` + predicate
	err := l.db.QueryRowContext(ctx, query, args...).Scan(&r.ID, &r.Version, &r.OwnerIdentity, &r.OwnerVersion, &r.OperationID, &r.AttemptID, &target, &evidence, &r.RouteIntent, &r.State, &issueRaw, &r.LeaseID, &r.LeaseGeneration, &r.CreatedAt, &r.ReservedAt, &r.ResolvedAt, &r.TerminalCode)
	if errors.Is(err, sql.ErrNoRows) {
		return D1LProvenanceRecord{}, ErrD1LProvenanceUnknown
	}
	if err != nil || len(target) != 32 || len(evidence) != 32 {
		return D1LProvenanceRecord{}, ErrD1LProvenanceUnknown
	}
	copy(r.TargetFingerprint[:], target)
	copy(r.EvidenceDigest[:], evidence)
	if err := assignD1LIssueID(&r.IssueID, issueRaw); err != nil {
		return D1LProvenanceRecord{}, ErrD1LProvenanceUnknown
	}
	return r, nil
}

// Reserve is T0: only its successful commit creates a durable issue attempt.
// The issue identity is generated after this transaction begins and is written
// only by the successful AVAILABLE -> RESERVED transition.
func (l *D1LProvenanceLedger) Reserve(ctx context.Context, p D1LProvenanceRecord) (D1LProvenanceReservation, error) {
	return l.reserve(ctx, p, nil)
}

// reserve is the package-private T0 implementation. The bounded hook exists
// only for same-package transaction-failure evidence; production callers use
// Reserve and cannot inject work into this transaction.
func (l *D1LProvenanceLedger) reserve(ctx context.Context, p D1LProvenanceRecord, beforeCommit func(*sql.Tx) error) (D1LProvenanceReservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !validD1LProvenanceRecord(p) || p.State != D1LProvenanceAvailable || p.IssueID != uuid.Nil {
		return D1LProvenanceReservation{}, ErrD1LProvenance
	}
	if err := l.ensure(ctx); err != nil {
		return D1LProvenanceReservation{}, err
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return D1LProvenanceReservation{}, err
	}
	defer tx.Rollback()
	issueID := uuid.New()
	var rec D1LProvenanceRecord
	var target, evidence []byte
	err = tx.QueryRowContext(ctx, `UPDATE security_control.admission_provenance SET state='RESERVED',issue_id=$10,reserved_at=GREATEST(clock_timestamp(), created_at) WHERE provenance_id=$1 AND provenance_version=$2 AND owner_identity=$3 AND owner_version=$4 AND operation_id=$5 AND attempt_id=$6 AND target_fingerprint=$7 AND evidence_digest=$8 AND route_intent=$9 AND issue_id IS NULL AND state='AVAILABLE' RETURNING provenance_id,provenance_version,owner_identity,owner_version,operation_id,attempt_id,target_fingerprint,evidence_digest,route_intent,state,issue_id,created_at,reserved_at`, p.ID, p.Version, p.OwnerIdentity, p.OwnerVersion, p.OperationID, p.AttemptID, p.TargetFingerprint[:], p.EvidenceDigest[:], p.RouteIntent, issueID).Scan(&rec.ID, &rec.Version, &rec.OwnerIdentity, &rec.OwnerVersion, &rec.OperationID, &rec.AttemptID, &target, &evidence, &rec.RouteIntent, &rec.State, &rec.IssueID, &rec.CreatedAt, &rec.ReservedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return D1LProvenanceReservation{}, ErrD1LProvenanceDuplicate
	}
	if err != nil {
		return D1LProvenanceReservation{}, err
	}
	if len(target) != 32 || len(evidence) != 32 {
		return D1LProvenanceReservation{}, ErrD1LProvenanceUnknown
	}
	copy(rec.TargetFingerprint[:], target)
	copy(rec.EvidenceDigest[:], evidence)
	if beforeCommit != nil {
		if err := beforeCommit(tx); err != nil {
			return D1LProvenanceReservation{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return D1LProvenanceReservation{}, ErrD1LProvenanceUnknown
	}
	return D1LProvenanceReservation{Record: rec}, nil
}

// completeIssue is T1. Lease insertion, exact linkage, and CONSUMED are
// one transaction; callers cannot supply expiry, generation, status, verifier,
// or activation material.
func (l *D1LProvenanceLedger) completeIssue(ctx context.Context, r D1LProvenanceReservation) (d1LLease, error) {
	return l.completeIssueWithHook(ctx, r, nil)
}

// completeIssueWithHook is a package-private bounded failure seam used only
// to prove that T1 errors roll back lease insertion and leave RESERVED state.
func (l *D1LProvenanceLedger) completeIssueWithHook(ctx context.Context, r D1LProvenanceReservation, beforeCommit func(*sql.Tx) error) (d1LLease, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	p := r.Record
	if !validD1LProvenanceRecord(p) || p.IssueID == uuid.Nil || p.State != D1LProvenanceReserved {
		return d1LLease{}, ErrD1LProvenance
	}
	if err := l.ensure(ctx); err != nil {
		return d1LLease{}, err
	}
	activationNonce := make([]byte, 32)
	defer zeroD1L(activationNonce)
	if _, err := rand.Read(activationNonce); err != nil {
		return d1LLease{}, err
	}
	// The persisted verifier is only a digest of the private nonce. The
	// one-shot presentation additionally carries a MAC over the complete lease
	// tuple, including the database-owned expiry and verifier digest.
	verifier := sha256.Sum256(activationNonce)
	leaseID := uuid.New()
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return d1LLease{}, err
	}
	defer tx.Rollback()
	var lease d1LLease
	var target, evidence, gotVerifier []byte
	var storedOperation, storedAttempt uuid.UUID
	var storedTarget, storedEvidence []byte
	var storedOwner, storedOwnerVersion, storedRoute string
	err = tx.QueryRowContext(ctx, `SELECT owner_identity,owner_version,operation_id,attempt_id,target_fingerprint,evidence_digest,route_intent FROM security_control.admission_provenance WHERE provenance_id=$1 AND provenance_version=$2 AND issue_id=$3 AND state='RESERVED' FOR UPDATE`, p.ID, p.Version, p.IssueID).Scan(&storedOwner, &storedOwnerVersion, &storedOperation, &storedAttempt, &storedTarget, &storedEvidence, &storedRoute)
	if errors.Is(err, sql.ErrNoRows) {
		return d1LLease{}, ErrD1LProvenanceDuplicate
	}
	if err != nil {
		return d1LLease{}, err
	}
	if storedOwner != p.OwnerIdentity || storedOwnerVersion != p.OwnerVersion || storedOperation != p.OperationID || storedAttempt != p.AttemptID || !equalBytes(storedTarget, p.TargetFingerprint[:]) || !equalBytes(storedEvidence, p.EvidenceDigest[:]) || storedRoute != p.RouteIntent {
		return d1LLease{}, ErrD1LProvenance
	}
	err = tx.QueryRowContext(ctx, d1LCreateOwnedLeaseSQL, leaseID, storedOperation, storedAttempt, storedTarget, storedEvidence, verifier[:], l.lifetime.Nanoseconds()).Scan(&lease.LeaseID, &lease.OperationID, &lease.AttemptID, &lease.Generation, &target, &evidence, &gotVerifier, &lease.Status, &lease.IssuedAt, &lease.ExpiresAt)
	if err != nil {
		return d1LLease{}, err
	}
	if beforeCommit != nil {
		if err := beforeCommit(tx); err != nil {
			return d1LLease{}, err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE security_control.admission_provenance SET state='CONSUMED',lease_id=$1,lease_generation=$2,resolved_at=GREATEST(clock_timestamp(), reserved_at) WHERE provenance_id=$3 AND provenance_version=$4 AND issue_id=$5 AND state='RESERVED'`, lease.LeaseID, lease.Generation, p.ID, p.Version, p.IssueID); err != nil {
		return d1LLease{}, err
	}
	lease.TargetFingerprint, lease.EvidenceDigest, lease.CapabilityVerifierDigest = append([]byte(nil), target...), append([]byte(nil), evidence...), append([]byte(nil), gotVerifier...)
	lease.activation = sealD1LActivation(activationNonce, lease)
	if err = tx.Commit(); err != nil {
		return d1LLease{}, ErrD1LProvenanceUnknown
	}
	return lease, nil
}

const d1LCreateOwnedLeaseSQL = `WITH c AS (SELECT clock_timestamp() now) INSERT INTO security_control.admission_leases(lease_id,operation_id,attempt_id,target_fingerprint,evidence_digest,capability_verifier_digest,status,issued_at,expires_at) SELECT $1,$2,$3,$4,$5,$6,'ISSUED',c.now,c.now+(($7::double precision / 1000000000.0) * interval '1 second') FROM c RETURNING lease_id,operation_id,attempt_id,generation,target_fingerprint,evidence_digest,capability_verifier_digest,status,issued_at,expires_at`

// Invalidate is the only legal T1 non-commit recovery. It locks and
// revalidates the complete owner reservation tuple before making the durable
// row unusable; it never repairs or mints a lease.
func (l *D1LProvenanceLedger) Invalidate(ctx context.Context, r D1LProvenanceReservation, code string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	p := r.Record
	if !validD1LProvenanceRecord(p) || p.IssueID == uuid.Nil || p.State != D1LProvenanceReserved || !validD1LProvenanceTerminalCode(code) {
		return ErrD1LProvenance
	}
	if err := l.ensure(ctx); err != nil {
		return err
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owner, ownerVersion, route, state string
	var operation, attempt, storedIssue uuid.UUID
	var target, evidence []byte
	err = tx.QueryRowContext(ctx, `SELECT owner_identity,owner_version,operation_id,attempt_id,target_fingerprint,evidence_digest,route_intent,state,issue_id FROM security_control.admission_provenance WHERE provenance_id=$1 AND provenance_version=$2 FOR UPDATE`, p.ID, p.Version).Scan(&owner, &ownerVersion, &operation, &attempt, &target, &evidence, &route, &state, &storedIssue)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrD1LProvenanceDuplicate
	}
	if err != nil {
		return err
	}
	if state != d1LProvenanceReserved {
		return ErrD1LProvenanceDuplicate
	}
	if owner != p.OwnerIdentity || ownerVersion != p.OwnerVersion || operation != p.OperationID || attempt != p.AttemptID || !equalBytes(target, p.TargetFingerprint[:]) || !equalBytes(evidence, p.EvidenceDigest[:]) || route != p.RouteIntent || storedIssue != p.IssueID {
		return ErrD1LProvenance
	}
	result, err := tx.ExecContext(ctx, `UPDATE security_control.admission_provenance SET state='INVALIDATED',terminal_code=$1,resolved_at=GREATEST(clock_timestamp(), reserved_at) WHERE provenance_id=$2 AND provenance_version=$3 AND issue_id=$4 AND state='RESERVED'`, code, p.ID, p.Version, p.IssueID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrD1LProvenanceDuplicate
	}
	if err := tx.Commit(); err != nil {
		return ErrD1LProvenanceUnknown
	}
	return nil
}

// ResolveReservedNonCommit is a locked, no-repair recovery operation. A row
// can only be proven T1-noncommitted while it is RESERVED and has no lease
// linkage; the operation then permanently invalidates that exact reservation.
func (l *D1LProvenanceLedger) ResolveReservedNonCommit(ctx context.Context, r D1LProvenanceReservation, code string) (D1LProvenanceRecord, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	p := r.Record
	if !validD1LProvenanceRecord(p) || p.IssueID == uuid.Nil || p.State != d1LProvenanceReserved || !validD1LProvenanceTerminalCode(code) {
		return D1LProvenanceRecord{}, ErrD1LProvenance
	}
	if err := l.ensure(ctx); err != nil {
		return D1LProvenanceRecord{}, err
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return D1LProvenanceRecord{}, err
	}
	defer tx.Rollback()
	var owner, ownerVersion, route, state string
	var operation, attempt, storedIssue, leaseID uuid.UUID
	var generation int64
	var target, evidence []byte
	err = tx.QueryRowContext(ctx, `SELECT owner_identity,owner_version,operation_id,attempt_id,target_fingerprint,evidence_digest,route_intent,state,issue_id,COALESCE(lease_id,'00000000-0000-0000-0000-000000000000'),COALESCE(lease_generation,0) FROM security_control.admission_provenance WHERE provenance_id=$1 AND provenance_version=$2 FOR UPDATE`, p.ID, p.Version).Scan(&owner, &ownerVersion, &operation, &attempt, &target, &evidence, &route, &state, &storedIssue, &leaseID, &generation)
	if errors.Is(err, sql.ErrNoRows) {
		return D1LProvenanceRecord{}, ErrD1LProvenanceUnknown
	}
	if err != nil {
		return D1LProvenanceRecord{}, err
	}
	if state != d1LProvenanceReserved || leaseID != uuid.Nil || generation != 0 || owner != p.OwnerIdentity || ownerVersion != p.OwnerVersion || operation != p.OperationID || attempt != p.AttemptID || !equalBytes(target, p.TargetFingerprint[:]) || !equalBytes(evidence, p.EvidenceDigest[:]) || route != p.RouteIntent || storedIssue != p.IssueID {
		return D1LProvenanceRecord{}, ErrD1LProvenance
	}
	if _, err := tx.ExecContext(ctx, `UPDATE security_control.admission_provenance SET state='INVALIDATED',terminal_code=$1,resolved_at=GREATEST(clock_timestamp(), reserved_at) WHERE provenance_id=$2 AND provenance_version=$3 AND issue_id=$4 AND state='RESERVED'`, code, p.ID, p.Version, p.IssueID); err != nil {
		return D1LProvenanceRecord{}, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT provenance_id,provenance_version,owner_identity,owner_version,operation_id,attempt_id,target_fingerprint,evidence_digest,route_intent,state,issue_id,COALESCE(lease_id,'00000000-0000-0000-0000-000000000000'),COALESCE(lease_generation,0),created_at,COALESCE(reserved_at,created_at),COALESCE(resolved_at,created_at),COALESCE(terminal_code,'') FROM security_control.admission_provenance WHERE provenance_id=$1 AND provenance_version=$2`, p.ID, p.Version).Scan(&p.ID, &p.Version, &p.OwnerIdentity, &p.OwnerVersion, &p.OperationID, &p.AttemptID, &target, &evidence, &p.RouteIntent, &p.State, &p.IssueID, &p.LeaseID, &p.LeaseGeneration, &p.CreatedAt, &p.ReservedAt, &p.ResolvedAt, &p.TerminalCode); err != nil {
		return D1LProvenanceRecord{}, err
	}
	copy(p.TargetFingerprint[:], target)
	copy(p.EvidenceDigest[:], evidence)
	if err := tx.Commit(); err != nil {
		return D1LProvenanceRecord{}, ErrD1LProvenanceUnknown
	}
	return p, nil
}

// RecoverReserved is a concise owner recovery alias for callers that only
// need the outcome and not the terminal projection.
func (l *D1LProvenanceLedger) RecoverReserved(ctx context.Context, r D1LProvenanceReservation, code string) error {
	_, err := l.ResolveReservedNonCommit(ctx, r, code)
	return err
}

func assignD1LIssueID(dst *uuid.UUID, raw sql.NullString) error {
	if dst == nil {
		return ErrD1LProvenanceUnknown
	}
	*dst = uuid.Nil
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return nil
	}
	id, err := uuid.Parse(raw.String)
	if err != nil || id == uuid.Nil {
		return ErrD1LProvenanceUnknown
	}
	*dst = id
	return nil
}

func validD1LProvenanceBinding(p D1LProvenanceRecord) bool {
	return p.OwnerIdentity == upstream.D1OwnerIdentity && strings.TrimSpace(p.OwnerVersion) != "" && p.OperationID != uuid.Nil && p.AttemptID != uuid.Nil && p.TargetFingerprint != [32]byte{} && p.EvidenceDigest != [32]byte{} && p.RouteIntent == upstream.D1OwnerIssueRoute && p.RouteIntent == strings.TrimSpace(p.RouteIntent)
}

func validD1LProvenanceRecord(p D1LProvenanceRecord) bool {
	return p.ID != uuid.Nil && p.Version > 0 && validD1LProvenanceBinding(p)
}

func validD1LProvenanceTerminalCode(code string) bool {
	switch code {
	case "T1_ROLLBACK", "OWNER_INVALIDATED", "PROVIDER_REJECTED", "RECOVERY_REQUIRED", "CONSUME_ABORTED":
		return true
	default:
		return false
	}
}

func isUniqueViolation(err error) bool {
	return err != nil && (containsText(err.Error(), "unique") || containsText(err.Error(), "duplicate key"))
}
func containsText(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if equalFold(s[i:i+len(sub)], sub) {
				return true
			}
		}
		return false
	})()
}
func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i], b[i]
		if x >= 'A' && x <= 'Z' {
			x += 'a' - 'A'
		}
		if y >= 'A' && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}
