package migrations

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	defaultD1LClientTimeout = 10 * time.Second
	maxD1LClientTimeout     = 2 * time.Minute
	d1lRunnerIdentity       = "spiffe://power-iot/a3/d1l-runner"
)

// AuthorizationClientConfig is deployment configuration, not request data.
// Trust roots, client credentials, and the expected provider URI are required
// so a client cannot silently fall back to plaintext or an unverified peer.
type AuthorizationClientConfig struct {
	Endpoint            string
	TrustRoots          *x509.CertPool
	TrustRootPEM        []byte
	TrustRootFile       string
	ClientCertificate   tls.Certificate
	ClientCertFile      string
	ClientKeyFile       string
	ExpectedProviderURI string
	Timeout             time.Duration
}

// Config is a short compatibility name for callers in the data layer.
type Config = AuthorizationClientConfig

func LoadAuthorizationClientConfig(get func(string) string) (AuthorizationClientConfig, error) {
	if get == nil {
		return AuthorizationClientConfig{}, errors.New("D1L authorization configuration source is required")
	}
	cfg := AuthorizationClientConfig{
		Endpoint:            firstConfigValue(get, "D1L_AUTHORIZATION_PROVIDER_ENDPOINT", "D1L_PROVIDER_ENDPOINT"),
		TrustRootFile:       firstConfigValue(get, "D1L_AUTHORIZATION_PROVIDER_CA_FILE", "D1L_PROVIDER_CA_FILE", "D1L_PROVIDER_TLS_CA_FILE"),
		ClientCertFile:      firstConfigValue(get, "D1L_AUTHORIZATION_PROVIDER_CLIENT_CERT_FILE", "D1L_CLIENT_CERT_FILE", "D1L_RUNNER_TLS_CERT_FILE"),
		ClientKeyFile:       firstConfigValue(get, "D1L_AUTHORIZATION_PROVIDER_CLIENT_KEY_FILE", "D1L_CLIENT_KEY_FILE", "D1L_RUNNER_TLS_KEY_FILE"),
		ExpectedProviderURI: firstConfigValue(get, "D1L_AUTHORIZATION_PROVIDER_URI", "D1L_PROVIDER_URI", "D1L_PROVIDER_EXPECTED_URI"),
	}
	if raw := firstConfigValue(get, "D1L_AUTHORIZATION_PROVIDER_TIMEOUT", "D1L_PROVIDER_TIMEOUT"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return AuthorizationClientConfig{}, errors.New("invalid D1L authorization provider timeout")
		}
		cfg.Timeout = d
	}
	return cfg, validateAuthorizationConfig(cfg)
}

func LoadD1LAuthorizationClientConfig(get func(string) string) (AuthorizationClientConfig, error) {
	return LoadAuthorizationClientConfig(get)
}

func firstConfigValue(get func(string) string, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(get(key)); value != "" {
			return value
		}
	}
	return ""
}

func validateAuthorizationConfig(cfg AuthorizationClientConfig) error {
	u, err := url.Parse(strings.TrimSpace(cfg.Endpoint))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("D1L authorization provider endpoint must be HTTPS")
	}
	if u.Path != "" && u.Path != "/" {
		return errors.New("D1L authorization provider endpoint must be an origin")
	}
	if strings.TrimSpace(cfg.ExpectedProviderURI) == "" || !validProviderURI(cfg.ExpectedProviderURI) {
		return errors.New("D1L authorization provider URI identity is required")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultD1LClientTimeout
	}
	if cfg.Timeout <= 0 || cfg.Timeout > maxD1LClientTimeout {
		return errors.New("D1L authorization provider timeout is out of bounds")
	}
	if cfg.TrustRoots == nil && len(cfg.TrustRootPEM) == 0 && cfg.TrustRootFile == "" {
		return errors.New("D1L authorization provider trust roots are required")
	}
	if len(cfg.ClientCertificate.Certificate) == 0 && (cfg.ClientCertFile == "" || cfg.ClientKeyFile == "") {
		return errors.New("D1L authorization provider client certificate and key are required")
	}
	return nil
}

func validProviderURI(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "spiffe" && u.Host != "" && u.Path != ""
}

func loadAuthorizationTLS(cfg AuthorizationClientConfig) (*tls.Config, error) {
	if err := validateAuthorizationConfig(cfg); err != nil {
		return nil, err
	}
	roots := cfg.TrustRoots
	if roots == nil {
		roots = x509.NewCertPool()
	}
	if cfg.TrustRootFile != "" {
		pem, err := os.ReadFile(cfg.TrustRootFile)
		if err != nil || !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("D1L authorization provider trust roots are unavailable")
		}
	} else if len(cfg.TrustRootPEM) != 0 && !roots.AppendCertsFromPEM(cfg.TrustRootPEM) {
		return nil, errors.New("D1L authorization provider trust roots are invalid")
	}
	cert := cfg.ClientCertificate
	if len(cert.Certificate) == 0 {
		var err error
		cert, err = tls.LoadX509KeyPair(cfg.ClientCertFile, cfg.ClientKeyFile)
		if err != nil {
			return nil, errors.New("D1L authorization provider client certificate is unavailable")
		}
	}
	if len(cert.Certificate) == 0 {
		return nil, errors.New("D1L authorization provider client certificate is empty")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil || len(leaf.URIs) != 1 || leaf.URIs[0].String() != d1lRunnerIdentity {
		return nil, errors.New("D1L authorization client identity must be d1l-runner")
	}
	u, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return nil, errors.New("D1L authorization provider endpoint is invalid")
	}
	tlsCfg := &tls.Config{
		MinVersion:         tls.VersionTLS13,
		RootCAs:            roots,
		Certificates:       []tls.Certificate{cert},
		InsecureSkipVerify: false,
		ServerName:         u.Hostname(),
	}
	// Standard verification remains enabled; this callback adds the pinned
	// provider URI SAN check rather than replacing chain verification.
	expected := cfg.ExpectedProviderURI
	tlsCfg.VerifyPeerCertificate = func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return errors.New("provider certificate missing")
		}
		leaf, err := x509.ParseCertificate(rawCerts[0])
		if err != nil {
			return errors.New("provider certificate invalid")
		}
		for _, identity := range leaf.URIs {
			if identity.String() == expected {
				return nil
			}
		}
		return errors.New("provider identity mismatch")
	}
	return tlsCfg, nil
}

func configError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("D1L authorization client configuration rejected: %w", err)
}
