package migrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/golang-migrate/migrate/v4/database"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/lib/pq"
)

// ProtectedMigrationState is the only state authority used by the dedicated
// runner. A state is never inferred from metadata without catalog evidence.
type ProtectedMigrationState string

const (
	ProtectedStateCleanV5      ProtectedMigrationState = "CLEAN_V5"
	ProtectedStateDirtyV5      ProtectedMigrationState = "DIRTY_V5"
	ProtectedStateTransitionV6 ProtectedMigrationState = "TRANSITION_DIRTY_V6"
	ProtectedStateCleanV6      ProtectedMigrationState = "CLEAN_V6"
	ProtectedStateBootstrap    ProtectedMigrationState = "SUPPORTED_BOOTSTRAP"
	ProtectedStateAmbiguous    ProtectedMigrationState = "AMBIGUOUS"
	ProtectedStateFuture       ProtectedMigrationState = "UNSUPPORTED_FUTURE"
)

type ProtectedCatalogState string

const (
	ProtectedCatalogExactV5 ProtectedCatalogState = "EXACT_V5"
	ProtectedCatalogExactV6 ProtectedCatalogState = "EXACT_V6"
	ProtectedCatalogPartial ProtectedCatalogState = "PARTIAL_OR_MIXED"
	ProtectedCatalogEmpty   ProtectedCatalogState = "EMPTY"
	ProtectedCatalogUnknown ProtectedCatalogState = "UNKNOWN"
)

type ProtectedMigrationOutcome string

const (
	ProtectedNotCommitted            ProtectedMigrationOutcome = "NOT_COMMITTED"
	ProtectedCommittedAndVerified    ProtectedMigrationOutcome = "COMMITTED_AND_VERIFIED"
	ProtectedCommittedPostVerifyFail ProtectedMigrationOutcome = "COMMITTED_BUT_POSTVERIFY_FAILED"
	ProtectedCommitOutcomeUnknown    ProtectedMigrationOutcome = "COMMIT_OUTCOME_UNKNOWN"
	ProtectedAlreadyComplete         ProtectedMigrationOutcome = "ALREADY_COMPLETE"
)

type ProtectedMigrationPhase string

const (
	protectedMigrationLockTimeout   = 30 * time.Second
	protectedMigrationUnlockTimeout = 10 * time.Second

	ProtectedPhaseInspection       ProtectedMigrationPhase = "INSPECTION"
	ProtectedPhaseMigrationLock    ProtectedMigrationPhase = "MIGRATION_LOCK"
	ProtectedPhaseDirtyMarker      ProtectedMigrationPhase = "DIRTY_MARKER"
	ProtectedPhaseEnforcement      ProtectedMigrationPhase = "ENFORCEMENT"
	ProtectedPhaseFinalMarker      ProtectedMigrationPhase = "FINAL_MARKER"
	ProtectedPhasePostVerification ProtectedMigrationPhase = "POST_VERIFICATION"
	ProtectedPhaseRecovery         ProtectedMigrationPhase = "RECOVERY"
)

var (
	ErrProtectedMigrationSpec             = errors.New("protected migration runner specification is invalid")
	ErrProtectedMigrationState            = errors.New("protected migration starting state is not eligible")
	ErrProtectedMigrationRecoveryRequired = errors.New("protected migration requires explicit recovery")
	ErrProtectedMigrationAlreadyComplete  = errors.New("protected migration is already clean v6")
	ErrProtectedMigrationUnknownCommit    = errors.New("protected migration commit outcome is unknown")
	ErrProtectedMigrationPostVerification = errors.New("protected migration post-verification failed")
	ErrProtectedMigrationNoRetry          = errors.New("protected migration body must not be retried automatically")
	ErrProtectedMigrationCatalog          = errors.New("protected migration catalog proof failed")
)

// ProtectedMigrationQueryer is deliberately read-only. Semantic verifiers may
// inspect facts but cannot mutate the database or metadata authority.
type ProtectedMigrationQueryer interface {
	QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}

// ProtectedMigrationVerifier supplies the frozen semantic/data checks owned by
// D2/D4/D5. D3 supplies exact metadata and catalog checks around this seam.
type ProtectedMigrationVerifier func(context.Context, ProtectedMigrationQueryer) error

// ProtectedMigrationSpec supplies only the later enforcement body and exact
// final catalog expectation. It intentionally contains no A2 or readiness API.
type ProtectedMigrationSpec struct {
	ExternalWriterAdmission ExternalWriterAdmission
	V6CatalogTables         []string
	Apply                   func(context.Context, *sql.Tx) error
	V5SemanticVerifier      ProtectedMigrationVerifier
	V6SemanticVerifier      ProtectedMigrationVerifier
}

type ProtectedRecoveryAction string

const (
	ProtectedRecoveryRestoreCleanV5  ProtectedRecoveryAction = "RESTORE_CLEAN_V5"
	ProtectedRecoveryCompleteCleanV6 ProtectedRecoveryAction = "COMPLETE_CLEAN_V6"
)

type ProtectedMigrationReport struct {
	State              ProtectedMigrationState
	Catalog            ProtectedCatalogState
	Metadata           MigrationMetadataSnapshot
	PostCommitState    ProtectedMigrationState
	PostCommitCatalog  ProtectedCatalogState
	Outcome            ProtectedMigrationOutcome
	Phase              ProtectedMigrationPhase
	BackendPID         int64
	FenceState         ExclusiveOwnershipState
	MigrationLockKey   int64
	MigrationLockOwned bool
	PostCommitVerified bool
	Committed          bool
	MigrationLockState ExclusiveOwnershipState
}

type ProtectedMigrationError struct {
	Outcome ProtectedMigrationOutcome
	Phase   ProtectedMigrationPhase
	State   ProtectedMigrationState
	Cause   error
}

func (e *ProtectedMigrationError) Error() string {
	if e == nil {
		return "protected migration failed"
	}
	return fmt.Sprintf("protected migration %s during %s (state=%s): %v", e.Outcome, e.Phase, e.State, e.Cause)
}

func (e *ProtectedMigrationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

type protectedMigrationHooks struct {
	Commit func(context.Context, *sql.Tx) error
}

type protectedInspection struct {
	Metadata MigrationMetadataSnapshot
	Catalog  ProtectedCatalogState
	State    ProtectedMigrationState
}

// RunProtectedMigration is the sole D3 transition authority. It never calls
// the generic migrate driver and never supplies an enforcement body itself.
func RunProtectedMigration(ctx context.Context, databaseURL string, spec ProtectedMigrationSpec) (ProtectedMigrationReport, error) {
	return runProtectedMigration(ctx, databaseURL, spec, protectedMigrationHooks{})
}

// InspectProtectedMigration acquires the same protected session/lock order but
// performs no metadata or DDL writes.
func InspectProtectedMigration(ctx context.Context, databaseURL string, spec ProtectedMigrationSpec) (report ProtectedMigrationReport, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateProtectedMigrationSpec(spec, false); err != nil {
		return report, err
	}
	parsed, err := parsePostgresDatabaseURL(databaseURL)
	if err != nil {
		return report, err
	}
	fence, err := openExclusiveWriterFence(ctx, parsed)
	if err != nil {
		return report, err
	}
	report.BackendPID = fence.BackendPID()
	defer func() {
		if closeErr := fence.Close(); closeErr != nil {
			report.FenceState = fence.State()
			report.PostCommitVerified = false
			if report.Outcome == ProtectedCommittedAndVerified {
				report.Outcome = ProtectedCommittedPostVerifyFail
			}
			if err == nil {
				err = closeErr
			} else {
				err = errors.Join(err, closeErr)
			}
		} else {
			report.FenceState = fence.State()
		}
	}()
	lock, err := acquireMigrationAdvisoryLock(ctx, fence.Conn(), parsed)
	if err != nil {
		report.Phase = ProtectedPhaseMigrationLock
		if lock != nil {
			report.MigrationLockState = lock.state
			if cleanupErr := lock.Close(ctx); cleanupErr != nil {
				err = errors.Join(err, cleanupErr)
			}
			report.MigrationLockState = lock.state
		} else {
			report.MigrationLockState = ExclusiveNotAttempted
		}
		return report, err
	}
	defer func() {
		if lockErr := lock.Close(ctx); lockErr != nil {
			amendProtectedCleanupError(&report, &err, lockErr)
		}
		report.MigrationLockOwned = lock.owned
		report.MigrationLockState = lock.state
	}()
	report.MigrationLockKey = lock.key
	inspection, err := inspectProtectedOn(ctx, fence.Conn(), parsed.config, spec)
	if err != nil {
		return report, err
	}
	report = fillProtectedReport(report, inspection)
	switch inspection.State {
	case ProtectedStateCleanV5, ProtectedStateCleanV6:
		return report, nil
	case ProtectedStateDirtyV5, ProtectedStateTransitionV6:
		return report, protectedError(&report, ProtectedNotCommitted, ProtectedPhaseInspection, ErrProtectedMigrationRecoveryRequired)
	default:
		return report, protectedError(&report, ProtectedNotCommitted, ProtectedPhaseInspection, ErrProtectedMigrationState)
	}
}

func RecoverProtectedMigration(ctx context.Context, databaseURL string, spec ProtectedMigrationSpec, action ProtectedRecoveryAction) (report ProtectedMigrationReport, err error) {
	return recoverProtectedMigration(ctx, databaseURL, spec, action, protectedMigrationHooks{})
}

func recoverProtectedMigration(ctx context.Context, databaseURL string, spec ProtectedMigrationSpec, action ProtectedRecoveryAction, hooks protectedMigrationHooks) (report ProtectedMigrationReport, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateProtectedMigrationSpec(spec, false); err != nil {
		return report, err
	}
	if err := RequireExternalWriterAdmission(spec.ExternalWriterAdmission); err != nil {
		return report, protectedError(&report, ProtectedNotCommitted, ProtectedPhaseInspection, err)
	}
	if action == ProtectedRecoveryRestoreCleanV5 && spec.V5SemanticVerifier == nil {
		return report, fmt.Errorf("%w: v5 semantic verifier is required for recovery", ErrProtectedMigrationSpec)
	}
	if action == ProtectedRecoveryCompleteCleanV6 && spec.V6SemanticVerifier == nil {
		return report, fmt.Errorf("%w: v6 semantic verifier is required for recovery", ErrProtectedMigrationSpec)
	}
	parsed, err := parsePostgresDatabaseURL(databaseURL)
	if err != nil {
		return report, err
	}
	fence, err := openExclusiveWriterFence(ctx, parsed)
	if err != nil {
		return report, err
	}
	report.BackendPID = fence.BackendPID()
	capability, err := fence.Capability()
	if err != nil {
		return report, protectedError(&report, ProtectedNotCommitted, ProtectedPhaseInspection, err)
	}
	if err := RequireProtectedWork(capability); err != nil {
		return report, protectedError(&report, ProtectedNotCommitted, ProtectedPhaseInspection, err)
	}
	defer func() {
		if closeErr := fence.Close(); closeErr != nil {
			report.FenceState = fence.State()
			report.PostCommitVerified = false
			amendProtectedCleanupError(&report, &err, closeErr)
		} else {
			report.FenceState = fence.State()
		}
	}()
	lock, err := acquireMigrationAdvisoryLock(ctx, fence.Conn(), parsed)
	if err != nil {
		report.Phase = ProtectedPhaseMigrationLock
		if lock != nil {
			report.MigrationLockState = lock.state
			if cleanupErr := lock.Close(ctx); cleanupErr != nil {
				err = errors.Join(err, cleanupErr)
			}
			report.MigrationLockState = lock.state
		} else {
			report.MigrationLockState = ExclusiveNotAttempted
		}
		return report, err
	}
	defer func() {
		if lockErr := lock.Close(ctx); lockErr != nil {
			amendProtectedCleanupError(&report, &err, lockErr)
		}
		report.MigrationLockOwned = lock.owned
		report.MigrationLockState = lock.state
	}()
	report.MigrationLockKey = lock.key
	inspection, err := inspectProtectedOn(ctx, fence.Conn(), parsed.config, spec)
	if err != nil {
		return report, err
	}
	report = fillProtectedReport(report, inspection)
	allowed := action == ProtectedRecoveryRestoreCleanV5 && (inspection.State == ProtectedStateDirtyV5 || inspection.State == ProtectedStateTransitionV6) && inspection.Catalog == ProtectedCatalogExactV5
	if action == ProtectedRecoveryCompleteCleanV6 {
		allowed = inspection.State == ProtectedStateTransitionV6 && inspection.Catalog == ProtectedCatalogExactV6
	}
	if !allowed {
		return report, protectedError(&report, ProtectedNotCommitted, ProtectedPhaseRecovery, fmt.Errorf("%w: action=%s catalog=%s", ErrProtectedMigrationRecoveryRequired, action, inspection.Catalog))
	}
	version := 5
	dirty := false
	if action == ProtectedRecoveryCompleteCleanV6 {
		version = 6
	}
	if err := setProtectedMetadataWithHooks(ctx, fence.Conn(), parsed.config, version, dirty, spec, hooks); err != nil {
		report.Phase = ProtectedPhaseRecovery
		if errors.Is(err, ErrProtectedMigrationUnknownCommit) {
			report.Outcome = ProtectedCommitOutcomeUnknown
			var cleanupErr error
			report.PostCommitState, report.PostCommitCatalog, cleanupErr = resolveUnknown(ctx, parsed, spec, fence, lock)
			return report, protectedError(&report, ProtectedCommitOutcomeUnknown, ProtectedPhaseRecovery, errors.Join(err, ErrProtectedMigrationNoRetry, cleanupErr))
		}
		return report, protectedError(&report, ProtectedNotCommitted, ProtectedPhaseRecovery, err)
	}
	fresh, err := pinnedProtectedInspection(ctx, fence.Conn(), parsed.config, spec)
	if err != nil {
		report.PostCommitState, report.PostCommitCatalog = ProtectedStateAmbiguous, ProtectedCatalogUnknown
		report.Outcome, report.Phase, report.Committed = ProtectedCommittedPostVerifyFail, ProtectedPhasePostVerification, true
		return report, &ProtectedMigrationError{Outcome: report.Outcome, Phase: report.Phase, State: inspection.State, Cause: errors.Join(err, ErrProtectedMigrationPostVerification, ErrProtectedMigrationNoRetry)}
	}
	report.PostCommitState, report.PostCommitCatalog = fresh.State, fresh.Catalog
	wantState, wantCatalog := ProtectedStateCleanV5, ProtectedCatalogExactV5
	if action == ProtectedRecoveryCompleteCleanV6 {
		wantState, wantCatalog = ProtectedStateCleanV6, ProtectedCatalogExactV6
	}
	if fresh.State != wantState || fresh.Catalog != wantCatalog {
		report.Outcome = ProtectedCommittedPostVerifyFail
		report.Phase = ProtectedPhasePostVerification
		report.Committed = true
		return report, &ProtectedMigrationError{Outcome: report.Outcome, Phase: report.Phase, State: inspection.State, Cause: errors.Join(fmt.Errorf("%w: recovery target state=%s catalog=%s, got state=%s catalog=%s", ErrProtectedMigrationPostVerification, wantState, wantCatalog, fresh.State, fresh.Catalog), ErrProtectedMigrationNoRetry)}
	}
	report.Outcome = ProtectedCommittedAndVerified
	report.Phase = ProtectedPhasePostVerification
	report.Committed, report.PostCommitVerified = true, true
	return report, nil
}

func runProtectedMigration(ctx context.Context, databaseURL string, spec ProtectedMigrationSpec, hooks protectedMigrationHooks) (report ProtectedMigrationReport, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateProtectedMigrationSpec(spec, true); err != nil {
		return report, err
	}
	if err := RequireExternalWriterAdmission(spec.ExternalWriterAdmission); err != nil {
		return report, protectedError(&report, ProtectedNotCommitted, ProtectedPhaseInspection, err)
	}
	parsed, err := parsePostgresDatabaseURL(databaseURL)
	if err != nil {
		return report, err
	}
	fence, err := openExclusiveWriterFence(ctx, parsed)
	if err != nil {
		return report, err
	}
	report.BackendPID = fence.BackendPID()
	defer func() {
		if closeErr := fence.Close(); closeErr != nil {
			report.FenceState = fence.State()
			report.PostCommitVerified = false
			amendProtectedCleanupError(&report, &err, closeErr)
		} else {
			report.FenceState = fence.State()
		}
	}()
	capability, err := fence.Capability()
	if err != nil {
		return report, protectedError(&report, ProtectedNotCommitted, ProtectedPhaseInspection, err)
	}
	if err := RequireProtectedWork(capability); err != nil {
		return report, protectedError(&report, ProtectedNotCommitted, ProtectedPhaseInspection, err)
	}
	lock, err := acquireMigrationAdvisoryLock(ctx, fence.Conn(), parsed)
	if err != nil {
		report.Phase = ProtectedPhaseMigrationLock
		if lock != nil {
			report.MigrationLockState = lock.state
			if cleanupErr := lock.Close(ctx); cleanupErr != nil {
				err = errors.Join(err, cleanupErr)
			}
			report.MigrationLockState = lock.state
		} else {
			report.MigrationLockState = ExclusiveNotAttempted
		}
		return report, err
	}
	defer func() {
		if lockErr := lock.Close(ctx); lockErr != nil {
			amendProtectedCleanupError(&report, &err, lockErr)
		}
		report.MigrationLockOwned = lock.owned
		report.MigrationLockState = lock.state
	}()
	report.MigrationLockKey = lock.key
	inspection, err := inspectProtectedOn(ctx, fence.Conn(), parsed.config, spec)
	if err != nil {
		return report, err
	}
	report = fillProtectedReport(report, inspection)
	switch inspection.State {
	case ProtectedStateCleanV6:
		report.Outcome, report.Phase = ProtectedAlreadyComplete, ProtectedPhaseInspection
		return report, protectedError(&report, report.Outcome, report.Phase, ErrProtectedMigrationAlreadyComplete)
	case ProtectedStateDirtyV5, ProtectedStateTransitionV6:
		return report, protectedError(&report, ProtectedNotCommitted, ProtectedPhaseInspection, ErrProtectedMigrationRecoveryRequired)
	case ProtectedStateAmbiguous, ProtectedStateFuture, ProtectedStateBootstrap:
		return report, protectedError(&report, ProtectedNotCommitted, ProtectedPhaseInspection, ErrProtectedMigrationState)
	case ProtectedStateCleanV5:
		// continue
	default:
		return report, protectedError(&report, ProtectedNotCommitted, ProtectedPhaseInspection, ErrProtectedMigrationState)
	}

	// The marker transaction is separate from the enforcement transaction. It
	// is the durable evidence that a body may have been started.
	markerTx, err := fence.Conn().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return report, protectedError(&report, ProtectedNotCommitted, ProtectedPhaseDirtyMarker, err)
	}
	markerInspection, err := inspectProtectedOn(ctx, markerTx, parsed.config, spec)
	if err != nil || markerInspection.State != ProtectedStateCleanV5 {
		_ = markerTx.Rollback()
		if err == nil {
			err = fmt.Errorf("marker recheck state=%s catalog=%s", markerInspection.State, markerInspection.Catalog)
		}
		return report, protectedError(&report, ProtectedNotCommitted, ProtectedPhaseDirtyMarker, err)
	}
	if err := setMetadataInTx(ctx, markerTx, parsed.config, 6, true, spec); err != nil {
		_ = markerTx.Rollback()
		return report, protectedError(&report, ProtectedNotCommitted, ProtectedPhaseDirtyMarker, err)
	}
	commit := hooks.Commit
	if commit == nil {
		commit = func(_ context.Context, tx *sql.Tx) error { return tx.Commit() }
	}
	if err := commit(ctx, markerTx); err != nil {
		report.Phase, report.Outcome = ProtectedPhaseDirtyMarker, ProtectedCommitOutcomeUnknown
		var cleanupErr error
		report.PostCommitState, report.PostCommitCatalog, cleanupErr = resolveUnknown(ctx, parsed, spec, fence, lock)
		return report, protectedError(&report, ProtectedCommitOutcomeUnknown, ProtectedPhaseDirtyMarker, errors.Join(ErrProtectedMigrationUnknownCommit, ErrProtectedMigrationNoRetry, err, cleanupErr))
	}

	bodyTx, err := fence.Conn().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		report.PostCommitState, report.PostCommitCatalog = ProtectedStateTransitionV6, ProtectedCatalogExactV5
		return report, protectedError(&report, ProtectedNotCommitted, ProtectedPhaseEnforcement, err)
	}
	bodyInspection, err := inspectProtectedOn(ctx, bodyTx, parsed.config, spec)
	if err != nil || bodyInspection.State != ProtectedStateTransitionV6 {
		_ = bodyTx.Rollback()
		if err == nil {
			err = fmt.Errorf("enforcement recheck state=%s catalog=%s", bodyInspection.State, bodyInspection.Catalog)
			report.PostCommitState, report.PostCommitCatalog = bodyInspection.State, bodyInspection.Catalog
		} else {
			report.PostCommitState, report.PostCommitCatalog = ProtectedStateAmbiguous, ProtectedCatalogUnknown
		}
		return report, protectedError(&report, ProtectedNotCommitted, ProtectedPhaseEnforcement, err)
	}
	if err := spec.Apply(ctx, bodyTx); err != nil {
		_ = bodyTx.Rollback()
		// A failed body is not replayed. Independent exact-v5 proof permits the
		// controlled metadata recovery required by the frozen S4/S5 path.
		fresh, freshErr := pinnedProtectedInspection(ctx, fence.Conn(), parsed.config, spec)
		if freshErr == nil {
			report.PostCommitState, report.PostCommitCatalog = fresh.State, fresh.Catalog
		}
		if freshErr == nil && fresh.State == ProtectedStateTransitionV6 && fresh.Catalog == ProtectedCatalogExactV5 {
			if restoreErr := setProtectedMetadataWithHooks(ctx, fence.Conn(), parsed.config, 5, false, spec, hooks); restoreErr != nil {
				if errors.Is(restoreErr, ErrProtectedMigrationUnknownCommit) {
					report.Phase, report.Outcome = ProtectedPhaseRecovery, ProtectedCommitOutcomeUnknown
					var cleanupErr error
					report.PostCommitState, report.PostCommitCatalog, cleanupErr = resolveUnknown(ctx, parsed, spec, fence, lock)
					return report, protectedError(&report, ProtectedCommitOutcomeUnknown, ProtectedPhaseRecovery, errors.Join(err, restoreErr, ErrProtectedMigrationNoRetry, cleanupErr))
				}
				return report, protectedError(&report, ProtectedCommittedPostVerifyFail, ProtectedPhasePostVerification, errors.Join(err, restoreErr, ErrProtectedMigrationPostVerification, ErrProtectedMigrationNoRetry))
			}
			fresh, freshErr = pinnedProtectedInspection(ctx, fence.Conn(), parsed.config, spec)
			if freshErr == nil {
				report.PostCommitState, report.PostCommitCatalog = fresh.State, fresh.Catalog
			} else {
				report.PostCommitState, report.PostCommitCatalog = ProtectedStateAmbiguous, ProtectedCatalogUnknown
			}
		}
		if freshErr != nil {
			report.PostCommitState, report.PostCommitCatalog = ProtectedStateAmbiguous, ProtectedCatalogUnknown
			report.Committed = true
			return report, protectedError(&report, ProtectedCommittedPostVerifyFail, ProtectedPhasePostVerification, errors.Join(err, freshErr, ErrProtectedMigrationPostVerification, ErrProtectedMigrationNoRetry))
		}
		if fresh.State != ProtectedStateCleanV5 || fresh.Catalog != ProtectedCatalogExactV5 {
			report.Committed = true
			return report, protectedError(&report, ProtectedCommittedPostVerifyFail, ProtectedPhasePostVerification, errors.Join(err, fmt.Errorf("metadata recovery target mismatch: state=%s catalog=%s", fresh.State, fresh.Catalog), ErrProtectedMigrationPostVerification, ErrProtectedMigrationNoRetry))
		}
		report.Committed = true
		return report, protectedError(&report, ProtectedCommittedPostVerifyFail, ProtectedPhasePostVerification, errors.Join(ErrProtectedMigrationPostVerification, ErrProtectedMigrationNoRetry, err))
	}
	if err := verifyCatalog(ctx, bodyTx, parsed.config, spec, ProtectedCatalogExactV6); err != nil {
		_ = bodyTx.Rollback()
		report.PostCommitState, report.PostCommitCatalog = ProtectedStateTransitionV6, ProtectedCatalogExactV5
		return report, protectedError(&report, ProtectedNotCommitted, ProtectedPhaseEnforcement, err)
	}
	if err := verifyMetadataOnly(ctx, bodyTx, parsed.config, 6, true); err != nil {
		_ = bodyTx.Rollback()
		report.PostCommitState, report.PostCommitCatalog = ProtectedStateTransitionV6, ProtectedCatalogExactV5
		return report, protectedError(&report, ProtectedNotCommitted, ProtectedPhaseEnforcement, err)
	}
	if err := commit(ctx, bodyTx); err != nil {
		report.Phase, report.Outcome = ProtectedPhaseEnforcement, ProtectedCommitOutcomeUnknown
		var cleanupErr error
		report.PostCommitState, report.PostCommitCatalog, cleanupErr = resolveUnknown(ctx, parsed, spec, fence, lock)
		return report, protectedError(&report, ProtectedCommitOutcomeUnknown, ProtectedPhaseEnforcement, errors.Join(ErrProtectedMigrationUnknownCommit, ErrProtectedMigrationNoRetry, err, cleanupErr))
	}
	report.Committed = true
	postBody, err := pinnedProtectedInspection(ctx, fence.Conn(), parsed.config, spec)
	if err != nil || postBody.State != ProtectedStateTransitionV6 || postBody.Catalog != ProtectedCatalogExactV6 {
		if err != nil {
			report.PostCommitState, report.PostCommitCatalog = ProtectedStateAmbiguous, ProtectedCatalogUnknown
		} else {
			report.PostCommitState, report.PostCommitCatalog = postBody.State, postBody.Catalog
		}
		if err == nil {
			err = fmt.Errorf("post-enforcement proof state=%s catalog=%s", postBody.State, postBody.Catalog)
		}
		return report, protectedError(&report, ProtectedCommittedPostVerifyFail, ProtectedPhasePostVerification, errors.Join(ErrProtectedMigrationPostVerification, ErrProtectedMigrationNoRetry, err))
	}

	finalTx, err := fence.Conn().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		report.PostCommitState, report.PostCommitCatalog = ProtectedStateTransitionV6, ProtectedCatalogExactV6
		return report, protectedError(&report, ProtectedCommittedPostVerifyFail, ProtectedPhasePostVerification, errors.Join(ErrProtectedMigrationPostVerification, ErrProtectedMigrationNoRetry, err))
	}
	finalInspection, err := inspectProtectedOn(ctx, finalTx, parsed.config, spec)
	if err != nil || finalInspection.State != ProtectedStateTransitionV6 || finalInspection.Catalog != ProtectedCatalogExactV6 {
		_ = finalTx.Rollback()
		if err == nil {
			err = fmt.Errorf("final marker recheck state=%s catalog=%s", finalInspection.State, finalInspection.Catalog)
			report.PostCommitState, report.PostCommitCatalog = finalInspection.State, finalInspection.Catalog
		} else {
			report.PostCommitState, report.PostCommitCatalog = ProtectedStateAmbiguous, ProtectedCatalogUnknown
		}
		return report, protectedError(&report, ProtectedCommittedPostVerifyFail, ProtectedPhasePostVerification, errors.Join(ErrProtectedMigrationPostVerification, ErrProtectedMigrationNoRetry, err))
	}
	if err := setMetadataInTx(ctx, finalTx, parsed.config, 6, false, spec); err != nil {
		_ = finalTx.Rollback()
		report.PostCommitState, report.PostCommitCatalog = ProtectedStateTransitionV6, ProtectedCatalogExactV6
		return report, protectedError(&report, ProtectedCommittedPostVerifyFail, ProtectedPhasePostVerification, errors.Join(ErrProtectedMigrationPostVerification, ErrProtectedMigrationNoRetry, err))
	}
	if err := commit(ctx, finalTx); err != nil {
		report.Phase, report.Outcome = ProtectedPhaseFinalMarker, ProtectedCommitOutcomeUnknown
		var cleanupErr error
		report.PostCommitState, report.PostCommitCatalog, cleanupErr = resolveUnknown(ctx, parsed, spec, fence, lock)
		return report, protectedError(&report, ProtectedCommitOutcomeUnknown, ProtectedPhaseFinalMarker, errors.Join(ErrProtectedMigrationUnknownCommit, ErrProtectedMigrationNoRetry, err, cleanupErr))
	}
	finalProof, err := pinnedProtectedInspection(ctx, fence.Conn(), parsed.config, spec)
	if err != nil || finalProof.State != ProtectedStateCleanV6 || finalProof.Catalog != ProtectedCatalogExactV6 {
		if err != nil {
			report.PostCommitState, report.PostCommitCatalog = ProtectedStateAmbiguous, ProtectedCatalogUnknown
		} else {
			report.PostCommitState, report.PostCommitCatalog = finalProof.State, finalProof.Catalog
		}
		if err == nil {
			err = fmt.Errorf("clean v6 proof state=%s catalog=%s", finalProof.State, finalProof.Catalog)
		}
		return report, protectedError(&report, ProtectedCommittedPostVerifyFail, ProtectedPhasePostVerification, errors.Join(ErrProtectedMigrationPostVerification, ErrProtectedMigrationNoRetry, err))
	}
	report.PostCommitState, report.PostCommitCatalog = finalProof.State, finalProof.Catalog
	report.Outcome, report.Phase, report.PostCommitVerified = ProtectedCommittedAndVerified, ProtectedPhasePostVerification, true
	return report, nil
}

func amendProtectedCleanupError(report *ProtectedMigrationReport, err *error, cleanup error) {
	if report == nil || err == nil || cleanup == nil {
		return
	}
	if report.Outcome == ProtectedCommitOutcomeUnknown {
		*err = errors.Join(*err, ErrProtectedMigrationPostVerification, ErrProtectedMigrationNoRetry, cleanup)
		return
	}
	if report.Committed || report.Outcome == ProtectedCommittedAndVerified || report.Outcome == ProtectedCommittedPostVerifyFail {
		report.Outcome, report.Phase, report.PostCommitVerified = ProtectedCommittedPostVerifyFail, ProtectedPhasePostVerification, false
		*err = errors.Join(*err, ErrProtectedMigrationPostVerification, ErrProtectedMigrationNoRetry, cleanup)
		return
	}
	*err = errors.Join(*err, cleanup)
}

func protectedError(report *ProtectedMigrationReport, outcome ProtectedMigrationOutcome, phase ProtectedMigrationPhase, cause error) error {
	if report != nil {
		report.Outcome = outcome
		report.Phase = phase
	}
	state := ProtectedStateAmbiguous
	if report != nil {
		state = report.State
	}
	return &ProtectedMigrationError{Outcome: outcome, Phase: phase, State: state, Cause: cause}
}

func fillProtectedReport(report ProtectedMigrationReport, inspection protectedInspection) ProtectedMigrationReport {
	report.State, report.Catalog, report.Metadata = inspection.State, inspection.Catalog, inspection.Metadata
	return report
}

func validateProtectedMigrationSpec(spec ProtectedMigrationSpec, requireApply bool) error {
	// V6CatalogTables is retained as a compatibility field, but it is not an
	// authority input. A caller may omit it or repeat the canonical expectation;
	// it may never replace or shrink D3's protected catalog universe.
	if spec.V6CatalogTables != nil {
		seen := map[string]bool{}
		for _, table := range spec.V6CatalogTables {
			if table == "" || strings.ContainsAny(table, "\".;") || seen[table] {
				return fmt.Errorf("%w: invalid or duplicate v6 catalog table %q", ErrProtectedMigrationSpec, table)
			}
			seen[table] = true
		}
		if !catalogTablesEqual(spec.V6CatalogTables, protectedV6CatalogTables) {
			return fmt.Errorf("%w: caller v6 catalog table set is not the canonical protected expectation", ErrProtectedMigrationSpec)
		}
	}
	if requireApply {
		if spec.Apply == nil {
			return fmt.Errorf("%w: enforcement body is required", ErrProtectedMigrationSpec)
		}
		if spec.V5SemanticVerifier == nil || spec.V6SemanticVerifier == nil {
			return fmt.Errorf("%w: v5 and v6 semantic verifiers are required for a transition", ErrProtectedMigrationSpec)
		}
	}
	return nil
}

func inspectProtectedOn(ctx context.Context, q ProtectedMigrationQueryer, config *postgres.Config, spec ProtectedMigrationSpec) (protectedInspection, error) {
	var schema string
	if err := q.QueryRowContext(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
		return protectedInspection{}, err
	}
	metadata, err := readMetadataSnapshot(ctx, q, config, schema)
	if err != nil {
		return protectedInspection{}, err
	}
	catalogTables, err := readCatalogTables(ctx, q, schema, config, metadata)
	if err != nil {
		return protectedInspection{}, err
	}
	// A3 v6 changes constraint validation/nullability without necessarily
	// changing the table set. Prove v6 structural state first, then v5.
	catalog := ProtectedCatalogPartial
	if len(catalogTables) == 0 {
		catalog = ProtectedCatalogEmpty
	}
	var v5ProofErr, v6ProofErr error
	if catalogTablesContainExpected(catalogTables, protectedV6CatalogTables) {
		v6ProofErr = verifySecurityCatalog(ctx, q, schema, true)
		if v6ProofErr == nil {
			catalog = ProtectedCatalogExactV6
		}
	}
	if catalog != ProtectedCatalogExactV6 && catalogTablesContainExpected(catalogTables, v5CatalogTables) {
		v5ProofErr = verifySecurityCatalog(ctx, q, schema, false)
		if v5ProofErr == nil {
			catalog = ProtectedCatalogExactV5
		}
	}
	if catalog == ProtectedCatalogExactV6 {
		if spec.V6SemanticVerifier != nil {
			if err := spec.V6SemanticVerifier(ctx, q); err != nil {
				catalog = ProtectedCatalogPartial
			}
		}
	}
	if catalog == ProtectedCatalogExactV5 && spec.V5SemanticVerifier != nil {
		if err := spec.V5SemanticVerifier(ctx, q); err != nil {
			catalog = ProtectedCatalogPartial
		}
	}
	state := classifyProtectedState(metadata, catalog)
	return protectedInspection{Metadata: metadata, Catalog: catalog, State: state}, nil
}

func classifyProtectedState(metadata MigrationMetadataSnapshot, catalog ProtectedCatalogState) ProtectedMigrationState {
	if !metadata.Exists || metadata.RowCount == 0 {
		if catalog == ProtectedCatalogEmpty {
			return ProtectedStateBootstrap
		}
		return ProtectedStateAmbiguous
	}
	if metadata.RowCount != 1 || !metadata.HasVersion {
		return ProtectedStateAmbiguous
	}
	switch metadata.Version {
	case 5:
		if metadata.Dirty {
			return ProtectedStateDirtyV5
		}
		if catalog == ProtectedCatalogExactV5 {
			return ProtectedStateCleanV5
		}
	case 6:
		if metadata.Dirty {
			return ProtectedStateTransitionV6
		}
		if catalog == ProtectedCatalogExactV6 {
			return ProtectedStateCleanV6
		}
	default:
		if metadata.Version > 6 {
			return ProtectedStateFuture
		}
	}
	return ProtectedStateAmbiguous
}

func readMetadataSnapshot(ctx context.Context, q ProtectedMigrationQueryer, config *postgres.Config, currentSchema string) (MigrationMetadataSnapshot, error) {
	schema, table, err := migrationMetadataIdentifiers(config, currentSchema)
	if err != nil {
		return MigrationMetadataSnapshot{}, err
	}
	qualified := quotedMigrationTable(schema, table)
	var relation sql.NullString
	if err := q.QueryRowContext(ctx, "SELECT to_regclass($1)", qualified).Scan(&relation); err != nil {
		return MigrationMetadataSnapshot{}, fmt.Errorf("inspect configured migration metadata relation: %w", err)
	}
	if !relation.Valid || relation.String == "" {
		return MigrationMetadataSnapshot{CatalogEmpty: true}, nil
	}
	var relkind string
	if err := q.QueryRowContext(ctx, `SELECT c.relkind FROM pg_class AS c JOIN pg_namespace AS n ON n.oid = c.relnamespace WHERE n.nspname = $1 AND c.relname = $2`, schema, table).Scan(&relkind); err != nil {
		return MigrationMetadataSnapshot{}, fmt.Errorf("inspect configured migration metadata relation kind: %w", err)
	}
	if relkind != "r" && relkind != "p" {
		return MigrationMetadataSnapshot{}, fmt.Errorf("configured migration metadata relation %s.%s is not a table", schema, table)
	}
	rows, err := q.QueryContext(ctx, "SELECT version, dirty FROM "+qualified+" ORDER BY version")
	if err != nil {
		return MigrationMetadataSnapshot{Exists: true}, fmt.Errorf("read configured migration metadata: %w", err)
	}
	defer rows.Close()
	snapshot := MigrationMetadataSnapshot{Exists: true}
	for rows.Next() {
		var version int
		var dirty bool
		if err := rows.Scan(&version, &dirty); err != nil {
			return snapshot, err
		}
		snapshot.RowCount++
		if snapshot.RowCount == 1 {
			snapshot.Version, snapshot.Dirty, snapshot.HasVersion = version, dirty, true
		}
	}
	if err := rows.Err(); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

func readCatalogTables(ctx context.Context, q ProtectedMigrationQueryer, schema string, config *postgres.Config, metadata MigrationMetadataSnapshot) ([]string, error) {
	metadataSchema, metadataTable, err := migrationMetadataIdentifiers(config, schema)
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT c.relname FROM pg_class AS c JOIN pg_namespace AS n ON n.oid = c.relnamespace WHERE n.nspname = $1 AND c.relkind IN ('r', 'p') ORDER BY c.relname`, schema)
	if err != nil {
		return nil, fmt.Errorf("inspect protected catalog: %w", err)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return nil, err
		}
		// Metadata is not part of the protected application catalog. Exclude
		// the canonical default name even when a custom configured authority
		// coexists with a conflicting public decoy relation.
		if table == postgres.DefaultMigrationsTable || (schema == metadataSchema && table == metadataTable) {
			continue
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tables, nil
}

func catalogTablesEqual(actual, expected []string) bool {
	if len(actual) != len(expected) {
		return false
	}
	a := append([]string(nil), actual...)
	e := append([]string(nil), expected...)
	sort.Strings(a)
	sort.Strings(e)
	for i := range a {
		if a[i] != e[i] {
			return false
		}
	}
	return true
}

// catalogTablesContainExpected deliberately ignores unrelated relations. The
// protected proof is closed-world only for the specified Security-owned
// objects; rejecting every unrelated table would incorrectly make a shared
// schema impossible to inspect.
func catalogTablesContainExpected(actual, expected []string) bool {
	seen := make(map[string]struct{}, len(actual))
	for _, table := range actual {
		seen[table] = struct{}{}
	}
	for _, table := range expected {
		if _, ok := seen[table]; !ok {
			return false
		}
	}
	return true
}

func classifyCatalogTables(actual, v6 []string) ProtectedCatalogState {
	if len(actual) == 0 {
		return ProtectedCatalogEmpty
	}
	exact := func(expected []string) bool {
		if len(actual) != len(expected) {
			return false
		}
		a := append([]string(nil), actual...)
		e := append([]string(nil), expected...)
		sort.Strings(a)
		sort.Strings(e)
		for i := range a {
			if a[i] != e[i] {
				return false
			}
		}
		return true
	}
	if exact(v5CatalogTables) {
		return ProtectedCatalogExactV5
	}
	if exact(v6) {
		return ProtectedCatalogExactV6
	}
	return ProtectedCatalogPartial
}

var v5CatalogTables = []string{"admin_binding_audits", "admin_binding_operations", "alert_logs", "clients", "daily_usages", "device_alert_settings", "device_assignments", "device_types", "devices", "measurement_points", "power_readings", "refresh_sessions", "refresh_tokens", "shops", "system_configs", "telemetry_ingest_keys", "user_shop_relations", "users"}

// The current checkpoint has no 000006 table additions. Keep this authority
// owned by D3 rather than accepting a caller-selected inventory.
var protectedV6CatalogTables = append([]string(nil), v5CatalogTables...)

var targetForeignKeys = []string{"security_shops_client_id_fkey", "security_devices_inventory_owner_client_id_fkey", "security_user_shop_relations_user_id_fkey", "security_user_shop_relations_shop_id_fkey", "security_admin_binding_operations_client_id_fkey", "security_admin_binding_audits_client_id_fkey", "security_admin_binding_audits_client_provenance_fkey"}

type protectedForeignKeyShape struct {
	parent, reference, child, referenceColumns string
}

var protectedForeignKeyShapes = map[string]protectedForeignKeyShape{
	"security_shops_client_id_fkey":                        {parent: "shops", reference: "clients", child: "client_id", referenceColumns: "id"},
	"security_devices_inventory_owner_client_id_fkey":      {parent: "devices", reference: "clients", child: "inventory_owner_client_id", referenceColumns: "id"},
	"security_user_shop_relations_user_id_fkey":            {parent: "user_shop_relations", reference: "users", child: "user_id", referenceColumns: "id"},
	"security_user_shop_relations_shop_id_fkey":            {parent: "user_shop_relations", reference: "shops", child: "shop_id", referenceColumns: "id"},
	"security_admin_binding_operations_client_id_fkey":     {parent: "admin_binding_operations", reference: "clients", child: "client_id", referenceColumns: "id"},
	"security_admin_binding_audits_client_id_fkey":         {parent: "admin_binding_audits", reference: "clients", child: "client_id", referenceColumns: "id"},
	"security_admin_binding_audits_client_provenance_fkey": {parent: "admin_binding_audits", reference: "admin_binding_operations", child: "operation_id,action,actor_id,scope_key,client_id", referenceColumns: "operation_id,operation,actor_id,scope_key,client_id"},
}

var targetNullableColumns = []string{"shops.client_id", "devices.inventory_owner_client_id", "admin_binding_operations.client_id", "admin_binding_audits.client_id"}

// These are compared to pg_get_functiondef(), PostgreSQL's deterministic
// canonical function representation. Whitespace-only formatting differences
// are immaterial; a same-name body replacement changes the canonical text.
var protectedFunctionDefinitions = map[string]string{
	"validate_admin_binding_audit_client_provenance": `CREATE OR REPLACE FUNCTION public.validate_admin_binding_audit_client_provenance()
 RETURNS trigger
 LANGUAGE plpgsql
AS $function$
DECLARE
    operation_client_id BIGINT;
BEGIN
    SELECT operation.client_id
      INTO operation_client_id
      FROM admin_binding_operations AS operation
     WHERE operation.operation_id = NEW.operation_id
       AND operation.operation = NEW.action
       AND operation.actor_id = NEW.actor_id
       AND operation.scope_key = NEW.scope_key;

    IF NOT FOUND OR operation_client_id IS DISTINCT FROM NEW.client_id THEN
        RAISE EXCEPTION 'admin binding audit Client provenance does not match its operation';
    END IF;
    RETURN NEW;
END;
$function$`,
	"prevent_admin_binding_audit_mutation": `CREATE OR REPLACE FUNCTION public.prevent_admin_binding_audit_mutation()
 RETURNS trigger
 LANGUAGE plpgsql
AS $function$
BEGIN
    RAISE EXCEPTION 'admin_binding_audits is immutable';
END;
$function$`,
}

var protectedForeignKeyParents = []string{"shops", "devices", "user_shop_relations", "admin_binding_operations", "admin_binding_audits"}

// Only these columns define the protected FK target universe. Existing
// unrelated provenance FKs on the same tables remain portable, while a
// differently named FK that touches a protected column is rejected.
var protectedForeignKeyTargetColumns = map[string]map[string]bool{
	"shops":                    {"client_id": true},
	"devices":                  {"inventory_owner_client_id": true},
	"user_shop_relations":      {"user_id": true, "shop_id": true},
	"admin_binding_operations": {"client_id": true},
	"admin_binding_audits":     {"client_id": true},
}

func touchesProtectedForeignKeyTarget(parent, childColumns string) bool {
	targets := protectedForeignKeyTargetColumns[parent]
	if len(targets) == 0 {
		return false
	}
	for _, column := range strings.Split(childColumns, ",") {
		if targets[column] {
			return true
		}
	}
	return false
}

func verifySecurityCatalog(ctx context.Context, q ProtectedMigrationQueryer, schema string, validated bool) error {
	expectedOIDs := make(map[string][2]int64, len(protectedForeignKeyShapes))
	for name, shape := range protectedForeignKeyShapes {
		var parentOID, referenceOID int64
		if err := q.QueryRowContext(ctx, "SELECT to_regclass($1)::oid", quotedMigrationTable(schema, shape.parent)).Scan(&parentOID); err != nil {
			return fmt.Errorf("%w: resolve parent relation for %s: %v", ErrProtectedMigrationCatalog, name, err)
		}
		if err := q.QueryRowContext(ctx, "SELECT to_regclass($1)::oid", quotedMigrationTable(schema, shape.reference)).Scan(&referenceOID); err != nil {
			return fmt.Errorf("%w: resolve reference relation for %s: %v", ErrProtectedMigrationCatalog, name, err)
		}
		expectedOIDs[name] = [2]int64{parentOID, referenceOID}
	}
	rows, err := q.QueryContext(ctx, `SELECT c.conname, c.convalidated, c.contype, c.confdeltype, c.confupdtype, c.confmatchtype, c.condeferrable, c.condeferred, r.oid::bigint, ref.oid::bigint, r.relname, refn.nspname, ref.relname, (SELECT string_agg(a.attname, ',' ORDER BY u.ord) FROM unnest(c.conkey) WITH ORDINALITY AS u(attnum, ord) JOIN pg_attribute AS a ON a.attrelid = c.conrelid AND a.attnum = u.attnum), (SELECT string_agg(a.attname, ',' ORDER BY u.ord) FROM unnest(c.confkey) WITH ORDINALITY AS u(attnum, ord) JOIN pg_attribute AS a ON a.attrelid = c.confrelid AND a.attnum = u.attnum) FROM pg_constraint AS c JOIN pg_class AS r ON r.oid = c.conrelid JOIN pg_class AS ref ON ref.oid = c.confrelid JOIN pg_namespace AS n ON n.oid = r.relnamespace JOIN pg_namespace AS refn ON refn.oid = ref.relnamespace WHERE n.nspname = $1 AND c.contype = 'f' AND r.relname = ANY($2)`, schema, pq.Array(protectedForeignKeyParents))
	if err != nil {
		return err
	}
	found := map[string]bool{}
	for rows.Next() {
		var name, del, upd, match, parent, referenceSchema, reference, childColumns, referenceColumns string
		var parentOID, referenceOID int64
		var valid, deferrable, deferred bool
		var typ string
		if err := rows.Scan(&name, &valid, &typ, &del, &upd, &match, &deferrable, &deferred, &parentOID, &referenceOID, &parent, &referenceSchema, &reference, &childColumns, &referenceColumns); err != nil {
			rows.Close()
			return err
		}
		shape, expected := protectedForeignKeyShapes[name]
		if !expected {
			if touchesProtectedForeignKeyTarget(parent, childColumns) {
				rows.Close()
				return fmt.Errorf("%w: unexpected protected foreign key %s on %s(%s)", ErrProtectedMigrationCatalog, name, parent, childColumns)
			}
			continue
		}
		oids := expectedOIDs[name]
		if typ != "f" || valid != validated || del != "r" || upd != "a" || match != "s" || deferrable || deferred || parentOID != oids[0] || referenceOID != oids[1] || parent != shape.parent || referenceSchema != schema || reference != shape.reference || childColumns != shape.child || referenceColumns != shape.referenceColumns {
			rows.Close()
			return fmt.Errorf("%w: foreign key %s properties or columns mismatch", ErrProtectedMigrationCatalog, name)
		}
		if found[name] {
			rows.Close()
			return fmt.Errorf("%w: duplicate foreign key %s", ErrProtectedMigrationCatalog, name)
		}
		found[name] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(found) != len(targetForeignKeys) {
		return fmt.Errorf("%w: foreign key count=%d want=%d", ErrProtectedMigrationCatalog, len(found), len(targetForeignKeys))
	}
	for _, column := range targetNullableColumns {
		parts := strings.SplitN(column, ".", 2)
		var notNull bool
		err := q.QueryRowContext(ctx, `SELECT a.attnotnull FROM pg_attribute AS a JOIN pg_class AS c ON c.oid = a.attrelid JOIN pg_namespace AS n ON n.oid = c.relnamespace WHERE n.nspname = $1 AND c.relname = $2 AND a.attname = $3 AND NOT a.attisdropped`, schema, parts[0], parts[1]).Scan(&notNull)
		if err != nil || notNull == !validated {
			return fmt.Errorf("%w: column %s nullability mismatch: %v", ErrProtectedMigrationCatalog, column, err)
		}
	}
	if err := verifyProtectedTrigger(ctx, q, schema, "admin_binding_audits_client_provenance", "public", "validate_admin_binding_audit_client_provenance", "create trigger admin_binding_audits_client_provenance before insert on admin_binding_audits for each row execute function validate_admin_binding_audit_client_provenance()"); err != nil {
		return err
	}
	if err := verifyProtectedTrigger(ctx, q, schema, "admin_binding_audits_immutable", "public", "prevent_admin_binding_audit_mutation", "create trigger admin_binding_audits_immutable before delete or update on admin_binding_audits for each row execute function prevent_admin_binding_audit_mutation()"); err != nil {
		return err
	}
	indices := map[string]struct {
		parent    string
		columns   string
		unique    bool
		partial   bool
		predicate string
	}{
		"security_shops_client_id_idx":                            {"shops", "client_id ASC", false, false, ""},
		"security_devices_inventory_owner_client_id_idx":          {"devices", "inventory_owner_client_id ASC", false, false, ""},
		"security_user_shop_relations_shop_user_idx":              {"user_shop_relations", "shop_id ASC,user_id ASC", false, false, ""},
		"security_admin_binding_operations_client_time_idx":       {"admin_binding_operations", "client_id ASC,created_at DESC", false, true, "(client_id IS NOT NULL)"},
		"security_admin_binding_audits_client_time_idx":           {"admin_binding_audits", "client_id ASC,occurred_at DESC", false, true, "(client_id IS NOT NULL)"},
		"security_admin_binding_operations_client_provenance_key": {"admin_binding_operations", "operation_id ASC,operation ASC,actor_id ASC,scope_key ASC,client_id ASC", true, false, ""},
	}
	for indexName, expected := range indices {
		var count int
		var valid, unique, partial sql.NullBool
		var parent, columns, method, predicate sql.NullString
		var keyColumns, allColumns int
		err := q.QueryRowContext(ctx, `SELECT count(*), bool_and(i.indisvalid AND i.indisready), bool_or(i.indisunique), bool_or(i.indpred IS NOT NULL), max(t.relname), max(am.amname), max(i.indnkeyatts), max(i.indnatts), max(pg_get_expr(i.indpred, i.indrelid)), max((SELECT string_agg(CASE WHEN u.attnum > 0 THEN a.attname ELSE '<expression>' END || CASE WHEN (i.indoption[u.ord - 1] & 1) <> 0 THEN ' DESC' ELSE ' ASC' END, ',' ORDER BY u.ord) FROM unnest(i.indkey) WITH ORDINALITY AS u(attnum, ord) LEFT JOIN pg_attribute AS a ON a.attrelid=i.indrelid AND a.attnum=u.attnum)) FROM pg_class AS t JOIN pg_namespace AS n ON n.oid=t.relnamespace JOIN pg_index AS i ON i.indrelid=t.oid JOIN pg_class AS idx ON idx.oid=i.indexrelid JOIN pg_am AS am ON am.oid=idx.relam WHERE n.nspname=$1 AND idx.relname=$2`, schema, indexName).Scan(&count, &valid, &unique, &partial, &parent, &method, &keyColumns, &allColumns, &predicate, &columns)
		actualPredicate := ""
		if predicate.Valid {
			actualPredicate = strings.ToLower(strings.Join(strings.Fields(predicate.String), " "))
		}
		expectedPredicate := strings.ToLower(strings.Join(strings.Fields(expected.predicate), " "))
		if err != nil || count != 1 || !valid.Valid || !valid.Bool || !unique.Valid || !partial.Valid || !parent.Valid || !method.Valid || !columns.Valid || keyColumns != allColumns || method.String != "btree" || unique.Bool != expected.unique || partial.Bool != expected.partial || actualPredicate != expectedPredicate || parent.String != expected.parent || columns.String != expected.columns {
			return fmt.Errorf("%w: required index %s mismatch count=%d parent=%q columns=%q unique=%v partial=%v valid=%v method=%s predicate=%q key_columns=%d all_columns=%d err=%v", ErrProtectedMigrationCatalog, indexName, count, parent.String, columns.String, unique, partial, valid, method.String, actualPredicate, keyColumns, allColumns, err)
		}
	}
	return nil
}

func verifyProtectedTrigger(ctx context.Context, q ProtectedMigrationQueryer, schema, name, functionSchema, functionName, expectedDefinition string) error {
	var count int
	var actualFunctionSchema, actualFunction, enabled, definition string
	var identityArguments, returnType, language, kind, volatility, functionDefinition string
	var functionOID int64
	var securityDefiner bool
	err := q.QueryRowContext(ctx, `SELECT count(*), max(pn.nspname), max(p.proname), max(t.tgenabled), max(pg_get_triggerdef(t.oid, true)), max(p.oid::bigint), max(pg_get_function_identity_arguments(p.oid)), max(format_type(p.prorettype, NULL)), max(l.lanname), max(p.prokind), max(p.provolatile), bool_or(p.prosecdef), max(pg_get_functiondef(p.oid)) FROM pg_trigger AS t JOIN pg_class AS c ON c.oid=t.tgrelid JOIN pg_namespace AS n ON n.oid=c.relnamespace JOIN pg_proc AS p ON p.oid=t.tgfoid JOIN pg_namespace AS pn ON pn.oid=p.pronamespace JOIN pg_language AS l ON l.oid=p.prolang WHERE n.nspname=$1 AND c.relname='admin_binding_audits' AND t.tgname=$2 AND NOT t.tgisinternal`, schema, name).Scan(&count, &actualFunctionSchema, &actualFunction, &enabled, &definition, &functionOID, &identityArguments, &returnType, &language, &kind, &volatility, &securityDefiner, &functionDefinition)
	normalizedTrigger := strings.ToLower(strings.Join(strings.Fields(definition), " "))
	expectedFunctionDefinition, knownFunction := protectedFunctionDefinitions[functionName]
	normalizedFunction := strings.ToLower(strings.Join(strings.Fields(functionDefinition), " "))
	if err != nil || count != 1 || functionOID == 0 || actualFunctionSchema != functionSchema || actualFunction != functionName || identityArguments != "" || returnType != "trigger" || language != "plpgsql" || kind != "f" || volatility != "v" || securityDefiner || enabled != "O" || normalizedTrigger != expectedDefinition || !knownFunction || normalizedFunction != strings.ToLower(strings.Join(strings.Fields(expectedFunctionDefinition), " ")) {
		return fmt.Errorf("%w: trigger %s count=%d function=%s.%s oid=%d identity=%q return=%s language=%s kind=%s volatility=%s security_definer=%t enabled=%s definition=%q function_definition=%q err=%v", ErrProtectedMigrationCatalog, name, count, actualFunctionSchema, actualFunction, functionOID, identityArguments, returnType, language, kind, volatility, securityDefiner, enabled, definition, functionDefinition, err)
	}
	return nil
}

func verifyCatalog(ctx context.Context, q ProtectedMigrationQueryer, config *postgres.Config, spec ProtectedMigrationSpec, want ProtectedCatalogState) error {
	var schema string
	if err := q.QueryRowContext(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
		return err
	}
	metadata, err := readMetadataSnapshot(ctx, q, config, schema)
	if err != nil {
		return err
	}
	tables, err := readCatalogTables(ctx, q, schema, config, metadata)
	if err != nil {
		return err
	}
	expectedTables := v5CatalogTables
	if want == ProtectedCatalogExactV6 {
		expectedTables = protectedV6CatalogTables
	}
	if !catalogTablesContainExpected(tables, expectedTables) {
		return fmt.Errorf("%w: want=%s protected table set is incomplete", ErrProtectedMigrationCatalog, want)
	}
	return verifySecurityCatalog(ctx, q, schema, want == ProtectedCatalogExactV6)
}

func verifyMetadataOnly(ctx context.Context, q ProtectedMigrationQueryer, config *postgres.Config, version int, dirty bool) error {
	var schema string
	if err := q.QueryRowContext(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
		return err
	}
	snapshot, err := readMetadataSnapshot(ctx, q, config, schema)
	if err != nil {
		return err
	}
	if snapshot.RowCount != 1 || snapshot.Version != version || snapshot.Dirty != dirty {
		return fmt.Errorf("metadata transition mismatch: %s", snapshot)
	}
	return nil
}

func setMetadataInTx(ctx context.Context, tx *sql.Tx, config *postgres.Config, version int, dirty bool, spec ProtectedMigrationSpec) error {
	var schema string
	if err := tx.QueryRowContext(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
		return err
	}
	metadataSchema, metadataTable, err := migrationMetadataIdentifiers(config, schema)
	if err != nil {
		return err
	}
	qualified := quotedMigrationTable(metadataSchema, metadataTable)
	result, err := tx.ExecContext(ctx, "UPDATE "+qualified+" SET version=$1, dirty=$2", version, dirty)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return fmt.Errorf("metadata transition cardinality=%d err=%v", count, err)
	}
	return verifyMetadataOnly(ctx, tx, config, version, dirty)
}

func setProtectedMetadata(ctx context.Context, conn *sql.Conn, config *postgres.Config, version int, dirty bool, spec ProtectedMigrationSpec) error {
	return setProtectedMetadataWithHooks(ctx, conn, config, version, dirty, spec, protectedMigrationHooks{})
}

func setProtectedMetadataWithHooks(ctx context.Context, conn *sql.Conn, config *postgres.Config, version int, dirty bool, spec ProtectedMigrationSpec, hooks protectedMigrationHooks) error {
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return err
	}
	if err := setMetadataInTx(ctx, tx, config, version, dirty, spec); err != nil {
		_ = tx.Rollback()
		return err
	}
	commit := hooks.Commit
	if commit == nil {
		commit = func(_ context.Context, tx *sql.Tx) error { return tx.Commit() }
	}
	if err := commit(ctx, tx); err != nil {
		return errors.Join(ErrProtectedMigrationUnknownCommit, err)
	}
	return nil
}

func independentProtectedInspection(ctx context.Context, parsed *parsedPostgresDatabaseURL, spec ProtectedMigrationSpec) (protectedInspection, error) {
	db, err := sql.Open("postgres", parsed.driverURL)
	if err != nil {
		return protectedInspection{}, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return protectedInspection{}, err
	}
	inspection, err := inspectProtectedOn(ctx, tx, parsed.config, spec)
	if rollbackErr := tx.Rollback(); err == nil && rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
		err = rollbackErr
	}
	return inspection, err
}

func pinnedProtectedInspection(ctx context.Context, conn *sql.Conn, config *postgres.Config, spec ProtectedMigrationSpec) (protectedInspection, error) {
	if conn == nil {
		return protectedInspection{}, errors.New("pinned protected inspection connection is required")
	}
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return protectedInspection{}, err
	}
	inspection, inspectErr := inspectProtectedOn(ctx, tx, config, spec)
	if rollbackErr := tx.Rollback(); inspectErr == nil && rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
		inspectErr = rollbackErr
	}
	return inspection, inspectErr
}

// discardUnknownProtectedSession invalidates the uncertain pinned session
// before any outcome inspection. A fresh probe independently proves the old
// backend disappeared and that both session-scoped locks can be acquired and
// released in canonical reverse order. The old conn is never reused.
func discardUnknownProtectedSession(fence *ExclusiveWriterFence, lock *migrationAdvisoryLock) error {
	if fence == nil || lock == nil || fence.pid == 0 || fence.dsn == "" {
		return ErrPhysicalConnectionDiscardRequired
	}
	pid, dsn, migrationKey := fence.pid, fence.dsn, lock.key
	if fence.conn != nil {
		_ = fence.conn.Close()
		fence.conn = nil
	}
	if fence.db != nil {
		_ = fence.db.Close()
		fence.db = nil
	}
	fence.state, fence.discarded = ExclusiveUnknown, true
	lock.conn, lock.owned, lock.state, lock.discarded = nil, false, ExclusiveUnknown, true

	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	probeDB, err := sql.Open("postgres", dsn)
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
		var present bool
		if err := probe.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE pid = $1)`, pid).Scan(&present); err != nil {
			return errors.Join(ErrPhysicalConnectionDiscardRequired, err)
		}
		if !present {
			var fenceOwned bool
			if err := probe.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1::bigint)", WriterFenceKey).Scan(&fenceOwned); err != nil {
				return errors.Join(ErrPhysicalConnectionDiscardRequired, err)
			}
			if fenceOwned {
				var migrationOwned bool
				if err := probe.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1::bigint)", migrationKey).Scan(&migrationOwned); err != nil {
					return errors.Join(ErrPhysicalConnectionDiscardRequired, err)
				}
				if migrationOwned {
					var migrationReleased, fenceReleased bool
					if err := probe.QueryRowContext(ctx, unlockWriterFenceSQL, migrationKey).Scan(&migrationReleased); err != nil {
						return errors.Join(ErrPhysicalConnectionDiscardRequired, err)
					}
					if err := probe.QueryRowContext(ctx, unlockWriterFenceSQL, WriterFenceKey).Scan(&fenceReleased); err != nil {
						return errors.Join(ErrPhysicalConnectionDiscardRequired, err)
					}
					if !migrationReleased || !fenceReleased {
						return errors.Join(ErrPhysicalConnectionDiscardRequired, ErrWriterFenceUnlockFailed)
					}
					fence.state, fence.discarded = ExclusiveReleased, true
					lock.state, lock.discarded = ExclusiveReleased, true
					return nil
				}
				var released bool
				if err := probe.QueryRowContext(ctx, unlockWriterFenceSQL, WriterFenceKey).Scan(&released); err != nil || !released {
					return errors.Join(ErrPhysicalConnectionDiscardRequired, ErrWriterFenceUnlockFailed, err)
				}
			}
		}
		select {
		case <-ctx.Done():
			return errors.Join(ErrPhysicalConnectionDiscardRequired, ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func resolveUnknown(ctx context.Context, parsed *parsedPostgresDatabaseURL, spec ProtectedMigrationSpec, fence *ExclusiveWriterFence, lock *migrationAdvisoryLock) (ProtectedMigrationState, ProtectedCatalogState, error) {
	if err := discardUnknownProtectedSession(fence, lock); err != nil {
		return ProtectedStateAmbiguous, ProtectedCatalogUnknown, err
	}
	inspection, err := independentProtectedInspection(ctx, parsed, spec)
	if err != nil {
		return ProtectedStateAmbiguous, ProtectedCatalogUnknown, err
	}
	return inspection.State, inspection.Catalog, nil
}

type migrationAdvisoryLock struct {
	conn      *sql.Conn
	key       int64
	owned     bool
	state     ExclusiveOwnershipState
	discarded bool
}

func acquireMigrationAdvisoryLock(ctx context.Context, conn *sql.Conn, parsed *parsedPostgresDatabaseURL) (*migrationAdvisoryLock, error) {
	if conn == nil || parsed == nil || parsed.config == nil {
		return nil, errors.New("migration advisory lock requires pinned connection and configuration")
	}
	var currentSchema string
	if err := conn.QueryRowContext(ctx, "SELECT current_schema()").Scan(&currentSchema); err != nil {
		return nil, err
	}
	schema, table, err := migrationMetadataIdentifiers(parsed.config, currentSchema)
	if err != nil {
		return nil, err
	}
	databaseName := parsed.config.DatabaseName
	if databaseName == "" {
		if err := conn.QueryRowContext(ctx, "SELECT current_database()").Scan(&databaseName); err != nil {
			return nil, fmt.Errorf("resolve migration advisory database name: %w", err)
		}
	}
	aid, err := database.GenerateAdvisoryLockId(databaseName, schema, table)
	if err != nil {
		return nil, err
	}
	key, err := strconv.ParseInt(aid, 10, 64)
	if err != nil {
		return nil, err
	}
	lock := &migrationAdvisoryLock{conn: conn, key: key, state: ExclusiveWaiting}
	deadline := time.NewTimer(protectedMigrationLockTimeout)
	defer deadline.Stop()
	for {
		attemptCtx, cancel := context.WithTimeout(ctx, time.Second)
		var acquired bool
		err := conn.QueryRowContext(attemptCtx, "SELECT pg_try_advisory_lock($1::bigint)", key).Scan(&acquired)
		cancel()
		if err != nil {
			lock.state = ExclusiveUnknown
			return lock, fmt.Errorf("acquire migration advisory lock: %w", err)
		}
		if acquired {
			lock.owned, lock.state = true, ExclusiveOwned
			return lock, nil
		}
		select {
		case <-ctx.Done():
			return lock, ctx.Err()
		case <-deadline.C:
			return lock, errors.New("migration advisory lock acquisition timed out")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (l *migrationAdvisoryLock) Close(ctx context.Context) error {
	if l == nil || l.discarded || (!l.owned && l.state != ExclusiveUnknown) {
		return nil
	}
	unlockCtx, cancel := context.WithTimeout(ctx, protectedMigrationUnlockTimeout)
	defer cancel()
	var unlocked bool
	if err := l.conn.QueryRowContext(unlockCtx, "SELECT pg_advisory_unlock($1::bigint)", l.key).Scan(&unlocked); err != nil {
		l.state, l.owned = ExclusiveUnknown, false
		return errors.Join(fmt.Errorf("release migration advisory lock: %w", err), ErrPhysicalConnectionDiscardRequired)
	}
	if !unlocked {
		l.state, l.owned = ExclusiveUnknown, false
		return errors.Join(errors.New("migration advisory lock release was not confirmed"), ErrPhysicalConnectionDiscardRequired)
	}
	l.state, l.owned = ExclusiveReleased, false
	return nil
}

// Keep the sql package's scanner types in the public verifier contract stable.
var _ ProtectedMigrationQueryer = (*sql.Conn)(nil)
var _ ProtectedMigrationQueryer = (*sql.Tx)(nil)
