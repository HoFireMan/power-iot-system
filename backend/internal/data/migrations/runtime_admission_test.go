package migrations

import (
	"errors"
	"testing"
)

func TestClassifyRuntimeAdmissionClosedWorldMatrix(t *testing.T) {
	tests := []struct {
		name    string
		state   ProtectedMigrationState
		want    RuntimeAdmissionDisposition
		wantErr error
	}{
		{name: "clean v6 serves", state: ProtectedStateCleanV6, want: RuntimeServeV6},
		{name: "clean v5 requires protected entry", state: ProtectedStateCleanV5, want: RuntimeProtectedMigrationNeeded, wantErr: ErrRuntimeProtectedMigrationRequired},
		{name: "dirty v5 refuses", state: ProtectedStateDirtyV5, wantErr: ErrRuntimeAdmissionRefused},
		{name: "partial v6 refuses", state: ProtectedStateTransitionV6, wantErr: ErrRuntimeAdmissionRefused},
		{name: "ambiguous refuses", state: ProtectedStateAmbiguous, wantErr: ErrRuntimeAdmissionRefused},
		{name: "future refuses", state: ProtectedStateFuture, wantErr: ErrRuntimeAdmissionRefused},
		{name: "unreadable refuses", state: ProtectedStateBootstrap, wantErr: ErrRuntimeAdmissionRefused},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ClassifyRuntimeAdmission(tt.state)
			if got != tt.want {
				t.Fatalf("disposition=%q, want %q", got, tt.want)
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error=%v, want errors.Is(..., %v)", err, tt.wantErr)
			}
		})
	}
}
