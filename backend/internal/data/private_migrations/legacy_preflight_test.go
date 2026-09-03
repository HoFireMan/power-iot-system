package migrations

import (
	"errors"
	"testing"
)

func TestLegacyDataPreflightDispositionMatrix(t *testing.T) {
	base := LegacyDataPreflightResult{
		Migration:   MigrationStateReport{ExpectedVersion: 4, ActualVersion: 4, MetadataRowCount: 1},
		WriterFence: WriterFenceRequiresMigrationOrchestration,
	}

	tests := []struct {
		name   string
		mutate func(*LegacyDataPreflightResult)
		want   PreflightStatus
	}{
		{
			name: "clean facts still require external writer orchestration",
			want: PreflightReconciliationRequired,
		},
		{
			name: "null shop client blocks",
			mutate: func(result *LegacyDataPreflightResult) {
				result.ShopClient.NullClientIDCount = 1
			},
			want: PreflightBlockingIntegrity,
		},
		{
			name: "orphan membership requires reconciliation",
			mutate: func(result *LegacyDataPreflightResult) {
				result.Membership.OrphanUserCount = 1
			},
			want: PreflightBlockingIntegrity,
		},
		{
			name: "unassigned device needs manual mapping",
			mutate: func(result *LegacyDataPreflightResult) {
				result.Devices.ManualMappingRequiredCount = 1
			},
			want: PreflightReconciliationRequired,
		},
		{
			name: "ambiguous provenance remains unresolved",
			mutate: func(result *LegacyDataPreflightResult) {
				result.AuditProvenance.UnresolvedCount = 1
			},
			want: PreflightReconciliationRequired,
		},
		{
			name: "dirty migration blocks",
			mutate: func(result *LegacyDataPreflightResult) {
				result.Migration.Dirty = true
			},
			want: PreflightBlockingIntegrity,
		},
		{
			name: "unexpected migration version blocks",
			mutate: func(result *LegacyDataPreflightResult) {
				result.Migration.ActualVersion = 3
			},
			want: PreflightBlockingIntegrity,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := base
			if test.mutate != nil {
				test.mutate(&result)
			}
			if got := result.Disposition(); got != test.want {
				t.Fatalf("Disposition()=%q, want %q", got, test.want)
			}
		})
	}
}

func TestLegacyDataPreflightDoesNotApproveMissingAuthorities(t *testing.T) {
	result := LegacyDataPreflightResult{}
	if result.AccountEligibility != RepresentationNotRepresented {
		t.Fatalf("account eligibility=%q, want %q", result.AccountEligibility, RepresentationNotRepresented)
	}
	if result.DeviceOwnerAuthority != RepresentationNotRepresented {
		t.Fatalf("device owner authority=%q, want %q", result.DeviceOwnerAuthority, RepresentationNotRepresented)
	}
	if result.WriterFence != WriterFenceRequiresMigrationOrchestration {
		t.Fatalf("writer fence=%q, want %q", result.WriterFence, WriterFenceRequiresMigrationOrchestration)
	}
}

func TestLegacyDataPreflightMigrationStateErrors(t *testing.T) {
	if !errors.Is(classifyMigrationMetadata([]migrationMetadata{{version: 4, dirty: true}}, 4), ErrDirtyMigrationState) {
		t.Fatal("dirty migration state was not classified as an error")
	}
	if !errors.Is(classifyMigrationMetadata([]migrationMetadata{{version: 3, dirty: false}}, 4), ErrUnexpectedMigrationVersion) {
		t.Fatal("unexpected migration version was not classified as an error")
	}
	if !errors.Is(classifyMigrationMetadata([]migrationMetadata{{version: 4, dirty: false}, {version: 5, dirty: true}}, 4), ErrMigrationMetadataCardinality) {
		t.Fatal("multiple migration metadata rows were not classified as an error")
	}
}

func TestLegacyDataPreflightClassificationsDoNotApproveLegacySignals(t *testing.T) {
	result := LegacyDataPreflightResult{
		AccountEligibility:   RepresentationNotRepresented,
		DeviceOwnerAuthority: RepresentationNotRepresented,
		WriterFence:          WriterFenceRequiresMigrationOrchestration,
		Users: UserEligibilityReport{Facts: []UserEligibilityFact{{
			ID:                  1,
			PasswordHashPresent: true,
			IsAdmin:             true,
			AutoApproved:        false,
			ReviewRequired:      true,
		}}},
	}
	if result.Users.Facts[0].AutoApproved {
		t.Fatal("legacy account facts must never auto-approve authentication")
	}
	if result.DeviceOwnerAuthority != RepresentationNotRepresented {
		t.Fatal("legacy device owner authority must remain unrepresented")
	}
}
