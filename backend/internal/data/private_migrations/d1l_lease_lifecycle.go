package migrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

var (
	ErrD1LLifecycle     = errors.New("D1-L lease lifecycle rejected")
	ErrD1LLeaseUnknown  = errors.New("D1-L lease is unknown")
	ErrD1LLeaseBinding  = errors.New("D1-L lease binding mismatch")
	ErrD1LLeaseTerminal = errors.New("D1-L lease is terminal")
)

type D1LLeaseIdentity struct {
	LeaseID, OperationID, AttemptID uuid.UUID
	Generation                      int64
	TargetFingerprint               []byte
	EvidenceDigest                  []byte
}
type D1LLeaseInspection struct {
	Identity                                     D1LLeaseIdentity
	TargetFingerprint, EvidenceDigest            []byte
	Status                                       string
	IssuedAt, ExpiresAt, ActivatedAt, TerminalAt time.Time
	TerminalCode, QuarantineCode                 string
}

type d1LLockedLease struct {
	inspection               D1LLeaseInspection
	capabilityVerifierDigest []byte
}

// lockLeaseForLifecycle takes the row lock using the immutable lease primary
// key, then validates the complete owner binding while that lock is held. A
// caller cannot transition a lease by presenting only a guessed lifecycle
// identity or by substituting either persisted digest.
func lockLeaseForLifecycle(ctx context.Context, tx *sql.Tx, id D1LLeaseIdentity) (d1LLockedLease, error) {
	var locked d1LLockedLease
	var target, evidence, verifier []byte
	var activated, terminal sql.NullTime
	var terminalCode, quarantineCode sql.NullString
	err := tx.QueryRowContext(ctx, `
SELECT lease_id, operation_id, attempt_id, generation,
       target_fingerprint, evidence_digest, status, issued_at, expires_at,
       activated_at, terminal_at, terminal_code, quarantine_code,
       capability_verifier_digest
FROM security_control.admission_leases
WHERE lease_id = $1
FOR UPDATE`, id.LeaseID).Scan(
		&locked.inspection.Identity.LeaseID,
		&locked.inspection.Identity.OperationID,
		&locked.inspection.Identity.AttemptID,
		&locked.inspection.Identity.Generation,
		&target, &evidence, &locked.inspection.Status,
		&locked.inspection.IssuedAt, &locked.inspection.ExpiresAt,
		&activated, &terminal, &terminalCode, &quarantineCode, &verifier,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return d1LLockedLease{}, ErrD1LLeaseUnknown
	}
	if err != nil {
		return d1LLockedLease{}, err
	}
	locked.inspection.TargetFingerprint = append([]byte(nil), target...)
	locked.inspection.EvidenceDigest = append([]byte(nil), evidence...)
	locked.inspection.Identity.TargetFingerprint = append([]byte(nil), target...)
	locked.inspection.Identity.EvidenceDigest = append([]byte(nil), evidence...)
	locked.capabilityVerifierDigest = append([]byte(nil), verifier...)
	if activated.Valid {
		locked.inspection.ActivatedAt = activated.Time
	}
	if terminal.Valid {
		locked.inspection.TerminalAt = terminal.Time
	}
	if terminalCode.Valid {
		locked.inspection.TerminalCode = terminalCode.String
	}
	if quarantineCode.Valid {
		locked.inspection.QuarantineCode = quarantineCode.String
	}
	if !sameLeaseIdentity(id, locked.inspection.Identity) {
		return d1LLockedLease{}, ErrD1LLeaseBinding
	}
	return locked, nil
}

func (l *D1LProvenanceLedger) InspectLease(ctx context.Context, id D1LLeaseIdentity) (D1LLeaseInspection, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !validLeaseIdentity(id) {
		return D1LLeaseInspection{}, ErrD1LLeaseBinding
	}
	if err := l.ensureLeaseDB(ctx); err != nil {
		return D1LLeaseInspection{}, err
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return D1LLeaseInspection{}, err
	}
	defer tx.Rollback()
	locked, err := lockLeaseForLifecycle(ctx, tx, id)
	if err != nil {
		return D1LLeaseInspection{}, err
	}
	// This clock read deliberately follows the potentially blocking row lock.
	var dbNow time.Time
	if err := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&dbNow); err != nil {
		return D1LLeaseInspection{}, err
	}
	out := locked.inspection
	if out.Status == d1LLeaseStatusIssued && !out.ExpiresAt.After(dbNow) {
		if err = tx.QueryRowContext(ctx, `UPDATE security_control.admission_leases SET status='EXPIRED',terminal_at=clock_timestamp(),terminal_code='EXPIRED' WHERE lease_id=$1 AND operation_id=$2 AND attempt_id=$3 AND generation=$4 AND target_fingerprint=$5 AND evidence_digest=$6 AND status='ISSUED' RETURNING terminal_at,terminal_code`, id.LeaseID, id.OperationID, id.AttemptID, id.Generation, id.TargetFingerprint, id.EvidenceDigest).Scan(&out.TerminalAt, &out.TerminalCode); err != nil {
			return D1LLeaseInspection{}, err
		}
		out.Status = "EXPIRED"
	} else if (out.Status == d1LLeaseStatusActive || out.Status == "QUARANTINE_PENDING") && !out.ExpiresAt.After(dbNow) {
		if err = tx.QueryRowContext(ctx, `UPDATE security_control.admission_leases SET status='REVOKED',terminal_at=clock_timestamp(),terminal_code='EXPIRED' WHERE lease_id=$1 AND operation_id=$2 AND attempt_id=$3 AND generation=$4 AND target_fingerprint=$5 AND evidence_digest=$6 AND status IN ('ACTIVE','QUARANTINE_PENDING') RETURNING terminal_at,terminal_code`, id.LeaseID, id.OperationID, id.AttemptID, id.Generation, id.TargetFingerprint, id.EvidenceDigest).Scan(&out.TerminalAt, &out.TerminalCode); err != nil {
			return D1LLeaseInspection{}, err
		}
		out.Status = "REVOKED"
	}
	if err = tx.Commit(); err != nil {
		return D1LLeaseInspection{}, ErrD1LProvenanceUnknown
	}
	return out, nil
}

// activateIssuedLease performs one owner-mediated ISSUED->ACTIVE transition.
// The raw presentation is accepted only at this private D1 owner seam and is
// never persisted or returned in the safe inspection result.
func (l *D1LProvenanceLedger) activateIssuedLease(ctx context.Context, lease *d1LLease) error {
	if lease == nil || len(lease.activation) == 0 {
		return ErrD1LLeaseBinding
	}
	defer func() {
		for i := range lease.activation {
			lease.activation[i] = 0
		}
		lease.activation = nil
	}()
	return l.activate(ctx, D1LLeaseIdentity{
		LeaseID: lease.LeaseID, OperationID: lease.OperationID, AttemptID: lease.AttemptID,
		Generation: lease.Generation, TargetFingerprint: lease.TargetFingerprint,
		EvidenceDigest: lease.EvidenceDigest,
	}, lease.activation)
}

// activate is deliberately package-private: presentation is bearer material
// and must only cross the owner-private lease seam. Safe callers use
// InspectLease and the lifecycle methods below.
func (l *D1LProvenanceLedger) activate(ctx context.Context, id D1LLeaseIdentity, presentation []byte) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if !validLeaseIdentity(id) || len(presentation) == 0 {
		return ErrD1LLeaseBinding
	}
	if err := l.ensureLeaseDB(ctx); err != nil {
		return err
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	locked, err := lockLeaseForLifecycle(ctx, tx, id)
	if err != nil {
		return err
	}
	expires := locked.inspection.ExpiresAt
	if locked.inspection.Status != "ISSUED" {
		return ErrD1LLeaseTerminal
	}
	var dbNow time.Time
	if err := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&dbNow); err != nil {
		return err
	}
	if !expires.After(dbNow) {
		if _, err := tx.ExecContext(ctx, `UPDATE security_control.admission_leases SET status='EXPIRED',terminal_at=clock_timestamp(),terminal_code='EXPIRED' WHERE lease_id=$1 AND operation_id=$2 AND attempt_id=$3 AND generation=$4 AND target_fingerprint=$5 AND evidence_digest=$6`, id.LeaseID, id.OperationID, id.AttemptID, id.Generation, id.TargetFingerprint, id.EvidenceDigest); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return ErrD1LProvenanceUnknown
		}
		return ErrD1LLeaseTerminal
	}
	lease := d1LLease{LeaseID: id.LeaseID, OperationID: id.OperationID, AttemptID: id.AttemptID, Generation: id.Generation, TargetFingerprint: locked.inspection.TargetFingerprint, EvidenceDigest: locked.inspection.EvidenceDigest, CapabilityVerifierDigest: locked.capabilityVerifierDigest, ExpiresAt: expires}
	if !verifyD1LActivation(presentation, lease) {
		return ErrD1LLeaseBinding
	}
	if _, err = tx.ExecContext(ctx, `UPDATE security_control.admission_leases SET status='ACTIVE',activated_at=clock_timestamp() WHERE lease_id=$1 AND operation_id=$2 AND attempt_id=$3 AND generation=$4 AND target_fingerprint=$5 AND evidence_digest=$6 AND status='ISSUED'`, id.LeaseID, id.OperationID, id.AttemptID, id.Generation, id.TargetFingerprint, id.EvidenceDigest); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return ErrD1LProvenanceUnknown
	}
	return nil
}

// ConsumeLease performs the ACTIVE consume decision while holding the exact
// lease row lock. Expiry is evaluated with the database clock, so a caller's
// wall clock cannot consume an already-expired lease and a consume/revoke race
// has one serialized winner.
func (l *D1LProvenanceLedger) ConsumeLease(ctx context.Context, id D1LLeaseIdentity) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if !validLeaseIdentity(id) {
		return ErrD1LLeaseBinding
	}
	if err := l.ensureLeaseDB(ctx); err != nil {
		return err
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	locked, err := lockLeaseForLifecycle(ctx, tx, id)
	if err != nil {
		return err
	}
	expiresAt := locked.inspection.ExpiresAt
	if locked.inspection.Status != d1LLeaseStatusActive {
		return ErrD1LLeaseTerminal
	}
	var dbNow time.Time
	if err = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&dbNow); err != nil {
		return err
	}
	if !expiresAt.After(dbNow) {
		if _, err = tx.ExecContext(ctx, `UPDATE security_control.admission_leases SET status='REVOKED',terminal_at=clock_timestamp(),terminal_code='EXPIRED' WHERE lease_id=$1 AND operation_id=$2 AND attempt_id=$3 AND generation=$4 AND target_fingerprint=$5 AND evidence_digest=$6 AND status='ACTIVE'`, id.LeaseID, id.OperationID, id.AttemptID, id.Generation, id.TargetFingerprint, id.EvidenceDigest); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return ErrD1LProvenanceUnknown
		}
		return ErrD1LLeaseTerminal
	}
	result, err := tx.ExecContext(ctx, `UPDATE security_control.admission_leases SET status='CONSUMED',terminal_at=clock_timestamp(),terminal_code='CONSUMED' WHERE lease_id=$1 AND operation_id=$2 AND attempt_id=$3 AND generation=$4 AND target_fingerprint=$5 AND evidence_digest=$6 AND status='ACTIVE' AND expires_at>$7`, id.LeaseID, id.OperationID, id.AttemptID, id.Generation, id.TargetFingerprint, id.EvidenceDigest, dbNow)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return ErrD1LLeaseTerminal
	}
	if err = tx.Commit(); err != nil {
		return ErrD1LProvenanceUnknown
	}
	return nil
}

func (l *D1LProvenanceLedger) RevokeLease(ctx context.Context, id D1LLeaseIdentity, code string) error {
	if !validD1LLeaseCode(code) {
		return ErrD1LLifecycle
	}
	return l.transitionLease(ctx, id, "REVOKED", code, `status IN ('ISSUED','ACTIVE','QUARANTINE_PENDING')`)
}

// BeginQuarantineLease advances ACTIVE to the non-terminal pending state. The
// final quarantine code/timestamp are written only by QuarantineLease.
func (l *D1LProvenanceLedger) BeginQuarantineLease(ctx context.Context, id D1LLeaseIdentity) error {
	return l.transitionLease(ctx, id, "QUARANTINE_PENDING", "", `status='ACTIVE'`)
}

func (l *D1LProvenanceLedger) QuarantineLease(ctx context.Context, id D1LLeaseIdentity, code string) error {
	if !validD1LLeaseCode(code) {
		return ErrD1LLifecycle
	}
	return l.transitionLease(ctx, id, "QUARANTINED", code, `status='QUARANTINE_PENDING'`)
}
func (l *D1LProvenanceLedger) transitionLease(ctx context.Context, id D1LLeaseIdentity, next, code, from string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if !validLeaseIdentity(id) {
		return ErrD1LLeaseBinding
	}
	if err := l.ensureLeaseDB(ctx); err != nil {
		return err
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	locked, err := lockLeaseForLifecycle(ctx, tx, id)
	if err != nil {
		return err
	}
	status := locked.inspection.Status
	expiresAt := locked.inspection.ExpiresAt
	var dbNow time.Time
	if err = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&dbNow); err != nil {
		return err
	}
	if (status == "ISSUED" || status == "ACTIVE" || status == "QUARANTINE_PENDING") && !expiresAt.After(dbNow) {
		if _, err = tx.ExecContext(ctx, `UPDATE security_control.admission_leases SET status=CASE WHEN status='ISSUED' THEN 'EXPIRED' ELSE 'REVOKED' END,terminal_at=clock_timestamp(),terminal_code='EXPIRED' WHERE lease_id=$1 AND operation_id=$2 AND attempt_id=$3 AND generation=$4 AND target_fingerprint=$5 AND evidence_digest=$6 AND status IN ('ISSUED','ACTIVE','QUARANTINE_PENDING')`, id.LeaseID, id.OperationID, id.AttemptID, id.Generation, id.TargetFingerprint, id.EvidenceDigest); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return ErrD1LProvenanceUnknown
		}
		return ErrD1LLeaseTerminal
	}
	var result sql.Result
	var execErr error
	switch next {
	case "QUARANTINE_PENDING":
		result, execErr = tx.ExecContext(ctx, fmt.Sprintf(`UPDATE security_control.admission_leases SET status='QUARANTINE_PENDING' WHERE lease_id=$1 AND operation_id=$2 AND attempt_id=$3 AND generation=$4 AND target_fingerprint=$5 AND evidence_digest=$6 AND %s`, from), id.LeaseID, id.OperationID, id.AttemptID, id.Generation, id.TargetFingerprint, id.EvidenceDigest)
	case "QUARANTINED":
		result, execErr = tx.ExecContext(ctx, fmt.Sprintf(`UPDATE security_control.admission_leases SET status='QUARANTINED',quarantined_at=clock_timestamp(),quarantine_code=$1 WHERE lease_id=$2 AND operation_id=$3 AND attempt_id=$4 AND generation=$5 AND target_fingerprint=$6 AND evidence_digest=$7 AND %s`, from), code, id.LeaseID, id.OperationID, id.AttemptID, id.Generation, id.TargetFingerprint, id.EvidenceDigest)
	default:
		result, execErr = tx.ExecContext(ctx, fmt.Sprintf(`UPDATE security_control.admission_leases SET status=$1,terminal_at=clock_timestamp(),terminal_code=$2 WHERE lease_id=$3 AND operation_id=$4 AND attempt_id=$5 AND generation=$6 AND target_fingerprint=$7 AND evidence_digest=$8 AND %s`, from), next, code, id.LeaseID, id.OperationID, id.AttemptID, id.Generation, id.TargetFingerprint, id.EvidenceDigest)
	}
	if execErr != nil {
		return execErr
	}
	n, rowsErr := result.RowsAffected()
	if rowsErr != nil {
		return rowsErr
	}
	if status == "CONSUMED" || status == "EXPIRED" || status == "REVOKED" || n != 1 {
		return ErrD1LLeaseTerminal
	}
	if err = tx.Commit(); err != nil {
		return ErrD1LProvenanceUnknown
	}
	return nil
}

// RecoverLease is the owner-side fail-closed recovery path for an outcome
// whose durable transition is uncertain. It never activates, reissues, or
// changes the lease tuple. ISSUED is terminalized as EXPIRED; every other
// recoverable non-terminal state is terminalized as REVOKED. If commit proof
// is unavailable, the returned projection is explicitly UNKNOWN.
func (l *D1LProvenanceLedger) RecoverLease(ctx context.Context, id D1LLeaseIdentity, recoveryDigest []byte, code string) (D1LLeaseInspection, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !validLeaseIdentity(id) || len(recoveryDigest) != 32 || !validD1LRecoveryCode(code) {
		return D1LLeaseInspection{}, ErrD1LLeaseBinding
	}
	if err := l.ensureLeaseDB(ctx); err != nil {
		return D1LLeaseInspection{}, err
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return D1LLeaseInspection{}, err
	}
	defer tx.Rollback()
	locked, err := lockLeaseForLifecycle(ctx, tx, id)
	if err != nil {
		return D1LLeaseInspection{}, err
	}
	if locked.inspection.Status != "ISSUED" && locked.inspection.Status != "ACTIVE" && locked.inspection.Status != "QUARANTINE_PENDING" && locked.inspection.Status != "QUARANTINED" {
		return D1LLeaseInspection{}, ErrD1LLeaseTerminal
	}
	var out d1lLeaseInspectionRecovery
	err = tx.QueryRowContext(ctx, `
UPDATE security_control.admission_leases
SET status = CASE WHEN status = 'ISSUED' THEN 'EXPIRED' ELSE 'REVOKED' END,
    recovery_digest = $7,
    terminal_at = clock_timestamp(),
    terminal_code = $8
WHERE lease_id = $1 AND operation_id = $2 AND attempt_id = $3 AND generation = $4
  AND target_fingerprint = $5 AND evidence_digest = $6
  AND status IN ('ISSUED','ACTIVE','QUARANTINE_PENDING','QUARANTINED')
RETURNING status, terminal_at, terminal_code`,
		id.LeaseID, id.OperationID, id.AttemptID, id.Generation,
		id.TargetFingerprint, id.EvidenceDigest, recoveryDigest, code,
	).Scan(&out.status, &out.terminalAt, &out.terminalCode)
	if errors.Is(err, sql.ErrNoRows) {
		return D1LLeaseInspection{}, ErrD1LLeaseTerminal
	}
	if err != nil {
		return D1LLeaseInspection{}, err
	}
	inspection := locked.inspection
	inspection.Status = out.status
	inspection.TerminalAt = out.terminalAt
	inspection.TerminalCode = out.terminalCode
	if err := tx.Commit(); err != nil {
		inspection.Status = "UNKNOWN"
		inspection.TerminalAt = time.Time{}
		inspection.TerminalCode = ""
		return inspection, ErrD1LProvenanceUnknown
	}
	return inspection, nil
}

type d1lLeaseInspectionRecovery struct {
	status, terminalCode string
	terminalAt           time.Time
}

func validLeaseIdentity(id D1LLeaseIdentity) bool {
	return id.LeaseID != uuid.Nil && id.OperationID != uuid.Nil && id.AttemptID != uuid.Nil && id.Generation > 0 && len(id.TargetFingerprint) == 32 && len(id.EvidenceDigest) == 32
}

func validD1LLeaseCode(code string) bool {
	switch code {
	case "OWNER_INVALIDATED", "REVOKED_BY_OWNER", "EXPIRED", "CONSUMED":
		return true
	default:
		return false
	}
}

func validD1LRecoveryCode(code string) bool {
	return code == "RECOVERY_REQUIRED" || code == "OWNER_INVALIDATED"
}

func sameLeaseIdentity(a, b D1LLeaseIdentity) bool {
	return a.LeaseID == b.LeaseID && a.OperationID == b.OperationID && a.AttemptID == b.AttemptID && a.Generation == b.Generation && equalBytes(a.TargetFingerprint, b.TargetFingerprint) && equalBytes(a.EvidenceDigest, b.EvidenceDigest)
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var d byte
	for i := range a {
		d |= a[i] ^ b[i]
	}
	return d == 0
}
func (l *D1LProvenanceLedger) ensureLeaseDB(ctx context.Context) error {
	if l == nil || l.db == nil {
		return ErrD1LLeaseStoreUnavailable
	}
	return ensureD1LNextLedgerReady(ctx, l.db)
}
