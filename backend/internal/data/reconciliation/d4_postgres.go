package reconciliation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
)

// PostgresD4Store is the production-shaped D5 adapter for the accepted D4
// ledger and observational journal contracts. D3 authority never crosses it.
type PostgresD4Store struct {
	db *sql.DB
}

func NewPostgresD4Store(db *sql.DB) *PostgresD4Store { return &PostgresD4Store{db: db} }

type D4ToD5BundleIngress interface {
	AcceptD4ToD5Bundle(context.Context, D4ToD5Bundle) (D4Record, error)
}

var (
	_ D4Ledger            = (*PostgresD4Store)(nil)
	_ D4Journal           = (*PostgresD4Store)(nil)
	_ D4ToD5BundleIngress = (*PostgresD4Store)(nil)
)

const d5LedgerTable = `"d4_operation_ledger"`
const d5JournalTable = `"d4_operation_journal"`

func targetBytes(tuple D4OwnerTuple) []byte {
	target := tuple.TargetFingerprint()
	return target[:]
}

func (s *PostgresD4Store) queryer() (interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("D5 PostgreSQL store is unavailable")
	}
	return s.db, nil
}

const d5LedgerColumns = `operation_id, attempt_id, target_fingerprint, generation,
 state, claim_id, disposition, commit_status, post_verification_status,
 cleanup_status, certainty, unknown, recovery_required, recovery_class,
 replay_disposition, safe_result, updated_at`

func scanD5Record(scanner interface{ Scan(...any) error }) (D4Record, error) {
	var operation, attempt, state string
	var target []byte
	var generation int64
	var claim sql.NullString
	var disposition, commitStatus, postStatus, cleanupStatus, certainty sql.NullString
	var unknown, recoveryRequired bool
	var recovery string
	var replay sql.NullString
	var safeJSON []byte
	var updated time.Time
	if err := scanner.Scan(&operation, &attempt, &target, &generation, &state, &claim, &disposition, &commitStatus, &postStatus, &cleanupStatus, &certainty, &unknown, &recoveryRequired, &recovery, &replay, &safeJSON, &updated); err != nil {
		return D4Record{}, err
	}
	operationID, err := uuid.Parse(operation)
	if err != nil {
		return D4Record{}, fmt.Errorf("parse operation identity: %w", err)
	}
	attemptID, err := uuid.Parse(attempt)
	if err != nil {
		return D4Record{}, fmt.Errorf("parse attempt identity: %w", err)
	}
	if len(target) != 32 || generation <= 0 {
		return D4Record{}, errors.New("invalid D5 tuple storage")
	}
	tuple, err := NewD4OwnerTuple(operationID, attemptID, target, uint64(generation))
	if err != nil {
		return D4Record{}, err
	}
	record := D4Record{Tuple: tuple, State: D4State(state), UpdatedAt: updated, Recovery: D4RecoveryClass(recovery)}
	if claim.Valid {
		record.ClaimID, err = uuid.Parse(claim.String)
		if err != nil {
			return D4Record{}, fmt.Errorf("invalid D5 claim identity: %w", err)
		}
	}
	if len(safeJSON) != 0 {
		var result D4SafeResult
		if err := json.Unmarshal(safeJSON, &result); err != nil {
			return D4Record{}, fmt.Errorf("decode D5 safe result: %w", err)
		}
		if err := result.ValidateFor(tuple); err != nil {
			return D4Record{}, fmt.Errorf("validate D5 safe result: %w", err)
		}
		stored := map[string]string{
			"disposition": disposition.String, "commit_status": commitStatus.String,
			"post_verification_status": postStatus.String, "cleanup_status": cleanupStatus.String,
			"certainty": certainty.String, "replay_disposition": replay.String,
		}
		want := map[string]string{
			"disposition": string(result.Disposition), "commit_status": string(result.CommitStatus),
			"post_verification_status": string(result.PostVerificationStatus), "cleanup_status": string(result.CleanupStatus),
			"certainty": string(result.Certainty), "replay_disposition": string(result.ReplayDisposition),
		}
		for name, value := range want {
			if !disposition.Valid && name == "disposition" || !commitStatus.Valid && name == "commit_status" || !postStatus.Valid && name == "post_verification_status" || !cleanupStatus.Valid && name == "cleanup_status" || !certainty.Valid && name == "certainty" || !replay.Valid && name == "replay_disposition" || stored[name] != value {
				return D4Record{}, fmt.Errorf("D5 safe result scalar %s disagrees with JSON", name)
			}
		}
		if unknown != result.Unknown || recoveryRequired != result.RecoveryRequired {
			return D4Record{}, errors.New("D5 safe result boolean scalars disagree with JSON")
		}
		record.Result = &result
	} else if disposition.Valid || commitStatus.Valid || postStatus.Valid || cleanupStatus.Valid || certainty.Valid || replay.Valid || unknown || recoveryRequired {
		return D4Record{}, errors.New("D5 scalar result fields exist without safe result JSON")
	}
	return record, nil
}

// AcceptD4ToD5Bundle validates and correlates a complete safe bundle without
// treating it as authority. It creates only a RECEIVED row when absent; any
// later state mutation still requires the D4 owner event/CAS contract.
func (s *PostgresD4Store) AcceptD4ToD5Bundle(ctx context.Context, bundle D4ToD5Bundle) (D4Record, error) {
	if err := bundle.ValidateForD5(); err != nil {
		return D4Record{}, err
	}
	if s == nil || s.db == nil {
		return D4Record{}, errors.New("D5 PostgreSQL store is unavailable")
	}
	correlation, err := json.Marshal(bundle.Correlation)
	if err != nil {
		return D4Record{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return D4Record{}, err
	}
	defer tx.Rollback()
	if bundle.State == D4Received {
		if _, err := tx.ExecContext(ctx, `INSERT INTO `+d5LedgerTable+` (operation_id,attempt_id,target_fingerprint,generation,state,updated_at) VALUES ($1,$2,$3,$4,'RECEIVED',$5) ON CONFLICT (operation_id,attempt_id) DO NOTHING`, bundle.Tuple.OperationID(), bundle.Tuple.AttemptID(), targetBytes(bundle.Tuple), bundle.Tuple.Generation(), bundle.CreatedAt.UTC()); err != nil {
			return D4Record{}, fmt.Errorf("D5 bundle receive: %w", err)
		}
	}
	record, err := scanD5Record(tx.QueryRowContext(ctx, `SELECT `+d5LedgerColumns+` FROM `+d5LedgerTable+` WHERE operation_id=$1 AND attempt_id=$2 AND target_fingerprint=$3 AND generation=$4 FOR UPDATE`, bundle.Tuple.OperationID(), bundle.Tuple.AttemptID(), targetBytes(bundle.Tuple), bundle.Tuple.Generation()))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return D4Record{}, &D4Error{Class: D4ErrorStale, Cause: errors.New("D4-to-D5 bundle tuple does not exist")}
		}
		return D4Record{}, err
	}
	if record.State != bundle.State {
		return D4Record{}, &D4Error{Class: D4ErrorCASConflict, Cause: errors.New("D4-to-D5 bundle state does not match durable record")}
	}
	var existing []byte
	if err := tx.QueryRowContext(ctx, `SELECT safe_correlation FROM `+d5LedgerTable+` WHERE operation_id=$1 AND attempt_id=$2 AND target_fingerprint=$3 AND generation=$4`, bundle.Tuple.OperationID(), bundle.Tuple.AttemptID(), targetBytes(bundle.Tuple), bundle.Tuple.Generation()).Scan(&existing); err != nil {
		return D4Record{}, err
	}
	if len(existing) != 0 {
		var prior D4SafeCorrelation
		if err := json.Unmarshal(existing, &prior); err != nil {
			return D4Record{}, fmt.Errorf("decode stored D5 safe correlation: %w", err)
		}
		if prior != *bundle.Correlation {
			return D4Record{}, &D4Error{Class: D4ErrorCASConflict, Cause: errors.New("D4-to-D5 bundle correlation conflicts with durable record")}
		}
	} else if _, err := tx.ExecContext(ctx, `UPDATE `+d5LedgerTable+` SET safe_correlation=$1::jsonb, updated_at=now() WHERE operation_id=$2 AND attempt_id=$3 AND target_fingerprint=$4 AND generation=$5 AND state=$6 AND safe_correlation IS NULL`, correlation, bundle.Tuple.OperationID(), bundle.Tuple.AttemptID(), targetBytes(bundle.Tuple), bundle.Tuple.Generation(), bundle.State); err != nil {
		return D4Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return D4Record{}, err
	}
	return s.Get(ctx, bundle.Tuple)
}

func (s *PostgresD4Store) Receive(ctx context.Context, request D4ReceiveRequest) (D4Record, error) {
	if err := contextErr(ctx); err != nil {
		return D4Record{}, err
	}
	if !request.Tuple.Valid() || request.ReceivedAt.IsZero() {
		return D4Record{}, &D4Error{Class: D4ErrorMalformed, Cause: errors.New("receive requires tuple and time")}
	}
	if s == nil || s.db == nil {
		return D4Record{}, errors.New("D5 PostgreSQL store is unavailable")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO `+d5LedgerTable+` (operation_id, attempt_id, target_fingerprint, generation, state, updated_at) VALUES ($1,$2,$3,$4,'RECEIVED',$5) ON CONFLICT (operation_id, attempt_id) DO NOTHING`, request.Tuple.OperationID(), request.Tuple.AttemptID(), targetBytes(request.Tuple), request.Tuple.Generation(), request.ReceivedAt.UTC())
	if err != nil {
		return D4Record{}, fmt.Errorf("D5 receive: %w", err)
	}
	return s.Get(ctx, request.Tuple)
}

func (s *PostgresD4Store) Claim(ctx context.Context, request D4ClaimRequest) (D4ClaimResult, error) {
	if err := contextErr(ctx); err != nil {
		return D4ClaimResult{}, err
	}
	if !request.Tuple.Valid() || request.WorkerID == uuid.Nil {
		return D4ClaimResult{}, &D4Error{Class: D4ErrorMalformed, Cause: errors.New("claim requires tuple and worker identity")}
	}
	if !request.Approval.approved || request.Approval.kind != D4EventAdmit || !request.Approval.tuple.Equal(request.Tuple) {
		return D4ClaimResult{}, &D4Error{Class: D4ErrorNonOwner, Cause: errors.New("claim requires owner admission approval")}
	}
	if s == nil || s.db == nil {
		return D4ClaimResult{}, errors.New("D5 PostgreSQL store is unavailable")
	}
	return s.claimWithTransaction(ctx, request)
}

func (s *PostgresD4Store) claimWithTransaction(ctx context.Context, request D4ClaimRequest) (D4ClaimResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return D4ClaimResult{}, err
	}
	defer tx.Rollback()
	var record D4Record
	row := tx.QueryRowContext(ctx, `UPDATE `+d5LedgerTable+` SET state='ADMITTED', claim_id=$5, updated_at=now() WHERE operation_id=$1 AND attempt_id=$2 AND target_fingerprint=$3 AND generation=$4 AND state='RECEIVED' AND claim_id IS NULL RETURNING `+d5LedgerColumns, request.Tuple.OperationID(), request.Tuple.AttemptID(), targetBytes(request.Tuple), request.Tuple.Generation(), request.WorkerID)
	record, err = scanD5Record(row)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return D4ClaimResult{}, err
		}
		return D4ClaimResult{Record: record, Won: true, ClaimID: record.ClaimID}, nil
	}
	current, getErr := s.getWithQueryer(ctx, tx, request.Tuple)
	if getErr != nil {
		return D4ClaimResult{}, getErr
	}
	if current.State == D4Received {
		return D4ClaimResult{}, &D4Error{Class: D4ErrorCASConflict, Cause: errors.New("claim lost concurrently")}
	}
	if current.ClaimID == uuid.Nil {
		return D4ClaimResult{}, &D4Error{Class: D4ErrorCASConflict, Cause: errors.New("existing record has no durable claim identity")}
	}
	if err := tx.Commit(); err != nil {
		return D4ClaimResult{}, err
	}
	return D4ClaimResult{Record: current, Won: false, ClaimID: current.ClaimID}, nil
}

func (s *PostgresD4Store) Transition(ctx context.Context, request D4TransitionRequest) (D4Record, error) {
	if err := contextErr(ctx); err != nil {
		return D4Record{}, err
	}
	if s == nil || s.db == nil {
		return D4Record{}, errors.New("D5 PostgreSQL store is unavailable")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return D4Record{}, err
	}
	defer tx.Rollback()
	record, err := s.getWithQueryer(ctx, tx, request.Tuple)
	if err != nil {
		return D4Record{}, err
	}
	if !record.Tuple.Equal(request.Event.Tuple) {
		return D4Record{}, tupleMismatchError(record.Tuple, request.Event.Tuple)
	}
	if record.State != request.Expected {
		return D4Record{}, &D4Error{Class: D4ErrorCASConflict, Cause: errors.New("expected state no longer matches")}
	}
	if err := validateD4EventForRecord(record, request.Event, request.Revalidator); err != nil {
		return D4Record{}, err
	}
	if request.ClaimID == uuid.Nil {
		preClaim := record.ClaimID == uuid.Nil && ((record.State == D4Received && (request.Event.Kind == D4EventTerminal || request.Event.Kind == D4EventRequireRecovery)) || (record.State == D4RecoveryRequired && request.Event.Kind == D4EventResolveRecovery))
		if !preClaim {
			return D4Record{}, &D4Error{Class: D4ErrorCASConflict, Cause: errors.New("claim is required for this progression")}
		}
	} else if record.ClaimID == uuid.Nil || record.ClaimID != request.ClaimID {
		return D4Record{}, &D4Error{Class: D4ErrorCASConflict, Cause: errors.New("worker claim is no longer current")}
	}
	next := d4TransitionTable[record.State][request.Event.Kind]
	var safeJSON []byte
	var args []any
	setResult := request.Event.Result != nil
	if setResult {
		safeJSON, err = json.Marshal(request.Event.Result)
		if err != nil {
			return D4Record{}, err
		}
	} else {
		safeJSON = []byte("null")
	}
	var disposition, commitStatus, postStatus, cleanupStatus, certainty, replay any
	var unknown, recoveryRequired any
	if setResult {
		r := request.Event.Result
		disposition, commitStatus, postStatus, cleanupStatus, certainty = r.Disposition, r.CommitStatus, r.PostVerificationStatus, r.CleanupStatus, r.Certainty
		unknown, recoveryRequired, replay = r.Unknown, r.RecoveryRequired, r.ReplayDisposition
	}
	query := `UPDATE ` + d5LedgerTable + ` SET state=$1, disposition=CASE WHEN $2 THEN $3 ELSE disposition END, commit_status=CASE WHEN $2 THEN $4 ELSE commit_status END, post_verification_status=CASE WHEN $2 THEN $5 ELSE post_verification_status END, cleanup_status=CASE WHEN $2 THEN $6 ELSE cleanup_status END, certainty=CASE WHEN $2 THEN $7 ELSE certainty END, unknown=CASE WHEN $2 THEN $8 ELSE unknown END, recovery_required=CASE WHEN $2 THEN $9 ELSE recovery_required END, replay_disposition=CASE WHEN $2 THEN $10 ELSE replay_disposition END, recovery_class=CASE WHEN $11 <> '' THEN $11 ELSE recovery_class END, safe_result=CASE WHEN $2 THEN $12::jsonb ELSE safe_result END, updated_at=now() WHERE operation_id=$13 AND attempt_id=$14 AND target_fingerprint=$15 AND generation=$16 AND state=$17 AND claim_id IS NOT DISTINCT FROM $18 RETURNING ` + d5LedgerColumns
	args = []any{next, setResult, disposition, commitStatus, postStatus, cleanupStatus, certainty, unknown, recoveryRequired, replay, request.Event.Recovery, safeJSON, request.Tuple.OperationID(), request.Tuple.AttemptID(), targetBytes(request.Tuple), request.Tuple.Generation(), request.Expected, nullableUUID(request.ClaimID)}
	updated, err := scanD5Record(tx.QueryRowContext(ctx, query, args...))
	if err != nil {
		return D4Record{}, &D4Error{Class: D4ErrorCASConflict, Cause: err}
	}
	event := D4JournalEvent{EventID: uuid.New(), Version: uint64(time.Now().UnixNano()), Tuple: request.Tuple, From: record.State, To: next, Result: request.Event.Result, Recovery: request.Event.Recovery, OccurredAt: time.Now().UTC()}
	if err := appendJournalTx(ctx, tx, event); err != nil {
		return D4Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return D4Record{}, err
	}
	return updated, nil
}

func nullableUUID(value uuid.UUID) any {
	if value == uuid.Nil {
		return nil
	}
	return value
}

func (s *PostgresD4Store) RecordSafeResult(ctx context.Context, tuple D4OwnerTuple, expected D4State, claimID uuid.UUID, result D4SafeResult) (D4Record, error) {
	approval, err := NewD4OwnerApproval(tuple, D4EventRecordResult, time.Now().UTC())
	if err != nil {
		return D4Record{}, err
	}
	return s.Transition(ctx, D4TransitionRequest{Tuple: tuple, Expected: expected, ClaimID: claimID, Event: D4OwnerEvent{Kind: D4EventRecordResult, Tuple: tuple, Approval: approval, Result: &result}})
}

func (s *PostgresD4Store) MarkRecovery(ctx context.Context, tuple D4OwnerTuple, expected D4State, claimID uuid.UUID, recovery D4RecoveryClass) (D4Record, error) {
	approval, err := NewD4OwnerApproval(tuple, D4EventRequireRecovery, time.Now().UTC())
	if err != nil {
		return D4Record{}, err
	}
	return s.Transition(ctx, D4TransitionRequest{Tuple: tuple, Expected: expected, ClaimID: claimID, Event: D4OwnerEvent{Kind: D4EventRequireRecovery, Tuple: tuple, Approval: approval, Recovery: recovery}})
}

func (s *PostgresD4Store) Get(ctx context.Context, tuple D4OwnerTuple) (D4Record, error) {
	if !tuple.Valid() {
		return D4Record{}, &D4Error{Class: D4ErrorMalformed, Cause: errors.New("get requires a valid tuple")}
	}
	return s.getWithQueryer(ctx, s.db, tuple)
}

func (s *PostgresD4Store) getWithQueryer(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, tuple D4OwnerTuple) (D4Record, error) {
	if err := contextErr(ctx); err != nil {
		return D4Record{}, err
	}
	record, err := scanD5Record(q.QueryRowContext(ctx, `SELECT `+d5LedgerColumns+` FROM `+d5LedgerTable+` WHERE operation_id=$1 AND attempt_id=$2 AND target_fingerprint=$3 AND generation=$4`, tuple.OperationID(), tuple.AttemptID(), targetBytes(tuple), tuple.Generation()))
	if errors.Is(err, sql.ErrNoRows) {
		return D4Record{}, &D4Error{Class: D4ErrorStale, Cause: errors.New("D5 ledger record does not exist")}
	}
	return record, err
}

func (s *PostgresD4Store) ListRecovery(ctx context.Context) ([]D4Record, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+d5LedgerColumns+` FROM `+d5LedgerTable+` WHERE state='RECOVERY_REQUIRED' ORDER BY updated_at, operation_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]D4Record, 0)
	for rows.Next() {
		record, err := scanD5Record(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

func appendJournalTx(ctx context.Context, tx *sql.Tx, event D4JournalEvent) error {
	if err := event.Validate(); err != nil {
		return err
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if err := ValidateSafeProjectionBytes(payload); err != nil {
		return err
	}
	digest := sha256.Sum256(payload)
	result, err := tx.ExecContext(ctx, `INSERT INTO `+d5JournalTable+` (event_id,event_version,operation_id,attempt_id,target_fingerprint,generation,from_state,to_state,recovery_class,correlation,safe_payload,payload_digest,occurred_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb,$12,$13) ON CONFLICT (event_id) DO NOTHING`, event.EventID, event.Version, event.Tuple.OperationID(), event.Tuple.AttemptID(), targetBytes(event.Tuple), event.Tuple.Generation(), event.From, event.To, event.Recovery, event.Correlation, payload, digest[:], event.OccurredAt)
	if err != nil {
		return fmt.Errorf("append D5 journal: %w", err)
	}
	if count, _ := result.RowsAffected(); count == 0 {
		var existingDigest []byte
		if err := tx.QueryRowContext(ctx, `SELECT payload_digest FROM `+d5JournalTable+` WHERE event_id=$1`, event.EventID).Scan(&existingDigest); err != nil {
			return err
		}
		if !bytes.Equal(existingDigest, digest[:]) {
			return errors.New("D5 journal event identity conflict")
		}
	}
	return nil
}

func (s *PostgresD4Store) Append(ctx context.Context, event D4JournalEvent) error {
	if s == nil || s.db == nil {
		return errors.New("D5 PostgreSQL store is unavailable")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := appendJournalTx(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *PostgresD4Store) Replay(ctx context.Context, apply func(D4JournalEvent) error) error {
	if apply == nil {
		return errors.New("D5 journal replay requires an observer")
	}
	if s == nil || s.db == nil {
		return errors.New("D5 PostgreSQL store is unavailable")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT event_id,event_version,operation_id,attempt_id,target_fingerprint,generation,from_state,to_state,recovery_class,correlation,safe_payload,payload_digest,occurred_at FROM `+d5JournalTable+` ORDER BY occurred_at,event_version,event_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var eventID uuid.UUID
		var version uint64
		var operation, attempt string
		var target []byte
		var generation uint64
		var from, to D4State
		var recovery D4RecoveryClass
		var correlation string
		var payload []byte
		var payloadDigest []byte
		var occurred time.Time
		if err := rows.Scan(&eventID, &version, &operation, &attempt, &target, &generation, &from, &to, &recovery, &correlation, &payload, &payloadDigest, &occurred); err != nil {
			return err
		}
		if err := ValidateSafeProjectionBytes(payload); err != nil {
			return fmt.Errorf("validate D5 journal payload: %w", err)
		}
		digest := sha256.Sum256(payload)
		if !bytes.Equal(payloadDigest, digest[:]) {
			return errors.New("D5 journal payload digest mismatch")
		}
		op, err := uuid.Parse(operation)
		if err != nil {
			return err
		}
		at, err := uuid.Parse(attempt)
		if err != nil {
			return err
		}
		tuple, err := NewD4OwnerTuple(op, at, target, generation)
		if err != nil {
			return err
		}
		var result D4JournalEvent
		if err := json.Unmarshal(payload, &result); err != nil {
			return err
		}
		if result.EventID != eventID || result.Version != version || !result.Tuple.Equal(tuple) || result.From != from || result.To != to || result.Recovery != recovery || result.Correlation != correlation || !result.OccurredAt.UTC().Truncate(time.Microsecond).Equal(occurred.UTC().Truncate(time.Microsecond)) {
			return errors.New("D5 journal payload identity disagrees with relational columns")
		}
		if err := result.Validate(); err != nil {
			return fmt.Errorf("validate D5 journal event: %w", err)
		}
		result.OccurredAt = occurred
		if err := apply(result); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *PostgresD4Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func sortUUIDs(values []uuid.UUID) {
	sort.Slice(values, func(i, j int) bool { return values[i].String() < values[j].String() })
}
