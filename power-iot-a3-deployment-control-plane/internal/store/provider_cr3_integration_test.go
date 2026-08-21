package store

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"power-iot-a3-deployment-control-plane/internal/ledger"
	"power-iot-a3-deployment-control-plane/migrations"
)

func TestCR3BeginClaimResolvePair(t *testing.T) {
	url := strings.TrimSpace(os.Getenv("D1L_PROVIDER_DATABASE_URL"))
	if url == "" {
		t.Skip("D1L_PROVIDER_DATABASE_URL is not configured; provider PostgreSQL checks skipped")
	}
	if err := validateProviderTestURL(url); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.DB.ExecContext(ctx, migrations.Bootstrap); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcquireAuthority(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.ReleaseAuthority()

	issue := func(t *testing.T) (IssueResult, ConsumeRequest) {
		t.Helper()
		rid, attempt := uuid.New(), uuid.New()
		bindings := map[string]string{"operation": "install", "attempt_id": attempt.String(), "target_id": "target-" + rid.String(), "installer_id": "installer", "evidence_hash": "evidence-" + rid.String()}
		out, e := s.Issue(ctx, RequestData{ID: rid.String(), AttemptID: attempt.String(), Role: "deployment-runbook", Scope: ledger.ScopeControlCatalogInstall, Bindings: bindings}, time.Minute)
		if e != nil {
			t.Fatal(e)
		}
		cid := uuid.New()
		return out, ConsumeRequest{ConsumeRequestID: cid.String(), AuthorizationID: out.AuthorizationID, IssuerRequestID: out.IssuerRequestID, Operation: bindings["operation"], AttemptID: out.AttemptID, TargetID: bindings["target_id"], InstallerID: bindings["installer_id"], EvidenceHash: bindings["evidence_hash"], Scope: out.Scope, Epoch: out.Epoch, Nonce: out.Nonce, Envelope: out.Envelope}
	}

	first, req := issue(t)
	begin, err := s.beginConsume(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if begin.State != ledger.Pending || begin.AuthorizationState != "ISSUED" {
		t.Fatalf("begin result = %#v, want PENDING/ISSUED", begin)
	}
	var parentState, childState string
	if err = s.DB.QueryRowContext(ctx, `SELECT state FROM d1l_provider.d1l_bootstrap_authorizations WHERE authorization_id=$1`, first.AuthorizationID).Scan(&parentState); err != nil {
		t.Fatal(err)
	}
	if err = s.DB.QueryRowContext(ctx, `SELECT state FROM d1l_provider.d1l_bootstrap_consume_intents WHERE consume_request_id=$1`, req.ConsumeRequestID).Scan(&childState); err != nil {
		t.Fatal(err)
	}
	if parentState != "ISSUED" || childState != "PENDING" {
		t.Fatalf("after begin parent=%s child=%s, want ISSUED/PENDING", parentState, childState)
	}
	claimed, err := s.claimConsume(ctx, req, begin)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.State != ledger.Claimed || claimed.AuthorizationState != "CONSUME_PENDING" {
		t.Fatalf("claim result = %#v, want CLAIMED/CONSUME_PENDING", claimed)
	}
	if err = s.DB.QueryRowContext(ctx, `SELECT state FROM d1l_provider.d1l_bootstrap_authorizations WHERE authorization_id=$1`, first.AuthorizationID).Scan(&parentState); err != nil {
		t.Fatal(err)
	}
	if err = s.DB.QueryRowContext(ctx, `SELECT state FROM d1l_provider.d1l_bootstrap_consume_intents WHERE consume_request_id=$1`, req.ConsumeRequestID).Scan(&childState); err != nil {
		t.Fatal(err)
	}
	if parentState != "CONSUME_PENDING" || childState != "CLAIMED" {
		t.Fatalf("after claim parent=%s child=%s, want CONSUME_PENDING/CLAIMED", parentState, childState)
	}

	second, pendingReq := issue(t)
	pending, err := s.beginConsume(ctx, pendingReq)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := s.ResolveConsume(ctx, ResolveConsumeRequest{ConsumeRequestID: pendingReq.ConsumeRequestID, AuthorizationID: second.AuthorizationID, IssuerRequestID: second.IssuerRequestID, Operation: pendingReq.Operation, AttemptID: pendingReq.AttemptID, TargetID: pendingReq.TargetID, InstallerID: pendingReq.InstallerID, EvidenceHash: pendingReq.EvidenceHash, Scope: pendingReq.Scope, Epoch: pendingReq.Epoch, Nonce: pendingReq.Nonce}, false)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.AuthState != ledger.Revoked || resolved.IntentState != ledger.Aborted {
		t.Fatalf("resolve result = %#v, want REVOKED/ABORTED", resolved)
	}
	resurrect, err := s.claimConsume(ctx, pendingReq, pending)
	if err != nil {
		t.Fatal(err)
	}
	if resurrect.State != ledger.Aborted || resurrect.AuthorizationState != "REVOKED" {
		t.Fatalf("forbidden resurrection result = %#v", resurrect)
	}
	if err = s.DB.QueryRowContext(ctx, `SELECT state FROM d1l_provider.d1l_bootstrap_authorizations WHERE authorization_id=$1`, second.AuthorizationID).Scan(&parentState); err != nil {
		t.Fatal(err)
	}
	if err = s.DB.QueryRowContext(ctx, `SELECT state FROM d1l_provider.d1l_bootstrap_consume_intents WHERE consume_request_id=$1`, pendingReq.ConsumeRequestID).Scan(&childState); err != nil {
		t.Fatal(err)
	}
	if parentState != "REVOKED" || childState != "ABORTED" {
		t.Fatalf("after forbidden resurrection parent=%s child=%s, want REVOKED/ABORTED", parentState, childState)
	}
}
