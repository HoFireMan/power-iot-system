package main

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"power-iot-backend/internal/data/private_migrations"
	"power-iot-backend/internal/testsupport"
)

func newB02OperatorDatabase(t *testing.T, cleanV6 bool) *testsupport.Database {
	t.Helper()
	source := os.Getenv("TEST_DATABASE_URL")
	if source == "" {
		t.Skip("TEST_DATABASE_URL is not set; B-02 operator PostgreSQL integration test not run")
	}
	database, err := testsupport.New(context.Background(), source, migrations.Up)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
	})
	if cleanV6 {
		report, err := migrations.RunD6ProtectedMigrationOperator(context.Background(), database.DSN(), func(context.Context) error { return nil })
		if err != nil || report.PostCommitState != migrations.ProtectedStateCleanV6 {
			t.Fatalf("D6 rehearsal report=%+v err=%v", report, err)
		}
	}
	return database
}

func operatorExecuteArgs(databaseURL, identity, evidence string) []string {
	return []string{
		"-rehearsal", "-execute", "-database-url", databaseURL,
		"-target-identity-file", identity, "-drain-evidence", evidence,
	}
}

func migrationVersion(t *testing.T, databaseURL string) (uint, bool) {
	t.Helper()
	version, dirty, err := migrations.Version(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	return version, dirty
}

func TestRehearsalCommandNoExecuteLeavesCleanV6(t *testing.T) {
	database := newB02OperatorDatabase(t, true)
	before, dirty := migrationVersion(t, database.DSN())
	if before != 6 || dirty {
		t.Fatalf("initial metadata version=%d dirty=%t", before, dirty)
	}
	var stdout, stderr testingWriter
	if got := run([]string{"-rehearsal"}, &stdout, &stderr); got != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", got, stdout.String(), stderr.String())
	}
	if stdout.String() != "B-02 rehearsal operator ready; no migration executed\n" {
		t.Fatalf("stdout=%q", stdout.String())
	}
	after, dirty := migrationVersion(t, database.DSN())
	if after != 6 || dirty {
		t.Fatalf("no-execute changed metadata version=%d dirty=%t", after, dirty)
	}
}

func TestRehearsalCommandWrongIdentityLeavesCleanV6(t *testing.T) {
	database := newB02OperatorDatabase(t, true)
	identity := writeRehearsalIdentity(t, "target=tcrfid01\nrole=power-iot-a3-production-operator\n")
	evidence := writeEvidence(t, validEvidence)
	var stdout, stderr testingWriter
	if got := run(operatorExecuteArgs(database.DSN(), identity, evidence), &stdout, &stderr); got != 2 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", got, stdout.String(), stderr.String())
	}
	version, dirty := migrationVersion(t, database.DSN())
	if version != 6 || dirty {
		t.Fatalf("wrong identity changed metadata version=%d dirty=%t", version, dirty)
	}
}

func TestRehearsalCommandRejectsCleanV5WithoutMutation(t *testing.T) {
	database := newB02OperatorDatabase(t, false)
	identity := writeRehearsalIdentity(t, "target=rehearsal\nrole=power-iot-a3-rehearsal-operator\n")
	evidence := writeEvidence(t, validEvidence)
	var stdout, stderr testingWriter
	if got := run(operatorExecuteArgs(database.DSN(), identity, evidence), &stdout, &stderr); got != 1 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", got, stdout.String(), stderr.String())
	}
	version, dirty := migrationVersion(t, database.DSN())
	if version != 5 || dirty {
		t.Fatalf("wrong initial state changed metadata version=%d dirty=%t", version, dirty)
	}
}

func TestRehearsalCommandExecutesB02AndPreservesAlreadyCompleteBehavior(t *testing.T) {
	database := newB02OperatorDatabase(t, true)
	identity := writeRehearsalIdentity(t, "target=rehearsal\nrole=power-iot-a3-rehearsal-operator\n")
	evidence := writeEvidence(t, validEvidence)
	args := operatorExecuteArgs(database.DSN(), identity, evidence)
	var stdout, stderr testingWriter
	if got := run(args, &stdout, &stderr); got != 0 {
		t.Fatalf("first execution=%d stdout=%q stderr=%q", got, stdout.String(), stderr.String())
	}
	if got := run(args, &stdout, &stderr); got != 0 {
		t.Fatalf("already-complete execution=%d stdout=%q stderr=%q", got, stdout.String(), stderr.String())
	}
	version, dirty := migrationVersion(t, database.DSN())
	if version != 7 || dirty {
		t.Fatalf("final metadata version=%d dirty=%t", version, dirty)
	}
	admission, err := migrations.BootstrapAndAdmit(context.Background(), database.DSN())
	if err != nil || admission.State != migrations.ProtectedStateCleanB02 || admission.Disposition != migrations.RuntimeServeB02 {
		t.Fatalf("final admission=%+v err=%v", admission, err)
	}

	db, err := sql.Open("postgres", database.DSN())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var sets, plans, ratePlans, tiers int
	for _, query := range []struct {
		query string
		out   *int
	}{
		{"SELECT count(*) FROM electricity_rate_sets", &sets},
		{"SELECT count(*) FROM electricity_tariff_plans", &plans},
		{"SELECT count(*) FROM electricity_rate_plans", &ratePlans},
		{"SELECT count(*) FROM electricity_rate_tiers", &tiers},
	} {
		if err := db.QueryRow(query.query).Scan(query.out); err != nil {
			t.Fatal(err)
		}
	}
	if sets != 1 || plans != 3 || ratePlans != 3 || tiers != 34 {
		t.Fatalf("catalog counts sets=%d plans=%d ratePlans=%d tiers=%d", sets, plans, ratePlans, tiers)
	}
	var versionCode string
	if err := db.QueryRow("SELECT version_code FROM electricity_rate_sets").Scan(&versionCode); err != nil {
		t.Fatal(err)
	}
	if versionCode != "TAIPOWER_2025_10_01" {
		t.Fatalf("rate set version=%q", versionCode)
	}
}

// testingWriter keeps command integration assertions independent of bytes.Buffer
// helpers used by the unit tests while satisfying io.Writer.
type testingWriter struct{ data []byte }

func (w *testingWriter) Write(p []byte) (int, error) {
	w.data = append(w.data, p...)
	return len(p), nil
}

func (w *testingWriter) String() string { return string(w.data) }
