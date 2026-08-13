package migrations

import (
	"errors"
	"testing"
)

func TestClassifyMigrationAdmission(t *testing.T) {
	clean := func(version int) MigrationMetadataSnapshot {
		return MigrationMetadataSnapshot{Exists: true, RowCount: 1, Version: version, HasVersion: true}
	}
	tests := []struct {
		name    string
		snap    MigrationMetadataSnapshot
		action  migrationGateAction
		latest  int
		wantErr error
	}{
		{name: "absent bootstrap", snap: MigrationMetadataSnapshot{CatalogEmpty: true}, action: migrationGateUp},
		{name: "empty relation bootstrap", snap: MigrationMetadataSnapshot{Exists: true, CatalogEmpty: true}, action: migrationGateUp},
		{name: "clean v4 bootstrap", snap: func() MigrationMetadataSnapshot { s := clean(4); s.CatalogVersion = 4; return s }(), action: migrationGateUp},
		{name: "zero metadata unknown catalog", snap: func() MigrationMetadataSnapshot { s := clean(0); s.CatalogVersion = -1; return s }(), action: migrationGateUp, wantErr: ErrMigrationGateAmbiguous},
		{name: "clean v5 current bootstrap no-op", snap: func() MigrationMetadataSnapshot { s := clean(5); s.CatalogVersion = 5; return s }(), action: migrationGateUp, latest: 5},
		{name: "partial v5 catalog", snap: clean(5), action: migrationGateUp, latest: 5, wantErr: ErrMigrationGateAmbiguous},
		{name: "clean v5 protected future upgrade", snap: func() MigrationMetadataSnapshot { s := clean(5); s.CatalogVersion = 5; return s }(), action: migrationGateUp, latest: 6, wantErr: ErrProtectedV5Upgrade},
		{name: "dirty v5", snap: MigrationMetadataSnapshot{Exists: true, RowCount: 1, Version: 5, Dirty: true, HasVersion: true}, action: migrationGateUp, wantErr: ErrMigrationGateDirty},
		{name: "duplicate metadata", snap: MigrationMetadataSnapshot{Exists: true, RowCount: 2, HasVersion: true}, action: migrationGateUp, wantErr: ErrMigrationGateAmbiguous},
		{name: "clean v6 up", snap: clean(6), action: migrationGateUp, latest: 6, wantErr: ErrMigrationGateFuture},
		{name: "clean v6 down", snap: clean(6), action: migrationGateDown, latest: 6, wantErr: ErrGuardedV6Down},
		{name: "dirty v6 down", snap: MigrationMetadataSnapshot{Exists: true, RowCount: 1, Version: 6, Dirty: true, HasVersion: true}, action: migrationGateDown, latest: 6, wantErr: ErrMigrationGateDirty},
		{name: "future v7", snap: clean(7), action: migrationGateDown, latest: 7, wantErr: ErrMigrationGateFuture},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := classifyMigrationAdmission(tt.snap, tt.action, tt.latest)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error=%v, want errors.Is(..., %v)", err, tt.wantErr)
			}
		})
	}
}

func TestExternalWriterAdmissionIsConservative(t *testing.T) {
	admission := AssessExternalWriterAdmission()
	if !admission.ManagedCooperativeWriters || admission.DirectSQLControlled || admission.OperationalDrainEvidence {
		t.Fatalf("unexpected external writer assessment: %+v", admission)
	}
	if !errors.Is(RequireExternalWriterAdmission(admission), ErrExternalWriterAdmissionRequired) {
		t.Fatal("missing external drain evidence was not rejected")
	}
	if err := RequireExternalWriterAdmission(ExternalWriterAdmission{ManagedCooperativeWriters: true, DirectSQLControlled: true, OperationalDrainEvidence: true}); err != nil {
		t.Fatalf("complete external admission rejected: %v", err)
	}
}

func TestMigrationGateErrorPreservesActionAndSnapshot(t *testing.T) {
	snapshot := MigrationMetadataSnapshot{Exists: true, RowCount: 1, Version: 5, Dirty: true, HasVersion: true}
	err := classifyMigrationAdmission(snapshot, migrationGateUp, 5)
	var gateErr *MigrationGateError
	if !errors.As(err, &gateErr) {
		t.Fatalf("error=%T, want MigrationGateError", err)
	}
	if gateErr.Action != migrationGateUp || gateErr.Snapshot != snapshot {
		t.Fatalf("gate error=%+v, want action/snapshot preserved", gateErr)
	}
}
