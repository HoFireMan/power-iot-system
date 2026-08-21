package migrations

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestD1LInstallerDigestV1(t *testing.T) {
	d := sha256.Sum256(d1LInstallerBytes)
	if got := hex.EncodeToString(d[:]); got != D1LInstallerDigestV1 {
		t.Fatalf("digest=%s want=%s", got, D1LInstallerDigestV1)
	}
	if len(d1LInstallerBytes) != 10000 {
		t.Fatalf("artifact length=%d want=10000", len(d1LInstallerBytes))
	}
	if !bytes.HasSuffix(d1LInstallerBytes, []byte{'\n'}) || bytes.HasSuffix(d1LInstallerBytes, []byte{'\n', '\n'}) {
		t.Fatal("artifact must have exactly one final LF")
	}
	if bytes.Contains(d1LInstallerBytes, []byte("IF NOT EXISTS")) || bytes.Contains(d1LInstallerBytes, []byte("BEGIN;")) || bytes.Contains(d1LInstallerBytes, []byte("COMMIT;")) || bytes.Contains(d1LInstallerBytes, []byte("INSERT INTO security_control.control_schema_migrations")) || bytes.Contains(d1LInstallerBytes, []byte("$1")) || bytes.Contains(d1LInstallerBytes, []byte("$2")) || bytes.Contains(d1LInstallerBytes, []byte("$3")) {
		t.Fatal("artifact contains forbidden migration control or runtime manifest")
	}
	if strings.Contains(string(d1LInstallerBytes), "--") || strings.Contains(string(d1LInstallerBytes), "/*") {
		t.Fatal("artifact contains comments")
	}
}

func TestD1LManifestStatementIsFixedParameterizedRunnerSQL(t *testing.T) {
	want := `INSERT INTO security_control.control_schema_migrations (
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
	if D1LManifestInsertSQL != want {
		t.Fatalf("manifest statement changed:\n%s", D1LManifestInsertSQL)
	}
}
