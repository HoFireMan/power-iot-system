package migrations

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
)

const maxD1LResponseBytes = 1 << 20

var (
	ErrProviderUnavailable  = errors.New("authorization provider unavailable")
	ErrProviderUnknown      = errors.New("authorization provider outcome unknown")
	ErrProviderRejected     = errors.New("authorization provider rejected request")
	ErrProviderUnauthorized = fmt.Errorf("%w: unauthorized", ErrProviderRejected)
	ErrBindingMismatch      = fmt.Errorf("%w: binding mismatch", ErrProviderRejected)
	ErrMalformedResponse    = errors.New("authorization provider returned malformed response")
)

// ClientError contains classification only. It intentionally does not retain
// URL, response body, request, or any opaque envelope bytes.
type ClientError struct {
	Kind    error
	Outcome ProviderOutcome
	Code    int
	Cause   error
}

func (e *ClientError) Error() string {
	if e == nil || e.Kind == nil {
		return "authorization provider error"
	}
	if e.Code != 0 {
		return e.Kind.Error() + " (HTTP status)"
	}
	return e.Kind.Error()
}
func (e *ClientError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Kind
}

// Classification returns the stable, non-secret outcome for this call.
func (e *ClientError) Classification() ProviderOutcome {
	if e == nil {
		return OutcomeUnknown
	}
	if e.Outcome == "" {
		return OutcomeUnknown
	}
	return e.Outcome
}

// AuthorizationClient is the runner's narrow provider seam. There are no
// Issue, ResolveIssue, Revoke, Expire, epoch, or administrative methods.
type AuthorizationClient struct {
	endpoint string
	http     *http.Client
}

type D1LAuthorizationClient = AuthorizationClient

type wireConsumeRequest struct {
	ConsumeRequestID string `json:"consume_request_id"`
	AuthorizationID  string `json:"authorization_id"`
	IssuerRequestID  string `json:"issuer_request_id"`
	Operation        string `json:"operation"`
	AttemptID        string `json:"attempt_id"`
	TargetID         string `json:"target_id"`
	InstallerID      string `json:"installer_id"`
	EvidenceHash     string `json:"evidence_hash"`
	Scope            string `json:"scope"`
	Nonce            string `json:"nonce"`
	Envelope         string `json:"envelope"`
	Epoch            int64  `json:"epoch"`
}

type wireResolveRequest struct {
	ConsumeRequestID string `json:"consume_request_id"`
	IssuerRequestID  string `json:"issuer_request_id"`
	Operation        string `json:"operation"`
	AttemptID        string `json:"attempt_id"`
	TargetID         string `json:"target_id"`
	InstallerID      string `json:"installer_id"`
	EvidenceHash     string `json:"evidence_hash"`
	Scope            string `json:"scope"`
	Epoch            int64  `json:"epoch"`
	Nonce            string `json:"nonce"`
}

func NewD1LAuthorizationClient(cfg AuthorizationClientConfig) (*AuthorizationClient, error) {
	return NewAuthorizationClient(cfg)
}

func NewAuthorizationClient(cfg AuthorizationClientConfig) (*AuthorizationClient, error) {
	if err := validateAuthorizationConfig(cfg); err != nil {
		return nil, configError(err)
	}
	tlsCfg, err := loadAuthorizationTLS(cfg)
	if err != nil {
		return nil, configError(err)
	}
	u, _ := url.Parse(cfg.Endpoint)
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultD1LClientTimeout
	}
	transport := &http.Transport{
		TLSClientConfig:   tlsCfg,
		DisableKeepAlives: true,
		ForceAttemptHTTP2: false,
	}
	return &AuthorizationClient{
		endpoint: strings.TrimRight(u.String(), "/"),
		http: &http.Client{
			Transport: transport,
			Timeout:   timeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return errors.New("redirect refused")
			},
		},
	}, nil
}

func (c *AuthorizationClient) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	if c == nil || c.http == nil || c.endpoint == "" {
		return nil, &ClientError{Kind: ErrProviderUnavailable, Outcome: OutcomeUnavailable}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, bytes.NewReader(body))
	if err != nil {
		return nil, &ClientError{Kind: ErrProviderUnavailable, Outcome: OutcomeUnavailable, Cause: err}
	}
	// A nil GetBody prevents net/http from replaying a POST body. The transport
	// also uses one-shot, non-keepalive connections.
	req.GetBody = nil
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		if method == http.MethodPost && (strings.Contains(err.Error(), "redirect refused") || !definitelyPreTransmission(err)) {
			return nil, &ClientError{Kind: ErrProviderUnknown, Outcome: OutcomeUnknown, Cause: err}
		}
		return nil, &ClientError{Kind: ErrProviderUnavailable, Outcome: OutcomeUnavailable, Cause: err}
	}
	return resp, nil
}

func definitelyPreTransmission(err error) bool {
	var op *net.OpError
	if errors.As(err, &op) && (op.Op == "dial" || op.Op == "resolve") {
		// Dial and name-resolution failures, including their timeout variants,
		// happen before an HTTP request can be transmitted.
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return false
	}
	// TLS verification and URL/redirect failures happen before an HTTP request
	// can reach the provider. A reset or EOF is intentionally not included.
	return strings.Contains(err.Error(), "x509:") || strings.Contains(err.Error(), "certificate") || strings.Contains(err.Error(), "tls:") || strings.Contains(err.Error(), "redirect refused")
}

func (c *AuthorizationClient) endpointPath(path string) string {
	return "/v1/authorizations/" + path
}

func (c *AuthorizationClient) Inspect(ctx context.Context, authorizationID string) (InspectResult, error) {
	if err := requireUUID(authorizationID, "authorization_id"); err != nil {
		return InspectResult{}, err
	}
	resp, err := c.do(ctx, http.MethodPost, c.endpointPath(authorizationID+":inspect"), []byte(`{}`))
	if err != nil {
		return InspectResult{Outcome: outcomeFromError(err)}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := responseError(resp, false)
		return InspectResult{Outcome: outcomeFromError(err)}, err
	}
	var out InspectResult
	if err := decodeResponse(resp, &out); err != nil || !validInspect(out, authorizationID) {
		return InspectResult{Outcome: OutcomeUnknown}, &ClientError{Kind: ErrMalformedResponse, Outcome: OutcomeUnknown}
	}
	out.Outcome = inspectOutcome(out)
	return out, nil
}

func (c *AuthorizationClient) Consume(ctx context.Context, in ConsumeRequest) (ConsumeResult, error) {
	if err := validateConsumeRequest(in); err != nil {
		return ConsumeResult{ConsumeRequestID: in.ConsumeRequestID}, err
	}
	wire := wireConsumeRequest{
		ConsumeRequestID: in.ConsumeRequestID, AuthorizationID: in.AuthorizationID,
		IssuerRequestID: in.IssuerRequestID, Operation: in.Operation, AttemptID: in.AttemptID,
		TargetID: in.TargetID, InstallerID: in.InstallerID, EvidenceHash: in.EvidenceHash,
		Scope: in.Scope, Nonce: in.Nonce, Envelope: string(in.Envelope), Epoch: in.Epoch,
	}
	body, _ := json.Marshal(wire)
	resp, err := c.do(ctx, http.MethodPost, c.endpointPath(in.AuthorizationID+":consume"), body)
	if err != nil {
		return ConsumeResult{AuthorizationID: in.AuthorizationID, ConsumeRequestID: in.ConsumeRequestID, State: ConsumeUnknown, Outcome: outcomeFromError(err)}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := responseError(resp, true)
		out := ConsumeResult{AuthorizationID: in.AuthorizationID, ConsumeRequestID: in.ConsumeRequestID, State: ConsumeUnknown, Outcome: outcomeFromError(err)}
		return out, err
	}
	var out ConsumeResult
	if err := decodeResponse(resp, &out); err != nil || !validConsume(out, in) {
		return ConsumeResult{AuthorizationID: in.AuthorizationID, ConsumeRequestID: in.ConsumeRequestID, State: ConsumeUnknown, Outcome: OutcomeUnknown}, &ClientError{Kind: ErrProviderUnknown, Outcome: OutcomeUnknown}
	}
	out.Outcome = consumeOutcome(out)
	return out, nil
}

func (c *AuthorizationClient) Resolve(ctx context.Context, in ResolveRequest) (ResolveResult, error) {
	if err := validateResolveRequest(in); err != nil {
		return ResolveResult{AuthorizationID: in.AuthorizationID, IssuerRequestID: in.IssuerRequestID, ConsumeRequestID: in.ConsumeRequestID}, err
	}
	wire := wireResolveRequest{ConsumeRequestID: in.ConsumeRequestID, IssuerRequestID: in.IssuerRequestID, Operation: in.Operation, AttemptID: in.AttemptID, TargetID: in.TargetID, InstallerID: in.InstallerID, EvidenceHash: in.EvidenceHash, Scope: in.Scope, Epoch: in.Epoch, Nonce: in.Nonce}
	body, _ := json.Marshal(wire)
	resp, err := c.do(ctx, http.MethodPost, c.endpointPath(in.AuthorizationID+":resolve"), body)
	if err != nil {
		return ResolveResult{AuthorizationID: in.AuthorizationID, IssuerRequestID: in.IssuerRequestID, ConsumeRequestID: in.ConsumeRequestID, Outcome: outcomeFromError(err)}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := responseError(resp, false)
		return ResolveResult{AuthorizationID: in.AuthorizationID, IssuerRequestID: in.IssuerRequestID, ConsumeRequestID: in.ConsumeRequestID, Outcome: outcomeFromError(err)}, err
	}
	var out ResolveResult
	if err := decodeResponse(resp, &out); err != nil || !validResolve(out, in) {
		return ResolveResult{AuthorizationID: in.AuthorizationID, IssuerRequestID: in.IssuerRequestID, ConsumeRequestID: in.ConsumeRequestID, Outcome: OutcomeUnknown}, &ClientError{Kind: ErrMalformedResponse, Outcome: OutcomeUnknown}
	}
	out.Outcome = resolveOutcome(out)
	return out, nil
}

func (c *AuthorizationClient) Health(ctx context.Context) (HealthResult, error) {
	return c.healthPath(ctx, "/healthz")
}
func (c *AuthorizationClient) Attestation(ctx context.Context) (AttestationResult, error) {
	return c.healthPath(ctx, "/readyz")
}
func (c *AuthorizationClient) healthPath(ctx context.Context, path string) (HealthResult, error) {
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return HealthResult{Outcome: outcomeFromError(err)}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := responseError(resp, false)
		return HealthResult{Outcome: outcomeFromError(err)}, err
	}
	var out HealthResult
	if err := decodeResponse(resp, &out); err != nil || strings.TrimSpace(out.Status) == "" {
		return HealthResult{Outcome: OutcomeUnknown}, &ClientError{Kind: ErrMalformedResponse, Outcome: OutcomeUnknown}
	}
	out.Outcome = OutcomeSuccess
	return out, nil
}

func responseError(resp *http.Response, _ bool) error {
	if resp.StatusCode == http.StatusServiceUnavailable {
		return &ClientError{Kind: ErrProviderUnavailable, Outcome: OutcomeUnavailable, Code: resp.StatusCode}
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return &ClientError{Kind: ErrProviderUnauthorized, Outcome: OutcomeUnauthorized, Code: resp.StatusCode}
	}
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusConflict {
		return &ClientError{Kind: ErrBindingMismatch, Outcome: OutcomeBindingMismatch, Code: resp.StatusCode}
	}
	return &ClientError{Kind: ErrProviderRejected, Outcome: OutcomeUnknown, Code: resp.StatusCode}
}

func outcomeFromError(err error) ProviderOutcome {
	var ce *ClientError
	if errors.As(err, &ce) {
		return ce.Classification()
	}
	return OutcomeUnknown
}

func inspectOutcome(out InspectResult) ProviderOutcome {
	switch out.State {
	case AuthorizationIssued:
		return OutcomeSuccess
	case AuthorizationConsumePending:
		return OutcomeInProgress
	case AuthorizationConsumed:
		return OutcomeAlreadyConsumed
	case AuthorizationExpired:
		return OutcomeExpired
	case AuthorizationRevoked:
		return OutcomeRevoked
	case AuthorizationConsumeUnknown:
		return OutcomeUnknown
	default:
		return OutcomeUnknown
	}
}

func consumeOutcome(out ConsumeResult) ProviderOutcome {
	if out.Detail == "durable outcome" && out.State == ConsumeConsumed {
		return OutcomeAlreadyConsumed
	}
	switch out.State {
	case ConsumePending, ConsumeClaimed:
		return OutcomeInProgress
	case ConsumeConsumed:
		return OutcomeSuccess
	case ConsumeUnknown:
		return OutcomeUnknown
	case ConsumeAborted:
		switch out.AuthorizationState {
		case string(AuthorizationExpired):
			return OutcomeExpired
		case string(AuthorizationRevoked), string(AuthorizationCancelled):
			return OutcomeRevoked
		}
		switch out.TerminalCode {
		case "EXPIRED":
			return OutcomeExpired
		case "REVOKED", "SECRET_UNAVAILABLE":
			return OutcomeRevoked
		}
		return OutcomeBindingMismatch
	default:
		return OutcomeUnknown
	}
}

func resolveOutcome(out ResolveResult) ProviderOutcome {
	if out.AuthState == AuthorizationConsumed && out.IntentState == ConsumeConsumed {
		return OutcomeSuccess
	}
	if out.AuthState == AuthorizationConsumeUnknown || out.IntentState == ConsumeUnknown {
		return OutcomeUnknown
	}
	if out.AuthState == AuthorizationExpired || out.TerminalCode == "EXPIRED" {
		return OutcomeExpired
	}
	if out.AuthState == AuthorizationRevoked || out.AuthState == AuthorizationCancelled {
		return OutcomeRevoked
	}
	if out.AuthState == AuthorizationConsumePending || out.IntentState == ConsumePending || out.IntentState == ConsumeClaimed {
		return OutcomeInProgress
	}
	return OutcomeUnknown
}

func decodeResponse(resp *http.Response, dst any) error {
	dec := json.NewDecoder(io.LimitReader(resp.Body, maxD1LResponseBytes))
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("trailing response data")
	}
	return nil
}

func requireUUID(value, label string) error {
	if _, err := uuid.Parse(value); err != nil {
		return fmt.Errorf("invalid %s", label)
	}
	return nil
}
func validNonce(value string) bool {
	b, err := base64.RawStdEncoding.DecodeString(value)
	return err == nil && len(b) == 16
}
func validInspect(out InspectResult, requestedID string) bool {
	if out.AuthorizationID != requestedID || !validState(out.State) || out.Epoch <= 0 || !validNonce(out.Nonce) || out.ExpiresAt.IsZero() || out.Scope != ScopeControlCatalogInstall || out.Bindings == nil {
		return false
	}
	if len(out.Bindings) != 5 || out.Bindings["operation"] == "" || out.Bindings["attempt_id"] == "" || out.Bindings["target_id"] == "" || out.Bindings["installer_id"] == "" || out.Bindings["evidence_hash"] == "" || out.Bindings["attempt_id"] != out.AttemptID {
		return false
	}
	for key := range out.Bindings {
		if key != "operation" && key != "attempt_id" && key != "target_id" && key != "installer_id" && key != "evidence_hash" {
			return false
		}
	}
	return requireUUID(out.IssuerRequestID, "issuer_request_id") == nil && requireUUID(out.AttemptID, "attempt_id") == nil && (out.ConsumeRequestID == "" || requireUUID(out.ConsumeRequestID, "consume_request_id") == nil)
}
func validConsume(out ConsumeResult, in ConsumeRequest) bool {
	if out.AuthorizationID != in.AuthorizationID || out.ConsumeRequestID != in.ConsumeRequestID || !validConsumeState(out.State) {
		return false
	}
	if out.PendingConsumeRequestID != "" {
		if requireUUID(out.PendingConsumeRequestID, "pending_consume_request_id") != nil || (out.State != ConsumePending && out.State != ConsumeClaimed) {
			return false
		}
	}
	if out.AuthorizationState != "" && !validState(AuthorizationState(out.AuthorizationState)) {
		return false
	}
	if out.TerminalState != "" && !validState(AuthorizationState(out.TerminalState)) {
		return false
	}
	switch out.State {
	case ConsumePending:
		return out.AuthorizationState == string(AuthorizationIssued) && validLiveTerminal(out.TerminalState, out.TerminalCode)
	case ConsumeClaimed:
		return out.AuthorizationState == string(AuthorizationConsumePending) && validLiveTerminal(out.TerminalState, out.TerminalCode)
	case ConsumeConsumed:
		return out.AuthorizationState == string(AuthorizationConsumed) && validConsumedTerminal(out.TerminalState, out.TerminalCode)
	case ConsumeAborted:
		return validAbortedTerminal(out.AuthorizationState, out.TerminalState, out.TerminalCode)
	case ConsumeUnknown:
		return validUnknownTerminal(out.AuthorizationState, out.TerminalState, out.TerminalCode)
	default:
		return false
	}
}
func validResolve(out ResolveResult, in ResolveRequest) bool {
	if out.AuthorizationID != in.AuthorizationID || out.IssuerRequestID != in.IssuerRequestID || out.ConsumeRequestID != in.ConsumeRequestID || !validState(out.AuthState) || (out.IntentState != "" && !validConsumeState(out.IntentState)) {
		return false
	}
	switch out.IntentState {
	case ConsumePending:
		return out.AuthState == AuthorizationIssued && validLiveTerminal(out.TerminalState, out.TerminalCode)
	case ConsumeClaimed:
		return out.AuthState == AuthorizationConsumePending && validLiveTerminal(out.TerminalState, out.TerminalCode)
	case ConsumeConsumed:
		return out.AuthState == AuthorizationConsumed && validConsumedTerminal(out.TerminalState, out.TerminalCode)
	case ConsumeAborted:
		return validAbortedTerminal(string(out.AuthState), out.TerminalState, out.TerminalCode)
	case ConsumeUnknown:
		return out.AuthState == AuthorizationConsumeUnknown && validUnknownTerminal(string(out.AuthState), out.TerminalState, out.TerminalCode)
	default:
		return out.AuthState != AuthorizationConsumePending && validTerminalForAuth(string(out.AuthState), out.TerminalState, out.TerminalCode)
	}
}
func validLiveTerminal(state, code string) bool { return state == "" && code == "" }
func validConsumedTerminal(state, code string) bool {
	return state == string(AuthorizationConsumed) && code == "CONSUMED"
}
func validAbortedTerminal(auth, state, code string) bool {
	if auth != string(AuthorizationExpired) && auth != string(AuthorizationRevoked) && auth != string(AuthorizationCancelled) {
		return false
	}
	return state == auth && code != ""
}
func validUnknownTerminal(auth, state, code string) bool {
	if auth == "" {
		return state == "" && code == ""
	}
	return auth == string(AuthorizationConsumeUnknown) && state == string(AuthorizationConsumeUnknown) && code != ""
}
func validTerminalForAuth(auth, state, code string) bool {
	switch AuthorizationState(auth) {
	case AuthorizationConsumed:
		return validConsumedTerminal(state, code)
	case AuthorizationExpired, AuthorizationRevoked, AuthorizationCancelled:
		return validAbortedTerminal(auth, state, code)
	case AuthorizationConsumeUnknown:
		return validUnknownTerminal(auth, state, code)
	default:
		return validLiveTerminal(state, code)
	}
}
func validState(s AuthorizationState) bool {
	switch s {
	case AuthorizationIssued, AuthorizationConsumePending, AuthorizationConsumed, AuthorizationRevoked, AuthorizationCancelled, AuthorizationExpired, AuthorizationConsumeUnknown:
		return true
	}
	return false
}
func validConsumeState(s ConsumeState) bool {
	switch s {
	case ConsumePending, ConsumeClaimed, ConsumeConsumed, ConsumeAborted, ConsumeUnknown:
		return true
	}
	return false
}
func validateConsumeRequest(in ConsumeRequest) error {
	if err := requireUUID(in.ConsumeRequestID, "consume_request_id"); err != nil {
		return err
	}
	if err := requireUUID(in.AuthorizationID, "authorization_id"); err != nil {
		return err
	}
	if err := requireUUID(in.IssuerRequestID, "issuer_request_id"); err != nil {
		return err
	}
	if err := requireUUID(in.AttemptID, "attempt_id"); err != nil {
		return err
	}
	if in.Epoch <= 0 || in.Operation == "" || in.TargetID == "" || in.InstallerID == "" || in.EvidenceHash == "" || in.Scope != ScopeControlCatalogInstall || !validNonce(in.Nonce) || len(in.Envelope) == 0 {
		return errors.New("invalid consume binding")
	}
	return nil
}
func validateResolveRequest(in ResolveRequest) error {
	if err := requireUUID(in.AuthorizationID, "authorization_id"); err != nil {
		return err
	}
	if err := requireUUID(in.IssuerRequestID, "issuer_request_id"); err != nil {
		return err
	}
	if in.ConsumeRequestID == "" {
		return errors.New("consume_request_id is required for owner resolve")
	}
	if err := requireUUID(in.ConsumeRequestID, "consume_request_id"); err != nil {
		return err
	}
	if err := requireUUID(in.AttemptID, "attempt_id"); err != nil {
		return err
	}
	if in.Epoch <= 0 || in.Operation == "" || in.TargetID == "" || in.InstallerID == "" || in.EvidenceHash == "" || in.Scope != ScopeControlCatalogInstall || !validNonce(in.Nonce) {
		return errors.New("invalid resolve binding")
	}
	return nil
}
