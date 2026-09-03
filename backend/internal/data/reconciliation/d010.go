package reconciliation

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"power-iot-backend/internal/data/private_migrations"

	"github.com/google/uuid"
)

// D010 is deliberately an owner-local authority seam. The exported values
// below are transport types only; the private seals are what make issuance
// authoritative. In particular, a report or a D007 capability is not a D010
// handoff and cannot be substituted for one.
const (
	D010Purpose           = "D3_PROTECTED_CONTINUATION"
	D010PredicateIdentity = D007PredicateIdentity
	D010PredicateVersion  = D007PredicateVersion
	maxD010Handoffs       = 4096
)

var (
	ErrD010HandoffInvalid = errors.New("D010 handoff is invalid")
	ErrD010HandoffExpired = errors.New("D010 handoff is stale or expired")
	ErrD010HandoffReused  = errors.New("D010 handoff was already consumed")
	ErrD010HandoffUnknown = errors.New("D010 handoff state is UNKNOWN")
	ErrD010HandoffIssued  = errors.New("D010 handoff was already issued")
)

// D009Evidence is the sealed post-transaction evidence accepted by D3. Its
// fields are descriptive only. A value made by copying its JSON/report fields
// has no private seal and is rejected by IssueD010Handoff.
type D009Evidence struct {
	// OperationID is the owner-issued D1 operation identity bound to this
	// execution evidence. It is never inferred from PlanID.
	OperationID       uuid.UUID
	AttemptID         uuid.UUID
	TargetFingerprint [32]byte
	Generation        int64
	TX1Identity       [32]byte
	TX2Identity       [32]byte
	FactsDigest       [32]byte
	PlanDigest        [32]byte
	PostCommitAsOf    time.Time
	seal              *d009EvidenceSeal
}

type d009EvidenceSeal struct{ identity [32]byte }

type d009ExecutionSeal struct {
	identity [32]byte
	evidence D009Evidence
}

func makeD009ExecutionSeal(report ExecutionReport, lease *migrations.D1LLeaseIdentity, target [32]byte) *d009ExecutionSeal {
	if lease == nil || lease.OperationID == uuid.Nil || report.OperationID != lease.OperationID || report.Outcome != ExecutionCommittedAndVerified || !report.Committed || !report.PostCommitVerified ||
		report.PlanDigest == "" || report.PostCommitFactsDigest == "" || report.PostCommitFactsAsOf.IsZero() {
		return nil
	}
	factsBytes, err := hex.DecodeString(report.PostCommitFactsDigest)
	if err != nil || len(factsBytes) != 32 {
		return nil
	}
	var facts [32]byte
	copy(facts[:], factsBytes)
	tx1 := sha256.Sum256([]byte("D009_TX1_SUCCESS:" + lease.OperationID.String() + ":" + report.PlanDigest))
	tx2 := sha256.Sum256([]byte("D009_TX2_SUCCESS:" + report.PostCommitFactsDigest + ":" + report.PostCommitFactsAsOf.UTC().Format(time.RFC3339Nano)))
	plan := sha256.Sum256([]byte(report.PlanDigest))
	evidence := D009Evidence{OperationID: lease.OperationID, AttemptID: lease.AttemptID, TargetFingerprint: target, Generation: lease.Generation,
		TX1Identity: tx1, TX2Identity: tx2, FactsDigest: facts, PlanDigest: plan, PostCommitAsOf: report.PostCommitFactsAsOf.UTC()}
	evidence.seal = &d009EvidenceSeal{identity: d009Identity(evidence)}
	return &d009ExecutionSeal{identity: evidence.seal.identity, evidence: evidence}
}

// D010HandoffContext is the verifier's expected operation context. It is not
// an authority and cannot mint or alter a handoff.
type D010HandoffContext struct {
	OperationID       uuid.UUID
	AttemptID         uuid.UUID
	TargetFingerprint [32]byte
	Generation        int64
}

// D010Handoff is opaque, non-serializable and one-shot. Copies share private
// state, so a copied value cannot obtain a second continuation.
type D010Handoff struct{ state *d010HandoffState }

func (D010Handoff) String() string               { return "D010_HANDOFF[opaque]" }
func (D010Handoff) GoString() string             { return "D010_HANDOFF[opaque]" }
func (D010Handoff) MarshalJSON() ([]byte, error) { return []byte("null"), nil }
func (D010Handoff) MarshalText() ([]byte, error) {
	return nil, errors.New("D010 handoff is not serializable")
}
func (D010Handoff) MarshalBinary() ([]byte, error) {
	return nil, errors.New("D010 handoff is not serializable")
}

// D010HandoffIssuer owns the one-shot ledger. Recreating an issuer without its
// state store deliberately makes existing handoffs UNKNOWN rather than live.
type D010HandoffIssuer struct {
	mu      sync.Mutex
	states  map[[32]byte]*d010HandoffState
	maximum int
}

type d010HandoffState struct {
	mu      sync.Mutex
	binding d010Binding
	status  d010Status
	issuer  *D010HandoffIssuer
}

type d010Status uint8

const (
	d010Unused d010Status = iota
	d010Consumed
	d010Rejected
	d010Unknown
)

type d010Binding struct {
	OperationID       uuid.UUID
	AttemptID         uuid.UUID
	TargetFingerprint [32]byte
	Generation        int64
	FactsDigest       [32]byte
	D007ProofDigest   [32]byte
	D009Digest        [32]byte
	FreshUntil        time.Time
	PredicateIdentity string
	PredicateVersion  string
	Purpose           string
	Identity          [32]byte
}

var defaultD010Issuer = NewD010HandoffIssuer()

func NewD010HandoffIssuer() *D010HandoffIssuer {
	return &D010HandoffIssuer{states: make(map[[32]byte]*d010HandoffState), maximum: maxD010Handoffs}
}

// D009EvidenceFromReport is the only report-to-D009 projection. The protected
// executor installs the private seal only after TX1/TX2 and post-commit
// verification have succeeded.
func D009EvidenceFromReport(report ExecutionReport) (D009Evidence, error) {
	if report.d009Seal == nil || report.d009Seal.evidence.seal == nil ||
		report.d009Seal.evidence.seal.identity != report.d009Seal.identity ||
		!validD009Evidence(report.d009Seal.evidence) {
		return D009Evidence{}, ErrD010HandoffInvalid
	}
	return report.d009Seal.evidence, nil
}

// IssueD010Handoff is D3's sole issuance seam. It accepts terminal D007
// validation/consumption evidence and sealed D009 TX1/TX2 evidence only.
func IssueD010Handoff(terminal D007TerminalEvidence, d009 D009Evidence) (D010Handoff, error) {
	return defaultD010Issuer.Issue(terminal, d009)
}

func (i *D010HandoffIssuer) Issue(terminal D007TerminalEvidence, d009 D009Evidence) (D010Handoff, error) {
	if i == nil || !validD007TerminalEvidence(terminal) || !validD009Evidence(d009) {
		return D010Handoff{}, ErrD010HandoffInvalid
	}
	if terminal.AttemptID != d009.AttemptID || terminal.Generation != d009.Generation ||
		terminal.TargetFingerprint != sha256Hex(d009.TargetFingerprint) ||
		terminal.FactsDigest != hex.EncodeToString(d009.FactsDigest[:]) {
		return D010Handoff{}, ErrD010HandoffInvalid
	}
	freshUntil := terminal.FreshUntil.UTC()
	if !freshUntil.After(time.Now().UTC()) {
		return D010Handoff{}, ErrD010HandoffExpired
	}
	d009Digest := d009Identity(d009)
	binding := d010Binding{OperationID: d009.OperationID, AttemptID: terminal.AttemptID, TargetFingerprint: d009.TargetFingerprint,
		Generation: terminal.Generation, FactsDigest: d009.FactsDigest, D007ProofDigest: mustHexDigest(terminal.ProofDigest),
		D009Digest: d009Digest, FreshUntil: freshUntil, PredicateIdentity: D010PredicateIdentity,
		PredicateVersion: D010PredicateVersion, Purpose: D010Purpose}
	binding.Identity = d010BindingIdentity(binding)

	// Consume the D007 terminal projection as an issuance input exactly once.
	// This does not consume D007 (D2 already did); it prevents a terminal
	// evidence projection from issuing multiple D010 handoffs.
	terminal.state.mu.Lock()
	if terminal.state.handoffIssued {
		terminal.state.mu.Unlock()
		return D010Handoff{}, ErrD010HandoffIssued
	}
	terminal.state.handoffIssued = true
	terminal.state.mu.Unlock()

	i.mu.Lock()
	defer i.mu.Unlock()
	if i.states == nil {
		i.states = make(map[[32]byte]*d010HandoffState)
	}
	if i.maximum <= 0 {
		i.maximum = maxD010Handoffs
	}
	if len(i.states) >= i.maximum {
		return D010Handoff{}, ErrD010HandoffInvalid
	}
	if _, exists := i.states[binding.Identity]; exists {
		return D010Handoff{}, ErrD010HandoffIssued
	}
	state := &d010HandoffState{binding: binding, status: d010Unused, issuer: i}
	i.states[binding.Identity] = state
	return D010Handoff{state: state}, nil
}

// VerifyD010Handoff validates the private D3 state and all caller-supplied
// operation identity. Verification is side-effect free; Consume is separate.
func VerifyD010Handoff(handoff D010Handoff, expected D010HandoffContext) error {
	if handoff.state == nil {
		return ErrD010HandoffInvalid
	}
	state := handoff.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.issuer == nil || !issuerOwns(state.issuer, state.binding.Identity) {
		return ErrD010HandoffUnknown
	}
	if state.status != d010Unused {
		if state.status == d010Unknown {
			return ErrD010HandoffUnknown
		}
		return ErrD010HandoffReused
	}
	if !validD010Binding(state.binding) || state.binding.OperationID != expected.OperationID || state.binding.AttemptID != expected.AttemptID ||
		state.binding.TargetFingerprint != expected.TargetFingerprint || state.binding.Generation != expected.Generation {
		state.status = d010Rejected
		return ErrD010HandoffInvalid
	}
	if !state.binding.FreshUntil.After(time.Now().UTC()) {
		state.status = d010Rejected
		return ErrD010HandoffExpired
	}
	return nil
}

// ConsumeD010Handoff atomically transitions the handoff to consumed. It is the
// only operation that authorizes the named D3 continuation.
func ConsumeD010Handoff(handoff D010Handoff, expected D010HandoffContext) error {
	if err := VerifyD010Handoff(handoff, expected); err != nil {
		return err
	}
	state := handoff.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.status != d010Unused {
		return ErrD010HandoffReused
	}
	if state.issuer == nil || !issuerOwns(state.issuer, state.binding.Identity) {
		state.status = d010Unknown
		return ErrD010HandoffUnknown
	}
	state.status = d010Consumed
	return nil
}

func issuerOwns(issuer *D010HandoffIssuer, identity [32]byte) bool {
	issuer.mu.Lock()
	defer issuer.mu.Unlock()
	state, ok := issuer.states[identity]
	return ok && state != nil
}

func validD009Evidence(e D009Evidence) bool {
	return e.seal != nil && e.OperationID != uuid.Nil && e.AttemptID != uuid.Nil && e.Generation > 0 && e.TargetFingerprint != [32]byte{} &&
		e.TX1Identity != [32]byte{} && e.TX2Identity != [32]byte{} && e.FactsDigest != [32]byte{} &&
		e.PlanDigest != [32]byte{} && !e.PostCommitAsOf.IsZero() && e.seal.identity == d009Identity(e)
}

func validD007TerminalEvidence(e D007TerminalEvidence) bool {
	if e.state == nil {
		return false
	}
	e.state.mu.Lock()
	defer e.state.mu.Unlock()
	if e.state.consumedAt.IsZero() || e.state.handoffIssued || e.Kind != "TERMINAL_D007_VALIDATION_CONSUMPTION_EVIDENCE" ||
		e.Status != "CONSUMED" || e.PredicateVersion != D007PredicateVersion || e.AttemptID == uuid.Nil || e.Generation <= 0 ||
		e.FactsDigest == "" || e.ProofDigest == "" || !e.FreshUntil.After(time.Now().UTC()) {
		return false
	}
	return e.state.identity == d007TerminalIdentity(e)
}

func d009Identity(e D009Evidence) [32]byte {
	h := sha256.New()
	h.Write([]byte("D009_TX1_TX2_SUCCESS_V1"))
	h.Write(e.OperationID[:])
	h.Write(e.AttemptID[:])
	h.Write(e.TargetFingerprint[:])
	putInt := make([]byte, 8)
	binary.BigEndian.PutUint64(putInt, uint64(e.Generation))
	h.Write(putInt)
	h.Write(e.TX1Identity[:])
	h.Write(e.TX2Identity[:])
	h.Write(e.FactsDigest[:])
	h.Write(e.PlanDigest[:])
	h.Write([]byte(e.PostCommitAsOf.UTC().Format(time.RFC3339Nano)))
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func d007TerminalIdentity(e D007TerminalEvidence) [32]byte {
	h := sha256.New()
	h.Write([]byte("D007_TERMINAL_V1"))
	h.Write(e.AttemptID[:])
	h.Write([]byte(e.TargetFingerprint))
	h.Write([]byte(e.FactsDigest))
	h.Write([]byte(e.ProofDigest))
	var generation [8]byte
	binary.BigEndian.PutUint64(generation[:], uint64(e.Generation))
	h.Write(generation[:])
	h.Write([]byte(e.FreshUntil.UTC().Format(time.RFC3339Nano)))
	h.Write([]byte(e.PredicateVersion))
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func d010BindingIdentity(b d010Binding) [32]byte {
	h := sha256.New()
	h.Write([]byte("D010_OPAQUE_HANDOFF_V1"))
	h.Write(b.OperationID[:])
	h.Write(b.AttemptID[:])
	h.Write(b.TargetFingerprint[:])
	var generation [8]byte
	binary.BigEndian.PutUint64(generation[:], uint64(b.Generation))
	h.Write(generation[:])
	h.Write(b.FactsDigest[:])
	h.Write(b.D007ProofDigest[:])
	h.Write(b.D009Digest[:])
	h.Write([]byte(b.FreshUntil.UTC().Format(time.RFC3339Nano)))
	h.Write([]byte(b.PredicateIdentity))
	h.Write([]byte(b.PredicateVersion))
	h.Write([]byte(b.Purpose))
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func validD010Binding(b d010Binding) bool {
	return b.OperationID != uuid.Nil && b.AttemptID != uuid.Nil && b.TargetFingerprint != [32]byte{} && b.Generation > 0 && b.FactsDigest != [32]byte{} &&
		b.D007ProofDigest != [32]byte{} && b.D009Digest != [32]byte{} && b.PredicateIdentity == D010PredicateIdentity &&
		b.PredicateVersion == D010PredicateVersion && b.Purpose == D010Purpose && !b.FreshUntil.IsZero() && b.Identity == d010BindingIdentity(b)
}

func sha256Hex(value [32]byte) string {
	digest := sha256.Sum256(value[:])
	return hex.EncodeToString(digest[:])
}
func mustHexDigest(value string) [32]byte {
	var out [32]byte
	decoded, err := hex.DecodeString(value)
	if err == nil && len(decoded) == len(out) {
		copy(out[:], decoded)
	}
	return out
}
