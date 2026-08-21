package migrations

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"strings"
	"testing"
)

func domain5DatabaseEvidence(t *testing.T, dsn string) (string, string) {
	t.Helper()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var databaseName, serverVersion string
	if err := db.QueryRowContext(context.Background(), "SELECT current_database(), current_setting('server_version')").Scan(&databaseName, &serverVersion); err != nil {
		t.Fatal(err)
	}
	return databaseName, serverVersion
}

func domain5UnknownConsumeResult(req ConsumeRequest) ConsumeResult {
	return ConsumeResult{
		Outcome:            OutcomeUnknown,
		AuthorizationID:    req.AuthorizationID,
		ConsumeRequestID:   req.ConsumeRequestID,
		State:              ConsumeUnknown,
		AuthorizationState: string(AuthorizationConsumeUnknown),
		TerminalState:      string(AuthorizationConsumeUnknown),
		TerminalCode:       "CONSUME_OUTCOME_UNKNOWN",
	}
}

func domain5ConsumedResolveResult(req ResolveRequest) ResolveResult {
	return ResolveResult{
		Outcome:          OutcomeSuccess,
		AuthorizationID:  req.AuthorizationID,
		IssuerRequestID:  req.IssuerRequestID,
		ConsumeRequestID: req.ConsumeRequestID,
		AuthState:        AuthorizationConsumed,
		IntentState:      ConsumeConsumed,
		TerminalState:    string(AuthorizationConsumed),
		TerminalCode:     "CONSUMED",
	}
}

func domain5UnknownResolveResult(req ResolveRequest) ResolveResult {
	return ResolveResult{
		Outcome:          OutcomeUnknown,
		AuthorizationID:  req.AuthorizationID,
		IssuerRequestID:  req.IssuerRequestID,
		ConsumeRequestID: req.ConsumeRequestID,
		AuthState:        AuthorizationConsumeUnknown,
		IntentState:      ConsumeUnknown,
		TerminalState:    string(AuthorizationConsumeUnknown),
		TerminalCode:     "CONSUME_OUTCOME_UNKNOWN",
	}
}

func domain5ExpiredResolveResult(req ResolveRequest) ResolveResult {
	return ResolveResult{
		Outcome:          OutcomeExpired,
		AuthorizationID:  req.AuthorizationID,
		IssuerRequestID:  req.IssuerRequestID,
		ConsumeRequestID: req.ConsumeRequestID,
		AuthState:        AuthorizationExpired,
		IntentState:      ConsumeAborted,
		TerminalState:    string(AuthorizationExpired),
		TerminalCode:     "EXPIRED",
	}
}

func TestD1LDomain5AmbiguousConsumeResolvesConsumedWithoutReplay(t *testing.T) {
	dsn := newD1LUnknownTestDatabase(t)
	cfg, provider := newD1LUnknownConfig(t, dsn)
	var resolved ResolveRequest
	provider.consumeFunc = func(req ConsumeRequest) (ConsumeResult, error) {
		return domain5UnknownConsumeResult(req), errors.New("response lost after provider request")
	}
	provider.resolveFunc = func(req ResolveRequest) (ResolveResult, error) {
		resolved = req
		return domain5ConsumedResolveResult(req), nil
	}
	report, err := d1LBootstrapWithHooks(context.Background(), cfg, d1LBootstrapHooks{})
	if err == nil || !errors.Is(err, ErrD1LNoRetry) || report.InstallState != D1LInstallNotInstalled || report.After != D1LV5Base {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if strings.Join(provider.calls, ",") != "attestation,inspect,consume,resolve" {
		t.Fatalf("provider calls=%v, want one Consume then one Resolve", provider.calls)
	}
	if resolved.ConsumeRequestID != report.ConsumeRequestID || resolved.AuthorizationID != cfg.AuthorizationID || resolved.Operation != cfg.OperationID || resolved.AttemptID != cfg.AttemptID || resolved.TargetID != report.TargetFingerprint || resolved.InstallerID != report.InstallerDigest || resolved.EvidenceHash != report.EvidenceDigest || resolved.Scope != ScopeControlCatalogInstall {
		t.Fatalf("Resolve did not preserve original binding tuple: %+v", resolved)
	}
	databaseName, serverVersion := domain5DatabaseEvidence(t, dsn)
	t.Logf("database=%s endpoint=127.0.0.1:55434 PostgreSQL=%s provider=%s target=%s state=%s", databaseName, serverVersion, strings.Join(provider.calls, ","), report.TargetFingerprint, report.InstallState)
}

func TestD1LDomain5AmbiguousConsumeUnresolvedFailsClosed(t *testing.T) {
	dsn := newD1LUnknownTestDatabase(t)
	cfg, provider := newD1LUnknownConfig(t, dsn)
	provider.consumeFunc = func(req ConsumeRequest) (ConsumeResult, error) {
		return domain5UnknownConsumeResult(req), errors.New("ambiguous provider transport")
	}
	provider.resolveFunc = func(req ResolveRequest) (ResolveResult, error) {
		return domain5UnknownResolveResult(req), nil
	}
	commitCalls := 0
	report, err := d1LBootstrapWithHooks(context.Background(), cfg, d1LBootstrapHooks{commit: func(context.Context, *sql.Tx) error {
		commitCalls++
		return nil
	}})
	if err == nil || !errors.Is(err, ErrD1LNoRetry) || report.InstallState != D1LInstallUnknown || report.After != D1LV5Base {
		t.Fatalf("unresolved recovery report=%+v err=%v", report, err)
	}
	if commitCalls != 0 || strings.Join(provider.calls, ",") != "attestation,inspect,consume,resolve" {
		t.Fatalf("commit=%d provider calls=%v; ambiguous recovery must not replay", commitCalls, provider.calls)
	}
	databaseName, serverVersion := domain5DatabaseEvidence(t, dsn)
	t.Logf("database=%s endpoint=127.0.0.1:55434 PostgreSQL=%s state=%s provider=%s", databaseName, serverVersion, report.InstallState, strings.Join(provider.calls, ","))
}

func TestD1LDomain5AmbiguousConsumeResolvesTerminalWithoutReplay(t *testing.T) {
	dsn := newD1LUnknownTestDatabase(t)
	cfg, provider := newD1LUnknownConfig(t, dsn)
	provider.consumeFunc = func(req ConsumeRequest) (ConsumeResult, error) {
		return ConsumeResult{Outcome: OutcomeInProgress, AuthorizationID: req.AuthorizationID, ConsumeRequestID: req.ConsumeRequestID, State: ConsumeClaimed, AuthorizationState: string(AuthorizationConsumePending), PendingConsumeRequestID: req.ConsumeRequestID}, nil
	}
	provider.resolveFunc = func(req ResolveRequest) (ResolveResult, error) {
		return domain5ExpiredResolveResult(req), nil
	}
	report, err := d1LBootstrapWithHooks(context.Background(), cfg, d1LBootstrapHooks{})
	if err == nil || !errors.Is(err, ErrD1LNoRetry) || report.InstallState != D1LInstallNotInstalled || report.After != D1LV5Base {
		t.Fatalf("terminal recovery report=%+v err=%v", report, err)
	}
	if strings.Join(provider.calls, ",") != "attestation,inspect,consume,resolve" {
		t.Fatalf("provider calls=%v, want one Consume then one Resolve", provider.calls)
	}
	databaseName, serverVersion := domain5DatabaseEvidence(t, dsn)
	t.Logf("database=%s endpoint=127.0.0.1:55434 PostgreSQL=%s state=%s provider=%s", databaseName, serverVersion, report.InstallState, strings.Join(provider.calls, ","))
}

func TestD1LDomain5DefinitelyPreTransmissionDoesNotResolveOrReplay(t *testing.T) {
	dsn := newD1LUnknownTestDatabase(t)
	cfg, provider := newD1LUnknownConfig(t, dsn)
	provider.consumeFunc = func(req ConsumeRequest) (ConsumeResult, error) {
		return domain5UnknownConsumeResult(req), &ClientError{
			Kind:    ErrProviderUnavailable,
			Outcome: OutcomeUnavailable,
			Cause:   &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")},
		}
	}
	provider.resolveFunc = func(ResolveRequest) (ResolveResult, error) {
		t.Fatal("definitely pre-transmission failure must not call Resolve")
		return ResolveResult{}, nil
	}
	commitCalls := 0
	report, err := d1LBootstrapWithHooks(context.Background(), cfg, d1LBootstrapHooks{commit: func(context.Context, *sql.Tx) error {
		commitCalls++
		return nil
	}})
	if err == nil || !errors.Is(err, ErrD1LNoRetry) || report.InstallState != D1LInstallNotInstalled || report.After != D1LV5Base {
		t.Fatalf("pre-transmission report=%+v err=%v", report, err)
	}
	if commitCalls != 0 || strings.Join(provider.calls, ",") != "attestation,inspect,consume" {
		t.Fatalf("commit=%d provider calls=%v", commitCalls, provider.calls)
	}
	databaseName, serverVersion := domain5DatabaseEvidence(t, dsn)
	t.Logf("database=%s endpoint=127.0.0.1:55434 PostgreSQL=%s state=%s provider=%s", databaseName, serverVersion, report.InstallState, strings.Join(provider.calls, ","))
}

func TestD1LDomain5TransactionFailuresRequireFreshBaseProof(t *testing.T) {
	tests := []struct {
		name string
		hook func(*d1LBootstrapHooks, *int, *int) // hook, DDL count, manifest count
	}{
		{
			name: "DDL",
			hook: func(h *d1LBootstrapHooks, ddl, _ *int) {
				h.ddl = func(context.Context, *sql.Tx) error {
					(*ddl)++
					return errors.New("controlled DDL failure")
				}
			},
		},
		{
			name: "manifest",
			hook: func(h *d1LBootstrapHooks, ddl, manifest *int) {
				h.manifest = func(ctx context.Context, tx *sql.Tx, _ []byte, _ []byte, _ string) error {
					(*manifest)++
					(*ddl)++
					if _, err := tx.ExecContext(ctx, string(d1LInstallerBytes)); err != nil {
						return err
					}
					return errors.New("controlled manifest failure")
				}
			},
		},
		{
			name: "pre-commit validation",
			hook: func(h *d1LBootstrapHooks, ddl, manifest *int) {
				h.ddl = func(ctx context.Context, tx *sql.Tx) error {
					(*ddl)++
					_, err := tx.ExecContext(ctx, string(d1LInstallerBytes))
					return err
				}
				h.manifest = func(ctx context.Context, tx *sql.Tx, target, installer []byte, installID string) error {
					(*manifest)++
					_, err := tx.ExecContext(ctx, D1LManifestInsertSQL, target, installer, installID)
					return err
				}
				h.preCommit = func(context.Context, *sql.Tx) error { return errors.New("controlled pre-commit validation failure") }
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dsn := newD1LUnknownTestDatabase(t)
			cfg, provider := newD1LUnknownConfig(t, dsn)
			ddlCalls, manifestCalls, commitCalls := 0, 0, 0
			hooks := d1LBootstrapHooks{commit: func(context.Context, *sql.Tx) error {
				commitCalls++
				return nil
			}}
			tc.hook(&hooks, &ddlCalls, &manifestCalls)
			report, err := d1LBootstrapWithHooks(context.Background(), cfg, hooks)
			if err == nil || report.InstallState != D1LInstallNotInstalled || report.After != D1LV5Base {
				t.Fatalf("failure report=%+v err=%v", report, err)
			}
			if commitCalls != 0 || len(provider.calls) != 3 || provider.calls[2] != "consume" {
				t.Fatalf("commit=%d provider calls=%v", commitCalls, provider.calls)
			}
			databaseName, serverVersion := domain5DatabaseEvidence(t, dsn)
			t.Logf("database=%s endpoint=127.0.0.1:55434 PostgreSQL=%s failure=%s consume=%d resolve=%d ddl=%d manifest=%d commit=%d fresh=%s", databaseName, serverVersion, tc.name, strings.Count(strings.Join(provider.calls, ","), "consume"), strings.Count(strings.Join(provider.calls, ","), "resolve"), ddlCalls, manifestCalls, commitCalls, report.After)
		})
	}
}
