package iot

import (
	"database/sql"
	"io/fs"
	"os"
	"testing"

	"power-iot-backend/internal/data/migrations"
	"power-iot-backend/internal/testsupport"
)

func TestMain(m *testing.M) {
	os.Exit(testsupport.Run(m,
		testsupport.Spec{Environment: "TEST_DATABASE_URL", SourceEnvironment: "TEST_DATABASE_URL", Migrate: migrateB02TestSchema},
	))
}

func ensureAlertsSchema(databaseURL string) error {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	for _, name := range []string{"sql/000010_measurement_point_identity.up.sql", "sql/000011_measurement_point_alerts.up.sql"} {
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

func migrateB02TestSchema(databaseURL string) error {
	if err := migrations.Up(databaseURL); err != nil {
		return err
	}
	body, err := fs.ReadFile(migrations.Files, "sql/000007_b02_coverage_foundation.up.sql")
	if err != nil {
		return err
	}
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(string(body))
	return err
}
