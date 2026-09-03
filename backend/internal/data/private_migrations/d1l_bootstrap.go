package migrations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/google/uuid"
)

// D1LManifestInsertSQL is the sole runner-owned D1-L manifest statement.
const D1LManifestInsertSQL = `INSERT INTO security_control.control_schema_migrations (
    control_version,
    dirty,
    target_fingerprint,
    installer_digest,
    install_id,
    installed_at
)
VALUES (
    1,
    false,
    $1::bytea,
    $2::bytea,
    $3::uuid,
    clock_timestamp()
);`

// D1LManifestTransitionSQL replaces the singleton current-state row. It is
// intentionally an UPDATE, not an append: v1 remains immutable artifact
// evidence while the active database carries only the current version.
const D1LManifestTransitionSQL = `UPDATE security_control.control_schema_migrations
SET control_version = 2,
    dirty = false,
    target_fingerprint = $1::bytea,
    installer_digest = $2::bytea,
    install_id = $3::uuid,
    installed_at = clock_timestamp()
WHERE control_version = 1 AND dirty = false;`

var (
	ErrD1LBootstrapState  = errors.New("D1-L bootstrap requires an exact recognized catalog state")
	ErrD1LProviderBinding = errors.New("D1-L provider binding mismatch")
	ErrD1LTransitionState = errors.New("D1-L additive transition requires VALID_V1")
	ErrD1LNoRetry         = errors.New("D1-L bootstrap does not retry consumed authority")
	ErrD1LCommitUnknown   = errors.New("D1-L bootstrap commit outcome is unknown")
)

type D1LBootstrapInstallState string

const (
	D1LInstallNotInstalled   D1LBootstrapInstallState = "NOT_INSTALLED"
	D1LInstallCommittedReady D1LBootstrapInstallState = "COMMITTED_READY"
	D1LInstallUnknown        D1LBootstrapInstallState = "UNKNOWN"
)

type D1LCleanupState string

const (
	D1LCleanupClean      D1LCleanupState = "CLEAN"
	D1LCleanupIncomplete D1LCleanupState = "CLEANUP_INCOMPLETE"
	D1LCleanupUnknown    D1LCleanupState = "UNKNOWN"
)

type D1LBootstrapConfig struct {
	DatabaseURL             string
	TargetFingerprint       []byte
	EvidenceDigest          []byte
	OperationID             string
	AttemptID               string
	AuthorizationID         string
	Envelope                io.Reader // protected inherited FD/pipe; read only immediately before Consume
	Provider                D1LProvider
	ExternalWriterAdmission ExternalWriterAdmission
}
type D1LProvider interface {
	Attestation(context.Context) (AttestationResult, error)
	Inspect(context.Context, string) (InspectResult, error)
	Consume(context.Context, ConsumeRequest) (ConsumeResult, error)
	Resolve(context.Context, ResolveRequest) (ResolveResult, error)
}
type D1LBootstrapReport struct {
	InstallState                                              D1LBootstrapInstallState
	CleanupState                                              D1LCleanupState
	Before                                                    D1LCatalogState
	After                                                     D1LCatalogState
	AuthorizationID, ConsumeRequestID, OperationID, AttemptID string
	ProviderEpoch                                             int64
	TargetFingerprint, InstallerDigest, EvidenceDigest        string
	BackendPID                                                int64
	MigrationLockKey                                          int64
	Committed                                                 bool
	StartedAt, FinishedAt                                     time.Time
}

type d1LBootstrapHooks struct {
	commit             func(context.Context, *sql.Tx) error
	ddl                func(context.Context, *sql.Tx) error
	ddlWithArtifact    func(context.Context, *sql.Tx, []byte) error
	manifest           func(context.Context, *sql.Tx, []byte, []byte, string) error
	preCommit          func(context.Context, *sql.Tx) error
	postCommitProof    func(context.Context, *sql.Conn, []byte, []byte, *postgres.Config) (D1LCatalogObservation, error)
	discard            func(*ExclusiveWriterFence, *migrationAdvisoryLock) error
	freshInspect       func(context.Context, string, []byte, []byte, *postgres.Config) (D1LCatalogObservation, error)
	beforeImmediateDDL func()
	artifactCheck      func(string, []byte, error)
	trace              func(string)
}

func (h d1LBootstrapHooks) mark(stage string) {
	if h.trace != nil {
		h.trace(stage)
	}
}

const (
	d1LArtifactCheckPreConsume      = "CURRENT_PRE_CONSUME_DIGEST_CHECK"
	d1LArtifactCheckImmediatePreDDL = "CURRENT_IMMEDIATE_PRE_DDL_DIGEST_CHECK"
)

func checkD1LInstallerArtifact(stage string, installer []byte, hooks d1LBootstrapHooks) error {
	err := verifyD1LInstallerArtifactBytes(installer)
	if hooks.artifactCheck != nil {
		// The observation hook is test-only; never let it mutate the slice
		// that remains bound to execution.
		hooks.artifactCheck(stage, append([]byte(nil), installer...), err)
	}
	return err
}

func D1LBootstrap(ctx context.Context, cfg D1LBootstrapConfig) (report D1LBootstrapReport, err error) {
	return d1LBootstrapWithHooks(ctx, cfg, d1LBootstrapHooks{})
}

// d1lUpgradeLedgerFn is an owner-private failure-injection seam. Production
// uses D1LUpgradeLedger; tests may replace it to prove the wrapper's
// transition-failure classification without changing migration architecture.
var (
	d1lBootstrapFn     = D1LBootstrap
	d1lUpgradeLedgerFn = D1LUpgradeLedger
)

// D1LBootstrapAndUpgrade keeps the provider-backed v1 installation and the
// additive ledger transition as separate authority steps while exposing one
// command-level bootstrap operation. A provider is never consulted for the
// ledger transition.
func D1LBootstrapAndUpgrade(ctx context.Context, cfg D1LBootstrapConfig) (report D1LBootstrapReport, err error) {
	report, err = d1lBootstrapFn(ctx, cfg)
	if err != nil || report.After != D1LValidV1 {
		return report, err
	}
	target, decodeErr := hex.DecodeString(report.TargetFingerprint)
	if decodeErr != nil {
		err = decodeErr
	} else {
		_, err = d1lUpgradeLedgerFn(ctx, cfg.DatabaseURL, target)
	}
	if err == nil {
		report.After = D1LValidNextLedgerReady
		report.InstallerDigest = D1LInstallerDigestNext
		return report, nil
	}
	// The provider-backed v1 install committed, but the additive transition did
	// not. Never leak the v1 command result as an overall ready/committed
	// outcome: retain v1 as the durable evidence and classify this command as
	// UNKNOWN so callers cannot proceed as if v2 were installed.
	report.After = D1LValidV1
	report.InstallState = D1LInstallUnknown
	report.Committed = false
	return report, err
}

// D1LUpgradeLedger performs the additive v1 -> next-version transition. It
// has no provider input: provider authority is consumed only by the initial
// v1 installation. DDL, singleton replacement, and exact next-version proof
// all execute on one protected PostgreSQL transaction.
func D1LUpgradeLedger(ctx context.Context, databaseURL string, target []byte) (D1LCatalogObservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(target) != 32 {
		return D1LCatalogObservation{State: D1LUnreadable}, ErrD1LProviderBinding
	}
	parsed, err := parsePostgresDatabaseURL(databaseURL)
	if err != nil {
		return D1LCatalogObservation{State: D1LUnreadable}, err
	}
	fence, err := pinExclusiveWriterFence(ctx, parsed)
	if err != nil {
		return D1LCatalogObservation{State: D1LUnreadable}, err
	}
	defer fence.Close()
	actual, err := deriveCR1TargetFingerprint(ctx, fence.Conn(), parsed.config)
	if err != nil {
		return D1LCatalogObservation{State: D1LUnreadable}, err
	}
	if !bytes.Equal(actual, target) {
		return D1LCatalogObservation{State: D1LWrongTarget}, ErrD1LProviderBinding
	}
	if err := fence.Acquire(ctx); err != nil {
		return D1LCatalogObservation{State: D1LUnreadable}, err
	}
	lock, err := acquireMigrationAdvisoryLock(ctx, fence.Conn(), parsed)
	if err != nil {
		return D1LCatalogObservation{State: D1LUnreadable}, err
	}
	defer lock.Close(ctx)
	before, err := RecognizeD1LCatalogWithConfig(ctx, fence.Conn(), actual, d1LInstallerDigestBytes(), parsed.config)
	if err != nil {
		return before, err
	}
	if before.State == D1LValidNextLedgerReady {
		return before, nil
	}
	if before.State != D1LValidV1 {
		return before, fmt.Errorf("%w: observed %s", ErrD1LTransitionState, before.State)
	}
	if err := verifyD1LInstallerArtifactBytes(d1LInstallerBytes); err != nil {
		return D1LCatalogObservation{State: D1LUnreadable}, err
	}
	artifact := D1LLedgerTransitionSQL()
	if err := verifyD1LLedgerTransitionArtifactBytes(artifact); err != nil {
		return D1LCatalogObservation{State: D1LUnreadable}, err
	}
	tx, err := fence.Conn().BeginTx(ctx, nil)
	if err != nil {
		return D1LCatalogObservation{State: D1LUnreadable}, err
	}
	rollback := func(cause error) (D1LCatalogObservation, error) {
		_ = tx.Rollback()
		return D1LCatalogObservation{State: D1LValidV1}, cause
	}
	if _, err := tx.ExecContext(ctx, string(artifact)); err != nil {
		return rollback(err)
	}
	transitionID := uuid.NewString()
	result, err := tx.ExecContext(ctx, D1LManifestTransitionSQL, actual, d1LLedgerTransitionDigestBytes(), transitionID)
	if err != nil {
		return rollback(err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		if err == nil {
			err = ErrD1LTransitionState
		}
		return rollback(err)
	}
	inTx, err := RecognizeD1LCatalogWithConfig(ctx, tx, actual, d1LLedgerTransitionDigestBytes(), parsed.config)
	if err != nil {
		return rollback(err)
	}
	if inTx.State != D1LValidNextLedgerReady {
		return rollback(fmt.Errorf("D1-L in-transaction transition proof observed %s: %s", inTx.State, inTx.Detail))
	}
	if err := tx.Commit(); err != nil {
		return D1LCatalogObservation{State: D1LUnreadable}, errors.Join(ErrD1LCommitUnknown, err)
	}
	after, err := RecognizeD1LCatalogWithConfig(ctx, fence.Conn(), actual, d1LLedgerTransitionDigestBytes(), parsed.config)
	if err != nil {
		return after, err
	}
	if after.State != D1LValidNextLedgerReady {
		return after, fmt.Errorf("D1-L post-transition proof observed %s", after.State)
	}
	return after, nil
}

func d1LBootstrapWithHooks(ctx context.Context, cfg D1LBootstrapConfig, hooks d1LBootstrapHooks) (report D1LBootstrapReport, err error) {
	report.StartedAt = time.Now().UTC()
	// NOT_INSTALLED is a proof-bearing result, never a default for an
	// interrupted attempt. It is assigned only by a fresh exact V5_BASE
	// inspection after the protected session has been discarded/proven clean.
	report.InstallState = D1LInstallUnknown
	report.CleanupState = D1LCleanupUnknown
	if err = checkD1LInstallerArtifact(d1LArtifactCheckPreConsume, d1LInstallerBytes, hooks); err != nil {
		return report, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if len(cfg.TargetFingerprint) != 32 || len(cfg.EvidenceDigest) != 32 {
		return report, fmt.Errorf("%w: digest lengths", ErrD1LProviderBinding)
	}
	if _, err = uuid.Parse(cfg.OperationID); err != nil {
		return report, fmt.Errorf("operation id: %w", err)
	}
	if _, err = uuid.Parse(cfg.AttemptID); err != nil {
		return report, fmt.Errorf("attempt id: %w", err)
	}
	if cfg.Provider == nil || cfg.Envelope == nil {
		return report, errors.New("D1-L provider and protected envelope are required")
	}
	report.OperationID, report.AttemptID, report.AuthorizationID = cfg.OperationID, cfg.AttemptID, cfg.AuthorizationID
	report.EvidenceDigest = hex.EncodeToString(cfg.EvidenceDigest)
	report.InstallerDigest = D1LInstallerDigestV1
	parsed, e := parsePostgresDatabaseURL(cfg.DatabaseURL)
	if e != nil {
		return report, e
	}
	fence, e := pinExclusiveWriterFence(ctx, parsed)
	if e != nil {
		return report, e
	}
	hooks.mark("PIN")
	report.BackendPID = fence.BackendPID()
	defer func() {
		if ce := fence.Close(); ce != nil {
			report.CleanupState = D1LCleanupIncomplete
			if err == nil {
				err = ce
			} else {
				err = errors.Join(err, ce)
			}
		}
		if report.FinishedAt.IsZero() {
			report.FinishedAt = time.Now().UTC()
		}
	}()
	actualTarget, e := deriveCR1TargetFingerprint(ctx, fence.Conn(), parsed.config)
	if e != nil {
		return report, e
	}
	hooks.mark("DERIVE")
	if !bytes.Equal(actualTarget, cfg.TargetFingerprint) {
		hooks.mark("VERIFY")
		return report, fmt.Errorf("%w: caller target does not match pinned PostgreSQL target", ErrD1LProviderBinding)
	}
	hooks.mark("VERIFY")
	report.TargetFingerprint = hex.EncodeToString(actualTarget)
	report.EvidenceDigest = hex.EncodeToString(cfg.EvidenceDigest)
	if e = fence.Acquire(ctx); e != nil {
		return report, e
	}
	hooks.mark("FENCE")
	lock, e := acquireMigrationAdvisoryLock(ctx, fence.Conn(), parsed)
	if e != nil {
		return report, e
	}
	report.MigrationLockKey = lock.key
	hooks.mark("LOCK")
	defer func() {
		if ce := lock.Close(ctx); ce != nil {
			report.CleanupState = D1LCleanupIncomplete
			if err == nil {
				err = ce
			} else {
				err = errors.Join(err, ce)
			}
		} else if report.CleanupState == D1LCleanupUnknown {
			report.CleanupState = D1LCleanupClean
		}
	}()
	meta, e := inspectMigrationMetadata(ctx, fence.Conn(), parsed.config)
	if e != nil {
		return report, recoverD1LTargetFailure(ctx, &report, parsed, actualTarget, fence, lock, e, true)
	}
	if meta.RowCount != 1 || meta.Version != 5 || meta.Dirty {
		return report, recoverD1LTargetFailure(ctx, &report, parsed, actualTarget, fence, lock, ErrD1LBootstrapState, true)
	}
	before, e := RecognizeD1LCatalogWithConfig(ctx, fence.Conn(), actualTarget, d1LInstallerDigestBytes(), parsed.config)
	if e != nil {
		return report, recoverD1LTargetFailure(ctx, &report, parsed, actualTarget, fence, lock, e, true)
	}
	report.Before = before.State
	if before.State == D1LExactReady {
		report.After = before.State
		report.InstallState = D1LInstallCommittedReady
		report.Committed = true
		return report, nil
	}
	if before.State != D1LV5Base {
		return report, recoverD1LTargetFailure(ctx, &report, parsed, actualTarget, fence, lock, fmt.Errorf("%w: observed %s", ErrD1LBootstrapState, before.State), true)
	}
	hooks.mark("V5_BASE")
	att, e := cfg.Provider.Attestation(ctx)
	if e != nil {
		return report, recoverD1LTargetFailure(ctx, &report, parsed, actualTarget, fence, lock, e, true)
	}
	if att.Outcome != OutcomeSuccess {
		return report, recoverD1LTargetFailure(ctx, &report, parsed, actualTarget, fence, lock, fmt.Errorf("provider attestation outcome=%s", att.Outcome), true)
	}
	ins, e := cfg.Provider.Inspect(ctx, cfg.AuthorizationID)
	if e != nil {
		return report, recoverD1LTargetFailure(ctx, &report, parsed, actualTarget, fence, lock, e, true)
	}
	if ins.Outcome != OutcomeSuccess {
		return report, recoverD1LTargetFailure(ctx, &report, parsed, actualTarget, fence, lock, fmt.Errorf("provider inspect outcome=%s", ins.Outcome), true)
	}
	validation, e := validateD1LInspect(ins, cfg, report.InstallerDigest, report.TargetFingerprint)
	if e != nil {
		return report, recoverD1LTargetFailure(ctx, &report, parsed, actualTarget, fence, lock, e, true)
	}
	admission := deriveExternalWriterAdmission(validation)
	if e = RequireExternalWriterAdmission(admission); e != nil {
		return report, recoverD1LTargetFailure(ctx, &report, parsed, actualTarget, fence, lock, e, true)
	}
	report.ProviderEpoch = ins.Epoch
	buf, e := readD1LEnvelope(cfg.Envelope)
	if e != nil {
		return report, recoverD1LTargetFailure(ctx, &report, parsed, actualTarget, fence, lock, e, true)
	}
	defer zeroD1L(buf)
	report.ConsumeRequestID = uuid.NewString()
	req := ConsumeRequest{ConsumeRequestID: report.ConsumeRequestID, AuthorizationID: cfg.AuthorizationID, IssuerRequestID: ins.IssuerRequestID, Operation: cfg.OperationID, AttemptID: cfg.AttemptID, TargetID: report.TargetFingerprint, InstallerID: report.InstallerDigest, EvidenceHash: report.EvidenceDigest, Scope: ScopeControlCatalogInstall, Nonce: ins.Nonce, Envelope: buf, Epoch: ins.Epoch}
	hooks.mark("CONSUME")
	cons, consumeErr := cfg.Provider.Consume(ctx, req)
	switch classifyD1LConsume(cons, consumeErr) {
	case d1LConsumeSuccess:
		// Only a definitive success can authorize the target transaction. The
		// provider call above is the sole Consume call for this authorization.
	case d1LConsumePreTransmission:
		return report, recoverD1LTargetFailure(ctx, &report, parsed, actualTarget, fence, lock, errors.Join(consumeErr, ErrD1LNoRetry), true)
	case d1LConsumeTerminal:
		return report, recoverD1LTargetFailure(ctx, &report, parsed, actualTarget, fence, lock, errors.Join(consumeErr, ErrD1LNoRetry), true)
	default:
		return report, recoverD1LAmbiguousConsume(ctx, &report, parsed, actualTarget, fence, lock, cfg.Provider, req, consumeErr, cons)
	}
	meta, e = inspectMigrationMetadata(ctx, fence.Conn(), parsed.config)
	if e != nil {
		return report, recoverD1LTargetFailure(ctx, &report, parsed, actualTarget, fence, lock, e, true)
	}
	if meta.RowCount != 1 || meta.Version != 5 || meta.Dirty {
		return report, recoverD1LTargetFailure(ctx, &report, parsed, actualTarget, fence, lock, ErrD1LBootstrapState, true)
	}
	guard, e := RecognizeD1LCatalogWithConfig(ctx, fence.Conn(), actualTarget, d1LInstallerDigestBytes(), parsed.config)
	if e != nil {
		return report, recoverD1LTargetFailure(ctx, &report, parsed, actualTarget, fence, lock, e, true)
	}
	if guard.State != D1LV5Base {
		return report, recoverD1LTargetFailure(ctx, &report, parsed, actualTarget, fence, lock, fmt.Errorf("post-Consume target guard observed %s", guard.State), true)
	}
	tx, e := fence.Conn().BeginTx(ctx, nil)
	if e != nil {
		return report, recoverD1LTargetFailure(ctx, &report, parsed, actualTarget, fence, lock, e, true)
	}
	installID := uuid.NewString()
	if hooks.beforeImmediateDDL != nil {
		hooks.beforeImmediateDDL()
	}
	// This is the execution-boundary load. The returned copy is the exact
	// byte slice checked below and subsequently passed to DDL; there is no
	// artifact read between the successful check and ExecContext.
	installer := D1LInstallerSQL()
	if e = checkD1LInstallerArtifact(d1LArtifactCheckImmediatePreDDL, installer, hooks); e != nil {
		return report, recoverD1LTransactionFailure(ctx, &report, parsed, actualTarget, fence, lock, tx, e)
	}
	hooks.mark("DDL")
	if hooks.ddlWithArtifact != nil {
		e = hooks.ddlWithArtifact(ctx, tx, installer)
	} else if hooks.ddl != nil {
		e = hooks.ddl(ctx, tx)
	} else {
		_, e = tx.ExecContext(ctx, string(installer))
	}
	if e != nil {
		return report, recoverD1LTransactionFailure(ctx, &report, parsed, actualTarget, fence, lock, tx, e)
	}
	if hooks.manifest != nil {
		e = hooks.manifest(ctx, tx, actualTarget, d1LInstallerDigestBytes(), installID)
	} else {
		_, e = tx.ExecContext(ctx, D1LManifestInsertSQL, actualTarget, d1LInstallerDigestBytes(), installID)
	}
	if e != nil {
		return report, recoverD1LTransactionFailure(ctx, &report, parsed, actualTarget, fence, lock, tx, e)
	}
	if hooks.preCommit != nil {
		if e = hooks.preCommit(ctx, tx); e != nil {
			return report, recoverD1LTransactionFailure(ctx, &report, parsed, actualTarget, fence, lock, tx, e)
		}
	}
	inTxMeta, e := inspectMigrationMetadataOn(ctx, tx, parsed.config)
	if e != nil {
		return report, recoverD1LTransactionFailure(ctx, &report, parsed, actualTarget, fence, lock, tx, e)
	}
	if inTxMeta.RowCount != 1 || inTxMeta.Version != 5 || inTxMeta.Dirty {
		return report, recoverD1LTransactionFailure(ctx, &report, parsed, actualTarget, fence, lock, tx, ErrD1LBootstrapState)
	}
	inTxCatalog, e := RecognizeD1LCatalogWithConfig(ctx, tx, actualTarget, d1LInstallerDigestBytes(), parsed.config)
	if e != nil {
		return report, recoverD1LTransactionFailure(ctx, &report, parsed, actualTarget, fence, lock, tx, e)
	}
	if inTxCatalog.State != D1LExactReady {
		return report, recoverD1LTransactionFailure(ctx, &report, parsed, actualTarget, fence, lock, tx, fmt.Errorf("pre-commit D1-L READY proof observed %s", inTxCatalog.State))
	}
	commit := hooks.commit
	if commit == nil {
		commit = func(_ context.Context, tx *sql.Tx) error { return tx.Commit() }
	}
	if e = commit(ctx, tx); e != nil {
		report.InstallState = D1LInstallUnknown
		discardErr := discardD1LProtectedSession(hooks, fence, lock)
		if discardErr != nil {
			// A failed discard proof leaves both the target outcome and the old
			// session ownership unresolved. Do not inspect or classify through a
			// fresh connection until the protected session is proven gone.
			return report, errors.Join(ErrD1LCommitUnknown, e, discardErr)
		}
		fresh, inspectErr := inspectD1LFreshTarget(hooks, ctx, parsed.driverURL, actualTarget, d1LInstallerDigestBytes(), parsed.config)
		if inspectErr == nil {
			classifyFreshD1LTarget(&report, fresh, true)
		} else {
			report.After = fresh.State
			report.InstallState = D1LInstallUnknown
		}
		return report, errors.Join(ErrD1LCommitUnknown, e, inspectErr)
	}
	report.Committed = true
	postCommitProof := hooks.postCommitProof
	if postCommitProof == nil {
		postCommitProof = func(ctx context.Context, conn *sql.Conn, target, installer []byte, config *postgres.Config) (D1LCatalogObservation, error) {
			return RecognizeD1LCatalogWithConfig(ctx, conn, target, installer, config)
		}
	}
	after, e := postCommitProof(ctx, fence.Conn(), actualTarget, d1LInstallerDigestBytes(), parsed.config)
	if e != nil || after.State != D1LExactReady {
		proofErr := e
		if proofErr == nil {
			proofErr = fmt.Errorf("post-commit D1-L READY proof observed %s", after.State)
		}
		report.InstallState = D1LInstallUnknown
		// COMMIT is known successful, but the old session proof is not an
		// independent durable-state observation. Discard it before recovery.
		discardErr := discardD1LProtectedSession(hooks, fence, lock)
		if discardErr != nil {
			return report, errors.Join(proofErr, discardErr)
		}
		fresh, inspectErr := inspectD1LFreshTarget(hooks, ctx, parsed.driverURL, actualTarget, d1LInstallerDigestBytes(), parsed.config)
		if inspectErr != nil {
			report.After = fresh.State
			report.InstallState = D1LInstallUnknown
			return report, errors.Join(proofErr, inspectErr)
		}
		report.After = fresh.State
		if fresh.State == D1LExactReady {
			report.InstallState = D1LInstallCommittedReady
		} else {
			// COMMIT succeeded, so even contradictory fresh BASE is not proof of
			// NOT_INSTALLED. Preserve the committed-but-ambiguous result.
			report.InstallState = D1LInstallUnknown
		}
		return report, proofErr
	}
	report.After = after.State
	report.InstallState = D1LInstallCommittedReady
	return report, nil
}

type d1LConsumeDisposition uint8

const (
	d1LConsumeSuccess d1LConsumeDisposition = iota + 1
	d1LConsumePreTransmission
	d1LConsumeTerminal
	d1LConsumeAmbiguous
)

type d1LProviderTruth uint8

const (
	d1LProviderTruthUnresolved d1LProviderTruth = iota
	d1LProviderTruthConsumed
	d1LProviderTruthNotConsumed
)

// classifyD1LConsume never turns an indeterminate mutation into a denial. A
// transport error is pre-transmission only when the existing provider client
// has retained a transport cause that proves dial/name-resolution/TLS failure.
// HTTP 503 and all other incomplete responses remain ambiguous and must be
// resolved with the exact original request tuple.
func classifyD1LConsume(result ConsumeResult, consumeErr error) d1LConsumeDisposition {
	if consumeErr != nil {
		if definitelyD1LPreTransmission(consumeErr) {
			return d1LConsumePreTransmission
		}
		if result.Outcome == OutcomeExpired || result.Outcome == OutcomePoisoned || result.Outcome == OutcomeRevoked || result.Outcome == OutcomeBindingMismatch || result.Outcome == OutcomeUnauthorized {
			return d1LConsumeTerminal
		}
		return d1LConsumeAmbiguous
	}
	switch result.Outcome {
	case OutcomeSuccess:
		if result.State == ConsumeConsumed {
			return d1LConsumeSuccess
		}
		return d1LConsumeAmbiguous
	case OutcomeExpired, OutcomePoisoned, OutcomeRevoked, OutcomeBindingMismatch, OutcomeUnauthorized, OutcomeAlreadyConsumed:
		return d1LConsumeTerminal
	default:
		return d1LConsumeAmbiguous
	}
}

func definitelyD1LPreTransmission(err error) bool {
	var clientErr *ClientError
	if errors.As(err, &clientErr) && clientErr.Cause != nil {
		return definitelyPreTransmission(clientErr.Cause)
	}
	return definitelyPreTransmission(err)
}

func discardD1LProtectedSession(hooks d1LBootstrapHooks, fence *ExclusiveWriterFence, lock *migrationAdvisoryLock) error {
	if hooks.discard != nil {
		return hooks.discard(fence, lock)
	}
	return discardUnknownProtectedSession(fence, lock)
}

func inspectD1LFreshTarget(hooks d1LBootstrapHooks, ctx context.Context, dsn string, target, installer []byte, config *postgres.Config) (D1LCatalogObservation, error) {
	if hooks.freshInspect != nil {
		return hooks.freshInspect(ctx, dsn, target, installer, config)
	}
	return independentD1LCatalogInspection(ctx, dsn, target, installer, config)
}

// recoverD1LTargetFailure is used once no further Provider mutation is
// allowed. It discards the protected session before opening an independent
// target connection, so local rollback/error state cannot become an install
// classification. Only exact fresh V5_BASE can produce NOT_INSTALLED.
func recoverD1LTargetFailure(ctx context.Context, report *D1LBootstrapReport, parsed *parsedPostgresDatabaseURL, target []byte, fence *ExclusiveWriterFence, lock *migrationAdvisoryLock, cause error, allowBase bool) error {
	discardErr := discardUnknownProtectedSession(fence, lock)
	if discardErr != nil {
		report.InstallState = D1LInstallUnknown
		return errors.Join(cause, discardErr)
	}
	fresh, inspectErr := independentD1LCatalogInspection(ctx, parsed.driverURL, target, d1LInstallerDigestBytes(), parsed.config)
	if inspectErr != nil {
		report.After = fresh.State
		report.InstallState = D1LInstallUnknown
		return errors.Join(cause, inspectErr)
	}
	classifyFreshD1LTarget(report, fresh, allowBase)
	return errors.Join(cause, inspectErr)
}

func recoverD1LTransactionFailure(ctx context.Context, report *D1LBootstrapReport, parsed *parsedPostgresDatabaseURL, target []byte, fence *ExclusiveWriterFence, lock *migrationAdvisoryLock, tx *sql.Tx, cause error) error {
	rollbackErr := tx.Rollback()
	if errors.Is(rollbackErr, sql.ErrTxDone) {
		rollbackErr = nil
	}
	return recoverD1LTargetFailure(ctx, report, parsed, target, fence, lock, errors.Join(cause, rollbackErr), true)
}

func classifyFreshD1LTarget(report *D1LBootstrapReport, fresh D1LCatalogObservation, allowBase bool) {
	report.After = fresh.State
	switch fresh.State {
	case D1LExactReady:
		report.InstallState = D1LInstallCommittedReady
		report.Committed = true
	case D1LV5Base:
		if allowBase {
			report.InstallState = D1LInstallNotInstalled
		} else {
			report.InstallState = D1LInstallUnknown
		}
	default:
		report.InstallState = D1LInstallUnknown
	}
}

func recoverD1LAmbiguousConsume(ctx context.Context, report *D1LBootstrapReport, parsed *parsedPostgresDatabaseURL, target []byte, fence *ExclusiveWriterFence, lock *migrationAdvisoryLock, provider D1LProvider, request ConsumeRequest, consumeErr error, consumeResult ConsumeResult) error {
	// This ordering is mandatory: Resolve never runs while the old protected
	// session or either advisory lock might still be live.
	discardErr := discardUnknownProtectedSession(fence, lock)
	if discardErr != nil {
		report.InstallState = D1LInstallUnknown
		return errors.Join(consumeErr, ErrD1LNoRetry, discardErr)
	}
	resolveRequest := ResolveRequest{
		ConsumeRequestID: request.ConsumeRequestID,
		AuthorizationID:  request.AuthorizationID,
		IssuerRequestID:  request.IssuerRequestID,
		Operation:        request.Operation,
		AttemptID:        request.AttemptID,
		TargetID:         request.TargetID,
		InstallerID:      request.InstallerID,
		EvidenceHash:     request.EvidenceHash,
		Scope:            request.Scope,
		Epoch:            request.Epoch,
		Nonce:            request.Nonce,
	}
	resolved, resolveErr := provider.Resolve(ctx, resolveRequest)
	truth := classifyD1LResolve(resolved, resolveErr, resolveRequest)
	fresh, inspectErr := independentD1LCatalogInspection(ctx, parsed.driverURL, target, d1LInstallerDigestBytes(), parsed.config)
	if inspectErr != nil {
		report.After = fresh.State
		report.InstallState = D1LInstallUnknown
	} else {
		// Keep durable provider truth separate from target truth. Both a
		// durably consumed request and a durably terminal/non-consumed request
		// forbid replay; they differ in whether the old authorization can ever
		// be reused. Neither fact alone classifies the target.
		switch truth {
		case d1LProviderTruthConsumed:
			classifyFreshD1LTarget(report, fresh, true)
		case d1LProviderTruthNotConsumed:
			classifyFreshD1LTarget(report, fresh, true)
		default:
			classifyFreshD1LTarget(report, fresh, false)
		}
	}
	if consumeErr == nil {
		consumeErr = fmt.Errorf("provider consume outcome=%s", consumeResult.Outcome)
	}
	return errors.Join(consumeErr, ErrD1LNoRetry, resolveErr, inspectErr)
}

func classifyD1LResolve(result ResolveResult, resolveErr error, request ResolveRequest) d1LProviderTruth {
	if resolveErr != nil || !validResolve(result, request) {
		return d1LProviderTruthUnresolved
	}
	if result.AuthState == AuthorizationConsumed && result.IntentState == ConsumeConsumed && result.Outcome == OutcomeSuccess {
		return d1LProviderTruthConsumed
	}
	if (result.AuthState == AuthorizationExpired || result.AuthState == AuthorizationRevoked || result.AuthState == AuthorizationCancelled) &&
		(result.IntentState == "" || result.IntentState == ConsumeAborted) &&
		(result.Outcome == OutcomeExpired || result.Outcome == OutcomeRevoked) {
		return d1LProviderTruthNotConsumed
	}
	return d1LProviderTruthUnresolved
}

func independentD1LCatalogInspection(ctx context.Context, dsn string, target, installer []byte, config *postgres.Config) (D1LCatalogObservation, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return D1LCatalogObservation{State: D1LUnreadable}, err
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		return D1LCatalogObservation{State: D1LUnreadable}, err
	}
	defer conn.Close()
	return RecognizeD1LCatalogWithConfig(ctx, conn, target, installer, config)
}

// d1LInspectValidation is an in-process witness returned only after the
// Provider Inspect binding has been checked. It is not an authority token and
// never crosses the runner/provider boundary.
type d1LInspectValidationMarker struct{}

type d1LInspectValidation struct {
	marker *d1LInspectValidationMarker
}

// deriveExternalWriterAdmission keeps the authorization evidence private: the
// public summary fields are populated only after the provider Inspect binding
// has produced its in-process validation witness.
func deriveExternalWriterAdmission(validation d1LInspectValidation) ExternalWriterAdmission {
	if validation.marker == nil {
		return ExternalWriterAdmission{}
	}
	return ExternalWriterAdmission{
		ManagedCooperativeWriters: true,
		DirectSQLControlled:       true,
		OperationalDrainEvidence:  true,
		evidence: &externalWriterAdmissionEvidence{
			managedCooperativeWriters: true,
			directSQLControlled:       true,
			operationalDrainEvidence:  true,
		},
	}
}

func validateD1LInspect(ins InspectResult, cfg D1LBootstrapConfig, installer, target string) (d1LInspectValidation, error) {
	if _, err := uuid.Parse(cfg.AuthorizationID); err != nil {
		return d1LInspectValidation{}, ErrD1LProviderBinding
	}
	if _, err := uuid.Parse(ins.IssuerRequestID); err != nil {
		return d1LInspectValidation{}, ErrD1LProviderBinding
	}
	if ins.AuthorizationID != cfg.AuthorizationID || ins.AttemptID != cfg.AttemptID || ins.State != AuthorizationIssued || ins.Scope != ScopeControlCatalogInstall || !validNonce(ins.Nonce) || ins.Epoch <= 0 || ins.ExpiresAt.IsZero() || !ins.ExpiresAt.After(time.Now()) {
		return d1LInspectValidation{}, ErrD1LProviderBinding
	}
	b := ins.Bindings
	if len(b) != 5 || b["operation"] != cfg.OperationID || b["attempt_id"] != cfg.AttemptID || b["target_id"] != target || b["installer_id"] != installer || b["evidence_hash"] != hex.EncodeToString(cfg.EvidenceDigest) {
		return d1LInspectValidation{}, ErrD1LProviderBinding
	}
	if installer != D1LInstallerDigestV1 {
		return d1LInspectValidation{}, ErrD1LProviderBinding
	}
	return d1LInspectValidation{marker: &d1LInspectValidationMarker{}}, nil
}
func readD1LEnvelope(r io.Reader) ([]byte, error) {
	const max = 1 << 20
	b, e := io.ReadAll(io.LimitReader(r, max+1))
	if e != nil {
		return nil, e
	}
	if len(b) == 0 || len(b) > max {
		return nil, errors.New("protected envelope exceeds bound")
	}
	return b, nil
}
func zeroD1L(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
func D1LTargetDigest(data []byte) [32]byte { return sha256.Sum256(data) }
