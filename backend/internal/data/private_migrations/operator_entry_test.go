package migrations

import (
	"context"
	"errors"
	"testing"
)

func TestRunD6ProtectedMigrationOperatorRequiresTrustedDrainCallback(t *testing.T) {
	_, err := RunD6ProtectedMigrationOperator(context.Background(), "unused", nil)
	if !errors.Is(err, ErrTrustedDrainAdmissionMissing) {
		t.Fatalf("error=%v, want trusted drain admission error", err)
	}
}

func TestRunD6ProtectedMigrationOperatorPreservesD5Result(t *testing.T) {
	previous := runD5MigrationOperator
	t.Cleanup(func() { runD5MigrationOperator = previous })
	wantReport := ProtectedMigrationReport{State: ProtectedStateCleanV5, Outcome: ProtectedNotCommitted, Phase: ProtectedPhaseInspection}
	wantErr := errors.New("protected result")
	called := false
	runD5MigrationOperator = func(_ context.Context, gotURL string, admission ExternalWriterAdmission) (ProtectedMigrationReport, error) {
		called = true
		if gotURL != "rehearsal" {
			t.Fatalf("database URL=%q, want rehearsal", gotURL)
		}
		if err := RequireExternalWriterAdmission(admission); err != nil {
			t.Fatalf("wrapper supplied untrusted admission: %v", err)
		}
		return wantReport, wantErr
	}
	gotReport, gotErr := RunD6ProtectedMigrationOperator(context.Background(), "rehearsal", func(context.Context) error { return nil })
	if !called {
		t.Fatal("D5 runner was not called")
	}
	if !errors.Is(gotErr, wantErr) || gotReport != wantReport {
		t.Fatalf("report=%+v err=%v, want report=%+v err=%v", gotReport, gotErr, wantReport, wantErr)
	}
}

func TestRunD6ProtectedMigrationOperatorRejectsDrainFailureBeforeD5(t *testing.T) {
	previous := runD5MigrationOperator
	t.Cleanup(func() { runD5MigrationOperator = previous })
	called := false
	runD5MigrationOperator = func(context.Context, string, ExternalWriterAdmission) (ProtectedMigrationReport, error) {
		called = true
		return ProtectedMigrationReport{}, nil
	}
	wantErr := errors.New("writer remains")
	_, gotErr := RunD6ProtectedMigrationOperator(context.Background(), "rehearsal", func(context.Context) error { return wantErr })
	if !errors.Is(gotErr, wantErr) || called {
		t.Fatalf("error=%v called=%t, want drain error and no D5 call", gotErr, called)
	}
}

func TestRunB02ProtectedMigrationOperatorRequiresTrustedDrainCallback(t *testing.T) {
	_, err := RunB02ProtectedMigrationOperator(context.Background(), "unused", nil)
	if !errors.Is(err, ErrTrustedDrainAdmissionMissing) {
		t.Fatalf("error=%v, want trusted drain admission error", err)
	}
}

func TestRunB02ProtectedMigrationOperatorPreservesResultAndUsesTrustedAdmission(t *testing.T) {
	previous := runB02MigrationOperator
	t.Cleanup(func() { runB02MigrationOperator = previous })
	wantReport := ProtectedMigrationReport{State: ProtectedStateCleanB02, Outcome: ProtectedAlreadyComplete}
	wantErr := errors.New("protected B-02 result")
	called := false
	runB02MigrationOperator = func(_ context.Context, gotURL string, admission ExternalWriterAdmission) (ProtectedMigrationReport, error) {
		called = true
		if gotURL != "rehearsal" {
			t.Fatalf("database URL=%q, want rehearsal", gotURL)
		}
		if err := RequireExternalWriterAdmission(admission); err != nil {
			t.Fatalf("wrapper supplied untrusted admission: %v", err)
		}
		return wantReport, wantErr
	}
	gotReport, gotErr := RunB02ProtectedMigrationOperator(context.Background(), "rehearsal", func(context.Context) error { return nil })
	if !called {
		t.Fatal("B-02 runner was not called")
	}
	if !errors.Is(gotErr, wantErr) || gotReport != wantReport {
		t.Fatalf("report=%+v err=%v, want report=%+v err=%v", gotReport, gotErr, wantReport, wantErr)
	}
}

func TestRunB02ProtectedMigrationOperatorRejectsDrainFailureBeforeB02(t *testing.T) {
	previous := runB02MigrationOperator
	t.Cleanup(func() { runB02MigrationOperator = previous })
	called := false
	runB02MigrationOperator = func(context.Context, string, ExternalWriterAdmission) (ProtectedMigrationReport, error) {
		called = true
		return ProtectedMigrationReport{}, nil
	}
	wantErr := errors.New("writer remains")
	_, gotErr := RunB02ProtectedMigrationOperator(context.Background(), "rehearsal", func(context.Context) error { return wantErr })
	if !errors.Is(gotErr, wantErr) || called {
		t.Fatalf("error=%v called=%t, want drain error and no B-02 call", gotErr, called)
	}
}
