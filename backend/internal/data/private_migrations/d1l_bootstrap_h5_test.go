package migrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4/database/postgres"
)

func h5InstallHooks(ddlCalls, manifestCalls *int) d1LBootstrapHooks {
	return d1LBootstrapHooks{
		ddl: func(ctx context.Context, tx *sql.Tx) error {
			(*ddlCalls)++
			_, err := tx.ExecContext(ctx, string(d1LInstallerBytes))
			return err
		},
		manifest: func(ctx context.Context, tx *sql.Tx, target, installer []byte, installID string) error {
			(*manifestCalls)++
			_, err := tx.ExecContext(ctx, D1LManifestInsertSQL, target, installer, installID)
			return err
		},
	}
}

func TestD1LCommitUnknownDiscardFailureRemainsUnknownWithoutFreshClassification(t *testing.T) {
	dsn := newD1LUnknownTestDatabase(t)
	cfg, provider := newD1LUnknownConfig(t, dsn)
	var ddlCalls, manifestCalls, commitCalls int
	var events []string
	discardErr := errors.New("controlled protected-session discard proof failure")
	freshCalled := false
	hooks := h5InstallHooks(&ddlCalls, &manifestCalls)
	hooks.commit = func(_ context.Context, tx *sql.Tx) error {
		commitCalls++
		_ = tx.Rollback()
		return errors.New("controlled commit acknowledgement loss")
	}
	hooks.discard = func(*ExclusiveWriterFence, *migrationAdvisoryLock) error {
		events = append(events, "discard")
		return discardErr
	}
	hooks.freshInspect = func(context.Context, string, []byte, []byte, *postgres.Config) (D1LCatalogObservation, error) {
		freshCalled = true
		events = append(events, "fresh-inspect")
		return D1LCatalogObservation{State: D1LExactReady}, nil
	}

	report, err := d1LBootstrapWithHooks(context.Background(), cfg, hooks)
	if err == nil || !errors.Is(err, ErrD1LCommitUnknown) || !errors.Is(err, discardErr) {
		t.Fatalf("err=%v, want commit-unknown and discard failure", err)
	}
	if report.InstallState != D1LInstallUnknown || report.Committed || report.After != "" {
		t.Fatalf("failed-discard report=%+v, must remain unclassified UNKNOWN", report)
	}
	if freshCalled || strings.Join(events, ",") != "discard" {
		t.Fatalf("recovery events=%v freshCalled=%v, fresh inspection must not follow failed discard", events, freshCalled)
	}
	if commitCalls != 1 || ddlCalls != 1 || manifestCalls != 1 || strings.Count(strings.Join(provider.calls, ","), "consume") != 1 {
		t.Fatalf("side effects consume=%d ddl=%d manifest=%d commit=%d provider=%v", strings.Count(strings.Join(provider.calls, ","), "consume"), ddlCalls, manifestCalls, commitCalls, provider.calls)
	}
	freshTruth, freshErr := independentD1LCatalogInspection(context.Background(), dsn, cfg.TargetFingerprint, d1LInstallerDigestBytes(), mustParsedConfig(t, dsn))
	if freshErr != nil || freshTruth.State != D1LV5Base {
		t.Fatalf("independent post-test truth=%+v err=%v, want observable V5_BASE without runner classification", freshTruth, freshErr)
	}
	databaseName, serverVersion := domain5DatabaseEvidence(t, dsn)
	t.Logf("B4 database=%s endpoint=127.0.0.1:55434 PostgreSQL=%s events=%s observable_fresh=%s consume=1 ddl=%d manifest=%d commit=%d state=%s", databaseName, serverVersion, strings.Join(events, ">"), freshTruth.State, ddlCalls, manifestCalls, commitCalls, report.InstallState)
}

type h5PostCommitCase struct {
	name              string
	freshState        D1LCatalogState
	freshErr          error
	discardErr        error
	mutateAfterCommit string
	postProofState    D1LCatalogState
	postProofErr      error
	wantState         D1LBootstrapInstallState
	wantAfter         D1LCatalogState
	wantFresh         bool
}

func TestD1LPostCommitProofRecoveryUsesDiscardAndFreshTruth(t *testing.T) {
	cases := []h5PostCommitCase{
		{
			name:           "C1 fresh READY",
			freshState:     D1LExactReady,
			postProofState: D1LUnreadable,
			postProofErr:   errors.New("controlled post-commit READY proof failure"),
			wantState:      D1LInstallCommittedReady,
			wantAfter:      D1LExactReady,
			wantFresh:      true,
		},
		{
			name:              "C2 fresh partial",
			freshState:        D1LPartial,
			mutateAfterCommit: "DROP TABLE security_control.control_schema_migrations",
			postProofState:    D1LUnreadable,
			postProofErr:      errors.New("controlled post-commit READY proof failure"),
			wantState:         D1LInstallUnknown,
			wantAfter:         D1LPartial,
			wantFresh:         true,
		},
		{
			name:           "C3 fresh inspection failure",
			freshState:     D1LUnreadable,
			freshErr:       errors.New("controlled fresh inspection failure"),
			postProofState: D1LUnreadable,
			postProofErr:   errors.New("controlled post-commit READY proof failure"),
			wantState:      D1LInstallUnknown,
			wantAfter:      D1LUnreadable,
			wantFresh:      true,
		},
		{
			name:           "C4 discard failure",
			freshState:     D1LExactReady,
			discardErr:     errors.New("controlled protected-session discard failure"),
			postProofState: D1LUnreadable,
			postProofErr:   errors.New("controlled post-commit READY proof failure"),
			wantState:      D1LInstallUnknown,
			wantAfter:      "",
			wantFresh:      false,
		},
		{
			name:              "C5 contradictory fresh BASE",
			freshState:        D1LV5Base,
			mutateAfterCommit: "DROP SCHEMA security_control CASCADE",
			postProofState:    D1LPartial,
			postProofErr:      nil,
			wantState:         D1LInstallUnknown,
			wantAfter:         D1LV5Base,
			wantFresh:         true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dsn := newD1LUnknownTestDatabase(t)
			cfg, provider := newD1LUnknownConfig(t, dsn)
			var ddlCalls, manifestCalls, commitCalls int
			var events []string
			hooks := h5InstallHooks(&ddlCalls, &manifestCalls)
			hooks.commit = func(ctx context.Context, tx *sql.Tx) error {
				commitCalls++
				if err := tx.Commit(); err != nil {
					return err
				}
				if tc.mutateAfterCommit != "" {
					probe, err := sql.Open("postgres", dsn)
					if err != nil {
						return err
					}
					defer probe.Close()
					if _, err := probe.ExecContext(ctx, tc.mutateAfterCommit); err != nil {
						return err
					}
				}
				return nil
			}
			hooks.postCommitProof = func(context.Context, *sql.Conn, []byte, []byte, *postgres.Config) (D1LCatalogObservation, error) {
				events = append(events, "post-proof")
				return D1LCatalogObservation{State: tc.postProofState}, tc.postProofErr
			}
			hooks.discard = func(fence *ExclusiveWriterFence, lock *migrationAdvisoryLock) error {
				events = append(events, "discard")
				if tc.discardErr != nil {
					return tc.discardErr
				}
				return discardUnknownProtectedSession(fence, lock)
			}
			hooks.freshInspect = func(ctx context.Context, dsn string, target, installer []byte, config *postgres.Config) (D1LCatalogObservation, error) {
				events = append(events, "fresh-inspect")
				if tc.freshErr != nil {
					return D1LCatalogObservation{State: tc.freshState}, tc.freshErr
				}
				if tc.mutateAfterCommit == "" {
					return independentD1LCatalogInspection(ctx, dsn, target, installer, config)
				}
				fresh, err := independentD1LCatalogInspection(ctx, dsn, target, installer, config)
				if err != nil {
					return fresh, err
				}
				if fresh.State != tc.freshState {
					return fresh, fmt.Errorf("fresh state=%s, want controlled state=%s", fresh.State, tc.freshState)
				}
				return fresh, nil
			}

			report, err := d1LBootstrapWithHooks(context.Background(), cfg, hooks)
			if err == nil || report.InstallState != tc.wantState || report.After != tc.wantAfter || !report.Committed {
				t.Fatalf("report=%+v err=%v, want state=%s after=%s committed=true", report, err, tc.wantState, tc.wantAfter)
			}
			if tc.postProofErr != nil && !errors.Is(err, tc.postProofErr) {
				t.Fatalf("err=%v does not preserve post-commit proof failure=%v", err, tc.postProofErr)
			}
			if tc.discardErr != nil && !errors.Is(err, tc.discardErr) {
				t.Fatalf("err=%v does not preserve discard failure=%v", err, tc.discardErr)
			}
			if tc.freshErr != nil && !errors.Is(err, tc.freshErr) {
				t.Fatalf("err=%v does not preserve fresh inspection failure=%v", err, tc.freshErr)
			}
			if got := strings.Join(events, ">"); tc.wantFresh && got != "post-proof>discard>fresh-inspect" {
				t.Fatalf("events=%s, want post-proof>discard>fresh-inspect", got)
			} else if !tc.wantFresh && got != "post-proof>discard" {
				t.Fatalf("events=%s, want post-proof>discard", got)
			}
			if commitCalls != 1 || ddlCalls != 1 || manifestCalls != 1 || strings.Count(strings.Join(provider.calls, ","), "consume") != 1 {
				t.Fatalf("side effects consume=%d ddl=%d manifest=%d commit=%d provider=%v", strings.Count(strings.Join(provider.calls, ","), "consume"), ddlCalls, manifestCalls, commitCalls, provider.calls)
			}
			databaseName, serverVersion := domain5DatabaseEvidence(t, dsn)
			t.Logf("%s database=%s endpoint=127.0.0.1:55434 PostgreSQL=%s events=%s consume=1 ddl=%d manifest=%d commit=%d state=%s after=%s", tc.name, databaseName, serverVersion, strings.Join(events, ">"), ddlCalls, manifestCalls, commitCalls, report.InstallState, report.After)
		})
	}
}
