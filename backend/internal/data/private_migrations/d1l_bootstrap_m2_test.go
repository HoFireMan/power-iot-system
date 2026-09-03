package migrations

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestD1LM2InitialArtifactMismatchStopsBeforeConsume(t *testing.T) {
	original := append([]byte(nil), d1LInstallerBytes...)
	defer func() { copy(d1LInstallerBytes, original) }()
	d1LInstallerBytes[0] ^= 0xff

	provider := &d1lTestProvider{}
	var checks []string
	var ddlCalls, manifestCalls, commitCalls int
	_, err := d1LBootstrapWithHooks(context.Background(), D1LBootstrapConfig{Provider: provider}, d1LBootstrapHooks{
		artifactCheck: func(stage string, _ []byte, checkErr error) {
			checks = append(checks, stage)
			if checkErr == nil {
				t.Errorf("artifact check %s unexpectedly passed", stage)
			}
		},
		ddlWithArtifact: func(context.Context, *sql.Tx, []byte) error {
			ddlCalls++
			return nil
		},
		manifest: func(context.Context, *sql.Tx, []byte, []byte, string) error {
			manifestCalls++
			return nil
		},
		commit: func(context.Context, *sql.Tx) error {
			commitCalls++
			return nil
		},
	})
	if !errors.Is(err, ErrD1LArtifactDigest) {
		t.Fatalf("error=%v, want artifact mismatch", err)
	}
	if strings.Join(checks, ",") != d1LArtifactCheckPreConsume {
		t.Fatalf("checks=%v, want only %s", checks, d1LArtifactCheckPreConsume)
	}
	if len(provider.calls) != 0 || ddlCalls != 0 || manifestCalls != 0 || commitCalls != 0 {
		t.Fatalf("side effects provider=%v ddl=%d manifest=%d commit=%d", provider.calls, ddlCalls, manifestCalls, commitCalls)
	}
}

func TestD1LM2ImmediateArtifactMismatchRollsBackWithoutReplay(t *testing.T) {
	dsn := newD1LUnknownTestDatabase(t)
	cfg, provider := newD1LUnknownConfig(t, dsn)
	original := append([]byte(nil), d1LInstallerBytes...)
	defer func() { copy(d1LInstallerBytes, original) }()

	var checks []string
	var ddlCalls, manifestCalls, commitCalls int
	hooks := d1LBootstrapHooks{
		artifactCheck: func(stage string, installer []byte, checkErr error) {
			checks = append(checks, stage)
			if stage == d1LArtifactCheckPreConsume && checkErr != nil {
				t.Fatalf("pre-Consume artifact check failed: %v", checkErr)
			}
			if stage == d1LArtifactCheckImmediatePreDDL {
				if checkErr == nil {
					t.Fatal("immediate artifact check unexpectedly passed")
				}
				if bytes.Equal(installer, original) {
					t.Fatal("immediate check saw unchanged artifact after controlled mutation")
				}
			}
		},
		beforeImmediateDDL: func() {
			d1LInstallerBytes[0] ^= 0xff
		},
		ddlWithArtifact: func(context.Context, *sql.Tx, []byte) error {
			ddlCalls++
			return nil
		},
		manifest: func(context.Context, *sql.Tx, []byte, []byte, string) error {
			manifestCalls++
			return nil
		},
		commit: func(context.Context, *sql.Tx) error {
			commitCalls++
			return nil
		},
	}
	report, err := d1LBootstrapWithHooks(context.Background(), cfg, hooks)
	if !errors.Is(err, ErrD1LArtifactDigest) {
		t.Fatalf("error=%v, want immediate artifact mismatch", err)
	}
	if report.InstallState != D1LInstallNotInstalled || report.After != D1LV5Base {
		t.Fatalf("report=%+v, want fresh exact V5_BASE classified NOT_INSTALLED", report)
	}
	if strings.Join(checks, ",") != d1LArtifactCheckPreConsume+","+d1LArtifactCheckImmediatePreDDL {
		t.Fatalf("checks=%v, want one pre-Consume and one immediate check", checks)
	}
	if strings.Join(provider.calls, ",") != "attestation,inspect,consume" {
		t.Fatalf("provider calls=%v, want one Consume and no authorization replay", provider.calls)
	}
	if ddlCalls != 0 || manifestCalls != 0 || commitCalls != 0 {
		t.Fatalf("side effects ddl=%d manifest=%d commit=%d", ddlCalls, manifestCalls, commitCalls)
	}
	databaseName, serverVersion := domain5DatabaseEvidence(t, dsn)
	t.Logf("M2-3 database=%s endpoint=127.0.0.1:55434 PostgreSQL=%s checks=%s consume=1 ddl=%d manifest=%d commit=%d authorization_replay=0 state=%s after=%s", databaseName, serverVersion, strings.Join(checks, ">"), ddlCalls, manifestCalls, commitCalls, report.InstallState, report.After)
}

func TestD1LM2VerifiedArtifactBytesAreExactlyExecuted(t *testing.T) {
	dsn := newD1LUnknownTestDatabase(t)
	cfg, provider := newD1LUnknownConfig(t, dsn)
	var preChecked, immediateChecked int
	var immediateBytes, executedBytes []byte
	var ddlCalls, manifestCalls, commitCalls int
	hooks := d1LBootstrapHooks{
		artifactCheck: func(stage string, installer []byte, checkErr error) {
			if checkErr != nil {
				t.Fatalf("artifact check %s failed: %v", stage, checkErr)
			}
			switch stage {
			case d1LArtifactCheckPreConsume:
				preChecked++
			case d1LArtifactCheckImmediatePreDDL:
				immediateChecked++
				immediateBytes = append([]byte(nil), installer...)
			}
		},
		ddlWithArtifact: func(ctx context.Context, tx *sql.Tx, installer []byte) error {
			ddlCalls++
			executedBytes = append([]byte(nil), installer...)
			_, err := tx.ExecContext(ctx, string(installer))
			return err
		},
		manifest: func(ctx context.Context, tx *sql.Tx, target, installer []byte, installID string) error {
			manifestCalls++
			_, err := tx.ExecContext(ctx, D1LManifestInsertSQL, target, installer, installID)
			return err
		},
		commit: func(ctx context.Context, tx *sql.Tx) error {
			commitCalls++
			return tx.Commit()
		},
	}
	report, err := d1LBootstrapWithHooks(context.Background(), cfg, hooks)
	if err != nil {
		t.Fatal(err)
	}
	if report.InstallState != D1LInstallCommittedReady || !report.Committed {
		t.Fatalf("report=%+v", report)
	}
	if preChecked != 1 || immediateChecked != 1 || ddlCalls != 1 || manifestCalls != 1 || commitCalls != 1 {
		t.Fatalf("checks pre=%d immediate=%d ddl=%d manifest=%d commit=%d", preChecked, immediateChecked, ddlCalls, manifestCalls, commitCalls)
	}
	if !bytes.Equal(immediateBytes, executedBytes) || !bytes.Equal(executedBytes, d1LInstallerBytes) {
		t.Fatal("successful immediate verification bytes differ from executed bytes or fixed artifact")
	}
	if strings.Join(provider.calls, ",") != "attestation,inspect,consume" {
		t.Fatalf("provider calls=%v, want one Consume", provider.calls)
	}
	databaseName, serverVersion := domain5DatabaseEvidence(t, dsn)
	t.Logf("M2-1/M2-4 database=%s endpoint=127.0.0.1:55434 PostgreSQL=%s checks=%d/%d consume=1 ddl=1 manifest=1 commit=1 exact_bytes=true state=%s", databaseName, serverVersion, preChecked, immediateChecked, report.InstallState)
}
