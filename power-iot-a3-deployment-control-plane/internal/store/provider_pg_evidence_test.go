package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"power-iot-a3-deployment-control-plane/internal/ledger"
	"power-iot-a3-deployment-control-plane/migrations"
)

func providerPGStore(t *testing.T) (*Store, string) {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv("D1L_PROVIDER_DATABASE_URL"))
	if raw == "" {
		t.Skip("D1L_PROVIDER_DATABASE_URL is not configured; provider PostgreSQL checks skipped")
	}
	if err := validateProviderTestURL(raw); err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err = s.DB.ExecContext(context.Background(), migrations.Bootstrap); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcquireAuthority(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.ReleaseAuthority)
	return s, raw
}

func providerIssue(t *testing.T, s *Store, ttl time.Duration) (IssueResult, ConsumeRequest) {
	t.Helper()
	rid, attempt := uuid.New(), uuid.New()
	bindings := map[string]string{
		"operation":     "install",
		"attempt_id":    attempt.String(),
		"target_id":     "target-" + rid.String(),
		"installer_id":  "installer",
		"evidence_hash": "evidence-" + rid.String(),
	}
	out, err := s.Issue(context.Background(), RequestData{
		ID: rid.String(), AttemptID: attempt.String(), Role: "deployment-runbook",
		Scope: ledger.ScopeControlCatalogInstall, Bindings: bindings,
	}, ttl)
	if err != nil {
		t.Fatal(err)
	}
	return out, ConsumeRequest{
		ConsumeRequestID: uuid.New().String(), AuthorizationID: out.AuthorizationID,
		IssuerRequestID: out.IssuerRequestID, Operation: bindings["operation"],
		AttemptID: out.AttemptID, TargetID: bindings["target_id"],
		InstallerID: bindings["installer_id"], EvidenceHash: bindings["evidence_hash"],
		Scope: out.Scope, Epoch: out.Epoch, Nonce: out.Nonce, Envelope: out.Envelope,
	}
}

func TestProviderPostgresBootstrapIsolation(t *testing.T) {
	s, raw := providerPGStore(t)
	var current string
	if err := s.DB.QueryRowContext(context.Background(), "SELECT current_database()").Scan(&current); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(raw), current) || !disposableProviderDatabaseName.MatchString(current) {
		t.Fatalf("unexpected provider database current=%q", current)
	}
	var version, count int
	if err := s.DB.QueryRowContext(context.Background(), "SELECT schema_version FROM d1l_provider.provider_control WHERE singleton=true").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRowContext(context.Background(), "SELECT count(*) FROM d1l_provider.schema_version").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if version != 1 || count != 1 {
		t.Fatalf("provider schema authority version=%d rows=%d", version, count)
	}
	for _, relation := range []string{
		"d1l_provider.provider_epochs", "d1l_provider.provider_control",
		"d1l_provider.d1l_issue_requests", "d1l_provider.d1l_bootstrap_authorizations",
		"d1l_provider.d1l_bootstrap_consume_intents",
	} {
		var exists bool
		if err := s.DB.QueryRowContext(context.Background(), "SELECT to_regclass($1) IS NOT NULL", relation).Scan(&exists); err != nil || !exists {
			t.Fatalf("provider relation missing %s: %v", relation, err)
		}
	}
	var targetSchema, targetMigrations bool
	if err := s.DB.QueryRowContext(context.Background(), "SELECT to_regclass('d1l_provider.security_control') IS NOT NULL, to_regclass('public.schema_migrations') IS NOT NULL").Scan(&targetSchema, &targetMigrations); err != nil {
		t.Fatal(err)
	}
	if targetSchema || targetMigrations {
		t.Fatalf("provider database contains target authority schema=%v migrations=%v", targetSchema, targetMigrations)
	}
	if _, err := s.DB.ExecContext(context.Background(), migrations.Bootstrap); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRowContext(context.Background(), "SELECT count(*) FROM d1l_provider.schema_version").Scan(&count); err != nil || count != 1 {
		t.Fatalf("bootstrap rerun changed provider version rows=%d err=%v", count, err)
	}
}

func TestProviderPostgresAuthorityLockAndEpoch(t *testing.T) {
	s1, raw := providerPGStore(t)
	epoch1 := s1.epoch
	if epoch1 <= 0 || !s1.AuthorityHealthy(context.Background()) {
		t.Fatal("first authority is not healthy")
	}
	oldBackendPID := s1.backendPID
	db2, err := sql.Open("pgx", raw)
	if err != nil {
		t.Fatal(err)
	}
	var lockHeld bool
	if err = db2.QueryRowContext(context.Background(), "SELECT pg_try_advisory_lock($1)", ledger.ExpectedLockKey()).Scan(&lockHeld); err != nil {
		t.Fatal(err)
	}
	if lockHeld {
		t.Fatal("independent connection acquired active provider authority lock")
	}
	// A second provider cannot safely use a blocking advisory-lock call in a
	// bounded test, so the independent pg_try_advisory_lock result above is the
	// exclusion proof. After release, the old backend must have no advisory lock
	// even though the sql.DB remains open for this inspection query.
	s1.ReleaseAuthority()
	var oldLockCount int
	if err = db2.QueryRowContext(context.Background(), "SELECT count(*) FROM pg_locks WHERE pid=$1 AND locktype='advisory'", oldBackendPID).Scan(&oldLockCount); err != nil {
		t.Fatal(err)
	}
	if oldLockCount != 0 {
		t.Fatalf("released authority backend %d still has %d advisory locks", oldBackendPID, oldLockCount)
	}
	if err = db2.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if _, err = s2.AcquireAuthority(context.Background()); err != nil {
		t.Fatal(err)
	}
	s2.ReleaseAuthority()
	if _, err = s1.Issue(context.Background(), RequestData{}, time.Minute); err == nil {
		t.Fatal("stale authority mutated after releasing pinned connection")
	}
	s3, err := Open(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	defer s3.Close()
	epoch3, err := s3.AcquireAuthority(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer s3.ReleaseAuthority()
	if epoch3 <= epoch1 {
		t.Fatalf("epoch did not advance: first=%d restart=%d", epoch1, epoch3)
	}
	var lockCount int
	if err := s3.DB.QueryRowContext(context.Background(), "SELECT count(*) FROM pg_locks WHERE pid=$1 AND locktype='advisory'", s3.backendPID).Scan(&lockCount); err != nil {
		t.Fatal(err)
	}
	if lockCount == 0 {
		t.Fatal("pinned authority connection has no visible advisory lock")
	}
}

func TestProviderPostgresRestartPoisonsLiveRows(t *testing.T) {
	s1, raw := providerPGStore(t)
	oldIssued, _ := providerIssue(t, s1, time.Minute)
	oldPending, pendingReq := providerIssue(t, s1, time.Minute)
	if _, err := s1.beginConsume(context.Background(), pendingReq); err != nil {
		t.Fatal(err)
	}
	oldClaimed, claimedReq := providerIssue(t, s1, time.Minute)
	begun, err := s1.beginConsume(context.Background(), claimedReq)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s1.claimConsume(context.Background(), claimedReq, begun); err != nil {
		t.Fatal(err)
	}
	oldEpoch := s1.epoch
	s1.ReleaseAuthority()
	s2, err := Open(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	newEpoch, err := s2.AcquireAuthority(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer s2.ReleaseAuthority()
	if newEpoch <= oldEpoch {
		t.Fatalf("restart epoch did not advance old=%d new=%d", oldEpoch, newEpoch)
	}
	checks := []struct {
		id, wantAuth, wantIntent string
		request                  string
	}{
		{oldIssued.AuthorizationID, "REVOKED", "", ""},
		{oldPending.AuthorizationID, "REVOKED", "ABORTED", pendingReq.ConsumeRequestID},
		{oldClaimed.AuthorizationID, "CONSUME_UNKNOWN", "UNKNOWN", claimedReq.ConsumeRequestID},
	}
	for _, tc := range checks {
		var authState, intentState string
		if err := s2.DB.QueryRowContext(context.Background(), "SELECT state FROM d1l_provider.d1l_bootstrap_authorizations WHERE authorization_id=$1", tc.id).Scan(&authState); err != nil {
			t.Fatal(err)
		}
		if authState != tc.wantAuth {
			t.Fatalf("authorization %s state=%s want=%s", tc.id, authState, tc.wantAuth)
		}
		if tc.request != "" {
			if err := s2.DB.QueryRowContext(context.Background(), "SELECT state FROM d1l_provider.d1l_bootstrap_consume_intents WHERE consume_request_id=$1", tc.request).Scan(&intentState); err != nil {
				t.Fatal(err)
			}
			if intentState != tc.wantIntent {
				t.Fatalf("intent %s state=%s want=%s", tc.request, intentState, tc.wantIntent)
			}
		}
	}
}

func TestProviderPostgresIssueResolutionAndSecretBoundary(t *testing.T) {
	s, _ := providerPGStore(t)
	first, _ := providerIssue(t, s, time.Minute)
	duplicate, err := s.Issue(context.Background(), RequestData{ID: first.IssuerRequestID, AttemptID: first.AttemptID, Role: "deployment-runbook", Scope: first.Scope, Bindings: first.Bindings}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.SecretAvailable || duplicate.Envelope != "" || duplicate.State != ledger.Issued {
		t.Fatalf("duplicate issue returned secret material: %#v", duplicate)
	}
	var verifierLen int
	if err := s.DB.QueryRowContext(context.Background(), "SELECT octet_length(secret_verifier) FROM d1l_provider.d1l_bootstrap_authorizations WHERE authorization_id=$1", first.AuthorizationID).Scan(&verifierLen); err != nil || verifierLen != sha256.Size {
		t.Fatalf("verifier length=%d err=%v", verifierLen, err)
	}
	lost, _ := providerIssue(t, s, time.Minute)
	resolved, err := s.ResolveIssue(context.Background(), lost.IssuerRequestID)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.AuthState != ledger.Revoked || resolved.TerminalCode != "SECRET_UNAVAILABLE" {
		t.Fatalf("lost-secret resolution=%#v", resolved)
	}
	attempt := uuid.New().String()
	_, err = s.Issue(context.Background(), RequestData{ID: uuid.New().String(), AttemptID: attempt, Role: "deployment-runbook", Scope: ledger.ScopeControlCatalogInstall, Bindings: map[string]string{"operation": "install", "attempt_id": attempt, "target_id": "t", "installer_id": "i", "evidence_hash": "e"}}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Issue(context.Background(), RequestData{ID: uuid.New().String(), AttemptID: attempt, Role: "deployment-runbook", Scope: ledger.ScopeControlCatalogInstall, Bindings: map[string]string{"operation": "install", "attempt_id": attempt, "target_id": "t2", "installer_id": "i", "evidence_hash": "e2"}}, time.Minute)
	if err == nil {
		t.Fatal("attempt namespace allowed duplicate live attempt")
	}
	unknown := uuid.New().String()
	result, err := s.ResolveIssue(context.Background(), unknown)
	if err != nil {
		t.Fatal(err)
	}
	if result.AuthState != ledger.Cancelled {
		t.Fatalf("unknown issue request result=%#v", result)
	}
}

func TestProviderPostgresRawSecretAbsent(t *testing.T) {
	s, _ := providerPGStore(t)
	issued, _ := providerIssue(t, s, time.Minute)
	parsed, err := parseEnvelope(issued.Envelope)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(parsed.secret)
	var verifier []byte
	if err := s.DB.QueryRowContext(context.Background(), "SELECT secret_verifier FROM d1l_provider.d1l_bootstrap_authorizations WHERE authorization_id=$1", issued.AuthorizationID).Scan(&verifier); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(verifier, want[:]) {
		t.Fatal("stored verifier does not equal H(S)")
	}
	rows, err := s.DB.QueryContext(context.Background(), "SELECT column_name FROM information_schema.columns WHERE table_schema='d1l_provider' AND table_name='d1l_bootstrap_authorizations'")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.ToLower(column), "secret") && column != "secret_verifier" {
			t.Fatalf("raw-secret column exists: %s", column)
		}
	}
	var persisted string
	if err := s.DB.QueryRowContext(context.Background(), "SELECT to_jsonb(a)::text FROM d1l_provider.d1l_bootstrap_authorizations a WHERE authorization_id=$1", issued.AuthorizationID).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	encoded := base64.RawStdEncoding.EncodeToString(parsed.secret)
	if strings.Contains(persisted, encoded) || strings.Contains(persisted, parsedSecretText(parsed.secret)) {
		t.Fatalf("raw secret appears in persisted authority row")
	}
}

func parsedSecretText(secret []byte) string { return string(secret) }

func TestProviderPostgresIssueResolveRowSerialization(t *testing.T) {
	s, raw := providerPGStore(t)
	rid, attempt := uuid.New(), uuid.New()
	other, err := sql.Open("pgx", raw)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	conn, err := other.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(context.Background(), `INSERT INTO d1l_provider.d1l_issue_requests(issuer_request_id,issuer_role,attempt_id,state) VALUES($1,'deployment-runbook',$2,'REQUESTED')`, rid, attempt); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(context.Background(), `SELECT issuer_request_id FROM d1l_provider.d1l_issue_requests WHERE issuer_request_id=$1 FOR UPDATE`, rid); err != nil {
		t.Fatal(err)
	}
	resolved := make(chan ResolveResult, 1)
	resolveErr := make(chan error, 1)
	go func() {
		out, e := s.ResolveIssue(context.Background(), rid.String())
		resolved <- out
		resolveErr <- e
	}()
	select {
	case <-resolved:
		t.Fatal("ResolveIssue bypassed an independent Issue row lock")
	case <-time.After(100 * time.Millisecond):
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if err = <-resolveErr; err != nil {
		t.Fatal(err)
	}
	out := <-resolved
	if out.AuthState != ledger.AuthState("TERMINAL") || out.TerminalCode != "SECRET_UNAVAILABLE" {
		t.Fatalf("serialized ResolveIssue result=%#v", out)
	}
	issueOut, err := s.Issue(context.Background(), RequestData{ID: rid.String(), AttemptID: attempt.String(), Role: "deployment-runbook", Scope: ledger.ScopeControlCatalogInstall, Bindings: map[string]string{"operation": "install", "attempt_id": attempt.String(), "target_id": "t", "installer_id": "i", "evidence_hash": "e"}}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if issueOut.State == ledger.Issued || issueOut.Envelope != "" {
		t.Fatalf("serialized tombstone was resurrected: %#v", issueOut)
	}
}

func TestProviderPostgresConsumeBindingExpiryAndStatus(t *testing.T) {
	s, raw := providerPGStore(t)
	issued, req := providerIssue(t, s, time.Minute)
	bad := req
	bad.Nonce = base64.RawStdEncoding.EncodeToString(make([]byte, 16))
	if _, err := s.Consume(context.Background(), bad); err == nil {
		t.Fatal("wrong nonce consumed authorization")
	}
	var intents int
	if err := s.DB.QueryRowContext(context.Background(), "SELECT count(*) FROM d1l_provider.d1l_bootstrap_consume_intents WHERE authorization_id=$1", issued.AuthorizationID).Scan(&intents); err != nil || intents != 0 {
		t.Fatalf("invalid binding created intents=%d err=%v", intents, err)
	}
	inspect, err := s.Inspect(context.Background(), issued.AuthorizationID)
	if err != nil || inspect.State != ledger.Issued {
		t.Fatalf("read-only inspect=%#v err=%v", inspect, err)
	}
	pending, err := s.beginConsume(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	inspect, err = s.Inspect(context.Background(), issued.AuthorizationID)
	// BeginConsume intentionally leaves the parent ISSUED while exposing the
	// durable PENDING child and its owner-bound request ID.
	if err != nil || inspect.State != ledger.Issued || inspect.IntentState != ledger.Pending || inspect.ConsumeRequestID != req.ConsumeRequestID {
		t.Fatalf("pending presentation inspect=%#v err=%v", inspect, err)
	}
	if _, err = s.claimConsume(context.Background(), req, pending); err != nil {
		t.Fatal(err)
	}
	inspect, err = s.Inspect(context.Background(), issued.AuthorizationID)
	if err != nil || inspect.State != ledger.ConsumePending || inspect.IntentState != ledger.Claimed {
		t.Fatalf("claimed inspect=%#v err=%v", inspect, err)
	}
	resolved, err := s.ResolveConsume(context.Background(), ResolveConsumeRequest{ConsumeRequestID: req.ConsumeRequestID, AuthorizationID: issued.AuthorizationID, IssuerRequestID: req.IssuerRequestID, Operation: req.Operation, AttemptID: req.AttemptID, TargetID: req.TargetID, InstallerID: req.InstallerID, EvidenceHash: req.EvidenceHash, Scope: req.Scope, Epoch: req.Epoch, Nonce: req.Nonce}, false)
	if err != nil || resolved.IntentState != ledger.Unknown || resolved.AuthState != ledger.AuthState("CONSUME_UNKNOWN") {
		t.Fatalf("claimed resolve=%#v err=%v", resolved, err)
	}
	short, shortReq := providerIssue(t, s, 20*time.Millisecond)
	time.Sleep(40 * time.Millisecond)
	expired, err := s.Consume(context.Background(), shortReq)
	if err != nil || expired.State != ledger.Aborted || expired.AuthorizationState != "EXPIRED" {
		t.Fatalf("expired outcome=%#v err=%v", expired, err)
	}
	var state string
	if err := s.DB.QueryRowContext(context.Background(), "SELECT state FROM d1l_provider.d1l_bootstrap_authorizations WHERE authorization_id=$1", short.AuthorizationID).Scan(&state); err != nil || state != "EXPIRED" {
		t.Fatalf("expired state=%s err=%v", state, err)
	}
	abandoned, abandonedReq := providerIssue(t, s, 20*time.Millisecond)
	if _, err := s.beginConsume(context.Background(), abandonedReq); err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	fresh := abandonedReq
	fresh.ConsumeRequestID = uuid.New().String()
	freshOut, err := s.Consume(context.Background(), fresh)
	if err != nil || freshOut.State != ledger.Aborted || freshOut.AuthorizationState != "EXPIRED" {
		t.Fatalf("expired abandoned consume=%#v err=%v", freshOut, err)
	}
	var childState string
	if err := s.DB.QueryRowContext(context.Background(), "SELECT state FROM d1l_provider.d1l_bootstrap_consume_intents WHERE authorization_id=$1", abandoned.AuthorizationID).Scan(&childState); err != nil || childState != "ABORTED" {
		t.Fatalf("expired abandoned child=%s err=%v", childState, err)
	}
	// An independent connection can hold the parent row; provider mutation must
	// wait for that lock rather than invert the parent/child order.
	blocked, blockedReq := providerIssue(t, s, time.Minute)
	other, err := sql.Open("pgx", raw)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	conn, err := other.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(context.Background(), "SELECT authorization_id FROM d1l_provider.d1l_bootstrap_authorizations WHERE authorization_id=$1 FOR UPDATE", blocked.AuthorizationID); err != nil {
		t.Fatal(err)
	}
	result := make(chan ConsumeResult, 1)
	errorsCh := make(chan error, 1)
	go func() {
		out, e := s.beginConsume(context.Background(), blockedReq)
		result <- out
		errorsCh <- e
	}()
	select {
	case <-result:
		t.Fatal("provider mutation bypassed an independent parent row lock")
	case <-time.After(100 * time.Millisecond):
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if err = <-errorsCh; err != nil {
		t.Fatal(err)
	}
	if out := <-result; out.State != ledger.Pending || out.AuthorizationState != "ISSUED" {
		t.Fatalf("cross-connection begin result=%#v", out)
	}
}

func TestProviderPostgresPendingCollisionAndOwnerInspect(t *testing.T) {
	s, _ := providerPGStore(t)
	issued, req := providerIssue(t, s, time.Minute)
	if _, err := s.beginConsume(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	second := req
	second.ConsumeRequestID = uuid.New().String()
	got, err := s.beginConsume(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != ledger.Pending || got.Detail != "IN_PROGRESS" || got.PendingConsumeRequestID != req.ConsumeRequestID {
		t.Fatalf("pending collision result=%#v", got)
	}
	if _, err := s.Inspect(context.Background(), issued.AuthorizationID, "store-direct"); err != nil {
		t.Fatalf("owner inspect rejected: %v", err)
	}
	if _, err := s.Inspect(context.Background(), issued.AuthorizationID, "spiffe://power-iot/a3/other-runner"); err == nil {
		t.Fatal("non-owner inspect accepted live pending intent")
	}
}

func TestProviderPostgresConcurrentConsumeOneWinner(t *testing.T) {
	s, _ := providerPGStore(t)
	issued, req := providerIssue(t, s, time.Minute)
	var wg sync.WaitGroup
	results := make(chan ConsumeResult, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := s.Consume(context.Background(), req)
			results <- out
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	var consumed int
	for out := range results {
		if out.State == ledger.IntentConsumed {
			consumed++
		}
	}
	if consumed != 2 {
		t.Fatalf("duplicate stable request did not return durable consumed result twice: %d", consumed)
	}
	for err := range errs {
		if err != nil && !errors.Is(err, ledger.ErrUnknownCommit) {
			t.Fatalf("duplicate consume error=%v", err)
		}
	}
	var parent, child string
	if err := s.DB.QueryRowContext(context.Background(), "SELECT state FROM d1l_provider.d1l_bootstrap_authorizations WHERE authorization_id=$1", issued.AuthorizationID).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRowContext(context.Background(), "SELECT state FROM d1l_provider.d1l_bootstrap_consume_intents WHERE consume_request_id=$1", req.ConsumeRequestID).Scan(&child); err != nil {
		t.Fatal(err)
	}
	if parent != "CONSUMED" || child != "CONSUMED" {
		t.Fatalf("final states parent=%s child=%s", parent, child)
	}
}

func TestProviderPostgresConcurrentAuthorityChecksAndConsumeNoPanic(t *testing.T) {
	s, _ := providerPGStore(t)
	issued, req := providerIssue(t, s, time.Minute)
	start := make(chan struct{})
	type result struct {
		out     ConsumeResult
		err     error
		healthy bool
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := req
			local.ConsumeRequestID = uuid.New().String()
			<-start
			healthy := s.AuthorityHealthy(context.Background())
			out, err := s.Consume(context.Background(), local)
			results <- result{out: out, err: err, healthy: healthy}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	var consumed, alreadyConsumed int
	for got := range results {
		if !got.healthy {
			t.Fatal("concurrent authority check reported unhealthy")
		}
		if got.out.State == ledger.IntentConsumed && got.out.Detail == "" {
			consumed++
			if got.err != nil && !errors.Is(got.err, ledger.ErrUnknownCommit) {
				t.Fatalf("winning consume error=%v", got.err)
			}
		} else if got.out.State == ledger.IntentConsumed && got.out.Detail == "durable outcome" && got.err == nil {
			alreadyConsumed++
		} else if got.err == nil {
			t.Fatalf("losing consume returned no durable outcome: %#v", got.out)
		}
	}
	if consumed != 1 || alreadyConsumed != 1 {
		t.Fatalf("concurrent distinct consumes produced winners=%d already_consumed=%d", consumed, alreadyConsumed)
	}
	var parent string
	if err := s.DB.QueryRowContext(context.Background(), "SELECT state FROM d1l_provider.d1l_bootstrap_authorizations WHERE authorization_id=$1", issued.AuthorizationID).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	if parent != "CONSUMED" {
		t.Fatalf("final parent state=%s, want CONSUMED", parent)
	}
}

func TestProviderPostgresAuthorityStartupFailureDiscardsLock(t *testing.T) {
	s, raw := providerPGStore(t)
	oldEpoch := s.epoch
	if _, err := s.DB.ExecContext(context.Background(), "DELETE FROM d1l_provider.schema_version WHERE version=1"); err != nil {
		t.Fatal(err)
	}
	s.ReleaseAuthority()
	s2, err := Open(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err = s2.AcquireAuthority(ctx); err == nil {
		t.Fatal("missing provider schema version admitted")
	}
	// Restore the disposable database to a valid state for subsequent tests and
	// cleanup, without changing production migration behavior.
	if _, err = s2.DB.ExecContext(context.Background(), "INSERT INTO d1l_provider.schema_version(version) VALUES(1)"); err != nil {
		t.Fatal(err)
	}
	// AcquireAuthority failed after taking the advisory lock. A fresh provider
	// must acquire promptly, proving the failed session was physically lost.
	s3, err := Open(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	defer s3.Close()
	freshEpoch, err := s3.AcquireAuthority(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer s3.ReleaseAuthority()
	if freshEpoch <= oldEpoch {
		t.Fatalf("epoch did not advance after failed authority startup: old=%d fresh=%d", oldEpoch, freshEpoch)
	}
}

func TestProviderPostgresUsesApprovedEndpoint(t *testing.T) {
	raw := strings.TrimSpace(os.Getenv("D1L_PROVIDER_DATABASE_URL"))
	if raw == "" {
		t.Skip("D1L_PROVIDER_DATABASE_URL is not configured; provider PostgreSQL checks skipped")
	}
	if err := validateProviderTestURL(raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "power_iot_db") {
		t.Fatal("target database name appeared in provider DSN")
	}
}
