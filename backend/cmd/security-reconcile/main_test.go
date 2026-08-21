package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseD1LeaseIdentityRequiresExactOwnerBinding(t *testing.T) {
	valid := func() (string, string, string, string, string) {
		return "018f7d5e-7b2b-7d7d-8d24-9dd4d5f0c101", "018f7d5e-7b2b-7d7d-8d24-9dd4d5f0c102", "018f7d5e-7b2b-7d7d-8d24-9dd4d5f0c103", strings.Repeat("ab", 32), strings.Repeat("cd", 32)
	}
	op, at, lease, target, evidence := valid()
	identity, err := parseD1LeaseIdentity(op, at, lease, 7, target, evidence)
	if err != nil || identity.Generation != 7 || len(identity.TargetFingerprint) != 32 || len(identity.EvidenceDigest) != 32 {
		t.Fatalf("identity=%+v err=%v", identity, err)
	}
	cases := []struct {
		name   string
		mutate func(*string, *string, *string, *int64, *string, *string)
	}{
		{name: "missing operation", mutate: func(op, _, _ *string, _ *int64, _, _ *string) { *op = "" }},
		{name: "zero generation", mutate: func(_, _, _ *string, generation *int64, _, _ *string) { *generation = 0 }},
		{name: "short target digest", mutate: func(_, _, _ *string, _ *int64, target, _ *string) { *target = "ab" }},
		{name: "malformed evidence digest", mutate: func(_, _, _ *string, _ *int64, _, evidence *string) { *evidence = "not-a-digest" }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			caseOp, caseAt, caseLease, caseGeneration, caseTarget, caseEvidence := op, at, lease, int64(7), target, evidence
			test.mutate(&caseOp, &caseAt, &caseLease, &caseGeneration, &caseTarget, &caseEvidence)
			if _, err := parseD1LeaseIdentity(caseOp, caseAt, caseLease, caseGeneration, caseTarget, caseEvidence); err == nil {
				t.Fatal("malformed D1 identity was accepted")
			}
		})
	}
}

func TestRunExecuteRequiresD1LeaseIdentityBeforeDatabaseAccess(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if got := run([]string{"-database-url", "postgres://invalid", "-execute"}, &stdout, &stderr); got != 2 {
		t.Fatalf("exit=%d out=%q err=%q", got, stdout.String(), stderr.String())
	}
	if stderr.String() != "invalid D1 lease identity\n" || stdout.Len() != 0 {
		t.Fatalf("fail-closed output=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunExecuteRequiresTargetBeforeDatabaseAccess(t *testing.T) {
	var stdout, stderr bytes.Buffer
	args := []string{
		"-database-url", "postgres://invalid", "-execute",
		"-d1-operation-id", "018f7d5e-7b2b-7d7d-8d24-9dd4d5f0c101",
		"-d1-attempt-id", "018f7d5e-7b2b-7d7d-8d24-9dd4d5f0c102",
		"-d1-lease-id", "018f7d5e-7b2b-7d7d-8d24-9dd4d5f0c103",
		"-d1-generation", "1",
		"-d1-target-fingerprint", strings.Repeat("ab", 32),
		"-d1-evidence-digest", strings.Repeat("cd", 32),
	}
	if got := run(args, &stdout, &stderr); got != 2 || stderr.String() != "target-id is required\n" || stdout.Len() != 0 {
		t.Fatalf("exit=%d out=%q err=%q", got, stdout.String(), stderr.String())
	}
}

func TestRunExecuteRejectsMalformedD1LeaseIdentityBeforeDatabaseAccess(t *testing.T) {
	var stdout, stderr bytes.Buffer
	args := []string{
		"-database-url", "postgres://invalid", "-execute",
		"-d1-operation-id", "not-a-uuid",
		"-d1-attempt-id", "018f7d5e-7b2b-7d7d-8d24-9dd4d5f0c102",
		"-d1-lease-id", "018f7d5e-7b2b-7d7d-8d24-9dd4d5f0c103",
		"-d1-generation", "1",
		"-d1-target-fingerprint", strings.Repeat("ab", 32),
		"-d1-evidence-digest", strings.Repeat("cd", 32),
	}
	if got := run(args, &stdout, &stderr); got != 2 || stderr.String() != "invalid D1 lease identity\n" || stdout.Len() != 0 {
		t.Fatalf("exit=%d out=%q err=%q", got, stdout.String(), stderr.String())
	}
}

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
