package migrations

import (
	"embed"
	"errors"
	"fmt"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// Files contains the versioned SQL migrations. SQL is the schema source of
// truth; GORM models are persistence representations only.
//
//go:embed sql/*.sql
var Files embed.FS

func newMigrator(databaseURL string) (*migrate.Migrate, error) {
	source, err := iofs.New(Files, "sql")
	if err != nil {
		return nil, err
	}
	return migrate.NewWithSourceInstance("iofs", source, databaseURL)
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

	err = m.Steps(-1)
	if err == nil || !strings.Contains(err.Error(), "cannot rollback admin binding persistence while audit or operation rows exist") {
		return err
	}

	// golang-migrate marks a failed step dirty before invoking the driver. The
	// guarded 000004 DOWN is an expected refusal, and its single implicit
	// PostgreSQL transaction guarantees that both tables remain intact, so
	// restore the truthful version marker while preserving the original refusal
	// for the caller.
	version, dirty, versionErr := m.Version()
	if versionErr != nil || !dirty || version != 3 {
		return err
	}
	if forceErr := m.Force(4); forceErr != nil {
		return fmt.Errorf("rollback refusal left migration version dirty: %w (original: %v)", forceErr, err)
	}
	return err
}

// Version returns the current migration version and dirty state.
func Version(databaseURL string) (uint, bool, error) {
	m, err := newMigrator(databaseURL)
	if err != nil {
		return 0, false, err
	}
	defer func() { _, _ = m.Close() }()
	version, dirty, err := m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return 0, false, nil
	}
	return version, dirty, err
}
