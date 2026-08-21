package migrations

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"power-iot-backend/internal/testsupport"
)

func newD1LUnknownTestDatabase(t *testing.T) string {
	t.Helper()
	db, err := testsupport.New(context.Background(), os.Getenv("TEST_DATABASE_URL"), Up)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db.DSN()
}

func newD1LUnknownConfig(t *testing.T, dsn string) (D1LBootstrapConfig, *d1lTestProvider) {
	t.Helper()
	cfg := D1LBootstrapConfig{
		DatabaseURL: dsn, TargetFingerprint: d1lTargetForTest(t, dsn), EvidenceDigest: []byte(strings.Repeat("e", 32)),
		OperationID: uuid.NewString(), AttemptID: uuid.NewString(), AuthorizationID: uuid.NewString(),
		Envelope: strings.NewReader("opaque-test-presentation"), ExternalWriterAdmission: trustedExternalWriterAdmissionForTest(),
	}
	provider := &d1lTestProvider{cfg: cfg}
	cfg.Provider = provider
	return cfg, provider
}

func TestD1LCommitUnknownLandedUsesFreshReadyWithoutReplay(t *testing.T) {
	dsn := newD1LUnknownTestDatabase(t)
	cfg, provider := newD1LUnknownConfig(t, dsn)
	commitCalls := 0
	report, err := d1LBootstrapWithHooks(context.Background(), cfg, d1LBootstrapHooks{commit: func(_ context.Context, tx *sql.Tx) error {
		commitCalls++
		if err := tx.Commit(); err != nil {
			return err
		}
		return errors.New("simulated acknowledgement loss after commit")
	}})
	if err == nil || !errors.Is(err, ErrD1LCommitUnknown) || report.InstallState != D1LInstallCommittedReady || !report.Committed {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if commitCalls != 1 || strings.Join(provider.calls, ",") != "attestation,inspect,consume" {
		t.Fatalf("commit=%d provider=%v", commitCalls, provider.calls)
	}
	freshPID := queryD1LPID(t, dsn)
	t.Logf("CU1 database=%s old_pid=%d fresh_pid=%d state=%s", databaseNameFromDSN(dsn), report.BackendPID, freshPID, report.InstallState)
}

func TestD1LCommitUnknownRolledBackUsesFreshBaseWithoutReplay(t *testing.T) {
	dsn := newD1LUnknownTestDatabase(t)
	cfg, provider := newD1LUnknownConfig(t, dsn)
	commitCalls := 0
	report, err := d1LBootstrapWithHooks(context.Background(), cfg, d1LBootstrapHooks{commit: func(_ context.Context, tx *sql.Tx) error {
		commitCalls++
		_ = tx.Rollback()
		return errors.New("simulated acknowledgement loss before commit")
	}})
	if err == nil || !errors.Is(err, ErrD1LCommitUnknown) || report.InstallState != D1LInstallNotInstalled || report.After != D1LV5Base {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if commitCalls != 1 || strings.Join(provider.calls, ",") != "attestation,inspect,consume" {
		t.Fatalf("commit=%d provider=%v", commitCalls, provider.calls)
	}
	freshPID := queryD1LPID(t, dsn)
	t.Logf("CU2 database=%s old_pid=%d fresh_pid=%d state=%s after=%s", databaseNameFromDSN(dsn), report.BackendPID, freshPID, report.InstallState, report.After)
}

func TestD1LCommitUnknownPartialFailsClosedWithoutReplay(t *testing.T) {
	dsn := newD1LUnknownTestDatabase(t)
	cfg, provider := newD1LUnknownConfig(t, dsn)
	commitCalls := 0
	report, err := d1LBootstrapWithHooks(context.Background(), cfg, d1LBootstrapHooks{commit: func(_ context.Context, tx *sql.Tx) error {
		commitCalls++
		if err := tx.Commit(); err != nil {
			return err
		}
		probe, err := sql.Open("postgres", dsn)
		if err != nil {
			return err
		}
		defer probe.Close()
		_, err = probe.Exec("DROP TABLE security_control.control_schema_migrations")
		if err != nil {
			return err
		}
		return errors.New("simulated acknowledgement loss with partial state")
	}})
	if err == nil || !errors.Is(err, ErrD1LCommitUnknown) || report.InstallState != D1LInstallUnknown {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if commitCalls != 1 || strings.Join(provider.calls, ",") != "attestation,inspect,consume" {
		t.Fatalf("commit=%d provider=%v", commitCalls, provider.calls)
	}
	freshPID := queryD1LPID(t, dsn)
	t.Logf("CU3 database=%s old_pid=%d fresh_pid=%d state=%s after=%s", databaseNameFromDSN(dsn), report.BackendPID, freshPID, report.InstallState, report.After)
}

func queryD1LPID(t *testing.T, dsn string) int64 {
	t.Helper()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var pid int64
	if err := db.QueryRow("SELECT pg_backend_pid()").Scan(&pid); err != nil {
		t.Fatal(err)
	}
	return pid
}

func databaseNameFromDSN(dsn string) string {
	const marker = "/"
	if i := strings.LastIndex(dsn, marker); i >= 0 {
		name := dsn[i+1:]
		if q := strings.IndexByte(name, '?'); q >= 0 {
			name = name[:q]
		}
		return name
	}
	return "unknown"
}

func TestD1LBootstrapAndUpgradeTransitionFailureIsUnknownNoncommitted(t *testing.T) {
	oldBootstrap, oldUpgrade := d1lBootstrapFn, d1lUpgradeLedgerFn
	t.Cleanup(func() {
		d1lBootstrapFn, d1lUpgradeLedgerFn = oldBootstrap, oldUpgrade
	})
	d1lBootstrapFn = func(context.Context, D1LBootstrapConfig) (D1LBootstrapReport, error) {
		return D1LBootstrapReport{After: D1LValidV1, InstallState: D1LInstallCommittedReady, Committed: true}, nil
	}
	d1lUpgradeLedgerFn = func(context.Context, string, []byte) (D1LCatalogObservation, error) {
		return D1LCatalogObservation{State: D1LValidV1}, errors.New("injected transition failure")
	}
	report, err := D1LBootstrapAndUpgrade(context.Background(), D1LBootstrapConfig{TargetFingerprint: make([]byte, 32)})
	if err == nil || report.After != D1LValidV1 || report.InstallState != D1LInstallUnknown || report.Committed {
		t.Fatalf("transition failure report=%+v err=%v", report, err)
	}
}
