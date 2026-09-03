package reconciliation

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"power-iot-backend/internal/data/private_migrations"
)

// MappingProgressionStatus is the semantic result of the mapping-required
// seam. Waiting is not an authorization result; it is the only resumable
// state before an admissible artifact is supplied.
type MappingProgressionStatus string

const (
	MappingProgressionAdmitted MappingProgressionStatus = "MAPPING_ADMITTED"
	MappingProgressionWaiting  MappingProgressionStatus = "WAITING_FOR_MAPPING"
	MappingProgressionDenied   MappingProgressionStatus = "DENIED"
)

var ErrMappingProgressionRejected = errors.New("mapping-required progression rejected")

// MappingExpectedCurrentBinding makes the expected NULL value unambiguous.
// MappingEntry.ExpectedCurrentClientID remains backwards compatible (nil
// means NULL for this seam), while Present prevents an omitted binding from
// being mistaken for an expected NULL value.
type MappingExpectedCurrentBinding struct {
	Category    MappingCategory
	ShopID      uint
	DeviceID    uint
	OperationID uuid.UUID
	ClientID    *uint
	Present     bool
}

// MappingProgressionRequest is the reconciliation-local typed D006 request.
// Artifact is evidence only: it cannot mint ownership, transfer ownership, or
// repair malformed source authority.
type MappingProgressionRequest struct {
	Artifact           *MappingArtifact
	SourceFactsDigest  string
	MappingBasisDigest string
	ExpectedCurrent    []MappingExpectedCurrentBinding
	TargetID           uint
	ClientID           uint
	OperationID        uuid.UUID
	AttemptID          uuid.UUID
	Generation         uint64
}

type MappingProgressionResult struct {
	Status              MappingProgressionStatus
	Classification      ProtectedClassification
	FreshClassification ProtectedClassification
	TargetID            uint
	ClientID            uint
	OperationID         uuid.UUID
	AttemptID           uuid.UUID
	Generation          uint64
	SourceFactsDigest   string
	MappingBasisDigest  string
	MappingDigest       string
	PlanDigest          string
	Reason              string
}

func mappingProgressionResult(request MappingProgressionRequest) MappingProgressionResult {
	return MappingProgressionResult{
		Status: MappingProgressionDenied, TargetID: request.TargetID,
		ClientID: request.ClientID, OperationID: request.OperationID,
		AttemptID: request.AttemptID, Generation: request.Generation,
	}
}

// AdmitMappingRequired validates one complete artifact against one exact
// source snapshot, then performs a fresh classification. BuildPlan is invoked
// only after admission and remains the single planner/execution contract.
func AdmitMappingRequired(facts FactSet, request MappingProgressionRequest) (MappingProgressionResult, error) {
	result := mappingProgressionResult(request)
	decisions, err := Classify(facts)
	if err != nil {
		result.Classification = ProtectedClassificationInvalid
		result.FreshClassification = ProtectedClassificationInvalid
		result.Reason = err.Error()
		return result, fmt.Errorf("%w: source classification: %v", ErrMappingProgressionRejected, err)
	}
	classification, reason, mappingRequired := mappingTargetAssessment(facts, decisions, request.TargetID)
	result.Classification, result.FreshClassification, result.Reason = classification, classification, reason
	if classification == ProtectedClassificationOwnedUnplaced {
		if request.Artifact == nil {
			result.Reason = "OWNED_UNPLACED cannot wait without an explicit structurally mapping-required classification"
			return result, fmt.Errorf("%w: %s", ErrMappingProgressionRejected, result.Reason)
		}
		result.Status = MappingProgressionWaiting
		result.Reason = "authoritative ownership exists without current placement; mapping cannot bypass OWNED_UNPLACED"
		return result, nil
	}
	if request.Artifact == nil {
		if mappingRequired {
			result.Status = MappingProgressionWaiting
			result.Reason = "complete mapping artifact is required for the structurally admissible mapping-required path"
			return result, nil
		}
		result.Reason = "mapping progression is not admissible for this source classification"
		return result, fmt.Errorf("%w: %s", ErrMappingProgressionRejected, result.Reason)
	}
	if err := validateMappingProgressionRequest(facts, request); err != nil {
		result.Reason = err.Error()
		return result, fmt.Errorf("%w: %v", ErrMappingProgressionRejected, err)
	}
	// Admission is complete. Reclassify the exact same authoritative snapshot
	// after admission; this deliberately does not treat mapping as authority.
	freshDecisions, err := Classify(facts)
	if err != nil {
		result.FreshClassification = ProtectedClassificationInvalid
		return result, fmt.Errorf("%w: fresh classification: %v", ErrMappingProgressionRejected, err)
	}
	freshClass, freshReason, _ := mappingTargetAssessment(facts, freshDecisions, request.TargetID)
	result.FreshClassification, result.Reason = freshClass, freshReason
	switch freshClass {
	case ProtectedClassificationInvalid, ProtectedClassificationAmbiguous, ProtectedClassificationConflicting:
		return result, fmt.Errorf("%w: fresh classification is %s: %s", ErrMappingProgressionRejected, freshClass, freshReason)
	case ProtectedClassificationOwnedUnplaced:
		result.Status = MappingProgressionWaiting
		return result, nil
	}
	plan, err := BuildPlan(facts, request.Artifact)
	if err != nil {
		result.Reason = err.Error()
		return result, fmt.Errorf("%w: fresh plan: %v", ErrMappingProgressionRejected, err)
	}
	if len(plan.Blockers) != 0 || len(plan.RequiredExplicitMappings) != 0 {
		result.Reason = fmt.Sprintf("fresh plan remains blocked: blockers=%v required=%v", plan.Blockers, plan.RequiredExplicitMappings)
		return result, fmt.Errorf("%w: %s", ErrMappingProgressionRejected, result.Reason)
	}
	mappingDigest, err := request.Artifact.DigestHex()
	if err != nil {
		return result, fmt.Errorf("%w: mapping digest: %v", ErrMappingProgressionRejected, err)
	}
	result.Status = MappingProgressionAdmitted
	result.MappingDigest = mappingDigest
	result.PlanDigest = hex.EncodeToString(plan.Digest)
	result.Reason = "mapping admitted and fresh classification produced an executable plan"
	return result, nil
}

// ExecuteWithMappingRequired adapts D006 to the already protected V2-02
// executor. The resolver validates against facts collected inside TX1, so a
// caller cannot replay an artifact against changed facts or skip reclassifying.
func (e *ProtectedExecutor) ExecuteWithMappingRequired(ctx context.Context, dsn string, request MappingProgressionRequest) (ExecutionReport, error) {
	if e == nil {
		return ExecutionReport{Outcome: ExecutionNotCommitted, Phase: PhasePlan}, &ExecutionError{Outcome: ExecutionNotCommitted, Phase: PhasePlan, Cause: errors.New("protected executor is required")}
	}
	if err := validateInvocationBinding(request, e.Lease); err != nil {
		return ExecutionReport{Outcome: ExecutionNotCommitted, Phase: PhasePlan}, &ExecutionError{Outcome: ExecutionNotCommitted, Phase: PhasePlan, Cause: err}
	}
	resolver := func(_ context.Context, facts FactSet) (*MappingArtifact, error) {
		result, err := AdmitMappingRequired(facts, request)
		if err != nil {
			return nil, err
		}
		if result.Status != MappingProgressionAdmitted {
			return nil, fmt.Errorf("%w: %s", ErrMappingProgressionRejected, result.Reason)
		}
		return request.Artifact, nil
	}
	return e.ExecuteWithMappingResolver(ctx, dsn, resolver)
}

func validateInvocationBinding(request MappingProgressionRequest, lease *migrations.D1LLeaseIdentity) error {
	if request.OperationID == uuid.Nil || request.AttemptID == uuid.Nil || request.TargetID == 0 || request.ClientID == 0 || request.Generation == 0 {
		return fmt.Errorf("%w: operation, attempt, target, client, and generation bindings are required", ErrMappingProgressionRejected)
	}
	if lease == nil {
		return fmt.Errorf("%w: owner-issued D1 lease binding is required", ErrMappingProgressionRejected)
	}
	if lease.OperationID != request.OperationID || lease.AttemptID != request.AttemptID || lease.Generation != int64(request.Generation) {
		return fmt.Errorf("%w: operation, attempt, or generation binding mismatch", ErrMappingProgressionRejected)
	}
	return nil
}

func validateMappingProgressionRequest(facts FactSet, request MappingProgressionRequest) error {
	if request.TargetID == 0 || request.ClientID == 0 || request.OperationID == uuid.Nil || request.AttemptID == uuid.Nil || request.Generation == 0 {
		return errors.New("operation, attempt, target, client, and generation bindings are required")
	}
	if request.SourceFactsDigest == "" || request.MappingBasisDigest == "" || !isDigestHex(request.SourceFactsDigest) || !isDigestHex(request.MappingBasisDigest) {
		return errors.New("source-facts and mapping-basis digests are required and must be SHA-256 hex")
	}
	raw, sourceDigest, err := CanonicalSourceFacts(facts)
	if err != nil {
		return fmt.Errorf("canonical source facts: %w", err)
	}
	if hex.EncodeToString(sourceDigest) != request.SourceFactsDigest {
		return errors.New("source-facts digest binding mismatch")
	}
	basis, err := MappingSourceFactsDigest(facts)
	if err != nil {
		return fmt.Errorf("canonical mapping basis: %w", err)
	}
	if hex.EncodeToString(basis) != request.MappingBasisDigest || request.Artifact.SourceFactsDigest != request.MappingBasisDigest {
		return errors.New("mapping-basis digest binding mismatch")
	}
	if len(raw) == 0 { // defensive: CanonicalSourceFacts already validates this
		return errors.New("source facts are empty")
	}
	if _, ok := findDevice(facts, request.TargetID); !ok {
		return errors.New("target device is not present in source facts")
	}
	if len(request.Artifact.Mappings) == 0 || len(request.ExpectedCurrent) != len(request.Artifact.Mappings) {
		return errors.New("mapping and expected-current coverage is incomplete")
	}
	seen := make(map[string]bool, len(request.ExpectedCurrent))
	for _, entry := range request.Artifact.Mappings {
		category, key, err := mappingKey(entry)
		if err != nil {
			return fmt.Errorf("malformed mapping: %w", err)
		}
		if entry.ClientID != request.ClientID {
			return errors.New("mapping client binding mismatch")
		}
		if category == MappingDevice {
			return errors.New("device mapping cannot create or transfer ownership")
		}
		if category == MappingAdminProvenance {
			if entry.OperationID != request.OperationID {
				return errors.New("mapping operation binding mismatch")
			}
			if !adminMappingTargetsDevice(facts, entry.OperationID, request.TargetID) {
				return errors.New("mapping target binding mismatch")
			}
		}
		binding, ok := findExpectedCurrentBinding(request.ExpectedCurrent, category, key)
		if !ok || !binding.Present {
			return fmt.Errorf("expected-current binding is incomplete for %s:%s", category, key)
		}
		if !sameUint(binding.ClientID, entry.ExpectedCurrentClientID) {
			return fmt.Errorf("expected-current binding mismatch for %s:%s", category, key)
		}
		actual, err := actualMappingCurrentClients(facts, entry)
		if err != nil {
			return fmt.Errorf("actual current binding for %s:%s: %w", category, key, err)
		}
		for _, current := range actual {
			if !sameUint(current, entry.ExpectedCurrentClientID) {
				return fmt.Errorf("stale expected-current value for %s:%s", category, key)
			}
		}
		seen[string(category)+"\x00"+key] = true
		if category == MappingShop && !shopMappingHasTargetAssignment(facts, request.TargetID, entry.ShopID) {
			return errors.New("shop mapping target is not bound to the target's current assignment")
		}
	}
	if len(seen) != len(request.ExpectedCurrent) {
		return errors.New("expected-current coverage contains an unknown or conflicting mapping")
	}
	return nil
}

func findExpectedCurrentBinding(bindings []MappingExpectedCurrentBinding, category MappingCategory, key string) (MappingExpectedCurrentBinding, bool) {
	var found MappingExpectedCurrentBinding
	count := 0
	for _, binding := range bindings {
		c, k, err := mappingKey(MappingEntry{Category: binding.Category, ShopID: binding.ShopID, DeviceID: binding.DeviceID, OperationID: binding.OperationID, ClientID: 1})
		if err == nil && c == category && k == key {
			found = binding
			count++
		}
	}
	return found, count == 1
}

// mappingTargetAssessment deliberately delegates the protected classification
// to the canonical helper. The separate mappingRequired bit is narrower than
// UNOWNED: only a device with the explicit, structurally valid no-placement
// decision may enter the resumable mapping seam.
func mappingTargetAssessment(facts FactSet, decisions []Decision, target uint) (ProtectedClassification, string, bool) {
	classification, reason := classifyProtectedTarget(facts, target)
	mappingRequired := false
	if classification == ProtectedClassificationUnowned {
		for _, decision := range decisions {
			if decision.DeviceID == target && decision.Classification == ExplicitMappingRequired && decision.Reason == "no structurally valid active assignment" {
				if device, ok := findDevice(facts, target); ok && device.ShopID == 0 {
					mappingRequired = true
				}
				break
			}
		}
	}
	return classification, reason, mappingRequired
}

func actualMappingCurrentClients(facts FactSet, entry MappingEntry) ([]*uint, error) {
	category, _, err := mappingKey(entry)
	if err != nil {
		return nil, err
	}
	switch category {
	case MappingShop:
		current, err := actualShopClientID(facts, entry.ShopID)
		if err != nil {
			return nil, err
		}
		return []*uint{current}, nil
	case MappingAdminProvenance:
		return actualAdminCurrentClients(facts, entry.OperationID)
	default:
		return nil, fmt.Errorf("category %s has no mapping current-value helper", category)
	}
}

func actualShopClientID(facts FactSet, shopID uint) (*uint, error) {
	for _, shop := range facts.Shops {
		if shop.ID == shopID {
			return cloneUint(shop.ClientID), nil
		}
	}
	return nil, fmt.Errorf("shop %d is missing", shopID)
}

func actualAdminCurrentClients(facts FactSet, operationID uuid.UUID) ([]*uint, error) {
	found := false
	current := make([]*uint, 0, 2)
	for _, op := range facts.AdminOperations {
		if op.OperationID == operationID {
			found = true
			current = append(current, cloneUint(op.ClientID))
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("admin operation %s is missing", operationID)
	}
	for _, audit := range facts.AdminAudits {
		if audit.OperationID == operationID {
			current = append(current, cloneUint(audit.ClientID))
		}
	}
	return current, nil
}

func adminMappingTargetsDevice(facts FactSet, operationID uuid.UUID, targetID uint) bool {
	found := false
	for _, audit := range facts.AdminAudits {
		if audit.OperationID != operationID {
			continue
		}
		found = true
		if audit.DeviceID != nil && *audit.DeviceID == targetID {
			return true
		}
	}
	return !found
}

func shopMappingHasTargetAssignment(facts FactSet, targetID, shopID uint) bool {
	for _, assignment := range facts.DeviceAssignments {
		if assignment.DeviceID != targetID || assignment.ValidFrom.After(facts.AsOf) || (assignment.ValidTo != nil && !facts.AsOf.Before(*assignment.ValidTo)) {
			continue
		}
		point, ok := findPoint(facts, assignment.MeasurementPointID)
		if ok && point.ShopID == shopID {
			return true
		}
	}
	return false
}

func findPoint(facts FactSet, id uuid.UUID) (MeasurementPointFact, bool) {
	for _, point := range facts.MeasurementPoints {
		if point.ID == id {
			return point, true
		}
	}
	return MeasurementPointFact{}, false
}
