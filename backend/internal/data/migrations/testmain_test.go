package migrations

import (
	"os"
	"testing"

	"power-iot-backend/internal/testsupport"
)

func TestMain(m *testing.M) {
	os.Exit(testsupport.Run(m,
		testsupport.Spec{Environment: "TEST_DATABASE_URL", SourceEnvironment: "TEST_DATABASE_URL", Migrate: Up},
		testsupport.Spec{Environment: "TEST_MIGRATION_DATABASE_URL", SourceEnvironment: "TEST_MIGRATION_DATABASE_URL", Migrate: Up},
	))
}
