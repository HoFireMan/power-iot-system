package migrations

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/lib/pq"
)

const (
	guardedDownSignal   = "MIGRATION_GUARDED_DOWN"
	guardedDownSQLState = "P0001"
)

type parsedPostgresDatabaseURL struct {
	driverURL string
	config    *postgres.Config
}

// parseQuotedMigrationTable accepts the exact quoted forms understood by the
// PostgreSQL migration driver: "table" or "schema"."table". It deliberately
// rejects trailing text, empty identifiers, and any unquoted separators.
func parseQuotedMigrationTable(value string) (schema string, table string, ok bool) {
	if len(value) < 2 || value[0] != '"' {
		return "", "", false
	}
	firstEnd := strings.IndexByte(value[1:], '"')
	if firstEnd < 0 {
		return "", "", false
	}
	firstEnd++
	if firstEnd == 1 {
		return "", "", false
	}
	if firstEnd == len(value)-1 {
		return "", value[1:firstEnd], true
	}
	if value[firstEnd+1] != '.' || firstEnd+2 >= len(value) || value[firstEnd+2] != '"' {
		return "", "", false
	}
	secondStart := firstEnd + 2
	secondEnd := strings.IndexByte(value[secondStart+1:], '"')
	if secondEnd < 0 {
		return "", "", false
	}
	secondEnd += secondStart + 1
	if secondEnd == secondStart+1 || secondEnd != len(value)-1 {
		return "", "", false
	}
	return value[1:firstEnd], value[secondStart+1 : secondEnd], true
}

// parsePostgresDatabaseURL mirrors golang-migrate's PostgreSQL URL handling for
// the options that affect connection setup and migration metadata. The driver
// URL is filtered before database/sql sees it because lib/pq rejects x-* keys.
func parsePostgresDatabaseURL(databaseURL string) (*parsedPostgresDatabaseURL, error) {
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse PostgreSQL database URL: %w", err)
	}
	query := parsed.Query()
	migrationsTable := query.Get("x-migrations-table")
	migrationsTableQuoted := false
	if value := query.Get("x-migrations-table-quoted"); value != "" {
		migrationsTableQuoted, err = strconv.ParseBool(value)
		if err != nil {
			return nil, fmt.Errorf("parse x-migrations-table-quoted: %w", err)
		}
	}
	if migrationsTableQuoted {
		if _, _, ok := parseQuotedMigrationTable(migrationsTable); !ok {
			return nil, fmt.Errorf("invalid quoted x-migrations-table %q", migrationsTable)
		}
	}
	statementTimeout := 0
	if value := query.Get("x-statement-timeout"); value != "" {
		statementTimeout, err = strconv.Atoi(value)
		if err != nil {
			return nil, fmt.Errorf("parse x-statement-timeout: %w", err)
		}
	}
	multiStatementEnabled := false
	if value := query.Get("x-multi-statement"); value != "" {
		multiStatementEnabled, err = strconv.ParseBool(value)
		if err != nil {
			return nil, fmt.Errorf("parse x-multi-statement: %w", err)
		}
	}
	multiStatementMaxSize := 10 * 1 << 20
	if value := query.Get("x-multi-statement-max-size"); value != "" {
		multiStatementMaxSize, err = strconv.Atoi(value)
		if err != nil {
			return nil, fmt.Errorf("parse x-multi-statement-max-size: %w", err)
		}
		if multiStatementMaxSize <= 0 {
			multiStatementMaxSize = 10 * 1 << 20
		}
	}
	config := &postgres.Config{
		DatabaseName:          parsed.Path,
		MigrationsTable:       migrationsTable,
		MigrationsTableQuoted: migrationsTableQuoted,
		MultiStatementEnabled: multiStatementEnabled,
		StatementTimeout:      time.Duration(statementTimeout) * time.Millisecond,
		MultiStatementMaxSize: multiStatementMaxSize,
	}
	return &parsedPostgresDatabaseURL{
		driverURL: migrate.FilterCustomQuery(parsed).String(),
		config:    config,
	}, nil
}

func migrationMetadataIdentifiers(config *postgres.Config, schemaName string) (string, string, error) {
	tableName := config.MigrationsTable
	if tableName == "" {
		tableName = postgres.DefaultMigrationsTable
	}
	if !config.MigrationsTableQuoted {
		return schemaName, tableName, nil
	}
	quotedSchema, quotedTable, ok := parseQuotedMigrationTable(tableName)
	if !ok {
		return "", "", fmt.Errorf("invalid quoted x-migrations-table %q", tableName)
	}
	if quotedSchema == "" {
		return schemaName, quotedTable, nil
	}
	return quotedSchema, quotedTable, nil
}

func quotedMigrationTable(schemaName, tableName string) string {
	return pq.QuoteIdentifier(schemaName) + "." + pq.QuoteIdentifier(tableName)
}

func beginReadOnlyMigrationInspection(ctx context.Context, db *sql.DB) (*sql.Tx, error) {
	return db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
}

// ErrGuardedDown identifies a deliberate, fail-closed migration rollback
// refusal. It is separate from migration or database failures.
var ErrGuardedDown = errors.New("migration down refused by guard")

// GuardedDownError reports a guarded rollback refusal after the migration
// metadata has been verified and restored to its original clean state. If
// RecoveryError is non-nil, the refusal was recognized but recovery was not
// completed; the error must not be treated as a clean migration state.
type GuardedDownError struct {
	FromVersion   uint
	ToVersion     int
	Cause         error
	RecoveryError error
}

func (e *GuardedDownError) Error() string {
	if e.RecoveryError != nil {
		return fmt.Sprintf("%s (%d -> %d): recovery failed: %v (guard: %v)", ErrGuardedDown, e.FromVersion, e.ToVersion, e.RecoveryError, e.Cause)
	}
	return fmt.Sprintf("%s (%d -> %d): %v", ErrGuardedDown, e.FromVersion, e.ToVersion, e.Cause)
}

func (e *GuardedDownError) Is(target error) bool {
	return target == ErrGuardedDown
}

func (e *GuardedDownError) Unwrap() []error {
	if e.RecoveryError == nil {
		return []error{e.Cause}
	}
	return []error{e.Cause, e.RecoveryError}
}

type migrationHandle struct {
	*migrate.Migrate
	sourceDriver   source.Driver
	databaseDriver database.Driver
	fence          *ExclusiveWriterFence
}

type migrationState struct {
	version int
	dirty   bool
}

type migrationStateStore interface {
	Version() (int, bool, error)
	SetVersion(version int, dirty bool) error
}

// Files contains the versioned SQL migrations. SQL is the schema source of
// truth; GORM models are persistence representations only.
//
//go:embed sql/*.sql
var Files embed.FS

func newMigratorLocked(ctx context.Context, databaseURL string, action migrationGateAction) (*migrationHandle, error) {
	parsed, err := parsePostgresDatabaseURL(databaseURL)
	if err != nil {
		return nil, err
	}
	fence, err := openExclusiveWriterFence(ctx, parsed)
	if err != nil {
		return nil, err
	}
	capability, err := fence.Capability()
	if err != nil {
		_ = fence.Close()
		return nil, err
	}
	if err := RequireProtectedWork(capability); err != nil {
		_ = fence.Close()
		return nil, err
	}
	metadata, err := inspectMigrationMetadata(ctx, fence.Conn(), parsed.config)
	if err != nil {
		_ = fence.Close()
		return nil, err
	}
	embeddedLatest, err := latestEmbeddedMigrationVersion()
	if err != nil {
		_ = fence.Close()
		return nil, err
	}
	if err := classifyMigrationAdmission(metadata, action, embeddedLatest); err != nil {
		_ = fence.Close()
		return nil, err
	}
	sourceDriver, err := iofs.New(Files, "sql")
	if err != nil {
		_ = fence.Close()
		return nil, err
	}
	// WithConnection must be initialized only after the canonical fence is
	// owned, and receives the exact pinned session held by the fence.
	databaseDriver, err := postgres.WithConnection(ctx, fence.Conn(), parsed.config)
	if err != nil {
		_ = sourceDriver.Close()
		_ = fence.Close()
		return nil, err
	}
	m, err := migrate.NewWithInstance("iofs", sourceDriver, "postgres", databaseDriver)
	if err != nil {
		_ = sourceDriver.Close()
		_ = fence.Close()
		return nil, err
	}
	return &migrationHandle{Migrate: m, sourceDriver: sourceDriver, databaseDriver: databaseDriver, fence: fence}, nil
}

func (m *migrationHandle) close() error {
	if m == nil {
		return nil
	}
	// The migrate PostgreSQL driver wraps the same *sql.Conn and its Close
	// would surrender that session. Unlock first, then close source/connection
	// resources through the fence owner.
	var errs []error
	if m.sourceDriver != nil {
		if err := m.sourceDriver.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if m.fence != nil {
		if err := m.fence.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func (m *migrationHandle) state() (migrationState, error) {
	version, dirty, err := m.databaseDriver.Version()
	if err != nil {
		return migrationState{}, err
	}
	return migrationState{version: version, dirty: dirty}, nil
}

func (m *migrationHandle) previousMigrationVersion(version int) (int, error) {
	if version < 0 {
		return database.NilVersion, nil
	}
	previous, err := m.sourceDriver.Prev(uint(version))
	if errors.Is(err, os.ErrNotExist) {
		return database.NilVersion, nil
	}
	if err != nil {
		return 0, err
	}
	return int(previous), nil
}

func guardedDownFailure(err error) bool {
	for err != nil {
		var postgresErr *pq.Error
		if errors.As(err, &postgresErr) {
			return string(postgresErr.Code) == guardedDownSQLState && postgresErr.Message == guardedDownSignal
		}

		switch wrapped := err.(type) {
		case database.Error:
			err = wrapped.OrigErr
		case *database.Error:
			err = wrapped.OrigErr
		default:
			unwrapper, ok := err.(interface{ Unwrap() error })
			if !ok {
				return false
			}
			err = unwrapper.Unwrap()
		}
	}
	return false
}

func guardedDownError(from migrationState, to int, cause error, recoveryErr error) error {
	return &GuardedDownError{
		FromVersion:   uint(from.version),
		ToVersion:     to,
		Cause:         cause,
		RecoveryError: recoveryErr,
	}
}

// recoverGuardedDown restores metadata only after the migration driver has
// reported the exact target version in its expected dirty state. The caller
// must hold the migration lock for the whole verification and recovery.
func recoverGuardedDown(store migrationStateStore, original migrationState, target int) error {
	afterVersion, afterDirty, err := store.Version()
	if err != nil {
		return err
	}
	if afterVersion != target || !afterDirty {
		return fmt.Errorf("guarded DOWN left unexpected metadata state version=%d dirty=%t; want version=%d dirty=true", afterVersion, afterDirty, target)
	}
	if err := store.SetVersion(original.version, original.dirty); err != nil {
		return err
	}
	restoredVersion, restoredDirty, err := store.Version()
	if err != nil {
		return err
	}
	if restoredVersion != original.version || restoredDirty != original.dirty {
		return fmt.Errorf("migration metadata recovery mismatch version=%d dirty=%t; want version=%d dirty=%t", restoredVersion, restoredDirty, original.version, original.dirty)
	}
	return nil
}

func (m *migrationHandle) runDownMigrationLocked() (migrationState, int, error) {
	original, err := m.state()
	if err != nil {
		return migrationState{}, 0, err
	}
	if original.dirty {
		return original, original.version, migrate.ErrDirty{Version: original.version}
	}
	if original.version == database.NilVersion {
		return original, original.version, migrate.ErrNoChange
	}

	target, err := m.previousMigrationVersion(original.version)
	if err != nil {
		return original, 0, err
	}
	body, _, err := m.sourceDriver.ReadDown(uint(original.version))
	if err != nil {
		return original, target, err
	}
	defer func() { _ = body.Close() }()

	if err := m.databaseDriver.SetVersion(target, true); err != nil {
		return original, target, err
	}
	if err := m.databaseDriver.Run(body); err != nil {
		return original, target, err
	}
	if err := m.databaseDriver.SetVersion(target, false); err != nil {
		return original, target, err
	}
	return original, target, nil
}

// downOneStep executes one DOWN while holding the same database migration lock
// for state inspection, migration execution, and any guarded recovery.
func handleDownFailure(store migrationStateStore, original migrationState, target int, stepErr error) error {
	if !guardedDownFailure(stepErr) {
		return stepErr
	}
	recoveryErr := recoverGuardedDown(store, original, target)
	return guardedDownError(original, target, stepErr, recoveryErr)
}

func (m *migrationHandle) downOneStep() (err error) {
	if err := m.databaseDriver.Lock(); err != nil {
		return err
	}
	defer func() {
		if unlockErr := m.databaseDriver.Unlock(); unlockErr != nil {
			if err == nil {
				err = unlockErr
			} else {
				err = errors.Join(err, unlockErr)
			}
		}
	}()

	original, target, stepErr := m.runDownMigrationLocked()
	return handleDownFailure(m.databaseDriver, original, target, stepErr)
}

// Bootstrap advances only through the pre-v5 migration policy to clean v5.
// It is the shared entrypoint for server, devseed, CLI, and test bootstrap;
// protected v5->v6 remains owned by the future dedicated A3 runner.
func Bootstrap(databaseURL string) error {
	return Up(databaseURL)
}

// Up is retained as the compatibility API, but is now the same capped,
// fail-closed bootstrap route rather than an unrestricted generic Up.
func Up(databaseURL string) (err error) {
	m, err := newMigratorLocked(context.Background(), databaseURL, migrationGateUp)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := m.close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	// D1 owns only the capped pre-v5 bootstrap route. A future migration
	// beyond v5 belongs to the dedicated A3 runner and must never be crossed
	// by this generic entrypoint.
	if migrateErr := m.Migrate.Migrate(protectedSchemaVersion); migrateErr != nil && !errors.Is(migrateErr, migrate.ErrNoChange) {
		return migrateErr
	}
	state, stateErr := m.state()
	if stateErr != nil {
		return stateErr
	}
	if state.dirty {
		return fmt.Errorf("migration completed with dirty metadata at version=%d", state.version)
	}
	return nil
}

// Down rolls back the most recently applied migration while the same pinned
// session owns the canonical exclusive fence. Destructive or irreversible
// compatibility changes fail closed in their SQL Down file.
func Down(databaseURL string) (err error) {
	m, err := newMigratorLocked(context.Background(), databaseURL, migrationGateDown)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := m.close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	return m.downOneStep()
}

// Version is deliberately implemented without golang-migrate initialization:
// postgres.WithConnection ensures the configured metadata table exists and
// therefore cannot be used by a read-only inspection path on a metadata-free
// database.
func Version(databaseURL string) (uint, bool, error) {
	parsed, err := parsePostgresDatabaseURL(databaseURL)
	if err != nil {
		return 0, false, err
	}
	db, err := sql.Open("postgres", parsed.driverURL)
	if err != nil {
		return 0, false, err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := beginReadOnlyMigrationInspection(ctx, db)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()
	if err := AcquireSharedWriterFence(ctx, tx); err != nil {
		return 0, false, err
	}
	var currentSchema string
	if err := tx.QueryRowContext(ctx, "SELECT current_schema()").Scan(&currentSchema); err != nil {
		return 0, false, err
	}
	schemaName, tableName, err := migrationMetadataIdentifiers(parsed.config, currentSchema)
	if err != nil {
		return 0, false, err
	}
	qualifiedTable := quotedMigrationTable(schemaName, tableName)
	var table sql.NullString
	if err := tx.QueryRowContext(ctx, "SELECT to_regclass($1)", qualifiedTable).Scan(&table); err != nil {
		return 0, false, err
	}
	if !table.Valid || table.String == "" {
		if err := tx.Commit(); err != nil {
			return 0, false, err
		}
		return 0, false, nil
	}
	var version int
	var dirty bool
	if err := tx.QueryRowContext(ctx, "SELECT version, dirty FROM "+qualifiedTable+" LIMIT 1").Scan(&version, &dirty); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			if commitErr := tx.Commit(); commitErr != nil {
				return 0, false, commitErr
			}
			return 0, false, nil
		}
		return 0, false, err
	}
	if err := tx.Commit(); err != nil {
		return 0, false, err
	}
	if version == database.NilVersion {
		return 0, false, nil
	}
	return uint(version), dirty, nil
}
