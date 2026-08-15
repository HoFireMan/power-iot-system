package store

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
)

var disposableProviderDatabaseName = regexp.MustCompile(`^d1l_provider_test_[a-z0-9]+$`)

// validateProviderTestURL is deliberately stricter than production config: it
// permits only a uniquely named disposable database on the approved test
// server and rejects every target/application database boundary.
func validateProviderTestURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("provider test DSN is required")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return fmt.Errorf("provider test DSN scheme is invalid")
	}
	if u.Hostname() != "127.0.0.1" || u.Port() != "55434" {
		return fmt.Errorf("provider test DSN must use 127.0.0.1:55434")
	}
	name := strings.TrimPrefix(u.Path, "/")
	if !disposableProviderDatabaseName.MatchString(name) {
		return fmt.Errorf("provider test database name is not disposable")
	}
	for _, key := range []string{"DATABASE_URL", "TEST_DATABASE_URL", "TEST_MIGRATION_DATABASE_URL"} {
		if other := strings.TrimSpace(os.Getenv(key)); other != "" && raw == other {
			return fmt.Errorf("provider test DSN reuses %s", key)
		}
	}
	return nil
}

func TestProviderTestURLGuard(t *testing.T) {
	for _, raw := range []string{
		"postgres://user@127.0.0.1:5432/d1l_provider_test_bad",
		"postgres://user@127.0.0.1:55434/power_iot_db",
		"postgres://user@127.0.0.1:55434/security_schema_integration",
		"postgres://user@localhost:55434/d1l_provider_test_bad",
		"postgres://user@127.0.0.1:55434/provider",
	} {
		if err := validateProviderTestURL(raw); err == nil {
			t.Fatalf("unsafe provider test DSN accepted: %s", raw)
		}
	}
	if err := validateProviderTestURL("postgres://user@127.0.0.1:55434/d1l_provider_test_abc123"); err != nil {
		t.Fatalf("safe provider test DSN rejected: %v", err)
	}
}

// This check is intentionally opt-in. It never falls back to the target
// DATABASE_URL or a conventional local PostgreSQL instance.
func TestProviderDatabaseExplicitOnly(t *testing.T) {
	raw := strings.TrimSpace(os.Getenv("D1L_PROVIDER_DATABASE_URL"))
	if raw == "" {
		t.Skip("D1L_PROVIDER_DATABASE_URL is not configured; provider PostgreSQL checks skipped")
	}
	if err := validateProviderTestURL(raw); err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
}
