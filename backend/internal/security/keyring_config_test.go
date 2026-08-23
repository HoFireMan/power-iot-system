package security

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadKeyringFromHostManagedFilesActiveOnly(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	privateFile := writePrivateKeyFile(t, private)

	keyring, err := LoadKeyring(KeyringConfig{ActiveKID: "active-host", ActivePrivateKeyFile: privateFile})
	if err != nil {
		t.Fatalf("active-only keyring: %v", err)
	}
	raw, err := keyring.IssueAccessToken("user", "session")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := keyring.VerifyAccessToken(raw); err != nil {
		t.Fatalf("loaded active key did not verify: %v", err)
	}
	if len(public) != ed25519.PublicKeySize {
		t.Fatal("test key generation returned an invalid public key")
	}
}

func TestLoadKeyringFromHostManagedFilesActiveAndRetiring(t *testing.T) {
	_, activePrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, retiringPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	activeFile := writePrivateKeyFile(t, activePrivate)
	retiringFile := writePublicKeyFile(t, retiringPrivate.Public().(ed25519.PublicKey))

	keyring, err := LoadKeyring(KeyringConfig{
		ActiveKID: "active-host", ActivePrivateKeyFile: activeFile,
		RetiringPublicKeyFiles: []RetiringPublicKeyFile{{KID: "retiring-host", File: retiringFile}},
	})
	if err != nil {
		t.Fatalf("active-plus-retiring keyring: %v", err)
	}
	if _, err := keyring.VerifyAccessToken(signTokenForTest(t, retiringPrivate, "retiring-host")); err != nil {
		t.Fatalf("loaded retiring key did not verify: %v", err)
	}
}

func TestLoadKeyringFromEnvRequiresExplicitConfiguration(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	privateFile := writePrivateKeyFile(t, private)
	values := map[string]string{
		JWTActiveKIDEnv:            "active-host",
		JWTActivePrivateKeyFileEnv: privateFile,
	}
	keyring, err := LoadKeyringFrom(func(name string) string { return values[name] })
	if err != nil || keyring == nil {
		t.Fatalf("valid environment configuration failed: keyring=%v err=%v", keyring != nil, err)
	}

	invalidEntries := []string{"retiring", "=file", "kid=", "kid=file,", "kid=file,,other=file"}
	for _, entry := range invalidEntries {
		values[JWTRetiringPublicKeysEnv] = entry
		if _, err := LoadKeyringFrom(func(name string) string { return values[name] }); err == nil {
			t.Fatalf("invalid retiring configuration %q accepted", entry)
		}
	}

	for _, values := range []map[string]string{
		{},
		{JWTActivePrivateKeyFileEnv: privateFile},
		{JWTActiveKIDEnv: "active-host"},
		{JWTActiveKIDEnv: "active-host", JWTActivePrivateKeyFileEnv: ""},
	} {
		if _, err := LoadKeyringFrom(func(name string) string { return values[name] }); err == nil {
			t.Fatalf("empty/default configuration accepted: %#v", values)
		}
	}
}

func TestLoadKeyringRejectsMalformedUnreadableAndDuplicateConfiguration(t *testing.T) {
	_, activePrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, retiringPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	activeFile := writePrivateKeyFile(t, activePrivate)
	retiringFile := writePublicKeyFile(t, retiringPrivate.Public().(ed25519.PublicKey))
	cases := []struct {
		name   string
		config KeyringConfig
	}{
		{"empty", KeyringConfig{}},
		{"invalid active kid", KeyringConfig{ActiveKID: "bad kid", ActivePrivateKeyFile: activeFile}},
		{"missing active file", KeyringConfig{ActiveKID: "active-host", ActivePrivateKeyFile: filepath.Join(t.TempDir(), "missing.pem")}},
		{"malformed active file", KeyringConfig{ActiveKID: "active-host", ActivePrivateKeyFile: writeBytesFile(t, []byte("not a key"), 0600)}},
		{"wrong active key type", KeyringConfig{ActiveKID: "active-host", ActivePrivateKeyFile: writePublicKeyFile(t, activePrivate.Public().(ed25519.PublicKey))}},
		{"missing retiring file", KeyringConfig{ActiveKID: "active-host", ActivePrivateKeyFile: activeFile, RetiringPublicKeyFiles: []RetiringPublicKeyFile{{KID: "retiring-host", File: filepath.Join(t.TempDir(), "missing.pem")}}}},
		{"malformed retiring file", KeyringConfig{ActiveKID: "active-host", ActivePrivateKeyFile: activeFile, RetiringPublicKeyFiles: []RetiringPublicKeyFile{{KID: "retiring-host", File: writeBytesFile(t, []byte("not a key"), 0600)}}}},
		{"duplicate retiring kid", KeyringConfig{ActiveKID: "active-host", ActivePrivateKeyFile: activeFile, RetiringPublicKeyFiles: []RetiringPublicKeyFile{{KID: "retiring-host", File: retiringFile}, {KID: "retiring-host", File: retiringFile}}}},
		{"active-retiring collision", KeyringConfig{ActiveKID: "active-host", ActivePrivateKeyFile: activeFile, RetiringPublicKeyFiles: []RetiringPublicKeyFile{{KID: "active-host", File: retiringFile}}}},
		{"invalid retiring kid", KeyringConfig{ActiveKID: "active-host", ActivePrivateKeyFile: activeFile, RetiringPublicKeyFiles: []RetiringPublicKeyFile{{KID: "bad kid", File: retiringFile}}}},
		{"empty retiring path", KeyringConfig{ActiveKID: "active-host", ActivePrivateKeyFile: activeFile, RetiringPublicKeyFiles: []RetiringPublicKeyFile{{KID: "retiring-host", File: ""}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := LoadKeyring(tc.config); !errorsIsInvalidKeyConfiguration(err) {
				t.Fatalf("configuration accepted or leaked a different error: %v", err)
			}
		})
	}

	unreadable := writeBytesFile(t, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: mustPKCS8(t, activePrivate)}), 0600)
	if err := os.Chmod(unreadable, 0000); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKeyring(KeyringConfig{ActiveKID: "active-host", ActivePrivateKeyFile: unreadable}); err == nil {
		t.Fatal("unreadable active key accepted")
	}
}

func TestLoadKeyringErrorsNeverContainPrivateKeyMaterial(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	secret := string(private)
	path := writeBytesFile(t, []byte(secret), 0600)
	_, err = LoadKeyring(KeyringConfig{ActiveKID: "active-host", ActivePrivateKeyFile: path})
	if err == nil {
		t.Fatal("malformed private key accepted")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), string(private[:16])) {
		t.Fatalf("key material appeared in error: %q", err)
	}
}

func writePrivateKeyFile(t *testing.T, private ed25519.PrivateKey) string {
	t.Helper()
	return writeBytesFile(t, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: mustPKCS8(t, private)}), 0600)
}

func writePublicKeyFile(t *testing.T, public ed25519.PublicKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	return writeBytesFile(t, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0600)
}

func writeBytesFile(t *testing.T, data []byte, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustPKCS8(t *testing.T, private ed25519.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func signTokenForTest(t *testing.T, private ed25519.PrivateKey, kid string) string {
	t.Helper()
	keyring, err := NewKeyring(SigningKey{KID: kid, Private: private}, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := keyring.IssueAccessToken("user", "session")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func errorsIsInvalidKeyConfiguration(err error) bool {
	return err != nil && errors.Is(err, ErrInvalidKeyConfiguration)
}
