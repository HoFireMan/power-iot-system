package security

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"
)

const (
	// JWTActiveKIDEnv names the mandatory active signing key identifier.
	JWTActiveKIDEnv = "JWT_ACTIVE_KID"
	// JWTActivePrivateKeyFileEnv names the host-managed PKCS#8 private key file.
	JWTActivePrivateKeyFileEnv = "JWT_ACTIVE_PRIVATE_KEY_FILE"
	// JWTRetiringPublicKeysEnv contains comma-separated kid=file entries.
	JWTRetiringPublicKeysEnv = "JWT_RETIRING_PUBLIC_KEYS"
)

var ErrInvalidKeyConfiguration = errors.New("invalid JWT key configuration")

// RetiringPublicKeyFile identifies one host-managed public verification key.
// Retiring keys are never used for signing.
type RetiringPublicKeyFile struct {
	KID  string
	File string
}

// KeyringConfig is the host-managed configuration needed to construct a
// security.Keyring. Key material is represented only by file paths here.
type KeyringConfig struct {
	ActiveKID              string
	ActivePrivateKeyFile   string
	RetiringPublicKeyFiles []RetiringPublicKeyFile
}

// LoadKeyringFromEnv loads the required production keyring configuration. It
// has no generated or default key path: an empty environment is rejected.
func LoadKeyringFromEnv() (*Keyring, error) {
	return LoadKeyringFrom(func(name string) string { return os.Getenv(name) })
}

// LoadKeyringFrom loads keyring configuration using get, which keeps process
// environment access outside the security implementation's tests and callers.
func LoadKeyringFrom(get func(string) string) (*Keyring, error) {
	if get == nil {
		return nil, ErrInvalidKeyConfiguration
	}
	config, err := parseKeyringConfig(get)
	if err != nil {
		return nil, err
	}
	return LoadKeyring(config)
}

// LoadKeyring reads and validates all configured host-managed Ed25519 keys.
// Errors intentionally identify only the configuration failure, never key
// bytes or private-key contents.
func LoadKeyring(config KeyringConfig) (*Keyring, error) {
	config.ActiveKID = strings.TrimSpace(config.ActiveKID)
	config.ActivePrivateKeyFile = strings.TrimSpace(config.ActivePrivateKeyFile)
	if !validKID(config.ActiveKID) || config.ActivePrivateKeyFile == "" || config.ActivePrivateKeyFile == "." {
		return nil, ErrInvalidKeyConfiguration
	}
	if info, err := os.Stat(config.ActivePrivateKeyFile); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0400 == 0 || info.Mode().Perm()&0077 != 0 {
		return nil, ErrInvalidKeyConfiguration
	}
	private, err := loadEd25519PrivateKey(config.ActivePrivateKeyFile)
	if err != nil {
		return nil, ErrInvalidKeyConfiguration
	}

	retiring := make([]VerificationKey, 0, len(config.RetiringPublicKeyFiles))
	seen := map[string]struct{}{config.ActiveKID: {}}
	for _, file := range config.RetiringPublicKeyFiles {
		kid := strings.TrimSpace(file.KID)
		path := strings.TrimSpace(file.File)
		if !validKID(kid) || path == "" || path == "." {
			return nil, ErrInvalidKeyConfiguration
		}
		if _, exists := seen[kid]; exists {
			return nil, ErrInvalidKeyConfiguration
		}
		seen[kid] = struct{}{}
		if info, statErr := os.Stat(path); statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0444 == 0 {
			return nil, ErrInvalidKeyConfiguration
		}
		public, loadErr := loadEd25519PublicKey(path)
		if loadErr != nil {
			return nil, ErrInvalidKeyConfiguration
		}
		retiring = append(retiring, VerificationKey{KID: kid, Public: public})
	}
	keyring, err := NewKeyring(SigningKey{KID: config.ActiveKID, Private: private}, retiring)
	if err != nil {
		return nil, ErrInvalidKeyConfiguration
	}
	return keyring, nil
}

func parseKeyringConfig(get func(string) string) (KeyringConfig, error) {
	config := KeyringConfig{
		ActiveKID:            strings.TrimSpace(get(JWTActiveKIDEnv)),
		ActivePrivateKeyFile: strings.TrimSpace(get(JWTActivePrivateKeyFileEnv)),
	}
	retiring := strings.TrimSpace(get(JWTRetiringPublicKeysEnv))
	if retiring == "" {
		return config, nil
	}
	for _, entry := range strings.Split(retiring, ",") {
		entry = strings.TrimSpace(entry)
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) != 2 {
			return KeyringConfig{}, ErrInvalidKeyConfiguration
		}
		kid, path := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if !validKID(kid) || path == "" || path == "." {
			return KeyringConfig{}, ErrInvalidKeyConfiguration
		}
		config.RetiringPublicKeyFiles = append(config.RetiringPublicKeyFiles, RetiringPublicKeyFile{KID: kid, File: path})
	}
	return config, nil
}

func loadEd25519PrivateKey(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	data = bytes.TrimSpace(data)
	block, rest := pem.Decode(data)
	if !bytes.HasPrefix(data, []byte("-----BEGIN ")) || block == nil || block.Type != "PRIVATE KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, fmt.Errorf("invalid private key encoding")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	private, ok := parsed.(ed25519.PrivateKey)
	if !ok || len(private) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("private key is not Ed25519")
	}
	return append(ed25519.PrivateKey(nil), private...), nil
}

func loadEd25519PublicKey(path string) (ed25519.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	data = bytes.TrimSpace(data)
	block, rest := pem.Decode(data)
	if !bytes.HasPrefix(data, []byte("-----BEGIN ")) || block == nil || block.Type != "PUBLIC KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, fmt.Errorf("invalid public key encoding")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	public, ok := parsed.(ed25519.PublicKey)
	if !ok || len(public) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("public key is not Ed25519")
	}
	return append(ed25519.PublicKey(nil), public...), nil
}
