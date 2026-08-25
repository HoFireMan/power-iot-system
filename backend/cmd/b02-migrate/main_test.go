package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func writeRehearsalIdentity(t *testing.T, contents string) string {
	t.Helper()
	path := t.TempDir() + "/identity"
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeEvidence(t *testing.T, contents string) string {
	t.Helper()
	path := t.TempDir() + "/evidence.json"
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

const validEvidence = `{"schema":1,"target":"rehearsal","http_writes_blocked":true,"mqtt_ingestion_blocked":true,"restart_suppressed":true,"direct_sql_controlled":true,"in_flight_writes_drained":true,"quiescence_proven":true,"observed_at":"2026-01-01T00:00:00Z"}`

func TestRunRehearsalReadinessDoesNotExecute(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if got := run([]string{"-rehearsal"}, &stdout, &stderr); got != 0 {
		t.Fatalf("exit=%d stderr=%s", got, stderr.String())
	}
	if !strings.Contains(stdout.String(), "no migration executed") {
		t.Fatalf("stdout=%q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestRunRejectsProductionStyleInvocation(t *testing.T) {
	for _, args := range [][]string{{"-production", "-execute"}, {"-rehearsal", "-target", "tcrfid01"}} {
		var stdout, stderr bytes.Buffer
		if got := run(args, &stdout, &stderr); got != 2 {
			t.Fatalf("args=%v exit=%d, want 2; stderr=%s", args, got, stderr.String())
		}
	}
}

func TestRunRejectsNonLocalAndLegacyDatabaseURLs(t *testing.T) {
	for _, tc := range []struct {
		name string
		url  string
		want string
	}{
		{name: "remote", url: "postgres://db.example/power_iot", want: "local 127.0.0.1"},
		{name: "legacy", url: "postgres://127.0.0.1:5432/power_iot", want: "legacy PostgreSQL port"},
		{name: "legacy with leading zero", url: "postgres://127.0.0.1:05432/power_iot", want: "legacy PostgreSQL port"},
		{name: "host override", url: "postgres://127.0.0.1:55434/power_iot?host=remote.example", want: "connection overrides"},
		{name: "port override", url: "postgres://127.0.0.1:55434/power_iot?port=5432", want: "connection overrides"},
		{name: "database override", url: "postgres://127.0.0.1:55434/power_iot?dbname=legacy", want: "connection overrides"},
		{name: "migration table override", url: "postgres://127.0.0.1:55434/power_iot?x-migrations-table=other", want: "connection overrides"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if got := run([]string{"-rehearsal", "-execute", "-database-url", tc.url}, &stdout, &stderr); got != 2 {
				t.Fatalf("exit=%d, want 2; stderr=%s", got, stderr.String())
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("stderr=%q, want %q", stderr.String(), tc.want)
			}
		})
	}
}

func TestRunRequiresRehearsal(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if got := run(nil, &stdout, &stderr); got != 2 {
		t.Fatalf("exit=%d, want 2; stderr=%s", got, stderr.String())
	}
	if !strings.Contains(stderr.String(), "-rehearsal") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestRunExecuteRequiresDatabaseIdentityAndEvidence(t *testing.T) {
	identity := writeRehearsalIdentity(t, "target=rehearsal\nrole=power-iot-a3-rehearsal-operator\n")
	evidence := writeEvidence(t, validEvidence)
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "database", args: []string{"-rehearsal", "-execute", "-target-identity-file", identity, "-drain-evidence", evidence}, want: "-database-url"},
		{name: "identity", args: []string{"-rehearsal", "-execute", "-database-url", "postgres://127.0.0.1:55434/db", "-drain-evidence", evidence}, want: "target-identity-file"},
		{name: "evidence", args: []string{"-rehearsal", "-execute", "-database-url", "postgres://127.0.0.1:55434/db", "-target-identity-file", identity}, want: "drain-evidence"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if got := run(tc.args, &stdout, &stderr); got != 2 {
				t.Fatalf("exit=%d, want 2; stderr=%s", got, stderr.String())
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("stderr=%q, want %q", stderr.String(), tc.want)
			}
		})
	}
}

func TestRunRejectsMalformedIncompleteAndWrongIdentityInputs(t *testing.T) {
	identity := writeRehearsalIdentity(t, "target=rehearsal\nrole=power-iot-a3-rehearsal-operator\n")
	malformedEvidence := writeEvidence(t, "not-json")
	badEvidence := writeEvidence(t, `{"schema":1,"target":"rehearsal","observed_at":"2026-01-01T00:00:00Z"}`)
	wrongTarget := writeEvidence(t, strings.Replace(validEvidence, `"target":"rehearsal"`, `"target":"tcrfid01"`, 1))
	wrongIdentity := writeRehearsalIdentity(t, "target=tcrfid01\nrole=power-iot-a3-production-operator\n")
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "malformed evidence", args: []string{"-rehearsal", "-execute", "-database-url", "postgres://127.0.0.1:55434/db", "-target-identity-file", identity, "-drain-evidence", malformedEvidence}, want: "drain evidence rejected"},
		{name: "incomplete evidence", args: []string{"-rehearsal", "-execute", "-database-url", "postgres://user:secret@127.0.0.1:55434/db", "-target-identity-file", identity, "-drain-evidence", badEvidence}, want: "protected B-02 migration failed"},
		{name: "wrong evidence target", args: []string{"-rehearsal", "-execute", "-database-url", "postgres://127.0.0.1:55434/db", "-target-identity-file", identity, "-drain-evidence", wrongTarget}, want: "drain evidence rejected"},
		{name: "wrong identity", args: []string{"-rehearsal", "-execute", "-database-url", "postgres://127.0.0.1:55434/db", "-target-identity-file", wrongIdentity, "-drain-evidence", badEvidence}, want: "target identity verification failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			got := run(tc.args, &stdout, &stderr)
			if tc.name == "incomplete evidence" {
				if got != 1 || !strings.Contains(stderr.String(), tc.want) {
					t.Fatalf("exit=%d stderr=%q", got, stderr.String())
				}
				if strings.Contains(stderr.String(), "secret") {
					t.Fatalf("credential leaked in stderr=%q", stderr.String())
				}
				return
			}
			if got != 2 || !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("exit=%d stderr=%q", got, stderr.String())
			}
		})
	}
}

func TestValidRehearsalDrainEvidenceIsAdmitted(t *testing.T) {
	evidence, err := loadDrainEvidence(writeEvidence(t, validEvidence))
	if err != nil {
		t.Fatal(err)
	}
	if err := rehearsalDrainAdmission(evidence)(t.Context()); err != nil {
		t.Fatalf("valid rehearsal evidence rejected: %v", err)
	}
}
