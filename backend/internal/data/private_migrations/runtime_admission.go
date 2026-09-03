package migrations

import (
	"context"
	"errors"
	"fmt"
)

// RuntimeAdmissionDisposition is the closed-world decision consumed by the
// backend process before it starts serving. It is a deployment decision, not
// a migration or D5 authority.
type RuntimeAdmissionDisposition string

const (
	RuntimeServeV6                  RuntimeAdmissionDisposition = "SERVE_CLEAN_V6"
	RuntimeServeB02                 RuntimeAdmissionDisposition = "SERVE_CLEAN_B02"
	RuntimeProtectedMigrationNeeded RuntimeAdmissionDisposition = "PROTECTED_MIGRATION_REQUIRED"
)

var (
	ErrRuntimeProtectedMigrationRequired = errors.New("backend startup requires the protected D6 migration entry")
	ErrRuntimeAdmissionRefused           = errors.New("backend startup refused by closed-world admission")
)

// RuntimeAdmissionReport contains only non-secret startup evidence.
type RuntimeAdmissionReport struct {
	Disposition RuntimeAdmissionDisposition
	Version     uint
	Dirty       bool
	State       ProtectedMigrationState
	Catalog     ProtectedCatalogState
	Metadata    MigrationMetadataSnapshot
}

// ClassifyRuntimeAdmission is the pure closed-world startup matrix. The
// backend may serve only an exact clean V6 or B-02 observation. Clean V5 is an
// explicit handoff to the protected operator entry; every other state fails
// closed.
func ClassifyRuntimeAdmission(state ProtectedMigrationState) (RuntimeAdmissionDisposition, error) {
	switch state {
	case ProtectedStateCleanV6:
		return RuntimeServeV6, nil
	case ProtectedStateCleanB02:
		return RuntimeServeB02, nil
	case ProtectedStateCleanV5:
		return RuntimeProtectedMigrationNeeded, ErrRuntimeProtectedMigrationRequired
	default:
		return "", fmt.Errorf("%w: state=%s", ErrRuntimeAdmissionRefused, state)
	}
}

// BootstrapAndAdmit performs the only backend startup admission sequence.
// Generic bootstrap is invoked only for metadata versions below V5 (or an
// empty recognized database); it is never invoked against V6 and therefore
// cannot silently downgrade or replay the protected migration.
func BootstrapAndAdmit(ctx context.Context, databaseURL string) (RuntimeAdmissionReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	version, dirty, err := Version(databaseURL)
	if err != nil {
		return RuntimeAdmissionReport{}, fmt.Errorf("inspect startup migration metadata: %w", err)
	}
	if dirty {
		return RuntimeAdmissionReport{Version: version, Dirty: true}, fmt.Errorf("%w: dirty metadata version=%d", ErrRuntimeAdmissionRefused, version)
	}
	if version < protectedSchemaVersion {
		if err := Bootstrap(databaseURL); err != nil {
			return RuntimeAdmissionReport{Version: version}, fmt.Errorf("generic bootstrap to clean V5 failed: %w", err)
		}
		version, dirty, err = Version(databaseURL)
		if err != nil {
			return RuntimeAdmissionReport{}, fmt.Errorf("reinspect post-bootstrap metadata: %w", err)
		}
		if dirty {
			return RuntimeAdmissionReport{Version: version, Dirty: true}, fmt.Errorf("%w: bootstrap left dirty metadata version=%d", ErrRuntimeAdmissionRefused, version)
		}
	}
	if version > protectedSchemaVersion+2 {
		return RuntimeAdmissionReport{Version: version}, fmt.Errorf("%w: future metadata version=%d", ErrRuntimeAdmissionRefused, version)
	}

	inspection, err := InspectProtectedMigration(ctx, databaseURL, D5MigrationSpec(ExternalWriterAdmission{}))
	if err != nil {
		return RuntimeAdmissionReport{Version: version}, fmt.Errorf("inspect protected startup state: %w", err)
	}
	report := RuntimeAdmissionReport{
		Version: version, Dirty: dirty, State: inspection.State, Catalog: inspection.Catalog,
		Metadata: inspection.Metadata,
	}
	disposition, classifyErr := ClassifyRuntimeAdmission(inspection.State)
	report.Disposition = disposition
	if classifyErr != nil {
		return report, classifyErr
	}
	return report, nil
}
