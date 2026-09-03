package migrations

import (
	"errors"
	"testing"
)

func TestClassifyRuntimeAdmissionClosedWorldMatrix(t *testing.T) {
	tests := []struct {
		name    string
		state   RuntimeSchemaState
		want    RuntimeAdmissionDisposition
		wantErr error
	}{
		{name: "clean v6", state: RuntimeSchemaCleanV6, want: RuntimeServeV6},
		{name: "clean b02", state: RuntimeSchemaCleanB02, want: RuntimeServeB02},
		{name: "clean v5 requires private operator", state: RuntimeSchemaCleanV5, want: RuntimeProtectedMigrationNeeded, wantErr: ErrRuntimeProtectedMigrationRequired},
		{name: "dirty state refuses", state: RuntimeSchemaDirty, wantErr: ErrRuntimeAdmissionRefused},
		{name: "unknown state refuses", state: RuntimeSchemaUnknown, wantErr: ErrRuntimeAdmissionRefused},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ClassifyRuntimeAdmission(tt.state)
			if got != tt.want || !errors.Is(err, tt.wantErr) {
				t.Fatalf("ClassifyRuntimeAdmission(%q)=(%q,%v), want (%q,%v)", tt.state, got, err, tt.want, tt.wantErr)
			}
		})
	}
}
