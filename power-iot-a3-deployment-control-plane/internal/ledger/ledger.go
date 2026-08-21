// Package ledger defines durable lifecycle values and request validation. Raw
// secrets intentionally have no ledger representation.
package ledger

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	AuthorityLabel             = "power-iot-a3-d1l-provider-authority/v1"
	ScopeControlCatalogInstall = "allow_control_catalog_install"
)

var ErrSecretUnavailable = errors.New("secret unavailable")
var ErrUnknownCommit = errors.New("commit outcome unknown")

type AuthState string

const (
	Requested      AuthState = "REQUESTED"
	Issued         AuthState = "ISSUED"
	ConsumePending AuthState = "CONSUME_PENDING"
	Consumed       AuthState = "CONSUMED"
	Revoked        AuthState = "REVOKED"
	Cancelled      AuthState = "CANCELLED"
)

type IntentState string

const (
	Pending        IntentState = "PENDING"
	Claimed        IntentState = "CLAIMED"
	Aborted        IntentState = "ABORTED"
	ConsumeUnknown IntentState = "CONSUME_UNKNOWN"
	Unknown        IntentState = "UNKNOWN"
	IntentConsumed IntentState = "CONSUMED"
)

func LockKey(label string) int64 {
	d := sha256.Sum256([]byte(label))
	return int64(binary.BigEndian.Uint64(d[:8]))
}
func ExpectedLockKey() int64 { return LockKey(AuthorityLabel) }

type IssueRequest struct {
	IssuerRequestID, AttemptID string
	Scope                      string
	Bindings                   map[string]string
	TTL                        time.Duration
}
type Binding struct {
	Operation, AttemptID, TargetID, InstallerID, EvidenceHash, Scope string
	Epoch                                                            int64
	Nonce, SecretVerifier                                            []byte
	ExpiresAt                                                        time.Time
}

func ValidateIssueRequest(r IssueRequest) error {
	if strings.TrimSpace(r.IssuerRequestID) == "" || strings.TrimSpace(r.AttemptID) == "" {
		return errors.New("issuer_request_id and attempt_id are required")
	}
	if r.Scope != ScopeControlCatalogInstall {
		return errors.New("unsupported scope")
	}
	if r.TTL <= 0 {
		return errors.New("expiry is required")
	}
	required := []string{"operation", "attempt_id", "target_id", "installer_id", "evidence_hash"}
	if len(r.Bindings) != len(required) {
		return errors.New("exact binding set is required")
	}
	for _, key := range required {
		if strings.TrimSpace(r.Bindings[key]) == "" {
			return fmt.Errorf("%s binding is required", key)
		}
	}
	for key := range r.Bindings {
		found := false
		for _, requiredKey := range required {
			if key == requiredKey {
				found = true
				break
			}
		}
		if !found {
			return errors.New("unknown binding")
		}
	}
	if r.Bindings["attempt_id"] != r.AttemptID {
		return errors.New("attempt binding mismatch")
	}
	return nil
}
func ValidateBinding(b Binding, supplied []byte, now time.Time) error {
	if len(supplied) == 0 {
		return errors.New("secret is required")
	}
	h := sha256.Sum256(supplied)
	if len(b.SecretVerifier) != sha256.Size || !equal(h[:], b.SecretVerifier) {
		return errors.New("secret verifier mismatch")
	}
	if len(b.Nonce) == 0 || b.Epoch <= 0 || b.Scope != ScopeControlCatalogInstall || b.ExpiresAt.Before(now) {
		return errors.New("binding is not valid")
	}
	if len(b.Nonce) != 16 {
		return errors.New("nonce is invalid")
	}
	for n, v := range map[string]string{"operation": b.Operation, "attempt_id": b.AttemptID, "target_id": b.TargetID, "installer_id": b.InstallerID, "evidence_hash": b.EvidenceHash} {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("%s is required", n)
		}
	}
	return nil
}
func equal(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var x byte
	for i := range a {
		x |= a[i] ^ b[i]
	}
	return x == 0
}
