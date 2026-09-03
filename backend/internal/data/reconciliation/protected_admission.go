package reconciliation

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"power-iot-backend/internal/data/private_migrations"
	"power-iot-backend/internal/data/reconciliation/sourceowner"
)

// ProtectedRoute identifies the only route that can admit protected PR1 work.
// Diagnostic and legacy values are deliberately representable so callers can
// be rejected explicitly at this boundary; neither is an authority path.
type ProtectedRoute string

const (
	ProtectedRouteCanonical  ProtectedRoute = "canonical-pr1-protected"
	ProtectedRouteDiagnostic ProtectedRoute = "diagnostic"
	ProtectedRouteLegacy     ProtectedRoute = "legacy"
)

// AdmissionStatus is a safe logical result. It contains no capability,
// session, transaction, lock, fence, SQL, DSN, or physical handle.
type AdmissionStatus string

const (
	AdmissionAllowed           AdmissionStatus = "ADMITTED"
	AdmissionWaitingForMapping AdmissionStatus = "WAITING_FOR_MAPPING"
	AdmissionDenied            AdmissionStatus = "DENIED"
)

// ProtectedClassification is the closed-world classification vocabulary at
// the canonical route boundary. OWNED_UNPLACED is intentionally distinct from
// unowned, invalid, ambiguous, and conflicting evidence.
type ProtectedClassification string

const (
	ProtectedClassificationOwnedPlaced   ProtectedClassification = "OWNED_PLACED"
	ProtectedClassificationOwnedUnplaced ProtectedClassification = "OWNED_UNPLACED"
	ProtectedClassificationUnowned       ProtectedClassification = "UNOWNED"
	ProtectedClassificationInvalid       ProtectedClassification = "INVALID"
	ProtectedClassificationAmbiguous     ProtectedClassification = "AMBIGUOUS"
	ProtectedClassificationConflicting   ProtectedClassification = "CONFLICTING"
)

// ProtectedD1Eligibility is an opaque, owner-validated D1 witness. Its
// private fields deliberately prevent callers from constructing equivalent
// authority by value. EvidenceDigest is the D1 issuance/provenance digest:
// historical owner evidence retained for exact lease inspection and binding,
// not a digest of the current classification snapshot. The D1 target
// fingerprint in this witness identifies the protected target database; it is
// not the TargetID classification selector below.
type ProtectedD1Eligibility struct {
	owner    *migrations.D1LOwnerService
	identity migrations.D1LLeaseIdentity
	inspect  func(context.Context, migrations.D1LLeaseIdentity) (migrations.D1LLeaseInspection, error)
}

// NewProtectedD1Eligibility defensively validates the complete issue
// projection through the D1 owner. D1LLeaseIssueResult is only a projection;
// neither it nor any caller-supplied status/expiry is trusted directly.
func NewProtectedD1Eligibility(ctx context.Context, owner *migrations.D1LOwnerService, issued migrations.D1LLeaseIssueResult) (ProtectedD1Eligibility, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if owner == nil {
		return ProtectedD1Eligibility{}, errors.New("D1 owner service is required")
	}
	identity := cloneD1LeaseIdentity(issued.Identity)
	if err := validateD1EligibilityProjection(identity, issued); err != nil {
		return ProtectedD1Eligibility{}, err
	}
	witness := ProtectedD1Eligibility{owner: owner, identity: identity}
	inspection, err := owner.Inspect(ctx, identity)
	if err != nil {
		return ProtectedD1Eligibility{}, fmt.Errorf("D1 eligibility inspection failed: %w", err)
	}
	if err := validateD1EligibilityInspection(identity, issued, inspection); err != nil {
		return ProtectedD1Eligibility{}, err
	}
	return witness, nil
}

func (e ProtectedD1Eligibility) inspectOwner(ctx context.Context) (migrations.D1LLeaseInspection, error) {
	if e.inspect != nil {
		return e.inspect(ctx, e.identity)
	}
	if e.owner == nil {
		return migrations.D1LLeaseInspection{}, errors.New("D1 eligibility witness is unavailable")
	}
	return e.owner.Inspect(ctx, e.identity)
}

func cloneD1LeaseIdentity(id migrations.D1LLeaseIdentity) migrations.D1LLeaseIdentity {
	id.TargetFingerprint = append([]byte(nil), id.TargetFingerprint...)
	id.EvidenceDigest = append([]byte(nil), id.EvidenceDigest...)
	return id
}

func validateD1EligibilityProjection(identity migrations.D1LLeaseIdentity, issued migrations.D1LLeaseIssueResult) error {
	if identity.LeaseID == uuid.Nil || identity.OperationID == uuid.Nil || identity.AttemptID == uuid.Nil || identity.Generation <= 0 {
		return errors.New("D1 eligibility identity is incomplete")
	}
	if len(identity.TargetFingerprint) != 32 || len(identity.EvidenceDigest) != 32 {
		return errors.New("D1 eligibility digests are malformed")
	}
	if !equalDigest(identity.TargetFingerprint, issued.TargetFingerprint) || !equalDigest(identity.EvidenceDigest, issued.EvidenceDigest) {
		return errors.New("D1 lease projection digest mismatch")
	}
	return nil
}

func d1EligibilityProjection(inspection migrations.D1LLeaseInspection) migrations.D1LLeaseIssueResult {
	return migrations.D1LLeaseIssueResult{
		Identity:          cloneD1LeaseIdentity(inspection.Identity),
		TargetFingerprint: append([]byte(nil), inspection.TargetFingerprint...),
		EvidenceDigest:    append([]byte(nil), inspection.EvidenceDigest...),
		Status:            inspection.Status, IssuedAt: inspection.IssuedAt,
		ExpiresAt: inspection.ExpiresAt, ActivatedAt: inspection.ActivatedAt,
	}
}

func validateD1EligibilityInspection(identity migrations.D1LLeaseIdentity, issued migrations.D1LLeaseIssueResult, inspection migrations.D1LLeaseInspection) error {
	if inspection.Status != "ACTIVE" || inspection.IssuedAt.IsZero() || inspection.ExpiresAt.IsZero() || inspection.ActivatedAt.IsZero() {
		return errors.New("D1 lease is not currently active")
	}
	if !sameD1LeaseIdentity(identity, inspection.Identity) || !equalDigest(identity.TargetFingerprint, inspection.TargetFingerprint) || !equalDigest(identity.EvidenceDigest, inspection.EvidenceDigest) {
		return errors.New("D1 lease owner binding mismatch")
	}
	if !inspection.IssuedAt.Equal(issued.IssuedAt) || !inspection.ExpiresAt.Equal(issued.ExpiresAt) || !inspection.ActivatedAt.Equal(issued.ActivatedAt) || inspection.Status != issued.Status {
		return errors.New("D1 lease projection is not an exact owner inspection")
	}
	return nil
}

func sameD1LeaseIdentity(a, b migrations.D1LLeaseIdentity) bool {
	return a.LeaseID == b.LeaseID && a.OperationID == b.OperationID && a.AttemptID == b.AttemptID && a.Generation == b.Generation && equalDigest(a.TargetFingerprint, b.TargetFingerprint) && equalDigest(a.EvidenceDigest, b.EvidenceDigest)
}

func equalDigest(a, b []byte) bool {
	return len(a) == 32 && len(b) == 32 && subtle.ConstantTimeCompare(a, b) == 1
}

// ProtectedAdmissionContext is the complete typed context for the canonical
// route. Source is produced by the source-owner pinned query-only collector;
// raw FactSet values are not accepted here. FreshUntil is compatibility
// metadata only; source-owner evidence supplies freshness authority.
type ProtectedAdmissionContext struct {
	Route            ProtectedRoute
	OperationID      uuid.UUID
	AttemptID        uuid.UUID
	TargetID         uint
	Generation       uint64
	ObservedAt       time.Time
	FreshUntil       time.Time
	Source           TrustedSourceSnapshot
	Eligibility      ProtectedD1Eligibility
	CallerAuthorized bool
}

// ProtectedAdmissionResult is safe semantic output. Binding fields are
// correlation data, not live authority.
type ProtectedAdmissionResult struct {
	Status         AdmissionStatus         `json:"status"`
	Classification ProtectedClassification `json:"classification"`
	OperationID    uuid.UUID               `json:"operation_id,omitempty"`
	AttemptID      uuid.UUID               `json:"attempt_id,omitempty"`
	TargetID       uint                    `json:"target_id,omitempty"`
	Generation     uint64                  `json:"generation,omitempty"`
	Reason         string                  `json:"reason,omitempty"`
}

type protectedAttemptKey struct {
	operation uuid.UUID
	attempt   uuid.UUID
}

type protectedAttemptState string

const (
	protectedAttemptActive          protectedAttemptState = "ACTIVE"
	protectedAttemptWaiting         protectedAttemptState = "WAITING_FOR_MAPPING"
	protectedAttemptTerminalDenied  protectedAttemptState = "TERMINAL_DENIED"
	protectedAttemptTerminalSuccess protectedAttemptState = "TERMINAL_SUCCESS"
)

type protectedAttemptBinding struct {
	target     uint
	generation uint64
	state      protectedAttemptState
}

// CanonicalProtectedAdmission is the in-memory logical route ledger. It
// prevents same-attempt re-entry and terminal replay without owning D1, D2,
// D3, A2, or physical authority.
type CanonicalProtectedAdmission struct {
	mu       sync.Mutex
	attempts map[protectedAttemptKey]protectedAttemptBinding
}

func NewCanonicalProtectedAdmission() *CanonicalProtectedAdmission {
	return &CanonicalProtectedAdmission{attempts: make(map[protectedAttemptKey]protectedAttemptBinding)}
}

// Admit is the sole protected admission entry. It validates route identity,
// context binding, freshness, sealed source evidence, and closed-world
// classification. The caller's boolean assertion is intentionally ignored.
func (a *CanonicalProtectedAdmission) Admit(request ProtectedAdmissionContext, now time.Time) ProtectedAdmissionResult {
	result := safeAdmissionResult(request)
	if a == nil {
		result.Classification = ProtectedClassificationInvalid
		result.Reason = "canonical admission service is unavailable"
		return result
	}
	if err := validateProtectedAdmissionContext(request, now); err != nil {
		result.Classification = ProtectedClassificationInvalid
		result.Reason = err.Error()
		return result
	}
	inspection, err := request.Eligibility.inspectOwner(context.Background())
	if err != nil {
		result.Classification = ProtectedClassificationInvalid
		result.Reason = fmt.Sprintf("D1 eligibility inspection failed: %v", err)
		return result
	}
	if err := validateD1EligibilityInspection(request.Eligibility.identity, d1EligibilityProjection(inspection), inspection); err != nil {
		result.Classification = ProtectedClassificationInvalid
		result.Reason = err.Error()
		return result
	}
	if inspection.Identity.OperationID != request.OperationID || inspection.Identity.AttemptID != request.AttemptID {
		result.Classification = ProtectedClassificationInvalid
		result.Reason = "D1 eligibility invocation binding mismatch"
		return result
	}
	// D1's issuance/provenance EvidenceDigest is historical owner evidence.
	// Source.Digest is current classification evidence collected for this
	// invocation. They are intentionally independent: current evidence B must
	// never be required to equal, transport, or reuse issuance evidence A.
	ownerGeneration, err := d1GenerationUint64(inspection.Identity.Generation)
	if err != nil {
		result.Classification = ProtectedClassificationInvalid
		result.Reason = err.Error()
		return result
	}
	// The caller's generation is correlation metadata only. The owner-issued
	// D1 generation is the binding recorded for replay protection and output.
	result.Generation = ownerGeneration

	key := protectedAttemptKey{operation: request.OperationID, attempt: request.AttemptID}
	a.mu.Lock()
	if a.attempts == nil {
		a.attempts = make(map[protectedAttemptKey]protectedAttemptBinding)
	}
	if binding, exists := a.attempts[key]; exists {
		a.mu.Unlock()
		return deniedForExistingAttempt(result, binding, request)
	}
	a.mu.Unlock()

	// Upstream Produce already consumed source-owner evidence. PR1 validates
	// that one-shot evidence again but must not consume it a second time.
	classification, reason := classifyProtectedTarget(trustedFactSet(request.Source.Facts()), request.TargetID)
	result.Classification = classification
	result.Reason = reason
	state := protectedAttemptTerminalDenied
	switch classification {
	case ProtectedClassificationOwnedPlaced:
		result.Status = AdmissionAllowed
		state = protectedAttemptActive
	case ProtectedClassificationOwnedUnplaced:
		result.Status = AdmissionWaitingForMapping
		state = protectedAttemptWaiting
	default:
		result.Status = AdmissionDenied
	}

	// Every complete canonical attempt that reaches semantic classification is
	// recorded, including denial and bounded waiting. This prevents ordinary
	// re-entry from bypassing a terminal result or the future mapping seam.
	a.mu.Lock()
	if existing, already := a.attempts[key]; already {
		a.mu.Unlock()
		return deniedForExistingAttempt(result, existing, request)
	}
	a.attempts[key] = protectedAttemptBinding{target: request.TargetID, generation: ownerGeneration, state: state}
	a.mu.Unlock()
	return result
}

func deniedForExistingAttempt(result ProtectedAdmissionResult, binding protectedAttemptBinding, request ProtectedAdmissionContext) ProtectedAdmissionResult {
	result.Status = AdmissionDenied
	result.Classification = ProtectedClassificationInvalid
	if binding.target != request.TargetID || binding.generation != request.Generation {
		result.Reason = "attempt binding mismatch"
		return result
	}
	switch binding.state {
	case protectedAttemptWaiting:
		result.Classification = ProtectedClassificationOwnedUnplaced
		result.Reason = "WAITING_FOR_MAPPING requires the future dedicated mapping-resumption seam"
	case protectedAttemptTerminalDenied, protectedAttemptTerminalSuccess:
		result.Reason = "terminal attempt replay is forbidden"
	default:
		result.Reason = "attempt has already entered the canonical route"
	}
	return result
}

// RecordTerminal closes an admitted attempt. A future call with the same
// operation/attempt is rejected as terminal replay and a mismatched binding
// cannot alter the recorded attempt.
func (a *CanonicalProtectedAdmission) RecordTerminal(request ProtectedAdmissionContext) error {
	if a == nil {
		return errors.New("canonical admission service is unavailable")
	}
	if request.Route != ProtectedRouteCanonical || request.OperationID == uuid.Nil || request.AttemptID == uuid.Nil || request.TargetID == 0 || request.Generation == 0 {
		return errors.New("terminal context is not a complete canonical binding")
	}
	key := protectedAttemptKey{operation: request.OperationID, attempt: request.AttemptID}
	a.mu.Lock()
	defer a.mu.Unlock()
	binding, ok := a.attempts[key]
	if !ok {
		return errors.New("terminal attempt was not admitted canonically")
	}
	if binding.target != request.TargetID || binding.generation != request.Generation {
		return errors.New("terminal context binding mismatch")
	}
	if binding.state != protectedAttemptActive {
		return errors.New("only an active admitted attempt can become terminal")
	}
	binding.state = protectedAttemptTerminalSuccess
	a.attempts[key] = binding
	return nil
}

func safeAdmissionResult(request ProtectedAdmissionContext) ProtectedAdmissionResult {
	return ProtectedAdmissionResult{
		Status:      AdmissionDenied,
		OperationID: request.OperationID,
		AttemptID:   request.AttemptID,
		TargetID:    request.TargetID,
		Generation:  request.Generation,
	}
}

func validateProtectedAdmissionContext(request ProtectedAdmissionContext, now time.Time) error {
	if request.Route != ProtectedRouteCanonical {
		return fmt.Errorf("route %q cannot authorize protected work", request.Route)
	}
	if request.OperationID == uuid.Nil || request.AttemptID == uuid.Nil {
		return errors.New("operation and attempt bindings are required")
	}
	if request.TargetID == 0 || request.Generation == 0 {
		return errors.New("target and generation bindings are required")
	}
	if request.ObservedAt.IsZero() {
		return errors.New("observation time is required")
	}
	// Admission callers do not select the freshness clock. The owner clock is
	// sampled here; the now argument remains only a compatibility parameter.
	ownerNow := time.Now().UTC()
	if ownerNow.Before(request.ObservedAt) {
		return errors.New("route context is from the future")
	}
	if request.Eligibility.owner == nil && request.Eligibility.inspect == nil {
		return errors.New("D1 eligibility witness is required")
	}
	if request.Eligibility.identity.OperationID != request.OperationID || request.Eligibility.identity.AttemptID != request.AttemptID {
		return errors.New("D1 eligibility invocation binding mismatch")
	}
	binding := sourceInvocationBinding(request)
	if err := request.Source.ValidateForInvocation(binding, ownerNow); err != nil {
		return err
	}
	if !request.Source.ObservedAt().Equal(request.ObservedAt) {
		return errors.New("source evidence time does not match admission observation")
	}
	return nil
}

func d1GenerationUint64(generation int64) (uint64, error) {
	if generation <= 0 {
		return 0, errors.New("D1 eligibility generation is invalid")
	}
	// Positive int64 values are explicitly checked before conversion; no
	// caller-controlled int64-to-uint64 conversion is used for authority.
	return uint64(generation), nil
}

func sourceInvocationBinding(request ProtectedAdmissionContext) sourceowner.InvocationBinding {
	return sourceowner.NewInvocationBinding(request.OperationID, request.AttemptID)
}

// trustedFactSet adapts the source-owner package's opaque snapshot projection
// into the reconciliation package's independent classification value types.
// It copies every row and does not expose or mint source-owner evidence.
func trustedFactSet(f sourceowner.FactSet) FactSet {
	out := FactSet{SchemaVersion: f.SchemaVersion, AsOf: f.AsOf}
	out.Clients = make([]ClientFact, len(f.Clients))
	for i, row := range f.Clients {
		out.Clients[i] = ClientFact(row)
	}
	out.Shops = make([]ShopFact, len(f.Shops))
	for i, row := range f.Shops {
		out.Shops[i] = ShopFact(row)
	}
	out.Users = make([]UserFact, len(f.Users))
	for i, row := range f.Users {
		out.Users[i] = UserFact(row)
	}
	out.UserShopRelations = make([]UserShopRelationFact, len(f.UserShopRelations))
	for i, row := range f.UserShopRelations {
		out.UserShopRelations[i] = UserShopRelationFact(row)
	}
	out.Devices = make([]DeviceFact, len(f.Devices))
	for i, row := range f.Devices {
		out.Devices[i] = DeviceFact(row)
	}
	out.MeasurementPoints = make([]MeasurementPointFact, len(f.MeasurementPoints))
	for i, row := range f.MeasurementPoints {
		out.MeasurementPoints[i] = MeasurementPointFact(row)
	}
	out.DeviceAssignments = make([]DeviceAssignmentFact, len(f.DeviceAssignments))
	for i, row := range f.DeviceAssignments {
		out.DeviceAssignments[i] = DeviceAssignmentFact(row)
	}
	out.AdminOperations = make([]AdminOperationFact, len(f.AdminOperations))
	for i, row := range f.AdminOperations {
		out.AdminOperations[i] = AdminOperationFact(row)
	}
	out.AdminAudits = make([]AdminAuditFact, len(f.AdminAudits))
	for i, row := range f.AdminAudits {
		out.AdminAudits[i] = AdminAuditFact(row)
	}
	return out
}

func classifyProtectedTarget(facts FactSet, targetID uint) (ProtectedClassification, string) {
	decisions, err := Classify(facts)
	if err != nil {
		return ProtectedClassificationInvalid, fmt.Sprintf("source classification failed closed: %v", err)
	}
	var targetDecision Decision
	foundDecision := false
	for _, decision := range decisions {
		if decision.DeviceID == targetID {
			targetDecision = decision
			foundDecision = true
			break
		}
	}
	if !foundDecision {
		return ProtectedClassificationInvalid, "target device is not present in source classification"
	}

	var device *DeviceFact
	for i := range facts.Devices {
		if facts.Devices[i].ID == targetID {
			device = &facts.Devices[i]
			break
		}
	}
	if device == nil {
		return ProtectedClassificationInvalid, "target device is not present in source context"
	}

	clients := make(map[uint]struct{}, len(facts.Clients))
	for _, client := range facts.Clients {
		clients[client.ID] = struct{}{}
	}
	var owner *uint
	if device.InventoryOwnerClientID != nil {
		if *device.InventoryOwnerClientID == 0 {
			return ProtectedClassificationInvalid, "inventory owner identity is invalid"
		}
		if _, ok := clients[*device.InventoryOwnerClientID]; !ok {
			return ProtectedClassificationInvalid, "inventory owner references a missing Client"
		}
		owner = device.InventoryOwnerClientID
	}

	points := make(map[uuid.UUID]MeasurementPointFact, len(facts.MeasurementPoints))
	for _, point := range facts.MeasurementPoints {
		points[point.ID] = point
	}
	shops := make(map[uint]ShopFact, len(facts.Shops))
	for _, shop := range facts.Shops {
		shops[shop.ID] = shop
	}

	active := make([]DeviceAssignmentFact, 0, 1)
	for _, assignment := range facts.DeviceAssignments {
		if assignment.DeviceID != targetID {
			continue
		}
		if assignment.ID == uuid.Nil || assignment.ValidFrom.IsZero() || (assignment.ValidTo != nil && !assignment.ValidTo.After(assignment.ValidFrom)) {
			return ProtectedClassificationInvalid, "target assignment evidence is malformed"
		}
		point, pointOK := points[assignment.MeasurementPointID]
		shop, shopOK := shops[point.ShopID]
		if assignment.MeasurementPointID == uuid.Nil || !pointOK || point.ShopID == 0 || !shopOK || shop.ID == 0 {
			return ProtectedClassificationInvalid, "target assignment has missing placement authority"
		}
		if shop.ClientID != nil {
			if *shop.ClientID == 0 {
				return ProtectedClassificationInvalid, "placement authority has an invalid Client"
			}
			if _, ok := clients[*shop.ClientID]; !ok {
				return ProtectedClassificationInvalid, "placement authority references a missing Client"
			}
		}
		if !assignment.ValidFrom.After(facts.AsOf) && (assignment.ValidTo == nil || facts.AsOf.Before(*assignment.ValidTo)) {
			active = append(active, assignment)
		}
	}
	if len(active) > 1 {
		return ProtectedClassificationAmbiguous, "multiple active placements are present"
	}
	if targetDecision.Classification == BlockingIntegrityError {
		if len(active) == 0 && owner != nil && strings.Contains(targetDecision.Reason, "existing inventory owner has no independent current authority") {
			return ProtectedClassificationOwnedUnplaced, "authoritative ownership exists without current placement"
		}
		if strings.Contains(targetDecision.Reason, "future assignment changes Client authority") || strings.Contains(targetDecision.Reason, "owner conflicts") {
			return ProtectedClassificationConflicting, targetDecision.Reason
		}
		return ProtectedClassificationInvalid, targetDecision.Reason
	}
	if len(active) == 0 {
		if owner != nil {
			return ProtectedClassificationOwnedUnplaced, "authoritative ownership exists without current placement"
		}
		if device.ShopID != 0 {
			return ProtectedClassificationUnowned, "Device.ShopID is compatibility-only and cannot establish ownership"
		}
		return ProtectedClassificationUnowned, "no independent ownership authority is present"
	}

	point := points[active[0].MeasurementPointID]
	shop := shops[point.ShopID]
	if owner == nil {
		return ProtectedClassificationUnowned, "placement does not establish ownership authority"
	}
	if shop.ClientID == nil {
		return ProtectedClassificationConflicting, "placement has no current Client authority"
	}
	if *owner != *shop.ClientID {
		return ProtectedClassificationConflicting, "ownership and placement Client authority conflict"
	}
	return ProtectedClassificationOwnedPlaced, "ownership and current placement agree through the relational path"
}
