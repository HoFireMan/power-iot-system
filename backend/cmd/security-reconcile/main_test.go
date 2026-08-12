package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunRejectsInvalidDuplicateExecuteValueWithoutEcho(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if got := run([]string{"-execute=false", "-execute=postgres://alice:supersecret@example.test/security"}, &stdout, &stderr); got != 2 {
		t.Fatalf("exit=%d out=%q err=%q", got, stdout.String(), stderr.String())
	}
	if strings.Contains(stderr.String(), "supersecret") || strings.Contains(stderr.String(), "postgres://") {
		t.Fatalf("credential-bearing duplicate error=%q", stderr.String())
	}
}

func TestRunRejectsUnexpectedPositionalExecuteValue(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if got := run([]string{"-execute", "false"}, &stdout, &stderr); got != 2 {
		t.Fatalf("exit=%d out=%q err=%q", got, stdout.String(), stderr.String())
	}
	if strings.Contains(stderr.String(), "postgres://") || strings.Contains(stderr.String(), "password") {
		t.Fatalf("unsafe positional error=%q", stderr.String())
	}
}

func TestRunInvalidExecuteValueDoesNotEchoCredential(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if got := run([]string{"-execute=postgres://alice:supersecret@example.test/security"}, &stdout, &stderr); got != 2 {
		t.Fatalf("exit=%d out=%q err=%q", got, stdout.String(), stderr.String())
	}
	if strings.Contains(stderr.String(), "supersecret") || strings.Contains(stderr.String(), "postgres://") {
		t.Fatalf("credential-bearing parse error=%q", stderr.String())
	}
}

func TestRunHelpDoesNotPrintDatabaseURLEnvironment(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://alice:supersecret@example.test/security")
	var stdout, stderr bytes.Buffer
	if got := run([]string{"-h"}, &stdout, &stderr); got != 2 {
		t.Fatalf("exit=%d stderr=%q", got, stderr.String())
	}
	if strings.Contains(stderr.String(), "supersecret") || strings.Contains(stderr.String(), "postgres://") {
		t.Fatalf("credential-bearing help output=%q", stderr.String())
	}
}

func TestRunRequiresDatabaseURLBeforeOpeningAnything(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if got := run([]string{"-database-url", "", "-execute"}, &stdout, &stderr); got != 2 {
		t.Fatalf("exit=%d stderr=%q", got, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("unexpected output=%q", stdout.String())
	}
}

func TestRunRedactsMalformedDatabaseURL(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if got := run([]string{"-database-url", "postgres://alice:supersecret@%zz/nope"}, &stdout, &stderr); got != 2 {
		t.Fatalf("exit=%d out=%q err=%q", got, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), "supersecret") || strings.Contains(stdout.String(), "alice:") {
		t.Fatalf("database credential leaked: %s", stdout.String())
	}
}

func TestRunSanitizesCredentialLookingMappingPath(t *testing.T) {
	path := "/tmp/password=supersecret/postgres://alice:secret@example.invalid/mapping.json"
	var stdout, stderr bytes.Buffer
	if got := run([]string{"-database-url", "postgres://invalid", "-mapping-file", path}, &stdout, &stderr); got != 2 {
		t.Fatalf("exit=%d out=%q err=%q", got, stdout.String(), stderr.String())
	}
	if strings.Contains(stderr.String(), "supersecret") || strings.Contains(stderr.String(), "postgres://") || strings.Contains(stderr.String(), path) {
		t.Fatalf("sensitive mapping path leaked: %q", stderr.String())
	}
}

func TestRunRejectsMalformedMappingBeforeDatabaseAccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mapping.json")
	if err := os.WriteFile(path, []byte(`{"schema":"wrong"}`), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if got := run([]string{"-database-url", "postgres://invalid", "-mapping-file", path}, &stdout, &stderr); got != 2 {
		t.Fatalf("exit=%d stderr=%q", got, stderr.String())
	}
	if !strings.Contains(stderr.String(), "failed to read mapping artifact") || strings.Contains(stderr.String(), "wrong") {
		t.Fatalf("stderr=%q", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("unexpected output=%q", stdout.String())
	}
}
