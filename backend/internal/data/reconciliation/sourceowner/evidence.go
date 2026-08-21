package sourceowner

import (
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// InvocationBinding identifies the one consumer invocation for which sealed
// source evidence was collected. It is binding metadata, not authority.
type InvocationBinding struct {
	OperationID uuid.UUID
	AttemptID   uuid.UUID
}

func NewInvocationBinding(operationID, attemptID uuid.UUID) InvocationBinding {
	return InvocationBinding{OperationID: operationID, AttemptID: attemptID}
}

type evidenceState struct {
	facts      FactSet
	digest     [32]byte
	binding    InvocationBinding
	freshUntil time.Time
	used       atomic.Bool
}

// Evidence is an opaque source-owner result. Its private state prevents
// consumer packages from constructing equivalent trusted evidence values.
type Evidence struct {
	state *evidenceState
}

const sourceFreshnessWindow = time.Minute

// newEvidence is intentionally package-private: only source-owner collection
// code and source-owner tests can mint Evidence.
func newEvidence(facts FactSet, binding InvocationBinding) (Evidence, error) {
	if binding.OperationID == uuid.Nil || binding.AttemptID == uuid.Nil {
		return Evidence{}, errors.New("source invocation binding is required")
	}
	if err := ValidateVersion(facts); err != nil {
		return Evidence{}, err
	}
	if err := ValidateFactIdentities(facts); err != nil {
		return Evidence{}, err
	}
	normalized := cloneFacts(facts)
	_, digest, err := CanonicalSourceFacts(normalized)
	if err != nil {
		return Evidence{}, err
	}
	var digestArray [32]byte
	copy(digestArray[:], digest)
	return Evidence{state: &evidenceState{
		facts:      normalized,
		digest:     digestArray,
		binding:    binding,
		freshUntil: normalized.AsOf.Add(sourceFreshnessWindow),
	}}, nil
}

// Facts returns a defensive source-facts copy for bounded classification. It
// does not expose the private evidence state or a mint path.
func (e Evidence) Facts() FactSet {
	if e.state == nil {
		return FactSet{}
	}
	return cloneFacts(e.state.facts)
}

func (e Evidence) ObservedAt() time.Time {
	if e.state == nil {
		return time.Time{}
	}
	return e.state.facts.AsOf
}

func (e Evidence) FreshUntil() time.Time {
	if e.state == nil {
		return time.Time{}
	}
	return e.state.freshUntil
}

func (e Evidence) Digest() [32]byte {
	if e.state == nil {
		return [32]byte{}
	}
	return e.state.digest
}

func (e Evidence) ValidateForInvocation(binding InvocationBinding, now time.Time) error {
	if e.state == nil {
		return errors.New("trusted source evidence is empty")
	}
	if e.state.binding != binding {
		return errors.New("trusted source evidence invocation binding mismatch")
	}
	if err := ValidateVersion(e.state.facts); err != nil {
		return fmt.Errorf("trusted source evidence is invalid: %w", err)
	}
	_, digest, err := CanonicalSourceFacts(e.state.facts)
	if err != nil {
		return fmt.Errorf("trusted source evidence is invalid: %w", err)
	}
	if string(digest) != string(e.state.digest[:]) {
		return errors.New("trusted source evidence digest mismatch")
	}
	if now.Before(e.state.facts.AsOf) || !now.Before(e.state.freshUntil) {
		return errors.New("trusted source evidence is stale")
	}
	return nil
}

func cloneFacts(facts FactSet) FactSet {
	cloned := NormalizeFacts(facts)
	for i := range cloned.Shops {
		if cloned.Shops[i].ClientID != nil {
			value := *cloned.Shops[i].ClientID
			cloned.Shops[i].ClientID = &value
		}
	}
	for i := range cloned.Users {
		if cloned.Users[i].CurrentShopID != nil {
			value := *cloned.Users[i].CurrentShopID
			cloned.Users[i].CurrentShopID = &value
		}
	}
	for i := range cloned.Devices {
		if cloned.Devices[i].InventoryOwnerClientID != nil {
			value := *cloned.Devices[i].InventoryOwnerClientID
			cloned.Devices[i].InventoryOwnerClientID = &value
		}
	}
	for i := range cloned.AdminOperations {
		cloned.AdminOperations[i].ScopeSnapshot = append([]byte(nil), cloned.AdminOperations[i].ScopeSnapshot...)
		cloned.AdminOperations[i].CanonicalRequestHash = append([]byte(nil), cloned.AdminOperations[i].CanonicalRequestHash...)
		cloned.AdminOperations[i].CommittedResponse = append([]byte(nil), cloned.AdminOperations[i].CommittedResponse...)
		if cloned.AdminOperations[i].ClientID != nil {
			value := *cloned.AdminOperations[i].ClientID
			cloned.AdminOperations[i].ClientID = &value
		}
		if cloned.AdminOperations[i].CommittedAt != nil {
			value := *cloned.AdminOperations[i].CommittedAt
			cloned.AdminOperations[i].CommittedAt = &value
		}
	}
	for i := range cloned.AdminAudits {
		cloned.AdminAudits[i].ScopeSnapshot = append([]byte(nil), cloned.AdminAudits[i].ScopeSnapshot...)
		cloned.AdminAudits[i].Metadata = append([]byte(nil), cloned.AdminAudits[i].Metadata...)
		if cloned.AdminAudits[i].EffectiveAt != nil {
			value := *cloned.AdminAudits[i].EffectiveAt
			cloned.AdminAudits[i].EffectiveAt = &value
		}
		if cloned.AdminAudits[i].ClientID != nil {
			value := *cloned.AdminAudits[i].ClientID
			cloned.AdminAudits[i].ClientID = &value
		}
		if cloned.AdminAudits[i].ShopID != nil {
			value := *cloned.AdminAudits[i].ShopID
			cloned.AdminAudits[i].ShopID = &value
		}
		if cloned.AdminAudits[i].DeviceID != nil {
			value := *cloned.AdminAudits[i].DeviceID
			cloned.AdminAudits[i].DeviceID = &value
		}
		if cloned.AdminAudits[i].DeviceSerialNumber != nil {
			value := *cloned.AdminAudits[i].DeviceSerialNumber
			cloned.AdminAudits[i].DeviceSerialNumber = &value
		}
		if cloned.AdminAudits[i].DeviceMAC != nil {
			value := *cloned.AdminAudits[i].DeviceMAC
			cloned.AdminAudits[i].DeviceMAC = &value
		}
		if cloned.AdminAudits[i].MeasurementPointID != nil {
			value := *cloned.AdminAudits[i].MeasurementPointID
			cloned.AdminAudits[i].MeasurementPointID = &value
		}
		if cloned.AdminAudits[i].OldMeasurementPointID != nil {
			value := *cloned.AdminAudits[i].OldMeasurementPointID
			cloned.AdminAudits[i].OldMeasurementPointID = &value
		}
		if cloned.AdminAudits[i].NewMeasurementPointID != nil {
			value := *cloned.AdminAudits[i].NewMeasurementPointID
			cloned.AdminAudits[i].NewMeasurementPointID = &value
		}
		if cloned.AdminAudits[i].OldAssignmentID != nil {
			value := *cloned.AdminAudits[i].OldAssignmentID
			cloned.AdminAudits[i].OldAssignmentID = &value
		}
		if cloned.AdminAudits[i].NewAssignmentID != nil {
			value := *cloned.AdminAudits[i].NewAssignmentID
			cloned.AdminAudits[i].NewAssignmentID = &value
		}
	}
	return cloned
}

// UseForInvocation consumes evidence for its one bound invocation. A copied
// Evidence value shares the private state and therefore cannot be replayed.
func (e Evidence) UseForInvocation(binding InvocationBinding, now time.Time) error {
	if err := e.ValidateForInvocation(binding, now); err != nil {
		return err
	}
	if !e.state.used.CompareAndSwap(false, true) {
		return errors.New("trusted source evidence was already used")
	}
	return nil
}

// UseForOwnerInvocation validates freshness against the owner package's clock.
// The D1 producer uses this seam rather than accepting a caller-selected
// validation timestamp, so a stale evidence value cannot be admitted by
// backdating an invocation request.
func (e Evidence) UseForOwnerInvocation(binding InvocationBinding) error {
	return e.UseForInvocation(binding, time.Now().UTC())
}
