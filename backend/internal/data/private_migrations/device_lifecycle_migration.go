package migrations

import (
	"context"
	"fmt"
	"io/fs"
)

// RunDeviceLifecycleMigration applies the lifecycle column to an already clean
// B-02 application schema under the existing protected writer admission. It is
// idempotent and does not alter the Admin Binding audit contract.
func RunDeviceLifecycleMigration(ctx context.Context, databaseURL string, admission ExternalWriterAdmission) (err error) {
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
		return fmt.Errorf("device lifecycle migration requires clean B-02, got %s", inspection.State)
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
	body, err := fs.ReadFile(Files, "sql/000012_device_retirement_lifecycle.up.sql")
	if err != nil {
		return err
	}
	tx, err := fence.Conn().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, string(body)); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("device lifecycle body: %w", err)
	}
	if err := verifyDeviceLifecycleCatalog(ctx, tx); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("device lifecycle verification: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("device lifecycle body commit outcome unknown: %w", err)
	}
	return nil
}
