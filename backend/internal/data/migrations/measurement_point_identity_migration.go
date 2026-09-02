package migrations

import (
	"context"
	"fmt"
	"io/fs"
)

// RunMeasurementPointIdentityMigration applies IDENT-002 through the
// protected post-B-02 writer-admission boundary. It is deliberately separate
// from generic migration Up/Down and is safe to replay after a clean install.
func RunMeasurementPointIdentityMigration(ctx context.Context, databaseURL string, admission ExternalWriterAdmission) (err error) {
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
	if inspection.State != ProtectedStateCleanB02 && inspection.State != ProtectedStateCleanB02IdentityRequired {
		return fmt.Errorf("measurement point identity migration requires clean B-02 or identity repair state, got %s", inspection.State)
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
	body, err := fs.ReadFile(Files, "sql/000010_measurement_point_identity.up.sql")
	if err != nil {
		return err
	}
	tx, err := fence.Conn().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, string(body)); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("measurement point identity body: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("measurement point identity body commit outcome unknown: %w", err)
	}
	return nil
}
