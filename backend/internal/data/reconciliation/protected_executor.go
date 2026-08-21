package reconciliation

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"power-iot-backend/internal/data/migrations"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// ExecutionOutcome is deliberately more precise than a boolean success value.
// In particular, a successful COMMIT followed by a failed durable verification
// must never be reported as ordinary success.
type ExecutionOutcome string

const (
	ExecutionNotCommitted              ExecutionOutcome = "NOT_COMMITTED"
	ExecutionCommittedAndVerified      ExecutionOutcome = "COMMITTED_AND_VERIFIED"
	ExecutionCommittedPostVerifyFailed ExecutionOutcome = "COMMITTED_BUT_POSTVERIFY_FAILED"
	ExecutionCommitOutcomeUnknown      ExecutionOutcome = "COMMIT_OUTCOME_UNKNOWN"
)

type ExecutionPhase string

const (
	PhaseFence       ExecutionPhase = "fence"
	PhaseTransaction ExecutionPhase = "transaction"
	PhaseRecheck     ExecutionPhase = "fresh-recheck"
	PhasePlan        ExecutionPhase = "fresh-plan"
	PhaseWrite       ExecutionPhase = "write"
	PhaseVerify      ExecutionPhase = "pre-commit-verify"
	PhaseCommit      ExecutionPhase = "commit"
	PhasePostVerify  ExecutionPhase = "post-commit-verify"
	PhaseCleanup     ExecutionPhase = "cleanup"
)

// ExecutionReport records the durable outcome and the evidence used by the
// protected execution. It is safe to inspect when Execute returns an error.
type ExecutionReport struct {
	Outcome  ExecutionOutcome
	Phase    ExecutionPhase
	FrozenAt time.Time
	// OperationID is the owner-issued D1 operation identity. It is distinct
	// from PlanID, which identifies the reconciliation plan artifact.
	OperationID            uuid.UUID
	PlanID                 uuid.UUID
	PlanDigest             string
	SourceFactsDigest      string
	MappingBasisDigest     string
	MappingDigest          string
	ExpectedAffectedCounts map[string]int
	AppliedAffectedCounts  map[string]int
	Committed              bool
	PostCommitVerified     bool
	TriggerRestored        bool
	BackendPID             int64                              `json:"-"`
	FenceState             migrations.ExclusiveOwnershipState `json:"-"`
	CleanupError           string
	PostCommitFactsDigest  string
	PostCommitFactsAsOf    time.Time
	D007Terminal           D007TerminalEvidence

	// d009Seal is owner-private post-TX2 evidence. It is never included in
	// reports or diagnostics and is the only source accepted by D3 issuance.
	d009Seal *d009ExecutionSeal
}

// String intentionally omits owner-private evidence and continuation state.
// Reports are commonly included in diagnostics with %+v, which must not turn
// private seals into an ordinary output channel.
func (r ExecutionReport) String() string {
	return fmt.Sprintf("ExecutionReport{outcome=%s phase=%s committed=%t post_commit_verified=%t plan_id=%s}", r.Outcome, r.Phase, r.Committed, r.PostCommitVerified, r.PlanID)
}

// ExecutionError retains the outcome category while preserving the database
// or validation error for errors.Is/errors.As and diagnostics.
type ExecutionError struct {
	Outcome ExecutionOutcome
	Phase   ExecutionPhase
	Cause   error
}

func (e *ExecutionError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("protected reconciliation %s during %s: %v", e.Outcome, e.Phase, e.Cause)
}

func (e *ExecutionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

var (
	ErrProtectedPlanBlocked       = errors.New("fresh reconciliation plan is blocked")
	ErrProtectedMappingStale      = errors.New("mapping artifact is stale for fresh source facts")
	ErrProtectedCASConflict       = errors.New("reconciliation compare-and-swap conflict")
	ErrProtectedTriggerUnexpected = errors.New("admin audit immutable trigger configuration is unexpected")
	ErrProtectedPostVerification  = errors.New("durable post-write verification failed")
)

// ProtectedExecutor consumes only the accepted A2.1 query-only collector. The
// executor owns the fence, transaction, writes, commit, post-commit reads, and
// cleanup when Execute is used.
// D1LeaseConsumer is the owner-mediated D1 consume seam. The executor only
// sequences the call; it does not inspect, mint, or reinterpret lease state.
type D1LeaseConsumer interface {
	ConsumeLease(context.Context, migrations.D1LLeaseIdentity) error
}

type ProtectedExecutor struct {
	Collector ReadOnlyCollectorWithConnection
	D1        D1LeaseConsumer
	Lease     *migrations.D1LLeaseIdentity
	// D007 is the D2-owned bounded issuance ledger. Production constructors use
	// the process owner so repeated attempts cannot mint a second capability.
	D007 *D007CapabilityIssuer
	// D010 is the D3-owned issuance ledger. The resulting handoff is retained
	// only in d010Handoff below and is never part of ExecutionReport.
	D010 *D010HandoffIssuer

	continuationMu sync.Mutex
	d010Handoff    *D010Handoff

	// hooks are narrow fault seams for same-package targeted A2.2 tests. They
	// are intentionally unexported so production callers cannot add writes or
	// alter authority after the protected plan is built.
	hooks protectedExecutionHooks
}

type protectedExecutionHooks struct {
	AfterFence       func(context.Context, *migrations.ExclusiveWriterFence) error
	AfterTransaction func(context.Context, *sql.Tx) error
	// AfterEntityLockPrefix fires only after the complete deterministic row-lock
	// prefix has been acquired and before frozen time/fact collection.
	AfterEntityLockPrefix func(context.Context, *sql.Tx) error
	AfterFrozenTime       func(context.Context, *sql.Tx, time.Time) error
	FrozenTime            func(time.Time) time.Time
	AfterFreshPlan        func(context.Context, *sql.Tx, FactSet, Plan) error
	BeforeWrite           func(context.Context, *sql.Tx, FactSet, Plan) error
	AfterWrite            func(context.Context, *sql.Tx, FactSet, Plan, PlanItem, int) error
	BeforeTriggerRestore  func(context.Context, *sql.Tx) error
	AfterTriggerRestore   func(context.Context, *sql.Tx) error
	BeforeCommit          func(context.Context, *sql.Tx, FactSet, Plan) error
	Commit                func(context.Context, *sql.Tx) error
	AfterCommit           func(context.Context, *sql.Conn) error
	AfterTX2              func(context.Context, *sql.Tx) error
	CloseFence            func(*migrations.ExclusiveWriterFence) error
}

// MappingResolver is a narrow execution seam for an artifact selected after
// the authoritative fresh snapshot exists. Its mapping-basis digest includes
// planner-relevant temporal state, while raw FactSet.AsOf remains execution
// metadata frozen by PostgreSQL only after the protected transaction starts.
type MappingResolver func(context.Context, FactSet) (*MappingArtifact, error)

func NewProtectedExecutor(collector ReadOnlyCollectorWithConnection) *ProtectedExecutor {
	return &ProtectedExecutor{Collector: collector, D007: defaultD007Issuer, D010: defaultD010Issuer}
}

// NewProtectedExecutorWithD1 is the explicit mutating composition seam. The
// lease identity is copied at construction so the executor consumes exactly
// the owner-issued identity selected by its caller; a nil consumer or invalid
// identity can never become a no-op authorization path.
func NewProtectedExecutorWithD1(collector ReadOnlyCollectorWithConnection, d1 D1LeaseConsumer, lease migrations.D1LLeaseIdentity) *ProtectedExecutor {
	identity := cloneD1LeaseIdentity(lease)
	return &ProtectedExecutor{Collector: collector, D1: d1, Lease: &identity, D007: defaultD007Issuer, D010: defaultD010Issuer}
}

// Execute opens the private pinned session used by the canonical writer fence.
// The mapping is the only caller-supplied authority artifact; all facts and the
// plan used for writes are rebuilt after the fence and transaction exist.
func (e *ProtectedExecutor) Execute(ctx context.Context, dsn string, mapping *MappingArtifact) (report ExecutionReport, err error) {
	return e.execute(ctx, dsn, mapping, nil)
}

// ExecuteWithMappingResolver supplies the explicit mapping only after fresh
// authoritative facts have been collected inside the protected transaction.
// The resolver cannot authorize writes directly: its artifact is still bound
// and validated by BuildPlan against those exact facts.
func (e *ProtectedExecutor) ExecuteWithMappingResolver(ctx context.Context, dsn string, resolver MappingResolver) (report ExecutionReport, err error) {
	if resolver == nil {
		return ExecutionReport{Outcome: ExecutionNotCommitted, Phase: PhasePlan}, &ExecutionError{Outcome: ExecutionNotCommitted, Phase: PhasePlan, Cause: errors.New("mapping resolver is required")}
	}
	return e.execute(ctx, dsn, nil, resolver)
}

func (e *ProtectedExecutor) execute(ctx context.Context, dsn string, mapping *MappingArtifact, resolver MappingResolver) (report ExecutionReport, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	initialReport := ExecutionReport{Outcome: ExecutionNotCommitted, Phase: PhaseFence}
	if e != nil && e.Lease != nil {
		initialReport.OperationID = e.Lease.OperationID
	}
	fence, err := migrations.OpenExclusiveWriterFence(ctx, dsn)
	if err != nil {
		return initialReport, &ExecutionError{Outcome: ExecutionNotCommitted, Phase: PhaseFence, Cause: err}
	}
	report.BackendPID = fence.BackendPID()
	closeFence := e.hooks.CloseFence
	if closeFence == nil {
		closeFence = func(f *migrations.ExclusiveWriterFence) error { return f.Close() }
	}
	defer func() {
		closeErr := closeFence(fence)
		report.FenceState = fence.State()
		if closeErr == nil {
			return
		}
		report.CleanupError = closeErr.Error()
		if report.Committed {
			cleanupErr := &ExecutionError{Outcome: report.Outcome, Phase: PhaseCleanup, Cause: closeErr}
			if err == nil {
				err = cleanupErr
			} else {
				err = errors.Join(err, cleanupErr)
			}
			return
		}
		cleanupErr := &ExecutionError{Outcome: ExecutionNotCommitted, Phase: PhaseCleanup, Cause: closeErr}
		if err == nil {
			err = cleanupErr
		} else {
			err = errors.Join(err, cleanupErr)
		}
	}()

	collector := e.Collector
	if collector == nil {
		// Keep validating/filtering the configured DSN for compatibility with
		// the existing PostgreSQL URL contract. GORM must not consume it here:
		// doing so would open and ping a second pool/session while the fence is
		// already the authoritative pinned connection.
		driverURL, filterErr := filteredPostgresURL(dsn)
		if filterErr != nil {
			report.Outcome, report.Phase = ExecutionNotCommitted, PhaseFence
			return report, &ExecutionError{Outcome: report.Outcome, Phase: report.Phase, Cause: filterErr}
		}
		gormDB, openErr := gorm.Open(postgres.New(postgres.Config{DSN: driverURL, Conn: fence.Conn()}), &gorm.Config{DisableAutomaticPing: true})
		if openErr != nil {
			report.Outcome, report.Phase = ExecutionNotCommitted, PhaseFence
			return report, &ExecutionError{Outcome: report.Outcome, Phase: report.Phase, Cause: openErr}
		}
		// The GORM dialector borrows fence.Conn(); closing its *sql.DB would
		// close the fence connection before the protected window is complete.
		// ExclusiveWriterFence remains the sole owner and closes it in Execute's
		// deferred cleanup.
		metadataTable, tableErr := migrations.ConfiguredMigrationTable(ctx, dsn, fence.Conn())
		if tableErr != nil {
			return initialReport, &ExecutionError{Outcome: ExecutionNotCommitted, Phase: PhaseFence, Cause: tableErr}
		}
		collector = NewPostgresFactCollectorWithMetadataTable(gormDB, metadataTable)
	}

	issuer := e.D007
	if issuer == nil {
		issuer = defaultD007Issuer
	}
	d010Issuer := e.D010
	if d010Issuer == nil {
		d010Issuer = defaultD010Issuer
	}
	innerReport, innerErr := executeProtected(ctx, fence, collector, mapping, resolver, e.D1, e.Lease, issuer, d010Issuer, e.installD010Handoff, e.hooks)
	innerReport.BackendPID = fence.BackendPID()
	report, err = innerReport, innerErr
	return report, err
}

// installD010Handoff retains the opaque continuation only on the owner object.
// It deliberately rejects replacing an unconsumed handoff: losing an issued
// bearer would otherwise create an untracked live continuation.
func (e *ProtectedExecutor) installD010Handoff(handoff D010Handoff) error {
	if e == nil || handoff.state == nil {
		return ErrD010HandoffInvalid
	}
	e.continuationMu.Lock()
	defer e.continuationMu.Unlock()
	if e.d010Handoff != nil {
		return ErrD010HandoffIssued
	}
	handoffCopy := handoff
	e.d010Handoff = &handoffCopy
	return nil
}

// ContinueD3Protected is the narrow owner seam for the named D3 protected
// continuation. It verifies and then consumes the private handoff under one
// lock; no D4 work is started here. Any failed attempt discards the bearer so
// copied, forged, stale, mismatched, replayed, or UNKNOWN handoffs fail closed.
func (e *ProtectedExecutor) ContinueD3Protected(expected D010HandoffContext) error {
	if e == nil {
		return ErrD010HandoffInvalid
	}
	e.continuationMu.Lock()
	defer e.continuationMu.Unlock()
	return e.continueD3ProtectedLocked(expected)
}

func (e *ProtectedExecutor) continueD3ProtectedLocked(expected D010HandoffContext) error {
	if e.d010Handoff == nil {
		return ErrD010HandoffInvalid
	}
	handoff := *e.d010Handoff
	if err := VerifyD010Handoff(handoff, expected); err != nil {
		e.d010Handoff = nil
		return err
	}
	consumeErr := ConsumeD010Handoff(handoff, expected)
	e.d010Handoff = nil
	return consumeErr
}

// executeProtected runs the complete protected window. The caller keeps the
// exclusive session lock held until this function returns; post-commit reads
// therefore occur before Execute's fence.Close releases it.
func executeProtected(ctx context.Context, fence *migrations.ExclusiveWriterFence, collector ReadOnlyCollectorWithConnection, mapping *MappingArtifact, resolver MappingResolver, d1 D1LeaseConsumer, lease *migrations.D1LLeaseIdentity, d007 *D007CapabilityIssuer, d010 *D010HandoffIssuer, installD010 func(D010Handoff) error, hooks protectedExecutionHooks) (ExecutionReport, error) {
	report := ExecutionReport{Outcome: ExecutionNotCommitted, Phase: PhaseFence, AppliedAffectedCounts: emptyAffectedCounts()}
	if lease != nil {
		// The owner-issued lease is the sole source of the execution operation
		// identity; never derive it from the plan artifact.
		report.OperationID = lease.OperationID
	}
	if fence == nil || fence.Conn() == nil {
		return report, protectedError(report, errors.New("pinned exclusive writer fence is required"))
	}
	if hooks.AfterFence != nil {
		if err := hooks.AfterFence(ctx, fence); err != nil {
			return report, protectedError(report, err)
		}
	}
	capability, err := fence.Capability()
	if err != nil {
		return report, protectedError(report, err)
	}
	if err := migrations.RequireProtectedWork(capability); err != nil {
		return report, protectedError(report, err)
	}
	if collector == nil {
		return report, protectedError(report, errors.New("query-only v5 collector is required"))
	}

	conn := fence.Conn()
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: false})
	if err != nil {
		report.Phase = PhaseTransaction
		return report, protectedError(report, err)
	}
	report.Phase = PhaseTransaction
	transactionOpen := true
	if hooks.AfterTransaction != nil {
		if err := hooks.AfterTransaction(ctx, tx); err != nil {
			_ = tx.Rollback()
			transactionOpen = false
			return report, protectedError(report, err)
		}
	}
	rollback := func() error {
		if !transactionOpen {
			return nil
		}
		transactionOpen = false
		return tx.Rollback()
	}
	fail := func(phase ExecutionPhase, cause error) (ExecutionReport, error) {
		report.Phase = phase
		report.Outcome = ExecutionNotCommitted
		rollbackErr := rollback()
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			cause = errors.Join(cause, fmt.Errorf("rollback: %w", rollbackErr))
		}
		return report, protectedError(report, cause)
	}

	// The full entity lock prefix precedes every authoritative collection and
	// mutation: Devices by ascending ID, then MeasurementPoints by UUID, then
	// assignments by UUID. Keep these statements together; adding a write or a
	// fact query before the prefix re-opens the reconciliation race.
	if err := lockProtectedEntityPrefix(ctx, tx); err != nil {
		return fail(PhaseTransaction, err)
	}
	if hooks.AfterEntityLockPrefix != nil {
		if err := hooks.AfterEntityLockPrefix(ctx, tx); err != nil {
			return fail(PhaseTransaction, err)
		}
	}

	// transaction_timestamp() is stable for this transaction and is sampled
	// exactly once, after the complete lock prefix. It is the sole temporal
	// authority input for this execution.
	if err := tx.QueryRowContext(ctx, "SELECT transaction_timestamp()").Scan(&report.FrozenAt); err != nil {
		return fail(PhaseTransaction, err)
	}
	report.FrozenAt = report.FrozenAt.UTC()
	if hooks.FrozenTime != nil {
		report.FrozenAt = hooks.FrozenTime(report.FrozenAt).UTC()
	}
	if hooks.AfterFrozenTime != nil {
		if err := hooks.AfterFrozenTime(ctx, tx, report.FrozenAt); err != nil {
			return fail(PhaseTransaction, err)
		}
	}

	facts, err := collector.CollectV5Pinned(ctx, report.FrozenAt, tx)
	if err != nil {
		return fail(PhaseRecheck, err)
	}
	if !facts.AsOf.Equal(report.FrozenAt) {
		return fail(PhaseRecheck, fmt.Errorf("collector changed frozen time: got %s want %s", facts.AsOf, report.FrozenAt))
	}
	if resolver != nil {
		mapping, err = resolver(ctx, facts)
		if err != nil {
			return fail(PhasePlan, err)
		}
	}
	plan, err := BuildPlan(facts, mapping)
	if err != nil {
		if strings.Contains(err.Error(), "explicit mapping artifact is stale for source facts") {
			err = errors.Join(ErrProtectedMappingStale, err)
		}
		return fail(PhasePlan, err)
	}
	report.PlanID = plan.PlanID
	report.PlanDigest = hex.EncodeToString(plan.Digest)
	report.SourceFactsDigest = plan.SourceFactsDigest
	if mappingBasisDigest, digestErr := MappingSourceFactsDigest(facts); digestErr != nil {
		return fail(PhasePlan, digestErr)
	} else {
		report.MappingBasisDigest = hex.EncodeToString(mappingBasisDigest)
	}
	report.MappingDigest = plan.MappingDigest
	report.ExpectedAffectedCounts = copyCounts(plan.ExpectedAffectedCounts)
	if len(plan.Blockers) != 0 || len(plan.RequiredExplicitMappings) != 0 {
		reason := fmt.Sprintf("blockers=%v required_explicit_mappings=%v", plan.Blockers, plan.RequiredExplicitMappings)
		return fail(PhasePlan, fmt.Errorf("%w: %s", ErrProtectedPlanBlocked, reason))
	}
	if err := validateExecutablePlan(plan, facts); err != nil {
		return fail(PhasePlan, err)
	}
	if hooks.AfterFreshPlan != nil {
		if err := hooks.AfterFreshPlan(ctx, tx, facts, plan); err != nil {
			return fail(PhasePlan, err)
		}
	}

	before := facts
	// The exclusive session fence has already drained cooperating writers and
	// blocks new cooperating writers before this point. A2.2 therefore keeps
	// the frozen global prefix (fence -> transaction -> fresh facts -> writes)
	// without adding a second entity-row lock order that could deadlock with
	// existing writers; every mutation remains an exact CAS.
	items := executableItems(plan.Items)
	if hasAuditWrites(items) {
		if err := inspectAuditTrigger(tx); err != nil {
			return fail(PhasePlan, err)
		}
	}

	triggerDisabled := false
	if hooks.BeforeWrite != nil && len(items) != 0 {
		if err := hooks.BeforeWrite(ctx, tx, facts, plan); err != nil {
			return fail(PhaseWrite, err)
		}
	}
	// D1 consumption is deliberately the final gate immediately before the
	// first A2 mutation/DDL. Mutating execution is fail-closed: a missing owner
	// consumer or exact owner-issued lease identity is never treated as a
	// no-op. D1 remains the sole authority for validity, expiry, and one-shot
	// state.
	if len(items) != 0 {
		if d1 == nil {
			return fail(PhaseWrite, errors.New("D1 lease consumer is required before protected mutation"))
		}
		if lease == nil || !validD1LeaseIdentity(*lease) {
			return fail(PhaseWrite, errors.New("exact owner-issued D1 lease identity is required before protected mutation"))
		}
		if err := d1.ConsumeLease(ctx, *lease); err != nil {
			return fail(PhaseWrite, err)
		}
	}
	restoreTrigger := func() error {
		if !triggerDisabled {
			return nil
		}
		if hooks.BeforeTriggerRestore != nil {
			if err := hooks.BeforeTriggerRestore(ctx, tx); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `ALTER TABLE public.admin_binding_audits ENABLE TRIGGER admin_binding_audits_immutable`); err != nil {
			return err
		}
		if err := verifyAuditTriggerEnabled(tx); err != nil {
			return err
		}
		if hooks.AfterTriggerRestore != nil {
			if err := hooks.AfterTriggerRestore(ctx, tx); err != nil {
				return err
			}
		}
		triggerDisabled = false
		report.TriggerRestored = true
		return nil
	}

	for itemIndex, item := range items {
		if item.Kind == PlanItemAdmin && item.AuditID != uuid.Nil && !triggerDisabled {
			if _, err := tx.ExecContext(ctx, `ALTER TABLE public.admin_binding_audits DISABLE TRIGGER admin_binding_audits_immutable`); err != nil {
				return fail(PhaseWrite, err)
			}
			triggerDisabled = true
		}
		if err := executeCASItem(ctx, tx, facts, item); err != nil {
			if restoreErr := restoreTrigger(); restoreErr != nil {
				err = errors.Join(err, fmt.Errorf("restore audit trigger: %w", restoreErr))
			}
			return fail(PhaseWrite, err)
		}
		incrementAffected(report.AppliedAffectedCounts, item)
		if hooks.AfterWrite != nil {
			if err := hooks.AfterWrite(ctx, tx, facts, plan, item, itemIndex); err != nil {
				if restoreErr := restoreTrigger(); restoreErr != nil {
					err = errors.Join(err, fmt.Errorf("restore audit trigger: %w", restoreErr))
				}
				return fail(PhaseWrite, err)
			}
		}
	}
	if err := restoreTrigger(); err != nil {
		return fail(PhaseVerify, err)
	}
	if err := verifyPlanItems(before, before, plan, false); err != nil {
		return fail(PhaseVerify, err)
	}
	afterBeforeCommit, err := collector.CollectV5Pinned(ctx, report.FrozenAt, tx)
	if err != nil {
		return fail(PhaseVerify, err)
	}
	if err := verifyPlanItems(before, afterBeforeCommit, plan, true); err != nil {
		return fail(PhaseVerify, err)
	}
	expectedFacts := applyExpectedFacts(before, items)
	if err := equalCanonicalFacts(expectedFacts, afterBeforeCommit); err != nil {
		return fail(PhaseVerify, err)
	}
	if err := compareCounts(plan.ExpectedAffectedCounts, report.AppliedAffectedCounts); err != nil {
		return fail(PhaseVerify, err)
	}
	report.TriggerRestored = !hasAuditWrites(items) || triggerDisabled == false

	if hooks.BeforeCommit != nil {
		if err := hooks.BeforeCommit(ctx, tx, facts, plan); err != nil {
			return fail(PhaseVerify, err)
		}
	}
	commit := hooks.Commit
	if commit == nil {
		commit = func(_ context.Context, tx *sql.Tx) error { return tx.Commit() }
	}
	if err := commit(ctx, tx); err != nil {
		transactionOpen = false
		report.Phase = PhaseCommit
		report.Outcome = ExecutionCommitOutcomeUnknown
		return report, &ExecutionError{Outcome: report.Outcome, Phase: report.Phase, Cause: err}
	}
	transactionOpen = false
	report.Committed = true
	if hooks.AfterCommit != nil {
		if err := hooks.AfterCommit(ctx, conn); err != nil {
			report.Outcome = ExecutionCommittedPostVerifyFailed
			report.Phase = PhasePostVerify
			return report, &ExecutionError{Outcome: report.Outcome, Phase: report.Phase, Cause: errors.Join(ErrProtectedPostVerification, err)}
		}
	}

	tx2, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		report.Outcome = ExecutionCommittedPostVerifyFailed
		report.Phase = PhasePostVerify
		return report, &ExecutionError{Outcome: report.Outcome, Phase: report.Phase, Cause: errors.Join(ErrProtectedPostVerification, err)}
	}
	if hooks.AfterTX2 != nil {
		if err := hooks.AfterTX2(ctx, tx2); err != nil {
			_ = tx2.Rollback()
			report.Outcome = ExecutionCommittedPostVerifyFailed
			report.Phase = PhasePostVerify
			return report, &ExecutionError{Outcome: report.Outcome, Phase: report.Phase, Cause: errors.Join(ErrProtectedPostVerification, err)}
		}
	}
	var tx2AsOf time.Time
	if err := tx2.QueryRowContext(ctx, "SELECT transaction_timestamp()").Scan(&tx2AsOf); err != nil {
		_ = tx2.Rollback()
		report.Outcome = ExecutionCommittedPostVerifyFailed
		report.Phase = PhasePostVerify
		return report, &ExecutionError{Outcome: report.Outcome, Phase: report.Phase, Cause: errors.Join(ErrProtectedPostVerification, err)}
	}
	tx2AsOf = tx2AsOf.UTC()
	postFacts, err := collector.CollectV5Pinned(ctx, tx2AsOf, tx2)
	if err == nil {
		err = validatePostCommitFactsAsOf(postFacts, tx2AsOf)
	}
	rollbackTX2 := tx2.Rollback()
	if err == nil && rollbackTX2 != nil && !errors.Is(rollbackTX2, sql.ErrTxDone) {
		err = rollbackTX2
	}
	if err != nil {
		report.Outcome = ExecutionCommittedPostVerifyFailed
		report.Phase = PhasePostVerify
		return report, &ExecutionError{Outcome: report.Outcome, Phase: report.Phase, Cause: errors.Join(ErrProtectedPostVerification, err)}
	}
	_, postDigest, digestErr := CanonicalSourceFacts(postFacts)
	if digestErr != nil {
		report.Outcome = ExecutionCommittedPostVerifyFailed
		report.Phase = PhasePostVerify
		return report, &ExecutionError{Outcome: report.Outcome, Phase: report.Phase, Cause: errors.Join(ErrProtectedPostVerification, digestErr)}
	}
	report.PostCommitFactsDigest = hex.EncodeToString(postDigest)
	report.PostCommitFactsAsOf = postFacts.AsOf
	if err := verifyPlanItems(before, postFacts, plan, true); err != nil {
		report.Outcome = ExecutionCommittedPostVerifyFailed
		report.Phase = PhasePostVerify
		return report, &ExecutionError{Outcome: report.Outcome, Phase: report.Phase, Cause: errors.Join(ErrProtectedPostVerification, err)}
	}
	// TX1 and TX2 intentionally have distinct transaction timestamps. The
	// timestamp was already checked against TX2 above; compare the durable
	// semantic facts at the TX2 observation without treating that expected
	// observation-time change as a data mutation.
	expectedPostCommitFacts := expectedFacts
	expectedPostCommitFacts.AsOf = postFacts.AsOf
	if err := equalCanonicalFacts(expectedPostCommitFacts, postFacts); err != nil {
		report.Outcome = ExecutionCommittedPostVerifyFailed
		report.Phase = PhasePostVerify
		return report, &ExecutionError{Outcome: report.Outcome, Phase: report.Phase, Cause: errors.Join(ErrProtectedPostVerification, err)}
	}
	if hasAuditWrites(items) {
		if err := inspectAuditTrigger(conn); err != nil {
			report.Outcome = ExecutionCommittedPostVerifyFailed
			report.Phase = PhasePostVerify
			return report, &ExecutionError{Outcome: report.Outcome, Phase: report.Phase, Cause: errors.Join(ErrProtectedPostVerification, err)}
		}
	}
	// TX2 and every durable post-verification predicate have succeeded. Only
	// now may the protected executor attach the unexported evidence seal.
	report.PostCommitVerified = true
	report.Outcome = ExecutionCommittedAndVerified
	report.Phase = PhasePostVerify
	// D2 readiness is strictly post-commit and post-TX2. It receives only the
	// fresh facts and safe A2 evidence, then issues and consumes one opaque
	// capability at its owner seam. The capability never enters report output.
	if d1 != nil && lease != nil {
		evidenceForReadiness := trustedReconciliationEvidence(report)
		decision := EvaluateReadiness(ReadinessRequest{
			Target: ReadinessForCutover, ProtectedState: migrations.ProtectedStateCleanV5,
			Facts: postFacts, Reconciliation: evidenceForReadiness,
		})
		if !decision.Ready {
			report.Outcome = ExecutionCommittedPostVerifyFailed
			report.Phase = PhasePostVerify
			return report, &ExecutionError{Outcome: report.Outcome, Phase: report.Phase, Cause: errors.Join(ErrProtectedPostVerification, readinessError(decision))}
		}
		if len(lease.TargetFingerprint) != 32 {
			report.Outcome = ExecutionCommittedPostVerifyFailed
			report.Phase = PhasePostVerify
			return report, &ExecutionError{Outcome: report.Outcome, Phase: report.Phase, Cause: errors.Join(ErrProtectedPostVerification, ErrD007CapabilityInvalid)}
		}
		var target [32]byte
		copy(target[:], lease.TargetFingerprint)
		freshUntil := d007FreshUntil(postFacts)
		live, issueErr := d007.issue(decision, postFacts, D007Binding{AttemptID: lease.AttemptID, TargetFingerprint: target, Generation: lease.Generation})
		if issueErr != nil {
			report.Outcome = ExecutionCommittedPostVerifyFailed
			report.Phase = PhasePostVerify
			return report, &ExecutionError{Outcome: report.Outcome, Phase: report.Phase, Cause: errors.Join(ErrProtectedPostVerification, issueErr)}
		}
		_, digestBytes, _ := CanonicalSourceFacts(postFacts)
		var factsDigest [32]byte
		copy(factsDigest[:], digestBytes)
		expected := D007CapabilityBinding{D007Binding: D007Binding{AttemptID: lease.AttemptID, TargetFingerprint: target, Generation: lease.Generation}, FactsDigest: factsDigest, ProofDigest: d007ProofDigest(decision, factsDigest), FreshUntil: freshUntil, PredicateVersion: D007PredicateVersion}
		evidence, consumeErr := ConsumeLiveD007Capability(live, expected, postFacts.AsOf)
		if consumeErr != nil {
			report.Outcome = ExecutionCommittedPostVerifyFailed
			report.Phase = PhasePostVerify
			return report, &ExecutionError{Outcome: report.Outcome, Phase: report.Phase, Cause: errors.Join(ErrProtectedPostVerification, consumeErr)}
		}
		report.D007Terminal = evidence

		// D007 is now terminal and TX1/TX2 verification is complete. Build the
		// sealed D009 projection from this exact owner report, immediately
		// exchange it for D010, then discard the private seal before returning
		// the ordinary report.
		if d010Err := issueProtectedD010(&report, lease, target, d010, installD010); d010Err != nil {
			report.Outcome = ExecutionCommittedPostVerifyFailed
			report.Phase = PhasePostVerify
			return report, &ExecutionError{Outcome: report.Outcome, Phase: report.Phase, Cause: errors.Join(ErrProtectedPostVerification, d010Err)}
		}
	}
	return report, nil
}

func issueProtectedD010(report *ExecutionReport, lease *migrations.D1LLeaseIdentity, target [32]byte, d010 *D010HandoffIssuer, install func(D010Handoff) error) error {
	if report == nil || lease == nil || d010 == nil || install == nil || lease.OperationID == uuid.Nil || report.OperationID != lease.OperationID {
		return ErrD010HandoffInvalid
	}
	// The seal exists only during this owner-local conversion and is cleared
	// before any report or diagnostic can observe it.
	report.d009Seal = makeD009ExecutionSeal(*report, lease, target)
	d009, err := D009EvidenceFromReport(*report)
	report.d009Seal = nil
	if err != nil {
		return err
	}
	issueD010 := IssueD010Handoff
	if d010 != defaultD010Issuer {
		issueD010 = d010.Issue
	}
	handoff, err := issueD010(report.D007Terminal, d009)
	if err != nil {
		return err
	}
	return install(handoff)
}

func validatePostCommitFactsAsOf(facts FactSet, tx2AsOf time.Time) error {
	if !facts.AsOf.Equal(tx2AsOf) {
		return fmt.Errorf("collector changed post-commit transaction time: got %s want %s", facts.AsOf, tx2AsOf)
	}
	return nil
}

// lockProtectedEntityPrefix is the canonical protected reconciliation row
// lock order. The collector currently reads the complete entity sets, so the
// safe narrow prefix is the complete Device, MeasurementPoint, and assignment
// tables. Every result is consumed before the next statement, ensuring all
// rows are locked before any authoritative collection or mutation begins.
func lockProtectedEntityPrefix(ctx context.Context, tx *sql.Tx) error {
	if tx == nil {
		return errors.New("protected reconciliation transaction is required")
	}
	queries := []string{
		`SELECT id FROM public.devices ORDER BY id FOR UPDATE`,
		`SELECT id FROM public.measurement_points ORDER BY id FOR UPDATE`,
		`SELECT id FROM public.device_assignments ORDER BY id FOR UPDATE`,
	}
	for _, query := range queries {
		rows, err := tx.QueryContext(ctx, query)
		if err != nil {
			return fmt.Errorf("lock protected entity prefix: %w", err)
		}
		for rows.Next() {
			var id interface{}
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan protected entity lock prefix: %w", err)
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("read protected entity lock prefix: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close protected entity lock prefix: %w", err)
		}
	}
	return nil
}

func protectedError(report ExecutionReport, cause error) error {
	return &ExecutionError{Outcome: report.Outcome, Phase: report.Phase, Cause: cause}
}

func validD1LeaseIdentity(identity migrations.D1LLeaseIdentity) bool {
	return identity.LeaseID != uuid.Nil && identity.OperationID != uuid.Nil && identity.AttemptID != uuid.Nil &&
		identity.Generation > 0 && len(identity.TargetFingerprint) == 32 && len(identity.EvidenceDigest) == 32
}

func emptyAffectedCounts() map[string]int {
	return map[string]int{
		ExpectedCountInventoryOwnerUpdates: 0,
		ExpectedCountShopClientUpdates:     0,
		ExpectedCountAdminClientUpdates:    0,
	}
}

func copyCounts(in map[string]int) map[string]int {
	out := make(map[string]int, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func compareCounts(expected, actual map[string]int) error {
	if len(expected) != len(actual) {
		return fmt.Errorf("affected-count key set differs: expected=%v actual=%v", expected, actual)
	}
	for key, value := range expected {
		if actual[key] != value {
			return fmt.Errorf("affected count %s=%d, want %d", key, actual[key], value)
		}
	}
	return nil
}

func validateExecutablePlan(plan Plan, facts FactSet) error {
	if plan.SchemaVersion != SchemaVersion || plan.PlanID == uuid.Nil || plan.SourceFactsDigest == "" || len(plan.Canonical) == 0 || len(plan.Digest) == 0 {
		return errors.New("fresh plan is incomplete")
	}
	if _, digest, err := CanonicalSourceFacts(facts); err != nil || plan.SourceFactsDigest != hex.EncodeToString(digest) {
		if err != nil {
			return fmt.Errorf("fresh plan source facts: %w", err)
		}
		return ErrProtectedMappingStale
	}
	for _, item := range plan.Items {
		flags := 0
		if item.SetInventoryOwner {
			flags++
		}
		if item.SetShopClient {
			flags++
		}
		if item.SetAdminClient {
			flags++
		}
		if flags > 1 || (flags == 1 && item.ExpectedAffectedCount != 1) || (flags == 0 && item.ExpectedAffectedCount != 0) {
			return fmt.Errorf("plan item %s has invalid write/count contract", item.StableID)
		}
	}
	return nil
}

func executableItems(items []PlanItem) []PlanItem {
	out := make([]PlanItem, 0, len(items))
	for _, item := range items {
		if item.SetInventoryOwner || item.SetShopClient || item.SetAdminClient {
			out = append(out, item)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		rank := func(item PlanItem) int {
			switch {
			case item.SetInventoryOwner:
				return 1
			case item.SetShopClient:
				return 2
			default:
				return 3
			}
		}
		ri, rj := rank(out[i]), rank(out[j])
		if ri != rj {
			return ri < rj
		}
		if out[i].OperationID != uuid.Nil || out[j].OperationID != uuid.Nil {
			if out[i].OperationID != out[j].OperationID {
				return out[i].OperationID.String() < out[j].OperationID.String()
			}
			if (out[i].AuditID == uuid.Nil) != (out[j].AuditID == uuid.Nil) {
				return out[i].AuditID == uuid.Nil
			}
			if out[i].AuditID != out[j].AuditID {
				return out[i].AuditID.String() < out[j].AuditID.String()
			}
		}
		if out[i].DeviceID != out[j].DeviceID {
			return out[i].DeviceID < out[j].DeviceID
		}
		if out[i].ShopID != out[j].ShopID {
			return out[i].ShopID < out[j].ShopID
		}
		return out[i].StableID.String() < out[j].StableID.String()
	})
	return out
}

func hasAuditWrites(items []PlanItem) bool {
	for _, item := range items {
		if item.SetAdminClient && item.AuditID != uuid.Nil {
			return true
		}
	}
	return false
}

func incrementAffected(counts map[string]int, item PlanItem) {
	switch {
	case item.SetInventoryOwner:
		counts[ExpectedCountInventoryOwnerUpdates]++
	case item.SetShopClient:
		counts[ExpectedCountShopClientUpdates]++
	case item.SetAdminClient:
		counts[ExpectedCountAdminClientUpdates]++
	}
}

func executeCASItem(ctx context.Context, tx *sql.Tx, facts FactSet, item PlanItem) error {
	intendedClient := itemIntendedClient(item)
	if intendedClient == nil && (item.SetInventoryOwner || item.SetShopClient || item.SetAdminClient) {
		return errors.New("write item has no intended Client")
	}
	var query string
	var args []interface{}
	switch {
	case item.SetShopClient:
		query = `UPDATE public.shops SET client_id = $1 WHERE id = $2 AND client_id IS NOT DISTINCT FROM $3`
		args = []interface{}{*intendedClient, item.ShopID, item.ExpectedCurrent.ClientID}
	case item.SetInventoryOwner:
		query = `UPDATE public.devices SET inventory_owner_client_id = $1 WHERE id = $2 AND inventory_owner_client_id IS NOT DISTINCT FROM $3`
		args = []interface{}{*intendedClient, item.DeviceID, item.ExpectedCurrent.InventoryOwnerClientID}
	case item.SetAdminClient:
		if item.AuditID != uuid.Nil {
			audit, ok := findAudit(facts, item.AuditID)
			if !ok {
				return fmt.Errorf("admin audit %s disappeared from fresh facts", item.AuditID)
			}
			query = `UPDATE public.admin_binding_audits SET client_id = $1 WHERE id = $2 AND operation_id = $3 AND action = $4 AND actor_id = $5 AND scope_key = $6 AND client_id IS NOT DISTINCT FROM $7`
			args = []interface{}{*intendedClient, audit.ID, audit.OperationID, audit.Action, audit.ActorID, audit.ScopeKey, item.ExpectedCurrent.ClientID}
		} else {
			op, ok := findOperation(facts, item.OperationID)
			if !ok {
				return fmt.Errorf("admin operation %s disappeared from fresh facts", item.OperationID)
			}
			query = `UPDATE public.admin_binding_operations SET client_id = $1 WHERE operation_id = $2 AND operation = $3 AND actor_id = $4 AND scope_key = $5 AND client_id IS NOT DISTINCT FROM $6`
			args = []interface{}{*intendedClient, op.OperationID, op.Operation, op.ActorID, op.ScopeKey, item.ExpectedCurrent.ClientID}
		}
	default:
		return errors.New("plan item has no supported write flag")
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("%w: item=%s affected=%d", ErrProtectedCASConflict, item.StableID, affected)
	}
	return nil
}

func findOperation(facts FactSet, id uuid.UUID) (AdminOperationFact, bool) {
	for _, operation := range facts.AdminOperations {
		if operation.OperationID == id {
			return operation, true
		}
	}
	return AdminOperationFact{}, false
}

func findAudit(facts FactSet, id uuid.UUID) (AdminAuditFact, bool) {
	for _, audit := range facts.AdminAudits {
		if audit.ID == id {
			return audit, true
		}
	}
	return AdminAuditFact{}, false
}

func verifyPlanItems(before, after FactSet, plan Plan, checkIntended bool) error {
	for _, item := range plan.Items {
		if item.SetShopClient {
			beforeShop, ok := findShop(before, item.ShopID)
			if !ok {
				return fmt.Errorf("shop %d missing from fresh facts", item.ShopID)
			}
			afterShop, ok := findShop(after, item.ShopID)
			if !ok {
				return fmt.Errorf("shop %d missing after reconciliation", item.ShopID)
			}
			if !sameUint(beforeShop.ClientID, item.ExpectedCurrent.ClientID) {
				return fmt.Errorf("shop %d pre-write value changed before execution", item.ShopID)
			}
			if checkIntended && !sameUint(afterShop.ClientID, item.IntendedClientID) {
				return fmt.Errorf("shop %d post-write Client mismatch", item.ShopID)
			}
		}
		if item.SetInventoryOwner {
			beforeDevice, ok := findDevice(before, item.DeviceID)
			if !ok {
				return fmt.Errorf("device %d missing from fresh facts", item.DeviceID)
			}
			afterDevice, ok := findDevice(after, item.DeviceID)
			if !ok {
				return fmt.Errorf("device %d missing after reconciliation", item.DeviceID)
			}
			if !sameUint(beforeDevice.InventoryOwnerClientID, item.ExpectedCurrent.InventoryOwnerClientID) {
				return fmt.Errorf("device %d pre-write value changed before execution", item.DeviceID)
			}
			if checkIntended && !sameUint(afterDevice.InventoryOwnerClientID, itemIntendedClient(item)) {
				return fmt.Errorf("device %d post-write owner mismatch", item.DeviceID)
			}
		}
		if item.SetAdminClient {
			if item.AuditID != uuid.Nil {
				beforeAudit, ok := findAudit(before, item.AuditID)
				if !ok {
					return fmt.Errorf("audit %s missing from fresh facts", item.AuditID)
				}
				afterAudit, ok := findAudit(after, item.AuditID)
				if !ok {
					return fmt.Errorf("audit %s missing after reconciliation", item.AuditID)
				}
				if !sameUint(beforeAudit.ClientID, item.ExpectedCurrent.ClientID) {
					return fmt.Errorf("audit %s pre-write value changed before execution", item.AuditID)
				}
				if checkIntended && !sameUint(afterAudit.ClientID, item.IntendedClientID) {
					return fmt.Errorf("audit %s post-write Client mismatch", item.AuditID)
				}
			} else {
				beforeOperation, ok := findOperation(before, item.OperationID)
				if !ok {
					return fmt.Errorf("operation %s missing from fresh facts", item.OperationID)
				}
				afterOperation, ok := findOperation(after, item.OperationID)
				if !ok {
					return fmt.Errorf("operation %s missing after reconciliation", item.OperationID)
				}
				if !sameUint(beforeOperation.ClientID, item.ExpectedCurrent.ClientID) {
					return fmt.Errorf("operation %s pre-write value changed before execution", item.OperationID)
				}
				if checkIntended && !sameUint(afterOperation.ClientID, item.IntendedClientID) {
					return fmt.Errorf("operation %s post-write Client mismatch", item.OperationID)
				}
			}
		}
	}
	return nil
}

func applyExpectedFacts(before FactSet, items []PlanItem) FactSet {
	after := normalizeFacts(before)
	for _, item := range items {
		if item.SetShopClient {
			for i := range after.Shops {
				if after.Shops[i].ID == item.ShopID {
					after.Shops[i].ClientID = cloneUint(item.IntendedClientID)
				}
			}
		}
		if item.SetInventoryOwner {
			for i := range after.Devices {
				if after.Devices[i].ID == item.DeviceID {
					after.Devices[i].InventoryOwnerClientID = cloneUint(itemIntendedClient(item))
				}
			}
		}
		if item.SetAdminClient {
			if item.AuditID != uuid.Nil {
				for i := range after.AdminAudits {
					if after.AdminAudits[i].ID == item.AuditID {
						after.AdminAudits[i].ClientID = cloneUint(item.IntendedClientID)
					}
				}
			} else {
				for i := range after.AdminOperations {
					if after.AdminOperations[i].OperationID == item.OperationID {
						after.AdminOperations[i].ClientID = cloneUint(item.IntendedClientID)
					}
				}
			}
		}
	}
	return after
}

func equalCanonicalFacts(expected, actual FactSet) error {
	expectedBytes, _, err := CanonicalSourceFacts(expected)
	if err != nil {
		return fmt.Errorf("canonicalize expected post-write facts: %w", err)
	}
	actualBytes, _, err := CanonicalSourceFacts(actual)
	if err != nil {
		return fmt.Errorf("canonicalize actual post-write facts: %w", err)
	}
	if !reflect.DeepEqual(expectedBytes, actualBytes) {
		return errors.New("post-write facts contain an unexpected mutation")
	}
	return nil
}

func itemIntendedClient(item PlanItem) *uint {
	if item.IntendedClientID != nil {
		return item.IntendedClientID
	}
	return item.IntendedOwnerClientID
}

func sameUint(left, right *uint) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func findShop(facts FactSet, id uint) (ShopFact, bool) {
	for _, shop := range facts.Shops {
		if shop.ID == id {
			return shop, true
		}
	}
	return ShopFact{}, false
}

func findDevice(facts FactSet, id uint) (DeviceFact, bool) {
	for _, device := range facts.Devices {
		if device.ID == id {
			return device, true
		}
	}
	return DeviceFact{}, false
}

type auditTriggerInfo struct {
	enabled        string
	functionSchema string
	function       string
	definition     string
}

func inspectAuditTrigger(conn ReadOnlyConnection) error {
	if conn == nil {
		return ErrProtectedTriggerUnexpected
	}
	rows, err := conn.QueryContext(context.Background(), `
SELECT t.tgenabled, pn.nspname, p.proname, pg_get_triggerdef(t.oid, true)
  FROM pg_trigger AS t
  JOIN pg_class AS c ON c.oid = t.tgrelid
  JOIN pg_namespace AS n ON n.oid = c.relnamespace
  JOIN pg_proc AS p ON p.oid = t.tgfoid
  JOIN pg_namespace AS pn ON pn.oid = p.pronamespace
 WHERE n.nspname = 'public'
   AND c.relname = 'admin_binding_audits'
   AND t.tgname = 'admin_binding_audits_immutable'
   AND NOT t.tgisinternal`)
	if err != nil {
		return fmt.Errorf("inspect immutable audit trigger: %w", err)
	}
	defer rows.Close()
	var found []auditTriggerInfo
	for rows.Next() {
		var info auditTriggerInfo
		if err := rows.Scan(&info.enabled, &info.functionSchema, &info.function, &info.definition); err != nil {
			return err
		}
		found = append(found, info)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(found) != 1 {
		return fmt.Errorf("%w: expected one immutable trigger, found %d", ErrProtectedTriggerUnexpected, len(found))
	}
	info := found[0]
	definition := strings.ToLower(strings.Join(strings.Fields(info.definition), " "))
	const expectedDefinition = "create trigger admin_binding_audits_immutable before delete or update on admin_binding_audits for each row execute function prevent_admin_binding_audit_mutation()"
	if info.functionSchema != "public" || info.function != "prevent_admin_binding_audit_mutation" || info.enabled != "O" || definition != expectedDefinition {
		return fmt.Errorf("%w: enabled=%q function=%s.%q definition=%q", ErrProtectedTriggerUnexpected, info.enabled, info.functionSchema, info.function, info.definition)
	}
	return nil
}

func verifyAuditTriggerEnabled(conn ReadOnlyConnection) error {
	return inspectAuditTrigger(conn)
}
