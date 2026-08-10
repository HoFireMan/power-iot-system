package migrations

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "github.com/lib/pq"
)

func TestSecuritySchemaWriterFenceDedicatedRoleCannotSupportRoleAdmission(t *testing.T) {
	dsn := os.Getenv("TEST_MIGRATION_DATABASE_URL")
	if dsn == "" {
		t.Fatal("TEST_MIGRATION_DATABASE_URL is required for writer-fence runtime evidence")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var role string
	var superuser bool
	if err := db.QueryRowContext(context.Background(), `
		SELECT current_user, rolsuper
		FROM pg_roles
		WHERE rolname = current_user`).Scan(&role, &superuser); err != nil {
		t.Fatal(err)
	}
	if !superuser {
		t.Fatalf("dedicated runtime role=%q is not the observed superuser boundary", role)
	}

	decision := AssessSecuritySchemaWriterFence()
	if decision.Status != WriterFenceDecisionRequired || decision.ProtectedWorkAllowed {
		t.Fatalf("role-admission evidence did not fail closed: role=%q decision=%+v", role, decision)
	}
}
