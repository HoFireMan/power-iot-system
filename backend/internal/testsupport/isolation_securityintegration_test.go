//go:build securityintegration

package testsupport

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"testing"

	"power-iot-backend/internal/data/migrations"

	_ "github.com/lib/pq"
)

// TestConcurrentDatabasesProveIsolation is an executable proof that setup can
// run concurrently and that migration metadata, catalog, and writes do not
// cross database boundaries.
func TestConcurrentDatabasesProveIsolation(t *testing.T) {
	source := os.Getenv("TEST_DATABASE_URL")
	if source == "" {
		source = os.Getenv("TEST_MIGRATION_DATABASE_URL")
	}
	if source == "" {
		t.Fatal("dedicated PostgreSQL source DSN is not set")
	}

	ctx := context.Background()
	results := make(chan *Database, 2)
	errors := make(chan error, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			database, err := New(ctx, source, migrations.Up)
			if err != nil {
				errors <- err
				return
			}
			results <- database
		}()
	}
	group.Wait()
	close(results)
	close(errors)
	var setupErr error
	for err := range errors {
		if setupErr == nil {
			setupErr = err
		}
	}
	var databases []*Database
	for database := range results {
		databases = append(databases, database)
	}
	if setupErr != nil {
		for _, database := range databases {
			_ = database.Close()
		}
		t.Fatal(setupErr)
	}
	if len(databases) != 2 {
		t.Fatalf("created %d isolated databases, want 2", len(databases))
	}
	defer func() {
		for _, database := range databases {
			if err := database.Close(); err != nil {
				t.Error(err)
			}
		}
	}()
	if databases[0].Name() == databases[1].Name() {
		t.Fatal("concurrent setup generated duplicate database names")
	}

	connections := make([]*sql.DB, 2)
	defer func() {
		for _, connection := range connections {
			if connection != nil {
				_ = connection.Close()
			}
		}
	}()
	for index, database := range databases {
		connection, err := sql.Open("postgres", database.DSN())
		if err != nil {
			t.Fatal(err)
		}
		connections[index] = connection
		var current string
		if err := connection.QueryRowContext(ctx, "SELECT current_database()").Scan(&current); err != nil {
			t.Fatal(err)
		}
		if current != database.Name() {
			t.Fatalf("current_database=%q, want generated database", current)
		}
		if _, err := connection.ExecContext(ctx, "CREATE TABLE isolation_probe (writer text NOT NULL)"); err != nil {
			t.Fatal(err)
		}
		if _, err := connection.ExecContext(ctx, "INSERT INTO isolation_probe(writer) VALUES ($1)", database.Name()); err != nil {
			t.Fatal(err)
		}
		var metadata bool
		if err := connection.QueryRowContext(ctx, "SELECT to_regclass('public.schema_migrations') IS NOT NULL").Scan(&metadata); err != nil {
			t.Fatal(err)
		}
		if !metadata {
			t.Fatal("migration metadata catalog is missing")
		}
	}

	// The same advisory key can be owned concurrently because advisory locks
	// are scoped to each isolated database, not shared across databases.
	fences := make(chan error, len(databases))
	for _, database := range databases {
		go func(database *Database) {
			fence, err := migrations.OpenExclusiveWriterFence(ctx, database.DSN())
			if err != nil {
				fences <- err
				return
			}
			fences <- fence.Close()
		}(database)
	}
	for range databases {
		if err := <-fences; err != nil {
			t.Fatal(err)
		}
	}

	for index, connection := range connections {
		var count int
		if err := connection.QueryRowContext(ctx, "SELECT count(*) FROM isolation_probe WHERE writer = $1", databases[index].Name()).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("database %q has %d own probe rows, want 1", databases[index].Name(), count)
		}
		var otherCount int
		if err := connection.QueryRowContext(ctx, "SELECT count(*) FROM isolation_probe WHERE writer = $1", databases[1-index].Name()).Scan(&otherCount); err != nil {
			t.Fatal(err)
		}
		if otherCount != 0 {
			t.Fatalf("database %q observed writer data from other database", databases[index].Name())
		}
	}
	if _, err := connections[0].ExecContext(ctx, "TRUNCATE isolation_probe"); err != nil {
		t.Fatal(err)
	}
	var otherStillPresent int
	if err := connections[1].QueryRowContext(ctx, "SELECT count(*) FROM isolation_probe").Scan(&otherStillPresent); err != nil {
		t.Fatal(err)
	}
	if otherStillPresent != 1 {
		t.Fatalf("reset in %q changed isolated database %q: rows=%d", databases[0].Name(), databases[1].Name(), otherStillPresent)
	}
	for _, connection := range connections {
		if err := connection.Close(); err != nil {
			t.Fatal(err)
		}
	}
	for _, database := range databases {
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
		if exists, err := databaseExists(ctx, source, database.Name()); err != nil {
			t.Fatal(err)
		} else if exists {
			t.Fatalf("cleaned database %q still exists", database.Name())
		}
	}
}

func databaseExists(ctx context.Context, sourceDSN, name string) (bool, error) {
	parsed, err := validateSource(sourceDSN)
	if err != nil {
		return false, err
	}
	parsed.Path = "/postgres"
	parsed.RawPath = ""
	admin, err := sql.Open("postgres", parsed.String())
	if err != nil {
		return false, err
	}
	defer admin.Close()
	var exists bool
	err = admin.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&exists)
	return exists, err
}
