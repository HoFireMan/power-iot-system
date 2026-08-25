package migrations

import (
	"context"
	"errors"
	"fmt"
)

var ErrTrustedDrainAdmissionMissing = errors.New("trusted external drain admission is required")

// TrustedDrainAdmissionCheck is the narrow handoff from the operator/runtime
// controls. Its implementation owns process, ingress, broker, direct-writer,
// and in-flight evidence. D6 receives only success/failure; it does not turn
// summary fields into authority.
type TrustedDrainAdmissionCheck func(context.Context) error

// RunD6ProtectedMigrationOperator is the production-shaped operator seam. It
// performs no generic migration and delegates every migration disposition to
// the accepted D5 runner. The callback is the trusted external admission
// boundary; D6 cannot construct the private proof directly.
func RunD6ProtectedMigrationOperator(ctx context.Context, databaseURL string, check TrustedDrainAdmissionCheck) (ProtectedMigrationReport, error) {
	if check == nil {
		return ProtectedMigrationReport{}, ErrTrustedDrainAdmissionMissing
	}
	if err := check(ctx); err != nil {
		return ProtectedMigrationReport{}, fmt.Errorf("external writer admission failed: %w", err)
	}
	return runD5MigrationOperator(ctx, databaseURL, trustedExternalWriterAdmission())
}

// RunB02ProtectedMigrationOperator is the LOCAL/REHEARSAL B-02 operator seam.
// The trusted drain callback is the only operational authority accepted here;
// the wrapper supplies the package-private proof and delegates migration
// semantics to the authoritative B-02 runner.
func RunB02ProtectedMigrationOperator(ctx context.Context, databaseURL string, check TrustedDrainAdmissionCheck) (ProtectedMigrationReport, error) {
	if check == nil {
		return ProtectedMigrationReport{}, ErrTrustedDrainAdmissionMissing
	}
	if err := check(ctx); err != nil {
		return ProtectedMigrationReport{}, fmt.Errorf("external writer admission failed: %w", err)
	}
	return runB02MigrationOperator(ctx, databaseURL, trustedExternalWriterAdmission())
}

// Kept as package seams so the operator wrappers can be tested for exact
// report/error propagation without replacing protected migration authority.
var runD5MigrationOperator = RunD5Migration
var runB02MigrationOperator = RunB02Migration

func trustedExternalWriterAdmission() ExternalWriterAdmission {
	return ExternalWriterAdmission{
		ManagedCooperativeWriters: true,
		DirectSQLControlled:       true,
		OperationalDrainEvidence:  true,
		evidence: &externalWriterAdmissionEvidence{
			managedCooperativeWriters: true,
			directSQLControlled:       true,
			operationalDrainEvidence:  true,
		},
	}
}
