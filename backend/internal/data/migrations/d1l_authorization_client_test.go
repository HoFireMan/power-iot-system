package migrations

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type d1lRoundTripFunc func(*http.Request) (*http.Response, error)

func (f d1lRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func d1lClientForTest(rt http.RoundTripper) *AuthorizationClient {
	return &AuthorizationClient{endpoint: "https://provider.invalid", http: &http.Client{Transport: rt, Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect refused") }}}
}

func d1lResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func TestAuthorizationClientSurfaceIsRunnerOnly(t *testing.T) {
	typ := reflect.TypeOf((*AuthorizationClient)(nil))
	for _, forbidden := range []string{"Issue", "ResolveIssue", "Revoke", "Expire", "SetEpoch", "Admin", "Recovery"} {
		if _, ok := typ.MethodByName(forbidden); ok {
			t.Fatalf("forbidden client method %s exposed", forbidden)
		}
	}
	for _, allowed := range []string{"Inspect", "Consume", "Resolve", "Health", "Attestation"} {
		if _, ok := typ.MethodByName(allowed); !ok {
			t.Fatalf("required client method %s missing", allowed)
		}
	}
}

func TestAuthorizationClientExactConsumeWireAndBindingValidation(t *testing.T) {
	aid, rid, cid, attempt := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	nonce := "AAAAAAAAAAAAAAAAAAAAAA" // 16 zero bytes, raw base64
	secret := []byte("opaque-secret-that-must-not-be-in-errors")
	var got wireConsumeRequest
	c := d1lClientForTest(d1lRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/authorizations/"+aid+":consume" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		return d1lResponse(http.StatusOK, `{"authorization_id":"`+aid+`","consume_request_id":"`+cid+`","state":"CONSUMED","authorization_state":"CONSUMED","terminal_state":"CONSUMED","terminal_code":"CONSUMED"}`), nil
	}))
	out, err := c.Consume(context.Background(), ConsumeRequest{ConsumeRequestID: cid, AuthorizationID: aid, IssuerRequestID: rid, Operation: "install", AttemptID: attempt, TargetID: "target", InstallerID: "installer", EvidenceHash: "evidence", Scope: ScopeControlCatalogInstall, Nonce: nonce, Envelope: secret, Epoch: 7})
	if err != nil || out.State != ConsumeConsumed {
		t.Fatalf("consume result=%+v err=%v", out, err)
	}
	if got.Envelope != string(secret) || got.AuthorizationID != aid || got.ConsumeRequestID != cid || got.Epoch != 7 || got.Scope != ScopeControlCatalogInstall {
		t.Fatalf("wire binding mismatch: %+v", got)
	}
	if _, err := c.Consume(context.Background(), ConsumeRequest{ConsumeRequestID: cid, AuthorizationID: aid, IssuerRequestID: rid, Operation: "install", AttemptID: attempt, TargetID: "target", InstallerID: "installer", EvidenceHash: "evidence", Scope: "wrong", Nonce: nonce, Envelope: secret, Epoch: 7}); err == nil {
		t.Fatal("unsupported scope accepted")
	}
}

func TestConsumeRequestFormattingRedactsEnvelope(t *testing.T) {
	secret := []byte("raw-capability-presentation")
	formatted := fmt.Sprintf("%+v", ConsumeRequest{AuthorizationID: "authorization", ConsumeRequestID: "request", Envelope: secret})
	if strings.Contains(formatted, string(secret)) {
		t.Fatalf("consume request formatting leaked envelope: %s", formatted)
	}
}

func TestAuthorizationClientUnknownPreservesConsumeRequestIDAndRedactsEnvelope(t *testing.T) {
	aid, rid, cid, attempt := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	secret := []byte("do-not-disclose")
	c := d1lClientForTest(d1lRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection reset by peer")
	}))
	out, err := c.Consume(context.Background(), ConsumeRequest{ConsumeRequestID: cid, AuthorizationID: aid, IssuerRequestID: rid, Operation: "install", AttemptID: attempt, TargetID: "target", InstallerID: "installer", EvidenceHash: "evidence", Scope: ScopeControlCatalogInstall, Nonce: "AAAAAAAAAAAAAAAAAAAAAA", Envelope: secret, Epoch: 7})
	if !errors.Is(err, ErrProviderUnknown) || out.State != ConsumeUnknown || out.ConsumeRequestID != cid {
		t.Fatalf("unknown result=%+v err=%v", out, err)
	}
	if strings.Contains(err.Error(), string(secret)) {
		t.Fatal("opaque envelope leaked in error")
	}
}

func TestAuthorizationClientMalformedAndMismatchedConsumeResponsesAreUnknown(t *testing.T) {
	aid, rid, cid, attempt := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	base := ConsumeRequest{ConsumeRequestID: cid, AuthorizationID: aid, IssuerRequestID: rid, Operation: "install", AttemptID: attempt, TargetID: "target", InstallerID: "installer", EvidenceHash: "evidence", Scope: ScopeControlCatalogInstall, Nonce: "AAAAAAAAAAAAAAAAAAAAAA", Envelope: []byte("opaque"), Epoch: 7}
	for _, response := range []string{"{", `{"authorization_id":"` + uuid.NewString() + `","consume_request_id":"` + cid + `","state":"CONSUMED"}`} {
		c := d1lClientForTest(d1lRoundTripFunc(func(*http.Request) (*http.Response, error) { return d1lResponse(http.StatusOK, response), nil }))
		out, err := c.Consume(context.Background(), base)
		if !errors.Is(err, ErrProviderUnknown) || out.State != ConsumeUnknown || out.ConsumeRequestID != cid {
			t.Fatalf("response=%q result=%+v err=%v", response, out, err)
		}
	}
}

func TestAuthorizationClientRejectsContradictoryConsumeState(t *testing.T) {
	aid, rid, cid, attempt := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	c := d1lClientForTest(d1lRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return d1lResponse(http.StatusOK, `{"authorization_id":"`+aid+`","consume_request_id":"`+cid+`","state":"CONSUMED","authorization_state":"REVOKED"}`), nil
	}))
	out, err := c.Consume(context.Background(), ConsumeRequest{ConsumeRequestID: cid, AuthorizationID: aid, IssuerRequestID: rid, Operation: "install", AttemptID: attempt, TargetID: "target", InstallerID: "installer", EvidenceHash: "evidence", Scope: ScopeControlCatalogInstall, Nonce: "AAAAAAAAAAAAAAAAAAAAAA", Envelope: []byte("opaque"), Epoch: 7})
	if !errors.Is(err, ErrProviderUnknown) || out.Outcome != OutcomeUnknown || out.State != ConsumeUnknown {
		t.Fatalf("contradictory response accepted: result=%+v err=%v", out, err)
	}
}

func TestValidConsumeRejectsIncompleteLifecyclePairs(t *testing.T) {
	aid, cid := uuid.NewString(), uuid.NewString()
	base := ConsumeRequest{AuthorizationID: aid, ConsumeRequestID: cid}
	for _, out := range []ConsumeResult{
		{AuthorizationID: aid, ConsumeRequestID: cid, State: ConsumePending, AuthorizationState: "", PendingConsumeRequestID: cid},
		{AuthorizationID: aid, ConsumeRequestID: cid, State: ConsumeClaimed, AuthorizationState: string(AuthorizationIssued), PendingConsumeRequestID: cid},
		{AuthorizationID: aid, ConsumeRequestID: cid, State: ConsumeConsumed, AuthorizationState: string(AuthorizationConsumed), TerminalState: string(AuthorizationRevoked)},
		{AuthorizationID: aid, ConsumeRequestID: cid, State: ConsumePending, AuthorizationState: string(AuthorizationIssued), TerminalState: string(AuthorizationConsumed)},
		{AuthorizationID: aid, ConsumeRequestID: cid, State: ConsumeUnknown, AuthorizationState: string(AuthorizationRevoked)},
	} {
		if validConsume(out, base) {
			t.Fatalf("incomplete lifecycle accepted: %#v", out)
		}
	}
	resolveIn := ResolveRequest{AuthorizationID: aid, IssuerRequestID: uuid.NewString(), ConsumeRequestID: cid}
	if validResolve(ResolveResult{AuthorizationID: aid, IssuerRequestID: resolveIn.IssuerRequestID, ConsumeRequestID: cid, AuthState: AuthorizationConsumed, IntentState: ConsumeConsumed, TerminalState: string(AuthorizationRevoked), TerminalCode: "REVOKED"}, resolveIn) {
		t.Fatal("contradictory resolve terminal state accepted")
	}
}

func TestAuthorizationClientNoAutomaticRetryAndHTTPClassification(t *testing.T) {
	aid, rid, cid, attempt := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	base := ConsumeRequest{ConsumeRequestID: cid, AuthorizationID: aid, IssuerRequestID: rid, Operation: "install", AttemptID: attempt, TargetID: "target", InstallerID: "installer", EvidenceHash: "evidence", Scope: ScopeControlCatalogInstall, Nonce: "AAAAAAAAAAAAAAAAAAAAAA", Envelope: []byte("opaque"), Epoch: 7}
	for _, tc := range []struct {
		status  int
		kind    error
		outcome ProviderOutcome
	}{
		{http.StatusUnauthorized, ErrProviderUnauthorized, OutcomeUnauthorized},
		{http.StatusForbidden, ErrProviderUnauthorized, OutcomeUnauthorized},
		{http.StatusConflict, ErrBindingMismatch, OutcomeBindingMismatch},
		{http.StatusServiceUnavailable, ErrProviderUnavailable, OutcomeUnavailable},
	} {
		calls := 0
		c := d1lClientForTest(d1lRoundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return d1lResponse(tc.status, `{"error":"request rejected"}`), nil
		}))
		out, err := c.Consume(context.Background(), base)
		if calls != 1 {
			t.Fatalf("status %d: RoundTrip calls=%d, want exactly one", tc.status, calls)
		}
		if !errors.Is(err, tc.kind) || out.Outcome != tc.outcome || out.ConsumeRequestID != cid {
			t.Fatalf("status %d: result=%+v err=%v", tc.status, out, err)
		}
	}
}

func TestAuthorizationClientRedirectsAreRefused(t *testing.T) {
	aid := uuid.NewString()
	calls := 0
	c := d1lClientForTest(d1lRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{"https://other.invalid/"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	}))
	out, err := c.Inspect(context.Background(), aid)
	if calls != 1 || !errors.Is(err, ErrProviderUnknown) || out.Outcome != OutcomeUnknown {
		t.Fatalf("redirect result=%+v err=%v calls=%d", out, err, calls)
	}
}

func TestAuthorizationClientOutcomeMapping(t *testing.T) {
	aid, rid, cid, attempt := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	base := ConsumeRequest{ConsumeRequestID: cid, AuthorizationID: aid, IssuerRequestID: rid, Operation: "install", AttemptID: attempt, TargetID: "target", InstallerID: "installer", EvidenceHash: "evidence", Scope: ScopeControlCatalogInstall, Nonce: "AAAAAAAAAAAAAAAAAAAAAA", Envelope: []byte("opaque"), Epoch: 7}
	for _, tc := range []struct {
		state, auth, terminal string
		want                  ProviderOutcome
	}{
		{"PENDING", "ISSUED", "", OutcomeInProgress},
		{"CLAIMED", "CONSUME_PENDING", "", OutcomeInProgress},
		{"CONSUMED", "CONSUMED", "", OutcomeSuccess},
		{"CONSUMED", "CONSUMED", "durable outcome", OutcomeAlreadyConsumed},
		{"ABORTED", "EXPIRED", "EXPIRED", OutcomeExpired},
		{"ABORTED", "REVOKED", "REVOKED", OutcomeRevoked},
		{"UNKNOWN", "CONSUME_UNKNOWN", "CONSUME_OUTCOME_UNKNOWN", OutcomeUnknown},
	} {
		terminalCode := tc.terminal
		if tc.state == "CONSUMED" && terminalCode == "" {
			terminalCode = "CONSUMED"
		}
		if terminalCode == "durable outcome" {
			terminalCode = "CONSUMED"
		}
		body := `{"authorization_id":"` + aid + `","consume_request_id":"` + cid + `","state":"` + tc.state + `","authorization_state":"` + tc.auth + `","terminal_state":"` + tc.auth + `","terminal_code":"` + terminalCode + `","detail":"` + tc.terminal + `"}`
		// The durable-outcome marker is carried in detail; terminal fields are
		// otherwise provider-shaped and deliberately not interpreted as secret.
		if tc.terminal == "" && tc.state != "CONSUMED" {
			body = `{"authorization_id":"` + aid + `","consume_request_id":"` + cid + `","state":"` + tc.state + `","authorization_state":"` + tc.auth + `","detail":"` + tc.terminal + `"}`
		}
		if tc.state == "PENDING" || tc.state == "CLAIMED" {
			body = strings.TrimSuffix(body, "}") + `,"pending_consume_request_id":"` + cid + `"}`
		}
		c := d1lClientForTest(d1lRoundTripFunc(func(*http.Request) (*http.Response, error) { return d1lResponse(http.StatusOK, body), nil }))
		out, err := c.Consume(context.Background(), base)
		if err != nil || out.Outcome != tc.want {
			t.Fatalf("state=%s auth=%s result=%+v err=%v", tc.state, tc.auth, out, err)
		}
	}
}

func TestAuthorizationClientFreshRequestReportsPendingOwnerWithoutChangingRequestID(t *testing.T) {
	aid, rid, cid, owner, attempt := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	base := ConsumeRequest{ConsumeRequestID: cid, AuthorizationID: aid, IssuerRequestID: rid, Operation: "install", AttemptID: attempt, TargetID: "target", InstallerID: "installer", EvidenceHash: "evidence", Scope: ScopeControlCatalogInstall, Nonce: "AAAAAAAAAAAAAAAAAAAAAA", Envelope: []byte("opaque"), Epoch: 7}
	c := d1lClientForTest(d1lRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return d1lResponse(http.StatusOK, `{"authorization_id":"`+aid+`","consume_request_id":"`+cid+`","state":"CLAIMED","authorization_state":"CONSUME_PENDING","detail":"IN_PROGRESS","pending_consume_request_id":"`+owner+`"}`), nil
	}))
	out, err := c.Consume(context.Background(), base)
	if err != nil || out.Outcome != OutcomeInProgress || out.ConsumeRequestID != cid || out.PendingConsumeRequestID != owner {
		t.Fatalf("pending outcome=%+v err=%v", out, err)
	}
}

func TestAuthorizationClientTLSIdentityValidation(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "runner"},
		URIs:         []*url.URL{mustTestURI(t, "spiffe://power-iot/a3/not-runner")},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Minute),
	}, &x509.Certificate{SerialNumber: big.NewInt(1)}, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewAuthorizationClient(AuthorizationClientConfig{
		Endpoint: "https://provider", TrustRoots: x509.NewCertPool(),
		ExpectedProviderURI: "spiffe://power-iot/a3/d1l-provider",
		ClientCertificate:   tls.Certificate{Certificate: [][]byte{der}}, Timeout: time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("wrong client identity accepted: %v", err)
	}
}

func mustTestURI(t *testing.T, raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestAuthorizationClientTimeoutIsUnknownAndConfigIsFailClosed(t *testing.T) {
	aid, rid, cid, attempt := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	c := d1lClientForTest(d1lRoundTripFunc(func(*http.Request) (*http.Response, error) { return nil, context.DeadlineExceeded }))
	out, err := c.Consume(context.Background(), ConsumeRequest{ConsumeRequestID: cid, AuthorizationID: aid, IssuerRequestID: rid, Operation: "install", AttemptID: attempt, TargetID: "target", InstallerID: "installer", EvidenceHash: "evidence", Scope: ScopeControlCatalogInstall, Nonce: "AAAAAAAAAAAAAAAAAAAAAA", Envelope: []byte("opaque"), Epoch: 7})
	if !errors.Is(err, ErrProviderUnknown) || out.State != ConsumeUnknown {
		t.Fatalf("timeout result=%+v err=%v", out, err)
	}
	if _, err := NewAuthorizationClient(AuthorizationClientConfig{Endpoint: "http://provider", Timeout: time.Second}); err == nil {
		t.Fatal("plaintext endpoint accepted")
	}
	if _, err := NewAuthorizationClient(AuthorizationClientConfig{Endpoint: "https://provider", ExpectedProviderURI: "spiffe://power-iot/a3/d1l-provider", Timeout: time.Second}); err == nil {
		t.Fatal("missing trust roots/client credentials accepted")
	}
}
