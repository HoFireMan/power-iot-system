package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"power-iot-a3-deployment-control-plane/internal/ledger"
)

type Store struct {
	DB *sql.DB
	mu sync.RWMutex
	// mutationMu serializes use of the single pinned writer connection within
	// this process; PostgreSQL row locks remain cross-process authority.
	mutationMu sync.Mutex
	pinned     *sql.Conn
	epoch      int64
	instance   uuid.UUID
	backendPID int64
}

func New(db *sql.DB) *Store { return &Store{DB: db} }

// discardConn physically closes the driver session behind conn. Conn.Close
// alone only returns a healthy session to database/sql's pool, which would
// preserve a session-scoped advisory lock and allow a stale authority to
// retain the singleton after release.
func discardConn(conn *sql.Conn) {
	if conn == nil {
		return
	}
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	_ = conn.Close()
}

func Open(ctx context.Context, url string) (*Store, error) {
	if url == "" {
		return nil, errors.New("D1L_PROVIDER_DATABASE_URL is required")
	}
	db, err := sql.Open("pgx", url)
	if err != nil {
		return nil, fmt.Errorf("provider database: %w", err)
	}
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("provider database unavailable: %w", err)
	}
	return New(db), nil
}
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	s.mu.Lock()
	if s.pinned != nil {
		discardConn(s.pinned)
		s.pinned = nil
	}
	s.mu.Unlock()
	if s.DB != nil {
		return s.DB.Close()
	}
	return nil
}
func (s *Store) ReleaseAuthority() {
	if s == nil {
		return
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	s.mu.Lock()
	if s.pinned != nil {
		discardConn(s.pinned)
		s.pinned = nil
	}
	s.epoch = 0
	s.backendPID = 0
	s.mu.Unlock()
}

// AcquireAuthority pins the exact physical connection which owns the advisory
// lock. No pool connection may perform authority mutations.
func (s *Store) AcquireAuthority(ctx context.Context) (int64, error) {
	return s.acquireAuthority(ctx, "")
}

// AcquireAuthorityWithBootstrap admits a freshly provisioned provider schema
// while the same pinned session already owns the singleton advisory lock.
// Startup must never run provider DDL/DML on an unlocked pool connection.
func (s *Store) AcquireAuthorityWithBootstrap(ctx context.Context, bootstrap string) (int64, error) {
	if strings.TrimSpace(bootstrap) == "" {
		return 0, errors.New("provider bootstrap is required")
	}
	return s.acquireAuthority(ctx, bootstrap)
}

func (s *Store) acquireAuthority(ctx context.Context, bootstrap string) (int64, error) {
	if s == nil || s.DB == nil {
		return 0, errors.New("provider database required")
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return 0, err
	}
	fail := func(e error) (int64, error) { discardConn(conn); return 0, e }
	var owned bool
	if err = conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", ledger.ExpectedLockKey()).Scan(&owned); err != nil {
		return fail(fmt.Errorf("authority lock: %w", err))
	}
	if !owned {
		return fail(errors.New("authority lock already held"))
	}
	if strings.TrimSpace(bootstrap) != "" {
		migrationTx, migrationErr := conn.BeginTx(ctx, nil)
		if migrationErr != nil {
			return fail(migrationErr)
		}
		if _, migrationErr = migrationTx.ExecContext(ctx, bootstrap); migrationErr != nil {
			_ = migrationTx.Rollback()
			return fail(fmt.Errorf("provider schema admission: %w", migrationErr))
		}
		if migrationErr = migrationTx.Commit(); migrationErr != nil {
			return fail(fmt.Errorf("provider schema admission: %w", migrationErr))
		}
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fail(err)
	}
	defer tx.Rollback()
	var version, count int
	if err = tx.QueryRowContext(ctx, "SELECT schema_version FROM d1l_provider.provider_control WHERE singleton=true FOR UPDATE").Scan(&version); err != nil {
		return fail(err)
	}
	var applied int
	if err = tx.QueryRowContext(ctx, "SELECT version FROM d1l_provider.schema_version WHERE version=1").Scan(&applied); err != nil {
		return fail(err)
	}
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM d1l_provider.schema_version").Scan(&count); err != nil {
		return fail(err)
	}
	if version != 1 || applied != 1 || count != 1 {
		return fail(fmt.Errorf("provider schema version is unsupported"))
	}
	instanceID := uuid.New()
	var epoch int64
	if err = tx.QueryRowContext(ctx, `INSERT INTO d1l_provider.provider_epochs(instance_id,started_at,live) VALUES($1,clock_timestamp(),true) RETURNING epoch_id`, instanceID).Scan(&epoch); err != nil {
		return fail(err)
	}
	// Poison in canonical parent-first order. Claimed work is unknown because
	// external work may already have happened; claimed consumes are never expired.
	if _, err = tx.ExecContext(ctx, `UPDATE d1l_provider.d1l_issue_requests SET state='TERMINAL',terminal_at=clock_timestamp(),terminal_code='AUTHORITY_EPOCH_REPLACED',terminal_consumer='authority-startup',updated_at=clock_timestamp() WHERE state='REQUESTED'`); err != nil {
		return fail(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE d1l_provider.d1l_bootstrap_authorizations SET state='REVOKED',revoked_reason='AUTHORITY_EPOCH_REPLACED',terminal_at=clock_timestamp(),terminal_code='AUTHORITY_EPOCH_REPLACED',terminal_consumer='authority-startup',updated_at=clock_timestamp() WHERE state='ISSUED'`); err != nil {
		return fail(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE d1l_provider.d1l_bootstrap_authorizations SET state='CONSUME_UNKNOWN',revoked_reason='AUTHORITY_EPOCH_REPLACED',consume_terminal_at=clock_timestamp(),consume_terminal_code='AUTHORITY_EPOCH_REPLACED',consume_consumer='authority-startup',terminal_at=clock_timestamp(),terminal_code='AUTHORITY_EPOCH_REPLACED',terminal_consumer='authority-startup',updated_at=clock_timestamp() WHERE state='CONSUME_PENDING'`); err != nil {
		return fail(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE d1l_provider.d1l_bootstrap_consume_intents i SET state='ABORTED',terminal_at=clock_timestamp(),terminal_code='AUTHORITY_EPOCH_REPLACED',terminal_consumer='authority-startup',updated_at=clock_timestamp() WHERE i.state='PENDING' AND EXISTS (SELECT 1 FROM d1l_provider.d1l_bootstrap_authorizations a WHERE a.authorization_id=i.authorization_id AND a.issuer_request_id=i.issuer_request_id AND a.epoch_id=i.epoch_id AND a.nonce=i.nonce AND a.operation=i.operation AND a.attempt_id=i.attempt_id AND a.target_id=i.target_id AND a.installer_id=i.installer_id AND a.evidence_hash=i.evidence_hash AND a.scope=i.scope AND a.state <> 'ISSUED')`); err != nil {
		return fail(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE d1l_provider.d1l_bootstrap_consume_intents i SET state='UNKNOWN',terminal_at=clock_timestamp(),terminal_code='AUTHORITY_EPOCH_REPLACED',terminal_consumer='authority-startup',updated_at=clock_timestamp() WHERE i.state='CLAIMED' AND EXISTS (SELECT 1 FROM d1l_provider.d1l_bootstrap_authorizations a WHERE a.authorization_id=i.authorization_id AND a.issuer_request_id=i.issuer_request_id AND a.epoch_id=i.epoch_id AND a.nonce=i.nonce AND a.operation=i.operation AND a.attempt_id=i.attempt_id AND a.target_id=i.target_id AND a.installer_id=i.installer_id AND a.evidence_hash=i.evidence_hash AND a.scope=i.scope)`); err != nil {
		return fail(err)
	}
	if _, err = tx.ExecContext(ctx, "UPDATE d1l_provider.provider_epochs SET live=false WHERE epoch_id<>$1 AND live", epoch); err != nil {
		return fail(err)
	}
	if _, err = tx.ExecContext(ctx, "UPDATE d1l_provider.provider_control SET current_epoch=$1,instance_id=$2,updated_at=clock_timestamp() WHERE singleton=true", epoch, instanceID); err != nil {
		return fail(err)
	}
	if err = tx.Commit(); err != nil {
		return fail(err)
	}
	var backendPID int64
	if err = conn.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&backendPID); err != nil {
		return fail(err)
	}
	s.mu.Lock()
	if s.pinned != nil {
		discardConn(s.pinned)
	}
	s.pinned, s.epoch, s.instance, s.backendPID = conn, epoch, instanceID, backendPID
	s.mu.Unlock()
	return epoch, nil
}

// assertAuthority proves that mutations still use the pinned backend which
// owns the canonical advisory lock, and that the lock's epoch is current.
// The pinned database connection is not safe for concurrent use. Serialize
// this check with mutations and authority lifecycle operations so a readiness
// probe cannot race a transaction or another check.
func (s *Store) assertAuthority(ctx context.Context) error {
	if s == nil {
		return errors.New("authority is not active")
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	return s.assertAuthorityLocked(ctx)
}

func (s *Store) assertAuthorityLocked(ctx context.Context) error {
	s.mu.RLock()
	conn, epoch, instance, pid := s.pinned, s.epoch, s.instance, s.backendPID
	s.mu.RUnlock()
	if conn == nil || epoch == 0 || pid == 0 {
		return errors.New("authority is not active")
	}
	var gotPID, currentEpoch int64
	var currentInstance uuid.UUID
	if err := conn.QueryRowContext(ctx, `SELECT pg_backend_pid(),current_epoch,instance_id FROM d1l_provider.provider_control WHERE singleton=true`).Scan(&gotPID, &currentEpoch, &currentInstance); err != nil {
		return err
	}
	if gotPID != pid || currentEpoch != epoch || currentInstance != instance {
		return errors.New("authority epoch or connection changed")
	}
	// A re-entrant try-lock on this exact backend is an ownership assertion.
	// Immediately undo only the extra re-entrant lock count.
	var owned bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", ledger.ExpectedLockKey()).Scan(&owned); err != nil || !owned {
		return errors.New("authority advisory lock is not owned")
	}
	var released bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_advisory_unlock($1)", ledger.ExpectedLockKey()).Scan(&released); err != nil || !released {
		return errors.New("authority advisory lock assertion failed")
	}
	return nil
}
func (s *Store) AuthorityHealthy(ctx context.Context) bool { return s.assertAuthority(ctx) == nil }

// requireAuthority is used by mutation entrypoints after they acquire
// mutationMu. Keeping the assertion in that critical section also protects
// the pinned connection for the transaction which follows.
func (s *Store) requireAuthority(ctx context.Context) error {
	if err := s.assertAuthorityLocked(ctx); err != nil {
		return errors.New("authority is not active")
	}
	return nil
}
func (s *Store) beginAuthorityTx(ctx context.Context) (*sql.Tx, error) {
	s.mu.RLock()
	conn := s.pinned
	s.mu.RUnlock()
	if conn == nil {
		return nil, errors.New("authority is not active")
	}
	return conn.BeginTx(ctx, nil)
}

func NewIssueSecret() ([]byte, []byte, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, nil, err
	}
	h := sha256.Sum256(secret)
	return secret, h[:], nil
}
func EncodeSecret(secret []byte) string { return base64.RawStdEncoding.EncodeToString(secret) }

type IssueResult struct {
	AuthorizationID string            `json:"authorization_id"`
	IssuerRequestID string            `json:"issuer_request_id"`
	AttemptID       string            `json:"attempt_id"`
	State           ledger.AuthState  `json:"state"`
	Epoch           int64             `json:"epoch"`
	Nonce           string            `json:"nonce"`
	ExpiresAt       time.Time         `json:"expires_at"`
	Scope           string            `json:"scope"`
	Bindings        map[string]string `json:"bindings"`
	Envelope        string            `json:"envelope,omitempty"`
	SecretAvailable bool              `json:"secret_available"`
}
type InspectResult struct {
	AuthorizationID  string             `json:"authorization_id"`
	IssuerRequestID  string             `json:"issuer_request_id"`
	AttemptID        string             `json:"attempt_id"`
	State            ledger.AuthState   `json:"state"`
	Epoch            int64              `json:"epoch"`
	Nonce            string             `json:"nonce"`
	ExpiresAt        time.Time          `json:"expires_at"`
	Scope            string             `json:"scope"`
	Bindings         map[string]string  `json:"bindings"`
	ConsumeRequestID string             `json:"consume_request_id,omitempty"`
	IntentState      ledger.IntentState `json:"intent_state,omitempty"`
	TerminalState    string             `json:"terminal_state,omitempty"`
	TerminalCode     string             `json:"terminal_code,omitempty"`
}
type ResolveResult struct {
	AuthorizationID  string             `json:"authorization_id"`
	IssuerRequestID  string             `json:"issuer_request_id"`
	ConsumeRequestID string             `json:"consume_request_id,omitempty"`
	AuthState        ledger.AuthState   `json:"authorization_state"`
	IntentState      ledger.IntentState `json:"intent_state,omitempty"`
	TerminalState    string             `json:"terminal_state,omitempty"`
	TerminalCode     string             `json:"terminal_code,omitempty"`
	Detail           string             `json:"detail,omitempty"`
}
type RequestData struct {
	ID, AttemptID, Role string
	Scope               string
	Bindings            map[string]string
}

func jsonBindings(b map[string]string) ([]byte, error) {
	if b == nil {
		return []byte(`{}`), nil
	}
	return json.Marshal(b)
}
func (s *Store) currentEpoch(ctx context.Context, tx *sql.Tx) (int64, error) {
	var epoch sql.NullInt64
	if err := tx.QueryRowContext(ctx, "SELECT current_epoch FROM d1l_provider.provider_control WHERE singleton=true").Scan(&epoch); err != nil {
		return 0, err
	}
	if !epoch.Valid || epoch.Int64 <= 0 {
		return 0, errors.New("authority epoch unavailable")
	}
	return epoch.Int64, nil
}

func (s *Store) Issue(ctx context.Context, r RequestData, ttl time.Duration) (IssueResult, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	if err := s.requireAuthority(ctx); err != nil {
		return IssueResult{}, err
	}
	if r.Role != "deployment-runbook" {
		return IssueResult{}, errors.New("issuer role rejected")
	}
	if err := ledger.ValidateIssueRequest(ledger.IssueRequest{IssuerRequestID: r.ID, AttemptID: r.AttemptID, Scope: r.Scope, Bindings: r.Bindings, TTL: ttl}); err != nil {
		return IssueResult{}, err
	}
	rid, err := uuid.Parse(r.ID)
	if err != nil {
		return IssueResult{}, errors.New("invalid issuer_request_id")
	}
	attempt, err := uuid.Parse(r.AttemptID)
	if err != nil {
		return IssueResult{}, errors.New("invalid attempt_id")
	}
	tx, err := s.beginAuthorityTx(ctx)
	if err != nil {
		return IssueResult{}, err
	}
	defer tx.Rollback()
	insertResult, err := tx.ExecContext(ctx, `INSERT INTO d1l_provider.d1l_issue_requests(issuer_request_id,issuer_role,attempt_id,state) VALUES($1,$2,$3,'REQUESTED') ON CONFLICT (issuer_request_id) DO NOTHING`, rid, r.Role, attempt)
	inserted := false
	if err == nil {
		n, rowsErr := insertResult.RowsAffected()
		if rowsErr != nil {
			return IssueResult{}, rowsErr
		}
		inserted = n == 1
	}
	if err != nil {
		// A different request in the selected (issuer_role, attempt_id) namespace is a conflict.
		var owner uuid.UUID
		if qerr := tx.QueryRowContext(ctx, `SELECT issuer_request_id FROM d1l_provider.d1l_issue_requests WHERE issuer_role=$1 AND attempt_id=$2`, r.Role, attempt).Scan(&owner); qerr == nil && owner != rid {
			return IssueResult{}, errors.New("attempt already belongs to another issuer request")
		}
		return IssueResult{}, err
	}
	var state string
	var storedAttempt uuid.UUID
	var authID uuid.NullUUID
	var role string
	err = tx.QueryRowContext(ctx, `SELECT issuer_role,attempt_id,state,authorization_id FROM d1l_provider.d1l_issue_requests WHERE issuer_request_id=$1 FOR UPDATE`, rid).Scan(&role, &storedAttempt, &state, &authID)
	if errors.Is(err, sql.ErrNoRows) {
		var owner uuid.UUID
		if qerr := tx.QueryRowContext(ctx, `SELECT issuer_request_id FROM d1l_provider.d1l_issue_requests WHERE issuer_role=$1 AND attempt_id=$2`, r.Role, attempt).Scan(&owner); qerr == nil {
			return IssueResult{}, errors.New("attempt already belongs to another issuer request")
		}
	}
	if err != nil {
		return IssueResult{}, err
	}
	if role != r.Role || storedAttempt != attempt {
		return IssueResult{}, errors.New("issuer request identity conflict")
	}
	if state != "REQUESTED" {
		if state == "ISSUED" && authID.Valid {
			if err = tx.Commit(); err != nil {
				return IssueResult{}, err
			}
			out, loadErr := s.inspectIssue(ctx, authID.UUID, false)
			if loadErr != nil {
				return IssueResult{}, loadErr
			}
			if out.Scope != r.Scope || !sameBindings(out.Bindings, r.Bindings) {
				return IssueResult{}, errors.New("issuer request binding conflict")
			}
			return out, nil
		}
		if err = tx.Commit(); err != nil {
			return IssueResult{}, err
		}
		return IssueResult{IssuerRequestID: r.ID, AttemptID: r.AttemptID, State: ledger.AuthState(state), Scope: r.Scope, Bindings: r.Bindings, SecretAvailable: false}, nil
	}
	if !inserted {
		// A REQUESTED row that was durably committed by an interrupted operation
		// is a tombstone, never an invitation to mint later.
		result, e := tx.ExecContext(ctx, `UPDATE d1l_provider.d1l_issue_requests SET state='TERMINAL',terminal_at=clock_timestamp(),terminal_code='SECRET_UNAVAILABLE',terminal_consumer='issue-recovery',updated_at=clock_timestamp() WHERE issuer_request_id=$1 AND issuer_role=$2 AND attempt_id=$3 AND state='REQUESTED'`, rid, r.Role, attempt)
		if e != nil {
			return IssueResult{}, e
		}
		if n, e := result.RowsAffected(); e != nil || n != 1 {
			return IssueResult{}, errors.New("issue request changed")
		}
		if err = tx.Commit(); err != nil {
			return IssueResult{}, err
		}
		return IssueResult{IssuerRequestID: r.ID, AttemptID: r.AttemptID, State: ledger.AuthState("TERMINAL"), Scope: r.Scope, Bindings: r.Bindings, SecretAvailable: false}, nil
	}
	secret, verifier, err := NewIssueSecret()
	if err != nil {
		return IssueResult{}, err
	}
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		return IssueResult{}, err
	}
	bjson, err := jsonBindings(r.Bindings)
	if err != nil {
		return IssueResult{}, err
	}
	auth := uuid.New()
	var expiry time.Time
	epoch, err := s.currentEpoch(ctx, tx)
	if err != nil {
		return IssueResult{}, err
	}
	err = tx.QueryRowContext(ctx, `INSERT INTO d1l_provider.d1l_bootstrap_authorizations(authorization_id,issuer_request_id,epoch_id,nonce,secret_verifier,scope,operation,attempt_id,target_id,installer_id,evidence_hash,bindings,state,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'ISSUED',clock_timestamp()+($13 * interval '1 second')) RETURNING authorization_id,expires_at`, auth, rid, epoch, nonce, verifier, r.Scope, r.Bindings["operation"], attempt, r.Bindings["target_id"], r.Bindings["installer_id"], r.Bindings["evidence_hash"], bjson, ttl.Seconds()).Scan(&auth, &expiry)
	if err != nil {
		return IssueResult{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE d1l_provider.d1l_issue_requests SET state='ISSUED',authorization_id=$1,updated_at=clock_timestamp() WHERE issuer_request_id=$2 AND state='REQUESTED'`, auth, rid)
	if err != nil {
		return IssueResult{}, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return IssueResult{}, errors.New("issue request changed")
	}
	if err = tx.Commit(); err != nil {
		return IssueResult{}, err
	}
	return IssueResult{AuthorizationID: auth.String(), IssuerRequestID: r.ID, AttemptID: r.AttemptID, State: ledger.Issued, Epoch: epoch, Nonce: base64.RawStdEncoding.EncodeToString(nonce), ExpiresAt: expiry, Scope: r.Scope, Bindings: r.Bindings, Envelope: EncodeEnvelope(auth, epoch, nonce, secret), SecretAvailable: true}, nil
}

func sameBindings(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func (s *Store) inspectIssue(ctx context.Context, aid uuid.UUID, includeSecret bool) (IssueResult, error) {
	var r IssueResult
	var nonce []byte
	var bindings []byte
	var state string
	var attempt uuid.UUID
	err := s.DB.QueryRowContext(ctx, `SELECT a.authorization_id,a.issuer_request_id,a.attempt_id,a.epoch_id,a.nonce,a.scope,a.bindings,a.state,a.expires_at FROM d1l_provider.d1l_bootstrap_authorizations a WHERE a.authorization_id=$1`, aid).Scan(&r.AuthorizationID, &r.IssuerRequestID, &attempt, &r.Epoch, &nonce, &r.Scope, &bindings, &state, &r.ExpiresAt)
	if err != nil {
		return r, err
	}
	r.AttemptID, r.State, r.Nonce = attempt.String(), ledger.AuthState(state), base64.RawStdEncoding.EncodeToString(nonce)
	if json.Unmarshal(bindings, &r.Bindings) != nil {
		return IssueResult{}, errors.New("binding metadata invalid")
	}
	r.SecretAvailable = includeSecret
	return r, nil
}

// Inspect reads only through the active pinned authority session. The
// variadic identity keeps direct store callers source-compatible; API callers
// always provide the authenticated URI.
func (s *Store) Inspect(ctx context.Context, aid string, identities ...string) (InspectResult, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	if err := s.assertAuthorityLocked(ctx); err != nil {
		return InspectResult{}, errors.New("authority is not active")
	}
	id, err := uuid.Parse(aid)
	if err != nil {
		return InspectResult{}, err
	}
	tx, err := s.beginAuthorityTx(ctx)
	if err != nil {
		return InspectResult{}, err
	}
	defer tx.Rollback()
	var r InspectResult
	var nonce []byte
	var bindings []byte
	var state string
	var attempt uuid.UUID
	var consumeID uuid.NullUUID
	var terminal sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT authorization_id,issuer_request_id,attempt_id,epoch_id,nonce,scope,bindings,state,expires_at,consume_request_id,terminal_code FROM d1l_provider.d1l_bootstrap_authorizations WHERE authorization_id=$1`, id).Scan(&r.AuthorizationID, &r.IssuerRequestID, &attempt, &r.Epoch, &nonce, &r.Scope, &bindings, &state, &r.ExpiresAt, &consumeID, &terminal)
	if err != nil {
		return r, err
	}
	r.AttemptID, r.State, r.Nonce = attempt.String(), ledger.AuthState(state), base64.RawStdEncoding.EncodeToString(nonce)
	if json.Unmarshal(bindings, &r.Bindings) != nil {
		return InspectResult{}, errors.New("binding metadata invalid")
	}
	if consumeID.Valid {
		r.ConsumeRequestID = consumeID.UUID.String()
		var intent, consumerIdentity string
		if err = tx.QueryRowContext(ctx, `SELECT state,consumer_identity FROM d1l_provider.d1l_bootstrap_consume_intents WHERE consume_request_id=$1`, consumeID.UUID).Scan(&intent, &consumerIdentity); err != nil {
			return InspectResult{}, err
		}
		r.IntentState = ledger.IntentState(intent)
		if len(identities) > 0 && strings.TrimSpace(identities[0]) != "" && strings.TrimSpace(identities[0]) != consumerIdentity {
			return InspectResult{}, errors.New("inspect owner rejected")
		}
	} else {
		var liveID uuid.UUID
		var intent, consumerIdentity string
		err = tx.QueryRowContext(ctx, `SELECT consume_request_id,state,consumer_identity FROM d1l_provider.d1l_bootstrap_consume_intents WHERE authorization_id=$1 AND state IN ('PENDING','CLAIMED') ORDER BY created_at LIMIT 1 FOR UPDATE`, id).Scan(&liveID, &intent, &consumerIdentity)
		if err == nil {
			r.ConsumeRequestID = liveID.String()
			r.IntentState = ledger.IntentState(intent)
			if len(identities) > 0 && strings.TrimSpace(identities[0]) != "" && strings.TrimSpace(identities[0]) != consumerIdentity {
				return InspectResult{}, errors.New("inspect owner rejected")
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return InspectResult{}, err
		}
	}
	if terminal.Valid {
		r.TerminalState = string(r.State)
		r.TerminalCode = terminal.String
	}
	if err = tx.Commit(); err != nil {
		return InspectResult{}, err
	}
	return r, nil
}
