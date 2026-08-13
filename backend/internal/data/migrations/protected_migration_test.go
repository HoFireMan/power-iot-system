package migrations

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestClassifyProtectedStateMatrix(t *testing.T) {
	clean := func(version int, dirty bool) MigrationMetadataSnapshot {
		return MigrationMetadataSnapshot{Exists: true, RowCount: 1, HasVersion: true, Version: version, Dirty: dirty}
	}
	tests := []struct {
		name     string
		metadata MigrationMetadataSnapshot
		catalog  ProtectedCatalogState
		want     ProtectedMigrationState
	}{
		{"clean v5", clean(5, false), ProtectedCatalogExactV5, ProtectedStateCleanV5},
		{"dirty v5 exact v5", clean(5, true), ProtectedCatalogExactV5, ProtectedStateDirtyV5},
		{"dirty v5 partial", clean(5, true), ProtectedCatalogPartial, ProtectedStateDirtyV5},
		{"dirty v5 v6-like", clean(5, true), ProtectedCatalogExactV6, ProtectedStateDirtyV5},
		{"dirty v6 exact v5", clean(6, true), ProtectedCatalogExactV5, ProtectedStateTransitionV6},
		{"dirty v6 partial", clean(6, true), ProtectedCatalogPartial, ProtectedStateTransitionV6},
		{"dirty v6 exact v6", clean(6, true), ProtectedCatalogExactV6, ProtectedStateTransitionV6},
		{"clean v6", clean(6, false), ProtectedCatalogExactV6, ProtectedStateCleanV6},
		{"clean v5 mismatch", clean(5, false), ProtectedCatalogPartial, ProtectedStateAmbiguous},
		{"duplicate metadata", MigrationMetadataSnapshot{Exists: true, RowCount: 2, HasVersion: true, Version: 5}, ProtectedCatalogExactV5, ProtectedStateAmbiguous},
		{"future", clean(7, false), ProtectedCatalogExactV6, ProtectedStateFuture},
		{"bootstrap", MigrationMetadataSnapshot{CatalogEmpty: true}, ProtectedCatalogEmpty, ProtectedStateBootstrap},
		{"missing metadata mixed catalog", MigrationMetadataSnapshot{}, ProtectedCatalogPartial, ProtectedStateAmbiguous},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyProtectedState(tc.metadata, tc.catalog); got != tc.want {
				t.Fatalf("state=%s, want %s", got, tc.want)
			}
		})
	}
}

func TestClassifyCatalogTablesIsExactAndFailClosed(t *testing.T) {
	v6 := append([]string(nil), v5CatalogTables...)
	if got := classifyCatalogTables(v5CatalogTables, v6); got != ProtectedCatalogExactV5 {
		t.Fatalf("v5 catalog=%s", got)
	}
	v6 = append(v6, "test_only_v6_fixture")
	if got := classifyCatalogTables(v6, v6); got != ProtectedCatalogExactV6 {
		t.Fatalf("v6 catalog=%s", got)
	}
	partial := append([]string(nil), v6...)
	partial = append(partial, "unexpected")
	if got := classifyCatalogTables(partial, v6); got != ProtectedCatalogPartial {
		t.Fatalf("partial catalog=%s", got)
	}
}

func TestProtectedMigrationSpecRejectsAmbiguousV6Expectations(t *testing.T) {
	base := ProtectedMigrationSpec{V6CatalogTables: []string{"a", "b"}, Apply: func(_ context.Context, _ *sql.Tx) error { return nil }}
	base.V5SemanticVerifier = func(context.Context, ProtectedMigrationQueryer) error { return nil }
	base.V6SemanticVerifier = func(context.Context, ProtectedMigrationQueryer) error { return nil }
	if err := validateProtectedMigrationSpec(base, true); err != nil {
		t.Fatal(err)
	}
	for _, tables := range [][]string{nil, {}, {"a", "a"}, {"a", "bad.name"}, {"a", "bad\"name"}} {
		spec := base
		spec.V6CatalogTables = tables
		if err := validateProtectedMigrationSpec(spec, false); !errors.Is(err, ErrProtectedMigrationSpec) {
			t.Fatalf("tables=%v err=%v", tables, err)
		}
	}
	missingApply := base
	missingApply.Apply = nil
	if err := validateProtectedMigrationSpec(missingApply, true); !errors.Is(err, ErrProtectedMigrationSpec) {
		t.Fatalf("missing Apply err=%v", err)
	}
	missingVerifier := base
	missingVerifier.V6SemanticVerifier = nil
	if err := validateProtectedMigrationSpec(missingVerifier, true); !errors.Is(err, ErrProtectedMigrationSpec) {
		t.Fatalf("missing verifier err=%v", err)
	}
}

func TestProtectedRecoveryActionNamesAreExplicit(t *testing.T) {
	if ProtectedRecoveryRestoreCleanV5 == ProtectedRecoveryCompleteCleanV6 {
		t.Fatal("recovery actions must remain distinct")
	}
}
