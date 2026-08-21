// Package providerdsn contains safety checks used only by opt-in Provider
// PostgreSQL integration tests. It is deliberately not part of production
// connection policy.
package providerdsn

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var disposableDatabaseName = regexp.MustCompile(`^d1l_provider_test_[a-z0-9]+$`)

// Keep the query surface deliberately smaller than pgx's accepted settings.
// In particular, do not allow credentials, file paths, routing, or arbitrary
// runtime parameters to reach pgx.ParseConfig.
var allowedQueryKeys = map[string]struct{}{
	"sslmode":          {},
	"application_name": {},
}

// Provider tests use plaintext local PostgreSQL endpoints. These are the only
// pgx sslmode values admitted by this opt-in test URL validator.
var allowedSSLModeValues = map[string]struct{}{
	"disable": {},
}

// IsDisposableDatabaseName reports whether name is a uniquely named database
// reserved for a Provider integration test.
func IsDisposableDatabaseName(name string) bool {
	return disposableDatabaseName.MatchString(name)
}

// ValidateProviderTestURL validates a Provider test DSN before it is passed to
// store.Open. This is stricter than production configuration: it permits only
// one approved loopback endpoint and one disposable database, and rejects
// query forms that pgx could interpret as another endpoint or database.
func ValidateProviderTestURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("provider test DSN is required")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Opaque != "" || u.Host == "" || u.Fragment != "" || u.ForceQuery {
		return fmt.Errorf("provider test DSN is invalid")
	}

	// pgx expands comma-separated URL hosts into its Fallbacks list. Check the
	// raw authority rather than url.URL.Hostname, which only exposes a portion
	// of a multi-host authority.
	if strings.Contains(u.Host, ",") {
		return fmt.Errorf("provider test DSN must use one endpoint")
	}
	host, portText, err := net.SplitHostPort(u.Host)
	if err != nil || host != "127.0.0.1" {
		return fmt.Errorf("provider test DSN must use 127.0.0.1")
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("provider test DSN port is invalid")
	}
	switch port {
	case 5432, 55433, 55434:
		return fmt.Errorf("provider test DSN uses a reserved port")
	}

	name := strings.TrimPrefix(u.Path, "/")
	if !IsDisposableDatabaseName(name) {
		return fmt.Errorf("provider test database name is not disposable")
	}
	if u.RawPath != "" {
		return fmt.Errorf("provider test database path is invalid")
	}

	if err := validateQuery(u.RawQuery); err != nil {
		return err
	}
	for _, key := range []string{"DATABASE_URL", "TEST_DATABASE_URL", "TEST_MIGRATION_DATABASE_URL"} {
		if other := strings.TrimSpace(os.Getenv(key)); other != "" && raw == other {
			return fmt.Errorf("provider test DSN reuses %s", key)
		}
	}
	return nil
}

func validateQuery(rawQuery string) error {
	if rawQuery == "" {
		return nil
	}
	seen := make(map[string]struct{})
	for _, field := range strings.Split(rawQuery, "&") {
		if field == "" {
			return fmt.Errorf("provider test DSN query is invalid")
		}
		keyText, valueText, hasValue := strings.Cut(field, "=")
		if !hasValue || keyText == "" {
			return fmt.Errorf("provider test DSN query is invalid")
		}
		if strings.Contains(keyText, "%") {
			return fmt.Errorf("provider test DSN query key is invalid")
		}
		key, err := url.QueryUnescape(keyText)
		if err != nil || key == "" {
			return fmt.Errorf("provider test DSN query key is invalid")
		}
		value, err := url.QueryUnescape(valueText)
		if err != nil || value == "" {
			return fmt.Errorf("provider test DSN query value is invalid")
		}
		normalizedKey := strings.ToLower(key)
		if _, duplicate := seen[normalizedKey]; duplicate {
			return fmt.Errorf("provider test DSN query contains a duplicate key")
		}
		seen[normalizedKey] = struct{}{}
		if _, allowed := allowedQueryKeys[key]; !allowed {
			return fmt.Errorf("provider test DSN query contains a disallowed key")
		}
		if key == "sslmode" {
			if _, allowed := allowedSSLModeValues[value]; !allowed {
				return fmt.Errorf("provider test DSN sslmode is invalid")
			}
		}
	}
	// ParseQuery rejects malformed semicolon separators; the explicit field
	// checks above reject empty fields and missing '=' that ParseQuery accepts.
	if _, err := url.ParseQuery(rawQuery); err != nil {
		return fmt.Errorf("provider test DSN query is invalid")
	}
	return nil
}
