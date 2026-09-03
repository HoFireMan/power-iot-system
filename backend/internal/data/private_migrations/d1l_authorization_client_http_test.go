package migrations

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func TestAuthorizationClientDurableReplayWire(t *testing.T) {
	aid, rid, cid, attempt := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	c := d1lClientForTest(d1lRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return d1lResponse(200, `{"authorization_id":"`+aid+`","consume_request_id":"`+cid+`","state":"CONSUMED","authorization_state":"CONSUMED","terminal_state":"CONSUMED","terminal_code":"CONSUMED","detail":"durable outcome"}`), nil
	}))
	out, err := c.Consume(context.Background(), ConsumeRequest{ConsumeRequestID: cid, AuthorizationID: aid, IssuerRequestID: rid, Operation: "install", AttemptID: attempt, TargetID: "target", InstallerID: "installer", EvidenceHash: "evidence", Scope: ScopeControlCatalogInstall, Nonce: "AAAAAAAAAAAAAAAAAAAAAA", Envelope: []byte("opaque"), Epoch: 3})
	if err != nil || out.Outcome != OutcomeAlreadyConsumed {
		t.Fatalf("durable replay=%+v err=%v", out, err)
	}
}

func TestAuthorizationClientInspectResolveAndHealthWire(t *testing.T) {
	aid, rid, cid, attempt := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	calls := 0
	c := d1lClientForTest(d1lRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		switch r.URL.Path {
		case "/v1/authorizations/" + aid + ":inspect":
			return d1lResponse(200, `{"authorization_id":"`+aid+`","issuer_request_id":"`+rid+`","attempt_id":"`+attempt+`","state":"ISSUED","epoch":3,"nonce":"AAAAAAAAAAAAAAAAAAAAAA","expires_at":"2028-01-01T00:00:00Z","scope":"allow_control_catalog_install","bindings":{"operation":"install","attempt_id":"`+attempt+`","target_id":"target","installer_id":"installer","evidence_hash":"evidence"}}`), nil
		case "/v1/authorizations/" + aid + ":resolve":
			var wire map[string]any
			if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
				t.Fatal(err)
			}
			if _, ok := wire["authorization_id"]; ok {
				t.Fatal("authorization_id must remain path-bound, not resolve body data")
			}
			for _, key := range []string{"consume_request_id", "issuer_request_id", "operation", "attempt_id", "target_id", "installer_id", "evidence_hash", "scope", "epoch", "nonce"} {
				if _, ok := wire[key]; !ok {
					t.Fatalf("resolve field %q missing: %#v", key, wire)
				}
			}
			return d1lResponse(200, `{"authorization_id":"`+aid+`","issuer_request_id":"`+rid+`","consume_request_id":"`+cid+`","authorization_state":"CONSUME_UNKNOWN","intent_state":"UNKNOWN","terminal_state":"CONSUME_UNKNOWN","terminal_code":"CONSUME_OUTCOME_UNKNOWN"}`), nil
		case "/healthz":
			return d1lResponse(200, `{"status":"ok"}`), nil
		case "/readyz":
			return d1lResponse(200, `{"status":"ready"}`), nil
		default:
			return d1lResponse(404, `{"error":"not found"}`), nil
		}
	}))
	if out, err := c.Inspect(context.Background(), aid); err != nil || out.State != AuthorizationIssued || out.Bindings["target_id"] != "target" {
		t.Fatalf("inspect=%+v err=%v", out, err)
	}
	out, err := c.Resolve(context.Background(), ResolveRequest{ConsumeRequestID: cid, AuthorizationID: aid, IssuerRequestID: rid, Operation: "install", AttemptID: attempt, TargetID: "target", InstallerID: "installer", EvidenceHash: "evidence", Scope: ScopeControlCatalogInstall, Epoch: 3, Nonce: "AAAAAAAAAAAAAAAAAAAAAA"})
	if err != nil || out.AuthState != AuthorizationConsumeUnknown || out.IntentState != ConsumeUnknown {
		t.Fatalf("resolve=%+v err=%v", out, err)
	}
	if health, err := c.Health(context.Background()); err != nil || health.Status != "ok" || health.Outcome != OutcomeSuccess {
		t.Fatalf("health=%+v err=%v", health, err)
	}
	if ready, err := c.Attestation(context.Background()); err != nil || ready.Status != "ready" || ready.Outcome != OutcomeSuccess {
		t.Fatalf("ready=%+v err=%v", ready, err)
	}
	if calls != 4 {
		t.Fatalf("calls=%d", calls)
	}
	if _, err := c.Resolve(context.Background(), ResolveRequest{AuthorizationID: aid, IssuerRequestID: rid, Operation: "install", AttemptID: attempt, TargetID: "target", InstallerID: "installer", EvidenceHash: "evidence", Scope: ScopeControlCatalogInstall, Epoch: 3, Nonce: "AAAAAAAAAAAAAAAAAAAAAA"}); err == nil {
		t.Fatal("missing consume request accepted")
	}
}
