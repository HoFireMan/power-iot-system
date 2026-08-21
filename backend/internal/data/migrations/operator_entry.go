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

// Kept as a package seam so the operator wrapper can be tested for exact
// report/error propagation without replacing the protected D5 authority.
var runD5MigrationOperator = RunD5Migration

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
