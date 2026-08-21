package migrations

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/google/uuid"
	"power-iot-backend/internal/testsupport"
)

type d1lTestProvider struct {
	calls       []string
	cfg         D1LBootstrapConfig
	consumeFunc func(ConsumeRequest) (ConsumeResult, error)
	resolveFunc func(ResolveRequest) (ResolveResult, error)
}

func (p *d1lTestProvider) Attestation(context.Context) (AttestationResult, error) {
	p.calls = append(p.calls, "attestation")
	return AttestationResult{Outcome: OutcomeSuccess}, nil
}
func (p *d1lTestProvider) Inspect(context.Context, string) (InspectResult, error) {
	p.calls = append(p.calls, "inspect")
	return InspectResult{
		Outcome: OutcomeSuccess, AuthorizationID: p.cfg.AuthorizationID,
		IssuerRequestID: uuid.NewString(), AttemptID: p.cfg.AttemptID,
		State: AuthorizationIssued, Epoch: 1, Nonce: "AAAAAAAAAAAAAAAAAAAAAA", Scope: ScopeControlCatalogInstall,
		ExpiresAt: time.Now().UTC().Add(time.Hour),
		Bindings: map[string]string{
			"operation": p.cfg.OperationID, "attempt_id": p.cfg.AttemptID,
			"target_id":     strings.ToLower(hexDigest(p.cfg.TargetFingerprint)),
			"installer_id":  D1LInstallerDigestV1,
			"evidence_hash": strings.ToLower(hexDigest(p.cfg.EvidenceDigest)),
		},
	}, nil
}
func (p *d1lTestProvider) Consume(_ context.Context, request ConsumeRequest) (ConsumeResult, error) {
	p.calls = append(p.calls, "consume")
	if p.consumeFunc != nil {
		return p.consumeFunc(request)
	}
	return ConsumeResult{Outcome: OutcomeSuccess, State: ConsumeConsumed}, nil
}
func (p *d1lTestProvider) Resolve(_ context.Context, request ResolveRequest) (ResolveResult, error) {
	p.calls = append(p.calls, "resolve")
	if p.resolveFunc != nil {
		return p.resolveFunc(request)
	}
	return ResolveResult{}, nil
}

func d1lTargetForTest(t *testing.T, dsn string) []byte {
	t.Helper()
	parsed, err := parsePostgresDatabaseURL(dsn)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", parsed.driverURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	target, err := deriveCR1TargetFingerprint(context.Background(), conn, parsed.config)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func hexDigest(b []byte) string {
	const hex = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2], out[i*2+1] = hex[v>>4], hex[v&15]
	}
	return string(out)
}

func TestD1LFixedDDLAndManifestUseSameTransactionAndPID(t *testing.T) {
	db, err := sql.Open("postgres", os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var pid int64
	var databaseName, serverVersion string
	if err := conn.QueryRowContext(context.Background(), "SELECT pg_backend_pid(), current_database(), current_setting('server_version')").Scan(&pid, &databaseName, &serverVersion); err != nil {
		t.Fatal(err)
	}
	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(context.Background(), string(d1LInstallerBytes)); err != nil {
		t.Fatal("DDL: ", err)
	}
	target := d1lTargetForTest(t, os.Getenv("TEST_DATABASE_URL"))
	installer := d1LInstallerDigestBytes()
	if _, err := tx.ExecContext(context.Background(), D1LManifestInsertSQL, []byte(target), installer, uuid.NewString()); err != nil {
		t.Fatal("manifest: ", err)
	}
	var txPID int64
	if err := tx.QueryRowContext(context.Background(), "SELECT pg_backend_pid()").Scan(&txPID); err != nil {
		t.Fatal(err)
	}
	if txPID != pid {
		t.Fatalf("transaction moved sessions: before=%d tx=%d", pid, txPID)
	}
	t.Logf("database=%s endpoint=127.0.0.1:55434 PostgreSQL=%s backend_pid=%d same_tx_pid=%d", databaseName, serverVersion, pid, txPID)
	obs, err := RecognizeD1LCatalog(context.Background(), tx, []byte(target), installer)
	if err != nil || obs.State != D1LExactReady {
		t.Fatalf("in-transaction recognition state=%s detail=%s err=%v", obs.State, obs.Detail, err)
	}
}

func TestD1LAtomicRollbackOnManifestFailure(t *testing.T) {
	db, err := sql.Open("postgres", os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(context.Background(), string(d1LInstallerBytes)); err != nil {
		t.Fatal("DDL: ", err)
	}
	if _, err := tx.ExecContext(context.Background(), D1LManifestInsertSQL, []byte("short"), d1LInstallerDigestBytes(), uuid.NewString()); err == nil {
		t.Fatal("short target unexpectedly accepted")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var relation sql.NullString
	if err := conn.QueryRowContext(context.Background(), "SELECT to_regclass('security_control.control_schema_migrations')").Scan(&relation); err != nil {
		t.Fatal(err)
	}
	if relation.Valid {
		t.Fatalf("atomic rollback left relation %q", relation.String)
	}
}

func TestD1LBootstrapRejectsMalformedApplicationBeforeProviderAndDDL(t *testing.T) {
	mutations := []struct {
		name string
		sql  string
	}{
		{name: "missing v5 foreign key", sql: "ALTER TABLE shops DROP CONSTRAINT security_shops_client_id_fkey"},
		{name: "missing v5 index", sql: "DROP INDEX security_shops_client_id_idx"},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			db, err := testsupport.New(context.Background(), os.Getenv("TEST_DATABASE_URL"), Up)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			t.Logf("database=%s mutation=%s", db.Name(), mutation.name)
			probe, err := sql.Open("postgres", db.DSN())
			if err != nil {
				t.Fatal(err)
			}
			defer probe.Close()
			if _, err := probe.Exec(mutation.sql); err != nil {
				t.Fatal(err)
			}
			target := d1lTargetForTest(t, db.DSN())
			cfg := D1LBootstrapConfig{
				DatabaseURL: db.DSN(), TargetFingerprint: target, EvidenceDigest: []byte(strings.Repeat("e", 32)),
				OperationID: uuid.NewString(), AttemptID: uuid.NewString(), AuthorizationID: uuid.NewString(),
				Envelope: strings.NewReader("opaque-test-presentation"), ExternalWriterAdmission: trustedExternalWriterAdmissionForTest(),
			}
			provider := &d1lTestProvider{cfg: cfg}
			cfg.Provider = provider
			if _, err := D1LBootstrap(context.Background(), cfg); !errors.Is(err, ErrD1LBootstrapState) {
				t.Fatalf("malformed application error=%v", err)
			}
			if len(provider.calls) != 0 {
				t.Fatalf("provider calls=%v, want none", provider.calls)
			}
			var controlExists bool
			if err := probe.QueryRow("SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname='security_control')").Scan(&controlExists); err != nil {
				t.Fatal(err)
			}
			if controlExists {
				t.Fatal("malformed application admitted DDL/control schema")
			}
			meta, err := inspectMigrationMetadata(context.Background(), mustPinnedConn(t, probe), mustParsedConfig(t, db.DSN()))
			if err != nil || meta.RowCount != 1 || meta.Version != 5 || meta.Dirty || meta.CatalogVersion != 5 {
				t.Fatalf("metadata after refusal=%+v err=%v", meta, err)
			}
		})
	}
}

func TestD1LBootstrapRejectsWrongTargetBeforeProviderAndDDL(t *testing.T) {
	dbA, err := testsupport.New(context.Background(), os.Getenv("TEST_DATABASE_URL"), Up)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dbA.Close() }()
	dbB, err := testsupport.New(context.Background(), os.Getenv("TEST_DATABASE_URL"), Up)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dbB.Close() }()
	t.Logf("databases=%s,%s", dbA.Name(), dbB.Name())
	targetA := d1lTargetForTest(t, dbA.DSN())
	wrong := append([]byte(nil), targetA...)
	wrong[0] ^= 0xff
	cases := []struct {
		name   string
		dsn    string
		target []byte
	}{
		{name: "arbitrary self-consistent wrong digest", dsn: dbA.DSN(), target: wrong},
		{name: "target B with target A digest", dsn: dbB.DSN(), target: targetA},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var trace []string
			cfg := D1LBootstrapConfig{
				DatabaseURL: tc.dsn, TargetFingerprint: tc.target, EvidenceDigest: []byte(strings.Repeat("e", 32)),
				OperationID: uuid.NewString(), AttemptID: uuid.NewString(), AuthorizationID: uuid.NewString(),
				Envelope: strings.NewReader("opaque-test-presentation"), ExternalWriterAdmission: trustedExternalWriterAdmissionForTest(),
			}
			provider := &d1lTestProvider{cfg: cfg}
			cfg.Provider = provider
			if _, err := d1LBootstrapWithHooks(context.Background(), cfg, d1LBootstrapHooks{trace: func(stage string) {
				trace = append(trace, stage)
			}}); !errors.Is(err, ErrD1LProviderBinding) {
				t.Fatalf("wrong target error=%v", err)
			}
			if got := strings.Join(trace, ","); got != "PIN,DERIVE,VERIFY" {
				t.Fatalf("wrong-target trace=%s, want PIN,DERIVE,VERIFY", got)
			}
			if len(provider.calls) != 0 {
				t.Fatalf("provider calls=%v, want none", provider.calls)
			}
			probe, err := sql.Open("postgres", tc.dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer probe.Close()
			var controlExists bool
			if err := probe.QueryRow("SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname='security_control')").Scan(&controlExists); err != nil {
				t.Fatal(err)
			}
			if controlExists {
				t.Fatal("wrong target admitted DDL/control schema")
			}
			var manifestRelations int
			if err := probe.QueryRow(`SELECT count(*) FROM pg_class AS c JOIN pg_namespace AS n ON n.oid = c.relnamespace WHERE n.nspname = 'security_control' AND c.relname = 'control_schema_migrations'`).Scan(&manifestRelations); err != nil {
				t.Fatal(err)
			}
			if manifestRelations != 0 {
				t.Fatalf("wrong target admitted manifest relation count=%d", manifestRelations)
			}
		})
	}
}

func mustParsedConfig(t *testing.T, dsn string) *postgres.Config {
	t.Helper()
	parsed, err := parsePostgresDatabaseURL(dsn)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.config
}

func mustPinnedConn(t *testing.T, db *sql.DB) *sql.Conn {
	t.Helper()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestD1LBootstrapSuccessConsumesOnceAndProvesReady(t *testing.T) {
	target := d1lTargetForTest(t, os.Getenv("TEST_DATABASE_URL"))
	evidence := []byte(strings.Repeat("e", 32))
	cfg := D1LBootstrapConfig{
		DatabaseURL: os.Getenv("TEST_DATABASE_URL"), TargetFingerprint: target, EvidenceDigest: evidence,
		OperationID: uuid.NewString(), AttemptID: uuid.NewString(), AuthorizationID: uuid.NewString(),
		Envelope: strings.NewReader("opaque-test-presentation"),
	}
	provider := &d1lTestProvider{cfg: cfg}
	cfg.Provider = provider
	var trace []string
	report, err := d1LBootstrapWithHooks(context.Background(), cfg, d1LBootstrapHooks{trace: func(stage string) {
		trace = append(trace, stage)
	}})
	if err != nil {
		t.Fatal(err)
	}
	if report.InstallState != D1LInstallCommittedReady || report.After != D1LExactReady || !report.Committed {
		t.Fatalf("report=%+v", report)
	}
	if got := strings.Join(trace, ","); got != "PIN,DERIVE,VERIFY,FENCE,LOCK,V5_BASE,CONSUME,DDL" {
		t.Fatalf("bootstrap trace=%s, want PIN,DERIVE,VERIFY,FENCE,LOCK,V5_BASE,CONSUME,DDL", got)
	}
	if report.BackendPID == 0 || report.MigrationLockKey == 0 {
		t.Fatalf("missing pinned-session evidence report=%+v", report)
	}
	t.Logf("database=%s trace=%s backend_pid=%d migration_lock_key=%d provider=%s", databaseNameFromDSN(os.Getenv("TEST_DATABASE_URL")), strings.Join(trace, "<"), report.BackendPID, report.MigrationLockKey, strings.Join(provider.calls, ","))
	cleanupDB, err := sql.Open("postgres", os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupDB.Close()
	if _, err := cleanupDB.ExecContext(context.Background(), "DROP SCHEMA security_control CASCADE"); err != nil {
		t.Fatal("test cleanup: ", err)
	}
	if strings.Join(provider.calls, ",") != "attestation,inspect,consume" {
		t.Fatalf("provider order=%v", provider.calls)
	}
}
