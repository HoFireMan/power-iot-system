package main

import (
	"database/sql"
	"io/fs"
	"os"
	"testing"

	"power-iot-backend/internal/data/private_migrations"
	"power-iot-backend/internal/testsupport"
)

func TestMain(m *testing.M) {
	os.Exit(testsupport.Run(m,
		testsupport.Spec{Environment: "TEST_DATABASE_URL", SourceEnvironment: "TEST_DATABASE_URL", Migrate: migrateDevseedSchema},
	))
}

func migrateDevseedSchema(databaseURL string) error {
	if err := migrations.Up(databaseURL); err != nil {
		return err
	}
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	body, err := fs.ReadFile(migrations.Files, "sql/000012_device_retirement_lifecycle.up.sql")
	if err != nil {
		return err
	}
	_, err = db.Exec(string(body))
	return err
}
