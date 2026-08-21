package migrations

import "time"

const ScopeControlCatalogInstall = "allow_control_catalog_install"

// ProviderOutcome is the fail-closed classification of a provider call. It is
// deliberately independent of the provider's lifecycle state strings so
// callers cannot accidentally treat an indeterminate mutation as success.
type ProviderOutcome string

const (
	OutcomeSuccess         ProviderOutcome = "SUCCESS"
	OutcomeInProgress      ProviderOutcome = "IN_PROGRESS"
	OutcomeAlreadyConsumed ProviderOutcome = "ALREADY_CONSUMED"
	OutcomeExpired         ProviderOutcome = "EXPIRED"
	OutcomePoisoned        ProviderOutcome = "POISONED"
	OutcomeRevoked         ProviderOutcome = "REVOKED"
	// OutcomePoisonedOrRevoked is a compatibility alias for callers that
	// intentionally collapse the provider's terminal denial states.
	OutcomePoisonedOrRevoked ProviderOutcome = OutcomeRevoked
	OutcomeBindingMismatch   ProviderOutcome = "BINDING_MISMATCH"
	OutcomeUnauthorized      ProviderOutcome = "UNAUTHORIZED"
	OutcomeUnavailable       ProviderOutcome = "UNAVAILABLE"
	OutcomeUnknown           ProviderOutcome = "UNKNOWN"
)

// D1L provider lifecycle values are intentionally a closed set. The client
// does not expose the provider's issuer or administrative operations.
type AuthorizationState string

const (
	AuthorizationIssued         AuthorizationState = "ISSUED"
	AuthorizationConsumePending AuthorizationState = "CONSUME_PENDING"
	AuthorizationConsumed       AuthorizationState = "CONSUMED"
	AuthorizationRevoked        AuthorizationState = "REVOKED"
	AuthorizationCancelled      AuthorizationState = "CANCELLED"
	AuthorizationExpired        AuthorizationState = "EXPIRED"
	AuthorizationConsumeUnknown AuthorizationState = "CONSUME_UNKNOWN"
)

type ConsumeState string

const (
	ConsumePending  ConsumeState = "PENDING"
	ConsumeClaimed  ConsumeState = "CLAIMED"
	ConsumeConsumed ConsumeState = "CONSUMED"
	ConsumeAborted  ConsumeState = "ABORTED"
	ConsumeUnknown  ConsumeState = "UNKNOWN"
)

// InspectResult mirrors only the provider's non-secret inspect response.
type InspectResult struct {
	Outcome          ProviderOutcome    `json:"-"`
	AuthorizationID  string             `json:"authorization_id"`
	IssuerRequestID  string             `json:"issuer_request_id"`
	AttemptID        string             `json:"attempt_id"`
	State            AuthorizationState `json:"state"`
	Epoch            int64              `json:"epoch"`
	Nonce            string             `json:"nonce"`
	ExpiresAt        time.Time          `json:"expires_at"`
	Scope            string             `json:"scope"`
	Bindings         map[string]string  `json:"bindings"`
	ConsumeRequestID string             `json:"consume_request_id,omitempty"`
	IntentState      ConsumeState       `json:"intent_state,omitempty"`
	TerminalState    string             `json:"terminal_state,omitempty"`
	TerminalCode     string             `json:"terminal_code,omitempty"`
}

// ConsumeRequest contains the complete CR3 binding tuple. Envelope is opaque
// to the client and is never included in errors, logs, or String methods.
type ConsumeRequest struct {
	ConsumeRequestID string
	AuthorizationID  string
	IssuerRequestID  string
	Operation        string
	AttemptID        string
	TargetID         string
	InstallerID      string
	EvidenceHash     string
	Scope            string
	Nonce            string
	Envelope         []byte
	Epoch            int64
}

// String intentionally omits Envelope: it is the one request field that may
// carry the raw capability presentation.
func (r ConsumeRequest) String() string {
	return "ConsumeRequest{authorization_id=" + r.AuthorizationID + ", consume_request_id=" + r.ConsumeRequestID + "}"
}

type ConsumeResult struct {
	Outcome                 ProviderOutcome `json:"-"`
	AuthorizationID         string          `json:"authorization_id"`
	ConsumeRequestID        string          `json:"consume_request_id"`
	State                   ConsumeState    `json:"state"`
	AuthorizationState      string          `json:"authorization_state,omitempty"`
	TerminalState           string          `json:"terminal_state,omitempty"`
	TerminalCode            string          `json:"terminal_code,omitempty"`
	Detail                  string          `json:"detail,omitempty"`
	PendingConsumeRequestID string          `json:"pending_consume_request_id,omitempty"`
}

// ResolveRequest is deliberately owner-only: there is no recovery flag and no
// operation for resolving an issuer request. The provider authenticates the
// d1l-runner certificate and applies the same tuple as Consume.
type ResolveRequest struct {
	ConsumeRequestID string
	AuthorizationID  string
	IssuerRequestID  string
	Operation        string
	AttemptID        string
	TargetID         string
	InstallerID      string
	EvidenceHash     string
	Scope            string
	Epoch            int64
	Nonce            string
}

type ResolveResult struct {
	Outcome          ProviderOutcome    `json:"-"`
	AuthorizationID  string             `json:"authorization_id"`
	IssuerRequestID  string             `json:"issuer_request_id"`
	ConsumeRequestID string             `json:"consume_request_id,omitempty"`
	AuthState        AuthorizationState `json:"authorization_state"`
	IntentState      ConsumeState       `json:"intent_state,omitempty"`
	TerminalState    string             `json:"terminal_state,omitempty"`
	TerminalCode     string             `json:"terminal_code,omitempty"`
	Detail           string             `json:"detail,omitempty"`
}

type HealthResult struct {
	Outcome ProviderOutcome `json:"-"`
	Status  string          `json:"status"`
}

// AttestationResult is kept separate at the API boundary even though the
// current provider wire protocol exposes readiness as /readyz.
type AttestationResult = HealthResult
