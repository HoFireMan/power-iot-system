package migrations

import (
	"context"
	"fmt"
	"io/fs"
)

// RunMeasurementPointAlertsMigration installs the MP-centered alert policy
// and lifecycle tables under the protected writer-admission boundary.
func RunMeasurementPointAlertsMigration(ctx context.Context, databaseURL string, admission ExternalWriterAdmission) (err error) {
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
		return fmt.Errorf("measurement point alerts migration requires clean B-02, got %s", inspection.State)
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
	body, err := fs.ReadFile(Files, "sql/000011_measurement_point_alerts.up.sql")
	if err != nil {
		return err
	}
	tx, err := fence.Conn().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, string(body)); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("measurement point alerts body: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("measurement point alerts body commit outcome unknown: %w", err)
	}
	return nil
}
