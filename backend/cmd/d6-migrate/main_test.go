package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"
)

func TestRunProductionEntryFailsClosedWithoutTarget(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if got := run([]string{"-production", "-execute", "-database-url", "postgres://db/app"}, &stdout, &stderr); got != 2 {
		t.Fatalf("exit=%d, want 2; stderr=%s", got, stderr.String())
	}
	if !strings.Contains(stderr.String(), "exact target tcrfid01") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestRunProductionEntryFailsClosedWithoutTrustedFD(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if got := run([]string{"-production", "-target", "tcrfid01", "-execute", "-database-url", "postgres://db/app"}, &stdout, &stderr); got != 2 {
		t.Fatalf("exit=%d, want 2; stderr=%s", got, stderr.String())
	}
	if !strings.Contains(stderr.String(), "target-identity-file") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestTrustedPipeAdmissionRequiresExactTargetLine(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	canonical := "D6-DRAIN-ADMISSION-V2 target=tcrfid01 result=PASS\n"
	signed := strings.TrimSuffix(canonical, "\n") + " sig=" + base64.RawStdEncoding.EncodeToString(ed25519.Sign(privateKey, []byte(canonical))) + "\n"
	for name, line := range map[string]string{
		"wrong target":       "D6-DRAIN-ADMISSION-V1 target=other result=PASS\n",
		"self asserted json": `{"target":"tcrfid01","result":"PASS"}` + "\n",
		"missing line":       "",
	} {
		t.Run(name, func(t *testing.T) {
			check := trustedPipeAdmission(strings.NewReader(line), "tcrfid01", publicKey)
			if err := check(t.Context()); err == nil {
				t.Fatal("untrusted admission accepted")
			}
		})
	}
	if err := trustedPipeAdmission(strings.NewReader(signed), "tcrfid01", publicKey)(t.Context()); err != nil {
		t.Fatalf("trusted admission rejected: %v", err)
	}
}
