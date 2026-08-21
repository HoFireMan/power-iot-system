package deployment

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

var (
	ErrApplicationProviderDatabaseMerged = errors.New("application and D1-L provider databases must be distinct")
	ErrBackendProviderDatabaseAccess     = errors.New("ordinary backend must not receive provider database access")
	ErrProductionSecretInConfig          = errors.New("production secrets must be externally injected")
	ErrPhaseATopologyIncomplete          = errors.New("Phase-A App/DB topology is incomplete")
)

// PhaseAConfig is the non-secret deployment contract for the approved split
// topology. Values are connection identities/endpoints, never credentials.
type PhaseAConfig struct {
	ApplicationDatabaseURL string
	ProviderDatabaseURL    string
	AppVMRole              string
	DBVMRole               string
	D1LPlacement           string
	ProviderPlacement      string
	SecretsSource          string
}

func ValidatePhaseAConfig(config PhaseAConfig) error {
	app, err := parseDatabaseIdentity(config.ApplicationDatabaseURL)
	if err != nil {
		return fmt.Errorf("application database URL: %w", err)
	}
	provider, err := parseDatabaseIdentity(config.ProviderDatabaseURL)
	if err != nil {
		return fmt.Errorf("provider database URL: %w", err)
	}
	if app == provider {
		return ErrApplicationProviderDatabaseMerged
	}
	if config.AppVMRole != "PowerIoT-App" || config.DBVMRole != "PowerIoT-DB" || config.D1LPlacement != "PowerIoT-App" || config.ProviderPlacement != "PowerIoT-DB" {
		return ErrPhaseATopologyIncomplete
	}
	if config.SecretsSource != "HOST_MANAGED_EXTERNALLY_INJECTED_RESTRICTED_SECRETS" {
		return ErrProductionSecretInConfig
	}
	return nil
}

// ValidateBackendDatabaseAccess is a least-privilege guard for the App-VM
// package. The ordinary backend owns only the application database; the D1-L
// authority is the sole provider-database consumer.
func ValidateBackendDatabaseAccess(providerDatabaseURL string) error {
	if strings.TrimSpace(providerDatabaseURL) != "" {
		return ErrBackendProviderDatabaseAccess
	}
	return nil
}

func parseDatabaseIdentity(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("database identity is required")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "postgres" && u.Scheme != "postgresql" || u.Host == "" || u.Path == "" {
		return "", errors.New("database identity must be a PostgreSQL URL")
	}
	if u.User != nil && (u.User.Username() != "" || func() bool { _, set := u.User.Password(); return set }()) {
		return "", ErrProductionSecretInConfig
	}
	return strings.ToLower(u.Host + u.Path), nil
}
