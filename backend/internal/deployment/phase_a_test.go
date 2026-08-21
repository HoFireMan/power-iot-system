package deployment

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func validPhaseAConfig() PhaseAConfig {
	return PhaseAConfig{
		ApplicationDatabaseURL: "postgres://db-vm/power_iot",
		ProviderDatabaseURL:    "postgres://db-vm/d1l_provider",
		AppVMRole:              "PowerIoT-App",
		DBVMRole:               "PowerIoT-DB",
		D1LPlacement:           "PowerIoT-App",
		ProviderPlacement:      "PowerIoT-DB",
		SecretsSource:          "HOST_MANAGED_EXTERNALLY_INJECTED_RESTRICTED_SECRETS",
	}
}

func TestValidatePhaseAConfigAcceptsSplitRoles(t *testing.T) {
	if err := ValidatePhaseAConfig(validPhaseAConfig()); err != nil {
		t.Fatalf("valid Phase-A config rejected: %v", err)
	}
}

func TestValidateBackendDatabaseAccessRejectsProviderEndpoint(t *testing.T) {
	if err := ValidateBackendDatabaseAccess("postgres://db-vm/d1l_provider"); !errors.Is(err, ErrBackendProviderDatabaseAccess) {
		t.Fatalf("backend provider endpoint was accepted: %v", err)
	}
	if err := ValidateBackendDatabaseAccess(""); err != nil {
		t.Fatalf("empty backend provider endpoint rejected: %v", err)
	}
}

func TestAppComposeUsesExactD1LContractAndLeastPrivilege(t *testing.T) {
	data, err := os.ReadFile("../../../infrastructure/d6/app-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	compose := string(data)
	backendStart := strings.Index(compose, "  backend:\n")
	d1lStart := strings.Index(compose, "  d1l-authority:\n")
	if backendStart < 0 || d1lStart < 0 || d1lStart <= backendStart {
		t.Fatal("Compose service boundaries not found")
	}
	backendBlock := compose[backendStart:d1lStart]
	if strings.Contains(backendBlock, "D1L_PROVIDER_DATABASE_URL") {
		t.Fatal("ordinary backend receives provider DB endpoint")
	}
	d1lBlock := compose[d1lStart:]
	for _, required := range []string{
		"D1L_PROVIDER_DATABASE_URL:",
		"D1L_PROVIDER_TLS_CERT_FILE: /run/poweriot/secrets/provider.crt",
		"D1L_PROVIDER_TLS_KEY_FILE: /run/poweriot/secrets/provider.key",
		"D1L_PROVIDER_TLS_CA_FILE: /run/poweriot/secrets/provider-ca.crt",
	} {
		if !strings.Contains(d1lBlock, required) {
			t.Fatalf("D1-L Compose contract missing %q", required)
		}
	}
}

func TestValidatePhaseAConfigRejectsMergedDatabases(t *testing.T) {
	config := validPhaseAConfig()
	config.ProviderDatabaseURL = config.ApplicationDatabaseURL
	if !errors.Is(ValidatePhaseAConfig(config), ErrApplicationProviderDatabaseMerged) {
		t.Fatalf("merged database config was accepted")
	}
}

func TestValidatePhaseAConfigRejectsCredentialBearingPackageURL(t *testing.T) {
	config := validPhaseAConfig()
	config.ApplicationDatabaseURL = "postgres://app:password@db-vm/power_iot"
	if !errors.Is(ValidatePhaseAConfig(config), ErrProductionSecretInConfig) {
		t.Fatalf("credential-bearing package URL was accepted")
	}
}

func TestValidatePhaseAConfigRequiresExternalSecretSource(t *testing.T) {
	config := validPhaseAConfig()
	config.SecretsSource = "COMPOSE_ENV_DEFAULT"
	if !errors.Is(ValidatePhaseAConfig(config), ErrProductionSecretInConfig) {
		t.Fatalf("non-external secret source was accepted")
	}
}
