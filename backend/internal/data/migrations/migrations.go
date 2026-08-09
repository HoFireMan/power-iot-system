package migrations

import (
	"embed"
	"errors"
	"fmt"
	"os"

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

func newMigrator(databaseURL string) (*migrationHandle, error) {
	sourceDriver, err := iofs.New(Files, "sql")
	if err != nil {
		return nil, err
	}

	databaseDriver, err := (&postgres.Postgres{}).Open(databaseURL)
	if err != nil {
		_ = sourceDriver.Close()
		return nil, err
	}
	m, err := migrate.NewWithInstance("iofs", sourceDriver, "postgres", databaseDriver)
	if err != nil {
		_ = databaseDriver.Close()
		_ = sourceDriver.Close()
		return nil, err
	}
	return &migrationHandle{
		Migrate:        m,
		sourceDriver:   sourceDriver,
		databaseDriver: databaseDriver,
	}, nil
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

// Up applies every pending migration. ErrNoChange is a successful no-op.
func Up(databaseURL string) error {
	m, err := newMigrator(databaseURL)
	if err != nil {
		return err
	}
	defer func() { _, _ = m.Close() }()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

// Down rolls back the most recently applied migration. Destructive or
// irreversible compatibility changes fail closed in their SQL Down file.
func Down(databaseURL string) error {
	m, err := newMigrator(databaseURL)
	if err != nil {
		return err
	}
	defer func() { _, _ = m.Close() }()
	return m.downOneStep()
}

// Version returns the current migration version and dirty state.
func Version(databaseURL string) (uint, bool, error) {
	m, err := newMigrator(databaseURL)
	if err != nil {
		return 0, false, err
	}
	defer func() { _, _ = m.Close() }()
	state, err := m.state()
	if err != nil {
		return 0, false, err
	}
	if state.version == database.NilVersion {
		return 0, false, nil
	}
	return uint(state.version), state.dirty, nil
}
