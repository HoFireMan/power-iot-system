package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
)

// D5MigrationSpec returns the D5-owned protected transition specification.
// External writer admission must be produced by the trusted D1-L/operator
// seam; public summary booleans are never sufficient.
func D5MigrationSpec(admission ExternalWriterAdmission) ProtectedMigrationSpec {
	return ProtectedMigrationSpec{
		ExternalWriterAdmission: admission,
		V6CatalogTables:         append([]string(nil), protectedV6CatalogTables...),
		Apply:                   applyD5Migration,
		V5SemanticVerifier:      verifyD5V5Preconditions,
		V6SemanticVerifier:      verifyD5V6Semantics,
	}
}

// RunD5Migration is the sole public D5 V5->V6 entrypoint. It deliberately
// delegates fence, advisory-lock, marker, commit-unknown, and recovery policy
// to the existing protected runner.
func RunD5Migration(ctx context.Context, databaseURL string, admission ExternalWriterAdmission) (ProtectedMigrationReport, error) {
	preflight, err := RunSecuritySchemaPreflightV5(ctx, databaseURL)
	if err != nil {
		return ProtectedMigrationReport{}, fmt.Errorf("D5 V5 preflight: %w", err)
	}
	if preflight.Disposition() == PreflightBlockingIntegrity || preflight.AuditProvenance.UnresolvedCount != 0 || preflight.OperationProvenance.UnresolvedCount != 0 {
		return ProtectedMigrationReport{}, fmt.Errorf("D5 V5 preflight rejected existing data: disposition=%s audit_unresolved=%d operation_unresolved=%d", preflight.Disposition(), preflight.AuditProvenance.UnresolvedCount, preflight.OperationProvenance.UnresolvedCount)
	}
	return RunProtectedMigration(ctx, databaseURL, D5MigrationSpec(admission))
}

func applyD5Migration(ctx context.Context, tx *sql.Tx) error {
	body, err := fs.ReadFile(Files, "sql/000006_d4_reconciliation.up.sql")
	if err != nil {
		return fmt.Errorf("read D5 migration body: %w", err)
	}
	if _, err := tx.ExecContext(ctx, string(body)); err != nil {
		return fmt.Errorf("apply D5 migration body: %w", err)
	}
	return nil
}

func verifyD5V5Preconditions(ctx context.Context, q ProtectedMigrationQueryer) error {
	for _, column := range []struct{ table, name string }{
		{"shops", "client_id"},
		{"devices", "inventory_owner_client_id"},
		{"admin_binding_operations", "client_id"},
		{"admin_binding_audits", "client_id"},
	} {
		var nulls int64
		query := fmt.Sprintf("SELECT count(*) FROM %s WHERE %s IS NULL", quoteIdentifier(column.table), quoteIdentifier(column.name))
		if err := q.QueryRowContext(ctx, query).Scan(&nulls); err != nil {
			return fmt.Errorf("D5 preflight %s.%s: %w", column.table, column.name, err)
		}
		if nulls != 0 {
			return fmt.Errorf("D5 preflight %s.%s has %d unresolved values", column.table, column.name, nulls)
		}
	}
	return nil
}

func verifyD5V6Semantics(ctx context.Context, q ProtectedMigrationQueryer) error {
	if err := verifyD5Catalog(ctx, q, "public"); err != nil {
		return err
	}
	return verifyD5V5Preconditions(ctx, q)
}

func quoteIdentifier(value string) string {
	if value == "" || value[0] == '"' {
		return value
	}
	return `"` + value + `"`
}
