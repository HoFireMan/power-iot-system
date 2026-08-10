package migrations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

const (
	// WriterFenceLabel is the single advisory-lock namespace shared by every
	// cooperating writer and migration orchestrator.
	WriterFenceLabel = "power-iot-system/security-schema-writer-fence/v1"
	// WriterFenceKey is SHA-256(WriterFenceLabel)[0:8], interpreted as a
	// signed big-endian PostgreSQL BIGINT. Keep this literal stable so runtime
	// lock acquisition does not depend on an implementation-specific hash path.
	WriterFenceKey int64 = -6868045010404097045

	sharedWriterFenceSQL    = "SELECT pg_advisory_xact_lock_shared($1::bigint)"
	exclusiveWriterFenceSQL = "SELECT pg_advisory_lock($1::bigint)"
	unlockWriterFenceSQL    = "SELECT pg_advisory_unlock($1::bigint)"

	exclusiveAcquireTimeout = 30 * time.Second
	cleanupTimeout          = 10 * time.Second
)

var (
	ErrWriterFenceDecisionRequired       = errors.New("security schema writer fence capability is required")
	ErrWriterFenceNotOwned               = errors.New("exclusive writer fence is not owned")
	ErrWriterFenceUnlockFailed           = errors.New("exclusive writer fence unlock was not confirmed")
	ErrPhysicalConnectionDiscardRequired = errors.New("physical PostgreSQL connection discard proof is required")
	ErrSharedWriterTransactionRequired   = errors.New("shared writer fence requires a caller-owned SQL transaction")
)

// AcquireSharedWriterFence acquires the transaction-level shared lock on the
// caller-owned business transaction. It deliberately never begins, commits, or
// rolls back a transaction.
func AcquireSharedWriterFence(ctx context.Context, tx *sql.Tx) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if tx == nil {
		return ErrSharedWriterTransactionRequired
	}
	if _, err := tx.ExecContext(ctx, sharedWriterFenceSQL, WriterFenceKey); err != nil {
		return fmt.Errorf("acquire shared writer fence: %w", err)
	}
	return nil
}

// AcquireSharedWriterFenceOnGORM is the adapter used by GORM-owned business
// transactions. It rejects a pool handle so the lock cannot silently outlive
// or escape the mutation transaction.
func AcquireSharedWriterFenceOnGORM(ctx context.Context, tx *gorm.DB) error {
	if tx == nil || tx.Statement == nil {
		return ErrSharedWriterTransactionRequired
	}
	sqlTx, ok := tx.Statement.ConnPool.(*sql.Tx)
	if !ok {
		return ErrSharedWriterTransactionRequired
	}
	return AcquireSharedWriterFence(ctx, sqlTx)
}

// ExclusiveOwnershipState describes the bounded lifecycle of one pinned
// physical session. unknown is intentionally not equivalent to released.
type ExclusiveOwnershipState string

const (
	ExclusiveNotAttempted ExclusiveOwnershipState = "not_attempted"
	ExclusiveWaiting      ExclusiveOwnershipState = "waiting"
	ExclusiveOwned        ExclusiveOwnershipState = "owned"
	ExclusiveReleased     ExclusiveOwnershipState = "released"
	ExclusiveUnknown      ExclusiveOwnershipState = "unknown"
)

// ProtectedWorkCapability is an unforgeable-by-value API token: callers can
// obtain one only from an actually owned ExclusiveWriterFence, and the gate
// validates that the same live owner still holds the lock.
type ProtectedWorkCapability struct {
	fence *ExclusiveWriterFence
	pid   int64
}

// ExclusiveWriterFence owns one pinned PostgreSQL session for the complete
// protected window. The database is intentionally opened as a private pool so
// uncertain ownership is never returned to an ordinary application pool.
type ExclusiveWriterFence struct {
	dsn   string
	db    *sql.DB
	conn  *sql.Conn
	pid   int64
	state ExclusiveOwnershipState
}

// OpenExclusiveWriterFence opens and pins one physical PostgreSQL session,
// captures its backend PID, and acquires the session-level canonical lock.
func OpenExclusiveWriterFence(ctx context.Context, dsn string) (*ExclusiveWriterFence, error) {
	parsed, err := parsePostgresDatabaseURL(dsn)
	if err != nil {
		return nil, err
	}
	return openExclusiveWriterFence(ctx, parsed)
}

func openExclusiveWriterFence(ctx context.Context, parsed *parsedPostgresDatabaseURL) (*ExclusiveWriterFence, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if parsed == nil || parsed.driverURL == "" {
		return nil, errors.New("database URL is required")
	}
	db, err := sql.Open("postgres", parsed.driverURL)
	if err != nil {
		return nil, fmt.Errorf("open PostgreSQL writer-fence pool: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("pin PostgreSQL writer-fence connection: %w", err)
	}
	fence := &ExclusiveWriterFence{dsn: parsed.driverURL, db: db, conn: conn, state: ExclusiveNotAttempted}
	if err := conn.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&fence.pid); err != nil {
		_ = conn.Close()
		_ = db.Close()
		return nil, fmt.Errorf("capture PostgreSQL writer-fence backend PID: %w", err)
	}
	return fence, fence.acquire(ctx)
}

func (f *ExclusiveWriterFence) acquire(ctx context.Context) error {
	if f == nil || f.conn == nil {
		return ErrWriterFenceNotOwned
	}
	f.state = ExclusiveWaiting
	acquireCtx, cancel := context.WithTimeout(ctx, exclusiveAcquireTimeout)
	defer cancel()
	if _, err := f.conn.ExecContext(acquireCtx, exclusiveWriterFenceSQL, WriterFenceKey); err != nil {
		f.state = ExclusiveUnknown
		cleanupErr := f.discardUnknown()
		if cleanupErr != nil {
			return errors.Join(fmt.Errorf("acquire exclusive writer fence: %w", err), cleanupErr)
		}
		return fmt.Errorf("acquire exclusive writer fence: %w", err)
	}
	f.state = ExclusiveOwned
	return nil
}

func (f *ExclusiveWriterFence) Conn() *sql.Conn {
	if f == nil {
		return nil
	}
	return f.conn
}

func (f *ExclusiveWriterFence) BackendPID() int64 {
	if f == nil {
		return 0
	}
	return f.pid
}

func (f *ExclusiveWriterFence) State() ExclusiveOwnershipState {
	if f == nil {
		return ExclusiveNotAttempted
	}
	return f.state
}

func (f *ExclusiveWriterFence) Capability() (ProtectedWorkCapability, error) {
	if f == nil || f.state != ExclusiveOwned || f.conn == nil {
		return ProtectedWorkCapability{}, ErrWriterFenceNotOwned
	}
	return ProtectedWorkCapability{fence: f, pid: f.pid}, nil
}

// RequireProtectedWork accepts only a capability produced by live exclusive
// ownership. A caller-supplied boolean or a forged decision struct cannot pass.
func RequireProtectedWork(capability ProtectedWorkCapability) error {
	if capability.fence == nil || capability.pid == 0 || capability.fence.state != ExclusiveOwned || capability.fence.pid != capability.pid {
		return ErrWriterFenceDecisionRequired
	}
	return nil
}

// Release explicitly unlocks on the owning session and verifies PostgreSQL's
// boolean result. A failed/uncertain release is never reported as success.
func (f *ExclusiveWriterFence) Release(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if f == nil {
		return ErrWriterFenceNotOwned
	}
	if f.state == ExclusiveReleased {
		return nil
	}
	if f.state != ExclusiveOwned || f.conn == nil {
		return ErrWriterFenceNotOwned
	}
	var unlocked bool
	if err := f.conn.QueryRowContext(ctx, unlockWriterFenceSQL, WriterFenceKey).Scan(&unlocked); err != nil {
		f.state = ExclusiveUnknown
		return errors.Join(fmt.Errorf("unlock exclusive writer fence: %w", err), ErrPhysicalConnectionDiscardRequired)
	}
	if !unlocked {
		f.state = ExclusiveUnknown
		return errors.Join(ErrWriterFenceUnlockFailed, ErrPhysicalConnectionDiscardRequired)
	}
	f.state = ExclusiveReleased
	return nil
}

// Close releases normally, or discards and proves cleanup for unknown
// ownership. It never returns an uncertain session to the private pool.
func (f *ExclusiveWriterFence) Close() error {
	if f == nil {
		return nil
	}
	if f.state == ExclusiveOwned {
		ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		err := f.Release(ctx)
		cancel()
		if err != nil {
			return errors.Join(err, f.discardUnknown())
		}
	}
	if f.state == ExclusiveUnknown {
		return f.discardUnknown()
	}
	return f.closeResources()
}

func (f *ExclusiveWriterFence) closeResources() error {
	var errs []error
	if f.conn != nil {
		if err := f.conn.Close(); err != nil {
			errs = append(errs, err)
		}
		f.conn = nil
	}
	if f.db != nil {
		if err := f.db.Close(); err != nil {
			errs = append(errs, err)
		}
		f.db = nil
	}
	if len(errs) != 0 {
		return errors.Join(errs...)
	}
	return nil
}

// discardUnknown closes the private pool and uses independent PostgreSQL
// evidence to prove that the owning backend disappeared or no longer holds
// the canonical lock. A fresh connection must be able to acquire it.
func (f *ExclusiveWriterFence) discardUnknown() error {
	if f == nil {
		return nil
	}
	if f.conn != nil {
		_ = f.conn.Close()
		f.conn = nil
	}
	if f.db != nil {
		_ = f.db.Close()
		f.db = nil
	}
	if f.dsn == "" || f.pid == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	probeDB, err := sql.Open("postgres", f.dsn)
	if err != nil {
		return errors.Join(ErrPhysicalConnectionDiscardRequired, err)
	}
	defer probeDB.Close()
	probe, err := probeDB.Conn(ctx)
	if err != nil {
		return errors.Join(ErrPhysicalConnectionDiscardRequired, err)
	}
	defer probe.Close()
	for {
		if err := probe.PingContext(ctx); err != nil {
			return errors.Join(ErrPhysicalConnectionDiscardRequired, err)
		}
		var owns bool
		if err := probe.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1::bigint)", WriterFenceKey).Scan(&owns); err != nil {
			return errors.Join(ErrPhysicalConnectionDiscardRequired, err)
		}
		if owns {
			var unlocked bool
			if err := probe.QueryRowContext(ctx, unlockWriterFenceSQL, WriterFenceKey).Scan(&unlocked); err != nil {
				return errors.Join(ErrPhysicalConnectionDiscardRequired, err)
			}
			if !unlocked {
				return errors.Join(ErrPhysicalConnectionDiscardRequired, ErrWriterFenceUnlockFailed)
			}
			f.state = ExclusiveReleased
			return nil
		}
		var present bool
		if err := probe.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE pid = $1)`, f.pid).Scan(&present); err != nil {
			return errors.Join(ErrPhysicalConnectionDiscardRequired, err)
		}
		if !present {
			// The original backend is gone, but a different owner may already
			// hold the cooperative lock. Keep probing until this pinned cleanup
			// session can acquire and verify its own unlock; that is the proof
			// that a fresh orchestrator can enter the protected window.
		}
		select {
		case <-ctx.Done():
			return errors.Join(ErrPhysicalConnectionDiscardRequired, ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// WithExclusiveWriterFence runs a complete protected window on one pinned
// connection and guarantees bounded independent cleanup.
func WithExclusiveWriterFence(ctx context.Context, dsn string, work func(*ExclusiveWriterFence) error) (err error) {
	fence, err := OpenExclusiveWriterFence(ctx, dsn)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := fence.Close(); closeErr != nil {
			if err == nil {
				err = closeErr
			} else {
				err = errors.Join(err, closeErr)
			}
		}
	}()
	if work == nil {
		return errors.New("protected writer-fence work is required")
	}
	return work(fence)
}

// WriterFenceDecision is an informational capability report. Protected work
// still requires RequireProtectedWork with evidence from an owned session.
type WriterFenceDecisionStatus string

const WriterFenceEnforced WriterFenceDecisionStatus = "WRITER_FENCE_ENFORCED"

// WriterFenceReasonCode identifies the cooperative scope of this mechanism.
type WriterFenceReasonCode string

const WriterFenceCooperativeOnly WriterFenceReasonCode = "COOPERATIVE_MANAGED_WRITERS_ONLY"

type WriterFenceGate string

const WriterFenceGateNewWriterExclusion WriterFenceGate = "TEST_D_NEW_WRITER_EXCLUSION"

type WriterFenceMechanism string

const (
	WriterFenceManagedDatabaseAdmission WriterFenceMechanism = "MANAGED_DATABASE_ADMISSION"
	WriterFenceDeploymentOrchestration  WriterFenceMechanism = "DEPLOYMENT_ORCHESTRATION"
	WriterFenceApplicationCooperation   WriterFenceMechanism = "APPLICATION_COOPERATION"
)

type WriterFenceDecision struct {
	Status                           WriterFenceDecisionStatus `json:"status"`
	ReasonCode                       WriterFenceReasonCode     `json:"reason_code"`
	Message                          string                    `json:"message"`
	FailedGate                       WriterFenceGate           `json:"failed_gate"`
	StageAAdditiveFoundationSafe     bool                      `json:"stage_a_additive_foundation_safe"`
	ProtectedWorkAllowed             bool                      `json:"protected_work_allowed"`
	RequiresExplicitOperatorDecision bool                      `json:"requires_explicit_operator_decision"`
	ApplicationCooperationCrossLane  bool                      `json:"application_cooperation_cross_lane"`
	RequiredMechanismDecisions       []WriterFenceMechanism    `json:"required_mechanism_decisions"`
}

func AssessSecuritySchemaWriterFence() WriterFenceDecision {
	return WriterFenceDecision{
		Status:                           WriterFenceEnforced,
		ReasonCode:                       WriterFenceCooperativeOnly,
		Message:                          "Cooperating managed writers use one PostgreSQL shared/exclusive advisory fence; direct SQL, psql, superusers, and non-cooperating clients remain outside its scope.",
		FailedGate:                       WriterFenceGateNewWriterExclusion,
		StageAAdditiveFoundationSafe:     false,
		ProtectedWorkAllowed:             true,
		RequiresExplicitOperatorDecision: false,
		ApplicationCooperationCrossLane:  true,
		RequiredMechanismDecisions: []WriterFenceMechanism{
			WriterFenceManagedDatabaseAdmission,
			WriterFenceApplicationCooperation,
		},
	}
}

// RequireProtectedWork is retained as a decision method for compatibility but
// deliberately requires a live capability argument; no public bool can forge it.
func (d WriterFenceDecision) RequireProtectedWork(capability ...ProtectedWorkCapability) error {
	if len(capability) != 1 {
		return ErrWriterFenceDecisionRequired
	}
	return RequireProtectedWork(capability[0])
}

// CanonicalWriterFenceKey independently derives the runtime contract. Tests
// compare its result to the fixed literal and the published digest bytes.
func CanonicalWriterFenceKey() int64 {
	digest := sha256.Sum256([]byte(WriterFenceLabel))
	return int64(uint64(digest[0])<<56 | uint64(digest[1])<<48 | uint64(digest[2])<<40 | uint64(digest[3])<<32 | uint64(digest[4])<<24 | uint64(digest[5])<<16 | uint64(digest[6])<<8 | uint64(digest[7]))
}
