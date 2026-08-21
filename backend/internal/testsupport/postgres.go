// Package testsupport contains test-only PostgreSQL isolation helpers.
package testsupport

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

const (
	dedicatedHost = "127.0.0.1"
	dedicatedPort = "55434"
	namePrefix    = "security_test_"
)

// Database is one disposable PostgreSQL database created from the repository's
// dedicated test PostgreSQL instance. Its DSN is safe to pass to test code;
// the helper never logs it.
type Database struct {
	admin *sql.DB
	name  string
	dsn   string

	closeOnce sync.Once
	closeErr  error
}

// Spec describes one package-local environment variable that must be replaced
// with a disposable database for the duration of a test process.
type Spec struct {
	Environment       string
	SourceEnvironment string
	Migrate           func(string) error
}

type environmentValue struct {
	value   string
	present bool
}

// Run provisions every required package-local database, runs the package test
// process, and drops only the databases created by this invocation. Missing
// source configuration and setup/cleanup failures are fatal rather than
// silently falling back to shared test databases.
func Run(m *testing.M, specs ...Spec) int {
	if m == nil {
		fmt.Fprintln(os.Stderr, "isolated PostgreSQL test runner requires testing.M")
		return 1
	}
	original := make(map[string]environmentValue, len(specs))
	sources := make(map[string]string, len(specs))
	for _, spec := range specs {
		if spec.Environment == "" || spec.SourceEnvironment == "" || spec.Migrate == nil {
			fmt.Fprintln(os.Stderr, "isolated PostgreSQL test runner received an incomplete database specification")
			return 1
		}
		if _, seen := original[spec.Environment]; seen {
			fmt.Fprintln(os.Stderr, "isolated PostgreSQL test runner received duplicate environment specifications")
			return 1
		}
		value, present := os.LookupEnv(spec.SourceEnvironment)
		if !present || value == "" {
			fmt.Fprintf(os.Stderr, "required dedicated PostgreSQL source %s is not configured\n", spec.SourceEnvironment)
			return 1
		}
		original[spec.Environment] = lookupEnvironment(spec.Environment)
		sources[spec.Environment] = value
	}

	databases := make([]*Database, 0, len(specs))
	cleanup := func() error {
		var cleanupErr error
		for index := len(databases) - 1; index >= 0; index-- {
			if err := databases[index].Close(); err != nil && cleanupErr == nil {
				cleanupErr = err
			}
		}
		for environment, value := range original {
			if err := restoreEnvironment(environment, value); err != nil && cleanupErr == nil {
				cleanupErr = fmt.Errorf("restore %s: %w", environment, err)
			}
		}
		return cleanupErr
	}

	for _, spec := range specs {
		database, err := New(context.Background(), sources[spec.Environment], spec.Migrate)
		if err != nil {
			_ = cleanup()
			fmt.Fprintf(os.Stderr, "isolated PostgreSQL test setup failed for %s: %v\n", spec.Environment, err)
			return 1
		}
		databases = append(databases, database)
		if err := os.Setenv(spec.Environment, database.DSN()); err != nil {
			_ = cleanup()
			fmt.Fprintf(os.Stderr, "isolated PostgreSQL test setup failed for %s\n", spec.Environment)
			return 1
		}
	}

	exitCode := m.Run()
	if err := cleanup(); err != nil {
		fmt.Fprintf(os.Stderr, "isolated PostgreSQL test cleanup failed: %v\n", err)
		return 1
	}
	return exitCode
}

// New creates a unique database and invokes migrate to initialize it. The
// callback is deliberately supplied by the caller so this package does not
// depend on the migration package (and cannot introduce an import cycle).
func New(ctx context.Context, sourceDSN string, migrate func(string) error) (*Database, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if migrate == nil {
		return nil, errors.New("test database migration callback is required")
	}
	parsed, err := validateSource(sourceDSN)
	if err != nil {
		return nil, err
	}
	name, err := generatedName()
	if err != nil {
		return nil, err
	}
	// postgres is used only as the administrator connection database; it is
	// never returned as a test target and is rejected as a source target above.
	adminDSN := *parsed
	adminDSN.Path = "/postgres"
	adminDSN.RawPath = ""
	q := adminDSN.Query()
	for key := range q {
		if strings.HasPrefix(strings.ToLower(key), "x-") {
			q.Del(key)
		}
	}
	adminDSN.RawQuery = q.Encode()

	admin, err := sql.Open("postgres", adminDSN.String())
	if err != nil {
		return nil, errors.New("open dedicated PostgreSQL administrator connection")
	}
	admin.SetMaxOpenConns(1)
	admin.SetMaxIdleConns(1)
	pingCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := admin.PingContext(pingCtx); err != nil {
		_ = admin.Close()
		return nil, fmt.Errorf("connect to dedicated PostgreSQL administrator: %w", err)
	}

	quoted := quoteIdentifier(name)
	if _, err := admin.ExecContext(pingCtx, "CREATE DATABASE "+quoted); err != nil {
		_ = admin.Close()
		return nil, fmt.Errorf("create isolated PostgreSQL test database: %w", err)
	}
	database := &Database{admin: admin, name: name, dsn: targetDSN(parsed, name)}
	if err := verifyTarget(ctx, database.dsn, name); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("verify isolated PostgreSQL test database: %w", err)
	}
	if err := migrate(database.dsn); err != nil {
		if cleanupErr := database.Close(); cleanupErr != nil {
			return nil, fmt.Errorf("migrate isolated PostgreSQL test database: %v; cleanup failed: %w", err, cleanupErr)
		}
		return nil, fmt.Errorf("migrate isolated PostgreSQL test database: %w", err)
	}
	return database, nil
}

// DSN returns the isolated database connection URL.
func (d *Database) DSN() string {
	if d == nil {
		return ""
	}
	return d.dsn
}

// Name returns the exact generated PostgreSQL database name.
func (d *Database) Name() string {
	if d == nil {
		return ""
	}
	return d.name
}

// Close drops exactly this helper-generated database. It refuses to perform
// cleanup if the name no longer has the complete generated-name shape.
func (d *Database) Close() error {
	if d == nil {
		return nil
	}
	d.closeOnce.Do(func() {
		if d.admin == nil {
			d.closeErr = errors.New("isolated PostgreSQL database has no administrator connection")
			return
		}
		if !validGeneratedName(d.name) {
			d.closeErr = errors.New("refusing cleanup of non-generated PostgreSQL database name")
			_ = d.admin.Close()
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := d.admin.ExecContext(ctx, "DROP DATABASE "+quoteIdentifier(d.name)+" WITH (FORCE)"); err != nil {
			d.closeErr = fmt.Errorf("drop isolated PostgreSQL test database: %w", err)
		} else {
			var exists bool
			if err := d.admin.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", d.name).Scan(&exists); err != nil {
				d.closeErr = fmt.Errorf("verify isolated PostgreSQL test database cleanup: %w", err)
			} else if exists {
				d.closeErr = errors.New("isolated PostgreSQL test database still exists after cleanup")
			}
		}
		if err := d.admin.Close(); d.closeErr == nil && err != nil {
			d.closeErr = fmt.Errorf("close dedicated PostgreSQL administrator: %w", err)
		}
	})
	return d.closeErr
}

func lookupEnvironment(name string) environmentValue {
	value, present := os.LookupEnv(name)
	return environmentValue{value: value, present: present}
}

func restoreEnvironment(name string, original environmentValue) error {
	if original.present {
		return os.Setenv(name, original.value)
	}
	return os.Unsetenv(name)
}

func validateSource(source string) (*url.URL, error) {
	if source == "" {
		return nil, errors.New("dedicated PostgreSQL test source DSN is required")
	}
	parsed, err := url.Parse(source)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Hostname() != dedicatedHost || parsed.Port() != dedicatedPort {
		return nil, errors.New("test PostgreSQL source DSN must use dedicated 127.0.0.1:55434")
	}
	// lib/pq permits connection parameters in the query to override URL
	// components. Reject those overrides so both the administrator and target
	// connections remain pinned to the dedicated endpoint and generated DB.
	for key := range parsed.Query() {
		switch strings.ToLower(key) {
		case "host", "hostaddr", "port", "dbname", "service":
			return nil, errors.New("test PostgreSQL source DSN contains an unsafe connection override")
		}
	}
	database := strings.ToLower(strings.TrimPrefix(parsed.Path, "/"))
	if database == "" || strings.Contains(database, "/") {
		return nil, errors.New("test PostgreSQL source DSN must name one database")
	}
	switch database {
	case "power_iot", "core", "postgres", "template", "template0", "template1", "baseline":
		return nil, errors.New("test PostgreSQL source DSN names a protected database")
	}
	return parsed, nil
}

func verifyTarget(ctx context.Context, dsn, wantName string) error {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return errors.New("open isolated PostgreSQL target")
	}
	defer db.Close()
	pingCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		return fmt.Errorf("connect to isolated PostgreSQL target: %w", err)
	}
	if _, err := validateSource(dsn); err != nil {
		return errors.New("isolated PostgreSQL target endpoint is not the dedicated test server")
	}
	var currentName string
	if err := db.QueryRowContext(pingCtx, "SELECT current_database()").Scan(&currentName); err != nil {
		return fmt.Errorf("inspect isolated PostgreSQL target: %w", err)
	}
	if currentName != wantName {
		return errors.New("isolated PostgreSQL target database identity is incorrect")
	}
	return nil
}

func targetDSN(source *url.URL, name string) string {
	copy := *source
	copy.Path = "/" + name
	copy.RawPath = ""
	return copy.String()
}

func generatedName() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", errors.New("generate isolated PostgreSQL test database name")
	}
	return fmt.Sprintf("%s%x", namePrefix, token), nil
}

func validGeneratedName(name string) bool {
	if !strings.HasPrefix(name, namePrefix) || len(name) != len(namePrefix)+32 {
		return false
	}
	for _, char := range name[len(namePrefix):] {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}

func quoteIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}
