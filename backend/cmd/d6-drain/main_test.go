package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunDrainFailsClosedForUnboundProductionTarget(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if got := run([]string{"-mode", "production", "-target", "other"}, &stdout, &stderr); got != 2 {
		t.Fatalf("exit=%d, want 2; stderr=%s", got, stderr.String())
	}
	if !strings.Contains(stderr.String(), "tcrfid01") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestVerifyTargetIdentityRequiresExactHostManagedRole(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "identity")
	if err := os.WriteFile(path, []byte("target=rehearsal\nrole=power-iot-a3-rehearsal-operator\n"), 0444); err != nil {
		t.Fatal(err)
	}
	if err := verifyTargetIdentity(path, "rehearsal", "rehearsal"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("target=tcrfid01\nrole=power-iot-a3-rehearsal-operator\n"), 0444); err != nil {
		t.Fatal(err)
	}
	if err := verifyTargetIdentity(path, "rehearsal", "rehearsal"); err == nil {
		t.Fatal("expected mismatched target identity to fail")
	}
}

func TestRunDrainRequiresExplicitRehearsalTarget(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if got := run([]string{"-mode", "rehearsal", "-target", "tcrfid01"}, &stdout, &stderr); got != 2 {
		t.Fatalf("exit=%d, want 2; stderr=%s", got, stderr.String())
	}
}
