package migrations

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
)

func TestBootstrapRejectsDirtyMetadataBeforeGenericMigration(t *testing.T) {
	dsn := os.Getenv("TEST_MIGRATION_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_MIGRATION_DATABASE_URL is required")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := Up(dsn); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE schema_migrations SET dirty = true WHERE version = 5"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, restoreErr := db.ExecContext(ctx, "UPDATE schema_migrations SET dirty = false WHERE version = 5"); restoreErr != nil {
			t.Errorf("restore clean metadata: %v", restoreErr)
		}
	}()

	err = Up(dsn)
	if !errors.Is(err, ErrMigrationGateDirty) {
		t.Fatalf("Up error=%v, want dirty admission refusal", err)
	}
	var version int
	var dirty bool
	if err := db.QueryRowContext(ctx, "SELECT version, dirty FROM schema_migrations").Scan(&version, &dirty); err != nil {
		t.Fatal(err)
	}
	if version != 5 || !dirty {
		t.Fatalf("dirty metadata changed after refusal: version=%d dirty=%t", version, dirty)
	}
}

func TestBootstrapKeepsCleanV5AtProtectedBoundary(t *testing.T) {
	dsn := os.Getenv("TEST_MIGRATION_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_MIGRATION_DATABASE_URL is required")
	}
	if err := Up(dsn); err != nil {
		t.Fatal(err)
	}
	var version int
	var dirty bool
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.QueryRow("SELECT version, dirty FROM schema_migrations").Scan(&version, &dirty); err != nil {
		t.Fatal(err)
	}
	if version != protectedSchemaVersion || dirty {
		t.Fatalf("bootstrap crossed or dirtied protected boundary: version=%d dirty=%t", version, dirty)
	}
}
