// Package upstream owns the trusted post-D1-L issue-provenance producer. It
// deliberately accepts only sealed source-owner evidence; callers cannot
// construct a provenance value from a raw FactSet or caller freshness claim.
package upstream

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"power-iot-backend/internal/data/reconciliation/sourceowner"
)

const ProvenanceVersion int64 = 1

// D1OwnerIdentity and D1OwnerIssueRoute are the only producer binding values
// accepted by the D1-L ledger. Keeping these values in the owner package makes
// the closed-world producer contract explicit at both production and record
// time.
const (
	D1OwnerIdentity   = "trusted-post-d1l-upstream"
	D1OwnerIssueRoute = "D1_ISSUE"
)

var ErrProvenance = errors.New("trusted issue provenance rejected")

type Binding struct {
	OperationID       uuid.UUID
	AttemptID         uuid.UUID
	TargetFingerprint [32]byte
	RouteIntent       string
}

type Provenance struct{ state *provenanceState }

type provenanceState struct {
	id             uuid.UUID
	version        int64
	ownerIdentity  string
	ownerVersion   string
	binding        Binding
	evidenceDigest [32]byte
	observedAt     time.Time
	oneShot        bool
}

// Produce is the sole producer seam. The returned value contains no bearer or
// activation material and is bound to one invocation and exact issue tuple.
func Produce(evidence sourceowner.Evidence, binding Binding, ownerVersion string) (Provenance, error) {
	if binding.OperationID == uuid.Nil || binding.AttemptID == uuid.Nil ||
		binding.TargetFingerprint == [32]byte{} || binding.RouteIntent != D1OwnerIssueRoute ||
		binding.RouteIntent != strings.TrimSpace(binding.RouteIntent) ||
		strings.TrimSpace(ownerVersion) == "" {
		return Provenance{}, fmt.Errorf("%w: complete authoritative binding and owner version are required", ErrProvenance)
	}
	sourceBinding := sourceowner.NewInvocationBinding(binding.OperationID, binding.AttemptID)
	// The producer consumes the owner evidence for this exact issue
	// invocation. A copied Evidence value shares the owner one-shot state, so a
	// second producer call cannot mint another provenance identity. Freshness is
	// checked by the owner package's clock; callers cannot backdate this call.
	if err := evidence.UseForOwnerInvocation(sourceBinding); err != nil {
		return Provenance{}, fmt.Errorf("%w: source evidence: %v", ErrProvenance, err)
	}
	digest := evidence.Digest()
	// Identity is owner-generated and independent of lease/attempt identity.
	id := uuid.New()
	return Provenance{state: &provenanceState{
		id: id, version: ProvenanceVersion, ownerIdentity: D1OwnerIdentity,
		ownerVersion: ownerVersion, binding: binding, evidenceDigest: digest,
		observedAt: evidence.ObservedAt(), oneShot: true,
	}}, nil
}

func (p Provenance) valid() error {
	if p.state == nil || p.state.id == uuid.Nil || p.state.version != ProvenanceVersion || !p.state.oneShot || p.state.ownerIdentity != D1OwnerIdentity || strings.TrimSpace(p.state.ownerVersion) == "" {
		return ErrProvenance
	}
	if p.state.binding.OperationID == uuid.Nil || p.state.binding.AttemptID == uuid.Nil || p.state.binding.TargetFingerprint == [32]byte{} || p.state.binding.RouteIntent != D1OwnerIssueRoute || p.state.binding.RouteIntent != strings.TrimSpace(p.state.binding.RouteIntent) || p.state.evidenceDigest == [32]byte{} || p.state.observedAt.IsZero() {
		return ErrProvenance
	}
	return nil
}

func (p Provenance) Identity() (uuid.UUID, int64, error) {
	if err := p.valid(); err != nil {
		return uuid.Nil, 0, err
	}
	return p.state.id, p.state.version, nil
}
func (p Provenance) OwnerIdentity() (string, string, error) {
	if err := p.valid(); err != nil {
		return "", "", err
	}
	return p.state.ownerIdentity, p.state.ownerVersion, nil
}
func (p Provenance) Binding() (Binding, error) {
	if err := p.valid(); err != nil {
		return Binding{}, err
	}
	return p.state.binding, nil
}
func (p Provenance) EvidenceDigest() ([32]byte, error) {
	if err := p.valid(); err != nil {
		return [32]byte{}, err
	}
	return p.state.evidenceDigest, nil
}
func (p Provenance) ObservedAt() (time.Time, error) {
	if err := p.valid(); err != nil {
		return time.Time{}, err
	}
	return p.state.observedAt, nil
}

// Digest is a safe witness for logs and linkage; it is not issue authority.
func (p Provenance) Digest() ([32]byte, error) {
	if err := p.valid(); err != nil {
		return [32]byte{}, err
	}
	id, _, _ := p.Identity()
	raw := sha256.Sum256([]byte(id.String() + ":" + p.state.ownerVersion))
	return raw, nil
}
