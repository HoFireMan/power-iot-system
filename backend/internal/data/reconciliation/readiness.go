package reconciliation

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"power-iot-backend/internal/data/migrations"

	"github.com/google/uuid"
)

// LifecycleState is the D2-only lifecycle boundary around protected
// reconciliation and readiness. It is intentionally not a migration authority:
// it cannot write migration metadata, run DDL, or advance v5 to v6.
type LifecycleState string

const (
	LifecycleBlocked                 LifecycleState = "BLOCKED"
	LifecycleCleanV5                 LifecycleState = "CLEAN_V5"
	LifecycleProtectedReconciliation LifecycleState = "PROTECTED_RECONCILIATION"
	LifecycleReadinessCheck          LifecycleState = "READINESS_CHECK"
	LifecycleReadinessFailed         LifecycleState = "READINESS_FAILED"
	LifecycleCutoverReadyV5          LifecycleState = "CUTOVER_READY_V5"
	LifecycleCleanV6                 LifecycleState = "CLEAN_V6"
)

// WriterAdmission describes the only writer lane exposed by this lifecycle
// seam. Ordinary v5 writers are deliberately not admitted here; D1 owns the
// separate pre-v6 runtime contract and D4 owns the protected composition.
type WriterAdmission string

const (
	WriterAdmissionDenied      WriterAdmission = "DENIED"
	WriterAdmissionProtectedA2 WriterAdmission = "PROTECTED_A2"
)

var (
	ErrLifecycleBlocked      = errors.New("lifecycle is fail-closed")
	ErrLifecycleTransition   = errors.New("invalid lifecycle transition")
	ErrLifecycleWriterDenied = errors.New("writer admission is denied by lifecycle state")
	ErrReadinessNotMet       = errors.New("readiness predicates did not pass")
	ErrReadinessEvidence     = errors.New("reconciliation evidence is not committed and post-verified")
	ErrD007CapabilityInvalid = errors.New("D007 readiness capability is invalid")
	ErrD007CapabilityExpired = errors.New("D007 readiness capability is expired")
	ErrD007CapabilityReused  = errors.New("D007 readiness capability was already consumed")
	ErrD007CapabilityIssued  = errors.New("D007 readiness capability was already issued for this binding")
)

const (
	D007PredicateIdentity = "D007_READINESS"
	D007PredicateVersion  = "D007_V2_02_PREDICATES_V1"
	maxD007IssuanceLedger = 4096
)

// Lifecycle is a small, in-memory state machine. A fresh value must be created
// from a fresh D3 protected-state observation for every protected attempt. No
// reset operation exists, so a failed readiness check cannot be reused for a
// later writer or cutover attempt.
type Lifecycle struct {
	state LifecycleState
}

// NewLifecycleFromProtectedState maps the D3 observation into the D2 lifecycle
// seam. Only exact clean v5 and clean v6 observations are admitted as useful
// states. Dirty, ambiguous, missing, duplicate, unreadable, bootstrap, and
// future observations all become BLOCKED and cannot start writers.
func NewLifecycleFromProtectedState(state migrations.ProtectedMigrationState) Lifecycle {
	switch state {
	case migrations.ProtectedStateCleanV5:
		return Lifecycle{state: LifecycleCleanV5}
	case migrations.ProtectedStateCleanV6:
		return Lifecycle{state: LifecycleCleanV6}
	default:
		return Lifecycle{state: LifecycleBlocked}
	}
}

func (l Lifecycle) State() LifecycleState {
	return l.state
}

// WriterAdmission returns a descriptive lane only. Call RequireWriterAdmission
// to enforce the lane and, for A2, prove live protected-fence ownership.
func (l Lifecycle) WriterAdmission() WriterAdmission {
	switch l.state {
	case LifecycleProtectedReconciliation:
		return WriterAdmissionProtectedA2
	default:
		return WriterAdmissionDenied
	}
}

// AdmitProtectedReconciliation enters the only D2 state that may host A2
// writes. Both the D1 external-writer evidence and the D3 protected-fence
// capability are required; a clean metadata/catalog observation alone never
// starts a writer.
func (l *Lifecycle) AdmitProtectedReconciliation(capability migrations.ProtectedWorkCapability, admission migrations.ExternalWriterAdmission) error {
	if l == nil || l.state != LifecycleCleanV5 {
		return lifecycleTransitionError(l, LifecycleProtectedReconciliation)
	}
	if err := migrations.RequireExternalWriterAdmission(admission); err != nil {
		return fmt.Errorf("%w: %v", ErrLifecycleWriterDenied, err)
	}
	if err := migrations.RequireProtectedWork(capability); err != nil {
		return fmt.Errorf("%w: %v", ErrLifecycleWriterDenied, err)
	}
	l.state = LifecycleProtectedReconciliation
	return nil
}

// BeginReadiness closes the A2 writer lane before the fresh readiness check.
// This is a state transition only; it does not release or acquire a fence and
// does not invoke a later orchestration phase.
func (l *Lifecycle) BeginReadiness() error {
	if l == nil || l.state != LifecycleProtectedReconciliation {
		return lifecycleTransitionError(l, LifecycleReadinessCheck)
	}
	l.state = LifecycleReadinessCheck
	return nil
}

// ApplyReadiness records the result of the fresh D2 predicate evaluation. A
// failed result permanently denies writers for this lifecycle instance. A
// successful v5 result only makes the protected cutover eligible; it does not
// imply D4, migration ownership, or permission to execute DDL.
func (l *Lifecycle) ApplyReadiness(decision ReadinessDecision) error {
	if l == nil || l.state != LifecycleReadinessCheck {
		return lifecycleTransitionError(l, LifecycleReadinessCheck)
	}
	if decision.proof == nil || decision.proof.Target != decision.Target || decision.proof.Ready != decision.Ready {
		l.state = LifecycleReadinessFailed
		return fmt.Errorf("%w: readiness decision was not produced by EvaluateReadiness", ErrReadinessNotMet)
	}
	if !decision.Ready {
		l.state = LifecycleReadinessFailed
		return readinessError(decision)
	}
	if decision.Target != ReadinessForCutover {
		l.state = LifecycleReadinessFailed
		return fmt.Errorf("%w: target=%s is not the protected v5 cutover target", ErrReadinessNotMet, decision.Target)
	}
	l.state = LifecycleCutoverReadyV5
	return nil
}

// RequireWriterAdmission enforces the state-specific writer lane. D2 never
// admits writes in blocked, clean-v5, readiness, failed, cutover-ready, or
// clean-v6 states. D1/D6 must perform the separate v6-compatible runtime
// admission; this seam cannot be used to bypass that deployment boundary.
func (l Lifecycle) RequireWriterAdmission(admission WriterAdmission, capability migrations.ProtectedWorkCapability) error {
	if admission == WriterAdmissionProtectedA2 && l.state == LifecycleProtectedReconciliation {
		if err := migrations.RequireProtectedWork(capability); err != nil {
			return fmt.Errorf("%w: %v", ErrLifecycleWriterDenied, err)
		}
		return nil
	}
	return fmt.Errorf("%w: state=%s admission=%s", ErrLifecycleWriterDenied, l.state, admission)
}

func lifecycleTransitionError(l *Lifecycle, target LifecycleState) error {
	state := LifecycleBlocked
	if l != nil {
		state = l.state
	}
	if state == LifecycleBlocked || state == LifecycleReadinessFailed {
		return fmt.Errorf("%w: state=%s target=%s", ErrLifecycleBlocked, state, target)
	}
	return fmt.Errorf("%w: state=%s target=%s", ErrLifecycleTransition, state, target)
}

// ReadinessTarget identifies a D2 decision's purpose. Neither target starts a
// writer or implies the D4 continuous orchestration phase.
type ReadinessTarget string

const (
	ReadinessForCutover ReadinessTarget = "V5_CUTOVER"
	ReadinessForServing ReadinessTarget = "V6_SERVING"
)

// ReconciliationEvidence is the narrow handoff from the protected A2 writer.
// Fresh facts and deterministic planning remain authoritative; these fields
// only ensure that D2 cannot certify a readiness check before A2's commit and
// durable post-commit verification have completed.
type ReconciliationEvidence struct {
	Outcome               ExecutionOutcome
	Committed             bool
	PostCommitVerified    bool
	PlanID                string
	PlanDigest            string
	SourceFactsDigest     string
	MappingBasisDigest    string
	MappingDigest         string
	PostCommitFactsDigest string
	PostCommitFactsAsOf   time.Time
	PredicateIdentity     string
	PredicateVersion      string

	// seal is only attached by the protected executor after TX2 verification.
	// Exported evidence fields alone are never sufficient for readiness.
	seal *executionEvidenceSeal
}

type executionEvidenceSeal struct {
	identity        [32]byte
	tx2PostVerified bool
}

// D007Binding is the non-secret identity tuple bound into a live D2
// capability. It is evidence metadata; it never grants physical authority.
type D007Binding struct {
	AttemptID         uuid.UUID
	TargetFingerprint [32]byte
	Generation        int64
}

// D007CapabilityBinding is the complete immutable validation tuple retained by
// D2. FactsDigest and ProofDigest bind the capability to the post-commit TX2
// observation and the D2 proof, while FreshUntil bounds its freshness.
type D007CapabilityBinding struct {
	D007Binding
	FactsDigest      [32]byte
	ProofDigest      [32]byte
	FreshUntil       time.Time
	PredicateVersion string
}

type d007CapabilityState struct {
	mu       sync.Mutex
	binding  D007CapabilityBinding
	consumed bool
}

// D007CapabilityIssuer is the bounded D2 issuance ledger. An issuance remains
// recorded after consumption: an attempt/binding is never handed a second
// live capability, even if the first one was consumed or expired.
type D007CapabilityIssuer struct {
	mu      sync.Mutex
	issued  map[D007Binding]struct{}
	maximum int
}

func NewD007CapabilityIssuer() *D007CapabilityIssuer {
	return &D007CapabilityIssuer{issued: make(map[D007Binding]struct{}), maximum: maxD007IssuanceLedger}
}

func (i *D007CapabilityIssuer) reserve(binding D007Binding) error {
	if i == nil {
		return ErrD007CapabilityInvalid
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.issued == nil {
		i.issued = make(map[D007Binding]struct{})
	}
	if _, exists := i.issued[binding]; exists {
		return ErrD007CapabilityIssued
	}
	if i.maximum <= 0 {
		i.maximum = maxD007IssuanceLedger
	}
	if len(i.issued) >= i.maximum {
		return ErrD007CapabilityInvalid
	}
	i.issued[binding] = struct{}{}
	return nil
}

// LiveD007Capability is deliberately opaque. Its private pointer makes a
// forged zero value invalid and makes copies share one-shot state. No raw
// bearer value or capability binding is serializable.
type LiveD007Capability struct{ state *d007CapabilityState }

func (LiveD007Capability) String() string               { return "LIVE_D007_CAPABILITY[opaque]" }
func (LiveD007Capability) MarshalJSON() ([]byte, error) { return []byte("null"), nil }

// D007TerminalEvidence is safe semantic evidence produced after D2 has
// authoritatively validated and consumed the live capability. It is distinct
// from LiveD007Capability and never contains the capability itself.
type D007TerminalEvidence struct {
	Kind              string    `json:"kind"`
	Status            string    `json:"status"`
	AttemptID         uuid.UUID `json:"attempt_id"`
	TargetFingerprint string    `json:"target_fingerprint_digest"`
	Generation        int64     `json:"generation"`
	FactsDigest       string    `json:"facts_digest"`
	ProofDigest       string    `json:"proof_digest"`
	FreshUntil        time.Time `json:"fresh_until"`
	ConsumedAt        time.Time `json:"consumed_at"`
	PredicateVersion  string    `json:"predicate_version"`

	// state is the D2-owned terminal projection seal. It is intentionally not
	// serialized or exposed: semantic fields alone are safe evidence, not D3
	// issuance authority.
	state *d007TerminalState
}

type d007TerminalState struct {
	mu            sync.Mutex
	identity      [32]byte
	consumedAt    time.Time
	handoffIssued bool
}

// ReconciliationEvidenceFromReport preserves the A2 evidence identifiers
// while keeping the readiness check anchored to a fresh fact snapshot.
func ReconciliationEvidenceFromReport(report ExecutionReport) ReconciliationEvidence {
	return ReconciliationEvidence{
		Outcome:               report.Outcome,
		Committed:             report.Committed,
		PostCommitVerified:    report.PostCommitVerified,
		PlanID:                report.PlanID.String(),
		PlanDigest:            report.PlanDigest,
		SourceFactsDigest:     report.SourceFactsDigest,
		MappingBasisDigest:    report.MappingBasisDigest,
		MappingDigest:         report.MappingDigest,
		PostCommitFactsDigest: report.PostCommitFactsDigest,
		PostCommitFactsAsOf:   report.PostCommitFactsAsOf,
	}
}

func trustedReconciliationEvidence(report ExecutionReport) ReconciliationEvidence {
	evidence := ReconciliationEvidenceFromReport(report)
	evidence.PredicateIdentity = D007PredicateIdentity
	evidence.PredicateVersion = D007PredicateVersion
	return sealReconciliationEvidence(evidence)
}

func reconciliationEvidenceIdentity(e ReconciliationEvidence) [32]byte {
	h := sha256.New()
	// Length-prefix every string so the proof cannot become ambiguous through
	// concatenation. Include every trusted A2 identity and authoritative fact.
	write := func(value string) {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(value)))
		h.Write(length[:])
		h.Write([]byte(value))
	}
	write(string(e.Outcome))
	write(strconv.FormatBool(e.Committed))
	write(strconv.FormatBool(e.PostCommitVerified))
	write(e.PlanID)
	write(e.PlanDigest)
	write(e.SourceFactsDigest)
	write(e.MappingBasisDigest)
	write(e.MappingDigest)
	write(e.PostCommitFactsDigest)
	write(e.PostCommitFactsAsOf.UTC().Format(time.RFC3339Nano))
	write(e.PredicateIdentity)
	write(e.PredicateVersion)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func sealReconciliationEvidence(e ReconciliationEvidence) ReconciliationEvidence {
	e.seal = &executionEvidenceSeal{identity: reconciliationEvidenceIdentity(e), tx2PostVerified: true}
	return e
}

func (e ReconciliationEvidence) valid(facts FactSet) bool {
	if e.Outcome != ExecutionCommittedAndVerified || !e.Committed || !e.PostCommitVerified ||
		e.PlanID == "" || e.PlanDigest == "" || e.SourceFactsDigest == "" ||
		e.MappingBasisDigest == "" || e.PostCommitFactsDigest == "" || e.PostCommitFactsAsOf.IsZero() ||
		e.PredicateIdentity != D007PredicateIdentity || e.PredicateVersion != D007PredicateVersion ||
		e.seal == nil || !e.seal.tx2PostVerified || e.seal.identity != reconciliationEvidenceIdentity(e) {
		return false
	}
	_, digest, err := CanonicalSourceFacts(facts)
	if err != nil || !e.PostCommitFactsAsOf.Equal(facts.AsOf.UTC()) {
		return false
	}
	// SourceFactsDigest and MappingBasisDigest identify the pre-A2 snapshot;
	// they must not be compared with post-A2 facts, which may legitimately
	// differ after reconciliation writes. Their exact values are protected by
	// the executor seal above. Only TX2's post-commit digest is recomputed here.
	return e.PostCommitFactsDigest == hex.EncodeToString(digest)
}

func readinessChecksDigest(checks []ReadinessCheck) [32]byte {
	h := sha256.New()
	h.Write([]byte("D007_READINESS_CHECKS_V1"))
	var count [8]byte
	binary.BigEndian.PutUint64(count[:], uint64(len(checks)))
	h.Write(count[:])
	write := func(value string) {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(value)))
		h.Write(length[:])
		h.Write([]byte(value))
	}
	for _, check := range checks {
		write(check.Name)
		if check.Passed {
			h.Write([]byte{1})
		} else {
			h.Write([]byte{0})
		}
		write(check.Reason)
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func d007ProofDigest(decision ReadinessDecision, factsDigest [32]byte) [32]byte {
	h := sha256.New()
	h.Write([]byte(D007PredicateIdentity))
	h.Write([]byte(D007PredicateVersion))
	h.Write([]byte(decision.Target))
	h.Write(decision.proof.EvidenceIdentity[:])
	h.Write(decision.proof.ChecksDigest[:])
	h.Write(factsDigest[:])
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

const d007FreshnessWindow = time.Minute

func d2Now() time.Time { return time.Now().UTC() }

func d007FreshUntil(facts FactSet) time.Time {
	return facts.AsOf.UTC().Add(d007FreshnessWindow)
}

func validD007BindingShape(binding D007CapabilityBinding) bool {
	return binding.AttemptID != uuid.Nil && binding.Generation > 0 &&
		binding.TargetFingerprint != [32]byte{} && binding.FactsDigest != [32]byte{} &&
		binding.ProofDigest != [32]byte{} && !binding.FreshUntil.IsZero() &&
		binding.PredicateVersion == D007PredicateVersion
}

func validD007Binding(binding D007CapabilityBinding) bool {
	return validD007BindingShape(binding) && binding.FreshUntil.After(d2Now())
}

var defaultD007Issuer = NewD007CapabilityIssuer()

// IssueLiveD007Capability is the sole D2 issuance seam. It accepts only a
// decision produced by EvaluateReadiness and a fresh TX2 fact digest; a caller
// cannot manufacture the unexported proof carried by ReadinessDecision.
func IssueLiveD007Capability(decision ReadinessDecision, facts FactSet, binding D007Binding, _ time.Time) (LiveD007Capability, error) {
	return defaultD007Issuer.issue(decision, facts, binding)
}

func (i *D007CapabilityIssuer) issue(decision ReadinessDecision, facts FactSet, binding D007Binding) (LiveD007Capability, error) {
	if decision.proof == nil || decision.Target != ReadinessForCutover || !decision.Ready ||
		decision.proof.Target != decision.Target || !decision.proof.Ready ||
		decision.proof.PredicateIdentity != D007PredicateIdentity || decision.proof.PredicateVersion != D007PredicateVersion ||
		decision.proof.EvidenceIdentity == [32]byte{} || decision.proof.ChecksDigest != readinessChecksDigest(decision.Checks) {
		return LiveD007Capability{}, ErrD007CapabilityInvalid
	}
	_, digestBytes, err := CanonicalSourceFacts(facts)
	now := d2Now()
	freshUntil := d007FreshUntil(facts)
	if err != nil || facts.AsOf.IsZero() || facts.AsOf.After(now) || !freshUntil.After(now) {
		return LiveD007Capability{}, ErrD007CapabilityInvalid
	}
	var factsDigest [32]byte
	copy(factsDigest[:], digestBytes)
	// EvaluateReadiness binds its proof to this exact canonical snapshot. Do
	// not allow a caller to pair a valid decision with a separately selected
	// or modified FactSet.
	if decision.proof.FactsDigest != factsDigest || !decision.proof.AsOf.Equal(facts.AsOf.UTC()) {
		return LiveD007Capability{}, ErrD007CapabilityInvalid
	}
	capBinding := D007CapabilityBinding{D007Binding: binding, FactsDigest: factsDigest, FreshUntil: freshUntil, PredicateVersion: D007PredicateVersion}
	capBinding.ProofDigest = d007ProofDigest(decision, factsDigest)
	if !validD007Binding(capBinding) {
		return LiveD007Capability{}, ErrD007CapabilityInvalid
	}
	if err := i.reserve(binding); err != nil {
		return LiveD007Capability{}, err
	}
	return LiveD007Capability{state: &d007CapabilityState{binding: capBinding}}, nil
}

// ConsumeLiveD007Capability performs authoritative D2 validation and exactly
// once consumption. Expected facts/proof/binding must be supplied by the same
// post-TX2 orchestration event; safe evidence cannot be substituted.
func ConsumeLiveD007Capability(capability LiveD007Capability, expected D007CapabilityBinding, _ time.Time) (D007TerminalEvidence, error) {
	if capability.state == nil {
		return D007TerminalEvidence{}, ErrD007CapabilityInvalid
	}
	now := d2Now()
	state := capability.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.consumed {
		return D007TerminalEvidence{}, ErrD007CapabilityReused
	}
	// Every validation attempt is terminal. This deliberately marks the
	// capability before returning any failure, including mismatches and expiry;
	// an uncertain or malformed presentation cannot be retried.
	terminalize := func(err error) (D007TerminalEvidence, error) {
		state.consumed = true
		return D007TerminalEvidence{}, err
	}
	if !validD007BindingShape(state.binding) || state.binding.PredicateVersion != expected.PredicateVersion ||
		state.binding.D007Binding != expected.D007Binding || state.binding.FactsDigest != expected.FactsDigest ||
		state.binding.ProofDigest != expected.ProofDigest || !state.binding.FreshUntil.Equal(expected.FreshUntil) {
		return terminalize(ErrD007CapabilityInvalid)
	}
	if !state.binding.FreshUntil.After(now.UTC()) {
		return terminalize(ErrD007CapabilityExpired)
	}
	state.consumed = true
	targetDigest := sha256.Sum256(state.binding.TargetFingerprint[:])
	evidence := D007TerminalEvidence{
		Kind: "TERMINAL_D007_VALIDATION_CONSUMPTION_EVIDENCE", Status: "CONSUMED",
		AttemptID:         state.binding.AttemptID,
		TargetFingerprint: hex.EncodeToString(targetDigest[:]),
		Generation:        state.binding.Generation, FactsDigest: hex.EncodeToString(state.binding.FactsDigest[:]),
		ProofDigest: hex.EncodeToString(state.binding.ProofDigest[:]), FreshUntil: state.binding.FreshUntil,
		ConsumedAt: now.UTC(), PredicateVersion: state.binding.PredicateVersion,
	}
	evidence.state = &d007TerminalState{consumedAt: now.UTC()}
	evidence.state.identity = d007TerminalIdentity(evidence)
	return evidence, nil
}

// ReadinessRequest contains only observations. EvaluateReadiness performs no
// I/O and has no write capability. For v5 cutover, Facts must be the fresh
// post-A2 snapshot collected under the already-owned protected session. For v6
// serving, D3 supplies the exact clean-v6 state and semantic proof marker.
type ReadinessRequest struct {
	Target                      ReadinessTarget
	ProtectedState              migrations.ProtectedMigrationState
	Facts                       FactSet
	Reconciliation              ReconciliationEvidence
	PostCutoverSemanticVerified bool
}

type ReadinessCheck struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Reason string `json:"reason,omitempty"`
}

// ReadinessDecision is evidence, not a capability. In particular, Ready=true
// never grants a fence, migration lock, DDL permission, or D4 orchestration.
// proof is deliberately unexported so a caller cannot manufacture a successful
// decision for Lifecycle.ApplyReadiness by filling exported fields.
type ReadinessDecision struct {
	Target ReadinessTarget  `json:"target"`
	Ready  bool             `json:"ready"`
	Checks []ReadinessCheck `json:"checks"`
	proof  *readinessProof
}

type readinessProof struct {
	Target            ReadinessTarget
	Ready             bool
	FactsDigest       [32]byte
	ChecksDigest      [32]byte
	AsOf              time.Time
	EvidenceIdentity  [32]byte
	PredicateIdentity string
	PredicateVersion  string
}

func (d ReadinessDecision) FailedPredicates() []string {
	failed := make([]string, 0)
	for _, check := range d.Checks {
		if !check.Passed {
			failed = append(failed, check.Name)
		}
	}
	return failed
}

// RequireReadiness is the fail-closed convenience seam for future protected
// orchestration. It still returns only D2 evidence; it does not acquire locks,
// start writers, or invoke a migration authority.
func RequireReadiness(request ReadinessRequest) (ReadinessDecision, error) {
	decision := EvaluateReadiness(request)
	if !decision.Ready {
		return decision, readinessError(decision)
	}
	return decision, nil
}

func readinessError(decision ReadinessDecision) error {
	failedPredicates := decision.FailedPredicates()
	failed := strings.Join(failedPredicates, ",")
	if failed == "" {
		failed = "unspecified"
	}
	cause := error(ErrReadinessNotMet)
	for _, predicate := range failedPredicates {
		if predicate == "reconciliation-committed-and-post-verified" {
			cause = errors.Join(cause, ErrReadinessEvidence)
			break
		}
	}
	return fmt.Errorf("%w: target=%s failed=%s", cause, decision.Target, failed)
}

// EvaluateReadiness evaluates the exact D2 semantic boundary. A non-clean D3
// state is always false, including dirty/ambiguous/missing/duplicate/unreadable
// and unsupported-future observations represented by D3 as non-clean states.
func EvaluateReadiness(request ReadinessRequest) ReadinessDecision {
	decision := ReadinessDecision{Target: request.Target, Checks: make([]ReadinessCheck, 0, 8)}
	add := func(name string, passed bool, reason string) {
		decision.Checks = append(decision.Checks, ReadinessCheck{Name: name, Passed: passed, Reason: reason})
		if !passed {
			decision.Ready = false
		}
	}

	var evidenceIdentity [32]byte
	switch request.Target {
	case ReadinessForCutover:
		add("protected-state-clean-v5", request.ProtectedState == migrations.ProtectedStateCleanV5,
			protectedStateReason(request.ProtectedState, migrations.ProtectedStateCleanV5))
		evidenceValid := request.Reconciliation.valid(request.Facts)
		if evidenceValid {
			evidenceIdentity = reconciliationEvidenceIdentity(request.Reconciliation)
		}
		add("reconciliation-committed-and-post-verified", evidenceValid,
			protectedEvidenceReason(request.Reconciliation, request.Facts))
		add("fresh-facts-v5", request.Facts.SchemaVersion == SchemaVersion && !request.Facts.AsOf.IsZero(),
			factsVersionReason(request.Facts))
		if request.Facts.SchemaVersion != SchemaVersion || request.Facts.AsOf.IsZero() {
			add("semantic-data-predicates", false, "facts are not a complete v5 snapshot")
			add("deterministic-post-a2-plan", false, "facts cannot produce a deterministic v5 plan")
			break
		}

		classes, classErr := ClassifyFacts(request.Facts)
		semanticOK := classErr == nil
		semanticReason := "all v5 fact classifications are terminal"
		if classErr != nil {
			semanticReason = classErr.Error()
		} else {
			for _, class := range classes {
				if class.Classification != AlreadyConsistent {
					semanticOK = false
					semanticReason = fmt.Sprintf("%s:%s:%s", class.Kind, class.StableID, class.Classification)
					break
				}
			}
		}
		add("semantic-data-predicates", semanticOK, semanticReason)

		plan, planErr := BuildPlan(request.Facts, nil)
		planOK := planErr == nil && plan.SchemaVersion == SchemaVersion && plan.AsOf.Equal(request.Facts.AsOf.UTC()) && len(plan.Blockers) == 0 && len(plan.RequiredExplicitMappings) == 0
		planReason := "fresh facts produce a blocker-free plan without required mappings"
		if planErr != nil {
			planReason = planErr.Error()
		} else if !planOK {
			planReason = fmt.Sprintf("plan blockers=%d required_mappings=%d", len(plan.Blockers), len(plan.RequiredExplicitMappings))
		}
		add("deterministic-post-a2-plan", planOK, planReason)
	case ReadinessForServing:
		add("protected-state-clean-v6", request.ProtectedState == migrations.ProtectedStateCleanV6,
			protectedStateReason(request.ProtectedState, migrations.ProtectedStateCleanV6))
		add("post-cutover-semantic-proof", request.PostCutoverSemanticVerified,
			"D3/D5 final semantic verifier did not provide proof")
	default:
		add("known-readiness-target", false, fmt.Sprintf("unsupported target %q", request.Target))
	}

	decision.Ready = len(decision.FailedPredicates()) == 0
	proof := &readinessProof{
		Target: decision.Target, Ready: decision.Ready, ChecksDigest: readinessChecksDigest(decision.Checks), AsOf: request.Facts.AsOf.UTC(),
		EvidenceIdentity: evidenceIdentity, PredicateIdentity: D007PredicateIdentity,
		PredicateVersion: D007PredicateVersion,
	}
	if _, digest, err := CanonicalSourceFacts(request.Facts); err == nil {
		copy(proof.FactsDigest[:], digest)
	}
	decision.proof = proof
	return decision
}

func protectedStateReason(got, want migrations.ProtectedMigrationState) string {
	if got == want {
		return "exact protected state observed"
	}
	return fmt.Sprintf("got protected state %s, want %s", got, want)
}

func protectedEvidenceReason(evidence ReconciliationEvidence, facts FactSet) string {
	if evidence.valid(facts) {
		return "A2 commit and durable post-commit verification are present"
	}
	return fmt.Sprintf("outcome=%s committed=%t post_commit_verified=%t", evidence.Outcome, evidence.Committed, evidence.PostCommitVerified)
}

func factsVersionReason(facts FactSet) string {
	if facts.SchemaVersion == SchemaVersion && !facts.AsOf.IsZero() {
		return "fresh v5 facts snapshot has schema version and timestamp"
	}
	return fmt.Sprintf("schema_version=%q as_of_zero=%t", facts.SchemaVersion, facts.AsOf.IsZero())
}
