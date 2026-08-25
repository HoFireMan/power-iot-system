package persistence

import (
	"database/sql"
	"io/fs"
	"os"
	"testing"

	_ "github.com/lib/pq"
	"power-iot-backend/internal/data/migrations"
	"power-iot-backend/internal/testsupport"
)

func migratePersistenceTestDatabase(dsn string) error {
	if err := migrations.Up(dsn); err != nil {
		return err
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	for _, name := range []string{
		"sql/000007_b02_coverage_foundation.up.sql",
		"sql/000008_dashboard_carbon_summary.up.sql",
		"sql/000009_billing_v1_catalog.up.sql",
	} {
		body, err := fs.ReadFile(migrations.Files, name)
		if err != nil {
			return err
		}
		if _, err := db.Exec(string(body)); err != nil {
			return err
		}
	}
	return nil
}

func TestMain(m *testing.M) {
	os.Exit(testsupport.Run(m,
		testsupport.Spec{Environment: "TEST_DATABASE_URL", SourceEnvironment: "TEST_DATABASE_URL", Migrate: migratePersistenceTestDatabase},
	))
}
