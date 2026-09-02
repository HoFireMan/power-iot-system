package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
)

// RunB02Migration is the protected post-V6 schema transition. It deliberately
// does not use the generic golang-migrate Up/Down route.
func RunB02Migration(ctx context.Context, databaseURL string, admission ExternalWriterAdmission) (report ProtectedMigrationReport, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := RequireExternalWriterAdmission(admission); err != nil {
		return report, err
	}
	inspection, err := InspectProtectedMigration(ctx, databaseURL, D5MigrationSpec(admission))
	if err != nil {
		return report, err
	}
	if inspection.State == ProtectedStateCleanB02 || inspection.State == ProtectedStateCleanB02IdentityRequired {
		if err := RunMeasurementPointIdentityMigration(ctx, databaseURL, admission); err != nil {
			return report, err
		}
		if err := RunDashboardCarbonMigration(ctx, databaseURL, admission); err != nil {
			return report, err
		}
		if err := RunBillingV1Migration(ctx, databaseURL, admission); err != nil {
			return report, err
		}
		report.State = ProtectedStateCleanB02
		report.PostCommitState = ProtectedStateCleanB02
		report.Outcome = ProtectedAlreadyComplete
		return report, nil
	}
	if inspection.State != ProtectedStateCleanV6 {
		return report, fmt.Errorf("B-02 requires clean V6 state, got %s", inspection.State)
	}

	fence, err := OpenExclusiveWriterFence(ctx, databaseURL)
	if err != nil {
		return report, err
	}
	defer func() {
		if closeErr := fence.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()
	capability, err := fence.Capability()
	if err != nil {
		return report, err
	}
	if err := RequireProtectedWork(capability); err != nil {
		return report, err
	}
	report.BackendPID = fence.BackendPID()

	metadataTable, err := configuredMetadataTable(ctx, databaseURL, fence.Conn())
	if err != nil {
		return report, err
	}
	mark, err := fence.Conn().BeginTx(ctx, nil)
	if err != nil {
		return report, err
	}
	result, err := mark.ExecContext(ctx, "UPDATE "+metadataTable+" SET dirty=true WHERE version=6 AND dirty=false")
	if err != nil {
		_ = mark.Rollback()
		return report, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		_ = mark.Rollback()
		return report, fmt.Errorf("B-02 dirty marker expected one V6 row, affected %d", count)
	}
	if err := mark.Commit(); err != nil {
		return report, fmt.Errorf("B-02 dirty marker commit: %w", err)
	}

	body, err := fs.ReadFile(Files, "sql/000007_b02_coverage_foundation.up.sql")
	if err != nil {
		return report, err
	}
	apply, err := fence.Conn().BeginTx(ctx, nil)
	if err != nil {
		return report, err
	}
	if _, err := apply.ExecContext(ctx, string(body)); err != nil {
		_ = apply.Rollback()
		return report, fmt.Errorf("B-02 body: %w", err)
	}
	if err := apply.Commit(); err != nil {
		return report, fmt.Errorf("B-02 body commit outcome unknown: %w", err)
	}
	// The dashboard carbon schema is installed under the same protected
	// writer admission for a fresh B-02 database. Existing B-02 databases can
	// apply the standalone 000008 migration through the protected operator.
	carbonBody, err := fs.ReadFile(Files, "sql/000008_dashboard_carbon_summary.up.sql")
	if err != nil {
		return report, err
	}
	carbonTx, err := fence.Conn().BeginTx(ctx, nil)
	if err != nil {
		return report, err
	}
	if _, err := carbonTx.ExecContext(ctx, string(carbonBody)); err != nil {
		_ = carbonTx.Rollback()
		return report, fmt.Errorf("dashboard carbon body: %w", err)
	}
	if err := carbonTx.Commit(); err != nil {
		return report, fmt.Errorf("dashboard carbon body commit outcome unknown: %w", err)
	}
	billingBody, err := fs.ReadFile(Files, "sql/000009_billing_v1_catalog.up.sql")
	if err != nil {
		return report, err
	}
	billingTx, err := fence.Conn().BeginTx(ctx, nil)
	if err != nil {
		return report, err
	}
	if _, err := billingTx.ExecContext(ctx, string(billingBody)); err != nil {
		_ = billingTx.Rollback()
		return report, fmt.Errorf("billing V1 body: %w", err)
	}
	if err := billingTx.Commit(); err != nil {
		return report, fmt.Errorf("billing V1 body commit outcome unknown: %w", err)
	}
	identityBody, err := fs.ReadFile(Files, "sql/000010_measurement_point_identity.up.sql")
	if err != nil {
		return report, err
	}
	identityTx, err := fence.Conn().BeginTx(ctx, nil)
	if err != nil {
		return report, err
	}
	if _, err := identityTx.ExecContext(ctx, string(identityBody)); err != nil {
		_ = identityTx.Rollback()
		return report, fmt.Errorf("measurement point identity body: %w", err)
	}
	if err := identityTx.Commit(); err != nil {
		return report, fmt.Errorf("measurement point identity body commit outcome unknown: %w", err)
	}

	final, err := fence.Conn().BeginTx(ctx, nil)
	if err != nil {
		return report, err
	}
	result, err = final.ExecContext(ctx, "UPDATE "+metadataTable+" SET version=7, dirty=false WHERE version=6 AND dirty=true")
	if err != nil {
		_ = final.Rollback()
		return report, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		_ = final.Rollback()
		return report, fmt.Errorf("B-02 final marker expected one dirty V6 row, affected %d", count)
	}
	if err := final.Commit(); err != nil {
		return report, fmt.Errorf("B-02 final marker commit outcome unknown: %w", err)
	}
	if err := verifyB02Catalog(ctx, fence.Conn()); err != nil {
		return report, err
	}
	report.State = ProtectedStateCleanV6
	report.PostCommitState = ProtectedStateCleanB02
	report.PostCommitCatalog = ProtectedCatalogExactV6
	report.Outcome = ProtectedCommittedAndVerified
	report.Committed = true
	report.PostCommitVerified = true
	return report, nil
}

// RunDashboardCarbonMigration applies the standalone 000008 feature schema to
// an already clean B-02 database. It is deliberately protected rather than
// reachable through generic migrations.Up, because this repository reserves
// post-V5 schema changes for an externally admitted writer-drain operation.
func RunDashboardCarbonMigration(ctx context.Context, databaseURL string, admission ExternalWriterAdmission) (err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := RequireExternalWriterAdmission(admission); err != nil {
		return err
	}
	inspection, err := InspectProtectedMigration(ctx, databaseURL, D5MigrationSpec(admission))
	if err != nil {
		return err
	}
	if inspection.State != ProtectedStateCleanB02 {
		return fmt.Errorf("dashboard carbon migration requires clean B-02, got %s", inspection.State)
	}
	fence, err := OpenExclusiveWriterFence(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := fence.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()
	capability, err := fence.Capability()
	if err != nil {
		return err
	}
	if err := RequireProtectedWork(capability); err != nil {
		return err
	}
	body, err := fs.ReadFile(Files, "sql/000008_dashboard_carbon_summary.up.sql")
	if err != nil {
		return err
	}
	tx, err := fence.Conn().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, string(body)); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("dashboard carbon body: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("dashboard carbon body commit outcome unknown: %w", err)
	}
	return nil
}

// RunBillingV1Migration applies the additive Billing V1 catalog through the
// protected writer-admission path. It is intentionally separate from generic
// migration Up and performs no Shop backfill.
func RunBillingV1Migration(ctx context.Context, databaseURL string, admission ExternalWriterAdmission) (err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := RequireExternalWriterAdmission(admission); err != nil {
		return err
	}
	inspection, err := InspectProtectedMigration(ctx, databaseURL, D5MigrationSpec(admission))
	if err != nil {
		return err
	}
	if inspection.State != ProtectedStateCleanB02 {
		return fmt.Errorf("billing V1 migration requires clean B-02, got %s", inspection.State)
	}
	fence, err := OpenExclusiveWriterFence(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := fence.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()
	capability, err := fence.Capability()
	if err != nil {
		return err
	}
	if err := RequireProtectedWork(capability); err != nil {
		return err
	}
	body, err := fs.ReadFile(Files, "sql/000009_billing_v1_catalog.up.sql")
	if err != nil {
		return err
	}
	tx, err := fence.Conn().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, string(body)); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("billing V1 body: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("billing V1 body commit outcome unknown: %w", err)
	}
	return nil
}

func configuredMetadataTable(ctx context.Context, databaseURL string, conn *sql.Conn) (string, error) {
	parsed, err := parsePostgresDatabaseURL(databaseURL)
	if err != nil {
		return "", err
	}
	var schema string
	if err := conn.QueryRowContext(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
		return "", err
	}
	metadataSchema, metadataTable, err := migrationMetadataIdentifiers(parsed.config, schema)
	if err != nil {
		return "", err
	}
	return quotedMigrationTable(metadataSchema, metadataTable), nil
}
