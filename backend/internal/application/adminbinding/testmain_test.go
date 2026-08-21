package adminbinding

import (
	"os"
	"testing"

	"power-iot-backend/internal/data/migrations"
	"power-iot-backend/internal/testsupport"
)

func TestMain(m *testing.M) {
	os.Exit(testsupport.Run(m,
		testsupport.Spec{Environment: "TEST_DATABASE_URL", SourceEnvironment: "TEST_DATABASE_URL", Migrate: migrations.Up},
	))
}
