package reconciliation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// SafeSemanticResult is the non-authoritative projection at the PR1-to-D4
// boundary. It contains semantic facts and correlation digests only; it never
// contains a D007 capability, a D010 handoff, or a physical handle.
type SafeSemanticResult struct {
	OperationID            string               `json:"operation_id,omitempty"`
	AttemptID              uuid.UUID            `json:"attempt_id,omitempty"`
	TargetFingerprint      string               `json:"target_fingerprint_digest,omitempty"`
	Generation             int64                `json:"generation,omitempty"`
	FactsDigest            string               `json:"facts_digest,omitempty"`
	ProofDigest            string               `json:"proof_digest,omitempty"`
	PredicateIdentity      string               `json:"predicate_identity,omitempty"`
	PredicateVersion       string               `json:"predicate_version,omitempty"`
	Outcome                ExecutionOutcome     `json:"outcome"`
	Phase                  ExecutionPhase       `json:"phase"`
	Disposition            string               `json:"disposition"`
	ResultClass            string               `json:"result_class"`
	Certainty              string               `json:"certainty"`
	CommitStatus           string               `json:"commit_status"`
	PostVerificationStatus string               `json:"post_verification_status"`
	CleanupStatus          string               `json:"cleanup_status"`
	Unknown                bool                 `json:"unknown"`
	UnknownReason          string               `json:"unknown_reason,omitempty"`
	RecoveryRequired       bool                 `json:"recovery_required"`
	RecoveryEvidence       SafeRecoveryEvidence `json:"recovery_evidence"`
	RetryDisposition       string               `json:"retry_disposition"`
	ReplayDisposition      string               `json:"replay_disposition"`
	PostCommitFactsDigest  string               `json:"post_commit_facts_digest,omitempty"`
	PostCommitFactsAsOf    string               `json:"post_commit_facts_as_of,omitempty"`
}

// SafeRecoveryEvidence preserves uncertainty and recovery correlation without
// exposing D3's physical recovery state.
type SafeRecoveryEvidence struct {
	Outcome           ExecutionOutcome `json:"outcome"`
	Phase             ExecutionPhase   `json:"phase"`
	CommitKnown       bool             `json:"commit_known"`
	PostVerifyKnown   bool             `json:"post_verify_known"`
	CleanupUncertain  bool             `json:"cleanup_uncertain"`
	CorrelationDigest string           `json:"correlation_digest,omitempty"`
}

// PR1SemanticResult is retained as a descriptive alias for consumers that use
// the PR1 terminology. It remains a safe, non-authoritative value.
type PR1SemanticResult = SafeSemanticResult

// PR1ToD4Result deliberately keeps its optional continuation private. D4 can
// request/consume it only through ContinueD3Protected, which routes through
// the D3 verifier and one-shot consumer; it cannot inspect or serialize it.
type PR1ToD4Result struct {
	Semantic SafeSemanticResult `json:"semantic"`

	continuation *d3BoundaryContinuation
}

type d3BoundaryContinuation struct {
	executor *ProtectedExecutor
	handoff  D010Handoff
}

var (
	ErrD3ContinuationRequired = errors.New("D3 continuation is required but unavailable")
	ErrD3ContinuationAbsent   = errors.New("D3 continuation is absent")
)

// NewPR1ToD4Result creates the safe projection without obtaining a
// continuation. This is the default path and is safe for serialization.
func NewPR1ToD4Result(report ExecutionReport) PR1ToD4Result {
	return PR1ToD4Result{Semantic: safeSemanticResultFromExecution(report)}
}

// SafeSemanticResultFromExecution is the report-only safe projection. It does
// not inspect or recover any owner-private seal or continuation.
func SafeSemanticResultFromExecution(report ExecutionReport) SafeSemanticResult {
	return safeSemanticResultFromExecution(report)
}

// ProjectSafeSemanticResult is a descriptive alias for callers that name the
// operation as a projection rather than a construction.
func ProjectSafeSemanticResult(report ExecutionReport) SafeSemanticResult {
	return SafeSemanticResultFromExecution(report)
}

// PR1ToD4Result returns the safe result and conditionally carries the private
// D3 handoff. Requiring a continuation never falls back to a report, D007
// evidence, or a reconstructed handoff.
func (e *ProtectedExecutor) PR1ToD4Result(report ExecutionReport, requireContinuation bool) (PR1ToD4Result, error) {
	result := NewPR1ToD4Result(report)
	if !requireContinuation {
		return result, nil
	}
	if e == nil || report.Outcome != ExecutionCommittedAndVerified || !report.Committed || !report.PostCommitVerified {
		return PR1ToD4Result{}, ErrD3ContinuationRequired
	}
	e.continuationMu.Lock()
	defer e.continuationMu.Unlock()
	if e.d010Handoff == nil || !d010ContinuationMatches(report, result.Semantic, *e.d010Handoff) {
		return PR1ToD4Result{}, ErrD3ContinuationRequired
	}
	result.continuation = &d3BoundaryContinuation{executor: e, handoff: *e.d010Handoff}
	return result, nil
}

// HasD3Continuation reports only whether the conditional private seam is
// present. It does not expose the bearer or imply D5 authority.
func (r PR1ToD4Result) HasD3Continuation() bool { return r.continuation != nil }

// ContinueD3Protected is the sole result-side continuation operation. The
// expected binding is supplied to D3 verification; this method never accepts
// a raw handoff and never performs D4 composition.
func (r *PR1ToD4Result) ContinueD3Protected(expected D010HandoffContext) error {
	if r == nil || r.continuation == nil || r.continuation.executor == nil {
		return ErrD3ContinuationAbsent
	}
	return r.continuation.executor.consumeD3Boundary(*r.continuation, expected)
}

func (e *ProtectedExecutor) consumeD3Boundary(continuation d3BoundaryContinuation, expected D010HandoffContext) error {
	e.continuationMu.Lock()
	defer e.continuationMu.Unlock()
	if e.d010Handoff == nil || e.d010Handoff.state != continuation.handoff.state {
		return ErrD3ContinuationAbsent
	}
	return e.continueD3ProtectedLocked(expected)
}

// d010ContinuationMatches binds the private D010 state to the complete owner
// report before the state crosses the PR1-to-D4 boundary. Success flags and a
// present handoff are insufficient: all D007 fields and the successful D009
// projection must be the exact evidence from which this handoff was issued.
func d010ContinuationMatches(report ExecutionReport, semantic SafeSemanticResult, handoff D010Handoff) bool {
	if handoff.state == nil {
		return false
	}
	state := handoff.state
	state.mu.Lock()
	binding, status, issuer := state.binding, state.status, state.issuer
	state.mu.Unlock()
	if status != d010Unused || !validD010Binding(binding) || issuer == nil || !issuerOwns(issuer, binding.Identity) ||
		!binding.FreshUntil.After(time.Now().UTC()) {
		return false
	}

	terminal := report.D007Terminal
	if report.Outcome != ExecutionCommittedAndVerified || report.Phase != PhasePostVerify || !report.Committed || !report.PostCommitVerified ||
		report.OperationID == uuid.Nil || terminal.state == nil || terminal.Kind != "TERMINAL_D007_VALIDATION_CONSUMPTION_EVIDENCE" ||
		terminal.Status != "CONSUMED" || terminal.PredicateVersion != D007PredicateVersion || terminal.AttemptID == uuid.Nil ||
		terminal.Generation <= 0 || terminal.FactsDigest == "" || terminal.ProofDigest == "" || terminal.FreshUntil.IsZero() ||
		!terminal.FreshUntil.After(time.Now().UTC()) || !terminal.FreshUntil.UTC().Equal(binding.FreshUntil.UTC()) ||
		terminal.TargetFingerprint != sha256Hex(binding.TargetFingerprint) || terminal.AttemptID != binding.AttemptID ||
		terminal.Generation != binding.Generation || terminal.FactsDigest != hex.EncodeToString(binding.FactsDigest[:]) ||
		terminal.ProofDigest != hex.EncodeToString(binding.D007ProofDigest[:]) {
		return false
	}
	terminal.state.mu.Lock()
	terminalStateValid := !terminal.state.consumedAt.IsZero() && terminal.state.handoffIssued &&
		terminal.ConsumedAt.UTC().Equal(terminal.state.consumedAt.UTC()) && terminal.state.identity == d007TerminalIdentity(terminal)
	terminal.state.mu.Unlock()
	if !terminalStateValid {
		return false
	}

	if semantic.OperationID != report.OperationID.String() || semantic.AttemptID != binding.AttemptID ||
		semantic.TargetFingerprint != terminal.TargetFingerprint || semantic.Generation != binding.Generation ||
		semantic.FactsDigest != terminal.FactsDigest || semantic.ProofDigest != terminal.ProofDigest ||
		semantic.PredicateIdentity != D007PredicateIdentity || semantic.PredicateVersion != D007PredicateVersion ||
		semantic.Outcome != report.Outcome || semantic.PostCommitFactsDigest != report.PostCommitFactsDigest {
		return false
	}
	if binding.OperationID != report.OperationID || semantic.OperationID == "" || report.PlanDigest == "" ||
		report.PostCommitFactsDigest == "" || report.PostCommitFactsAsOf.IsZero() ||
		report.PostCommitFactsDigest != terminal.FactsDigest {
		return false
	}

	factsDigest, err := hex.DecodeString(report.PostCommitFactsDigest)
	if err != nil || len(factsDigest) != 32 {
		return false
	}
	var facts [32]byte
	copy(facts[:], factsDigest)
	tx1 := sha256.Sum256([]byte("D009_TX1_SUCCESS:" + report.OperationID.String() + ":" + report.PlanDigest))
	tx2 := sha256.Sum256([]byte("D009_TX2_SUCCESS:" + report.PostCommitFactsDigest + ":" + report.PostCommitFactsAsOf.UTC().Format(time.RFC3339Nano)))
	plan := sha256.Sum256([]byte(report.PlanDigest))
	d009 := D009Evidence{OperationID: report.OperationID, AttemptID: terminal.AttemptID, TargetFingerprint: binding.TargetFingerprint,
		Generation: terminal.Generation, TX1Identity: tx1, TX2Identity: tx2, FactsDigest: facts, PlanDigest: plan,
		PostCommitAsOf: report.PostCommitFactsAsOf.UTC()}
	return d009Identity(d009) == binding.D009Digest
}

func safeSemanticResultFromExecution(report ExecutionReport) SafeSemanticResult {
	result := SafeSemanticResult{
		// OperationID is the exact owner-issued D1 operation identity. PlanID
		// identifies only the reconciliation artifact and must not be projected
		// as the operation correlation value.
		Outcome: report.Outcome, Phase: report.Phase,
		PostCommitFactsDigest: report.PostCommitFactsDigest,
		RecoveryEvidence:      SafeRecoveryEvidence{Outcome: report.Outcome, Phase: report.Phase},
		ReplayDisposition:     "NOT_REPLAYABLE",
	}
	if report.OperationID != uuid.Nil {
		result.OperationID = report.OperationID.String()
	}
	if terminal := report.D007Terminal; terminal.AttemptID != uuid.Nil {
		result.AttemptID = terminal.AttemptID
		result.TargetFingerprint = terminal.TargetFingerprint
		result.Generation = terminal.Generation
		result.FactsDigest = terminal.FactsDigest
		result.ProofDigest = terminal.ProofDigest
		result.PredicateIdentity = D007PredicateIdentity
		result.PredicateVersion = terminal.PredicateVersion
	}
	if !report.PostCommitFactsAsOf.IsZero() {
		result.PostCommitFactsAsOf = report.PostCommitFactsAsOf.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	}

	result.CommitStatus = "NOT_COMMITTED"
	if report.Outcome == ExecutionCommitOutcomeUnknown {
		result.CommitStatus = "COMMIT_UNKNOWN"
	} else if report.Outcome == ExecutionCommittedAndVerified || report.Outcome == ExecutionCommittedPostVerifyFailed {
		result.CommitStatus = "COMMITTED"
	}
	result.PostVerificationStatus = "NOT_VERIFIED"
	if report.PostCommitVerified {
		result.PostVerificationStatus = "VERIFIED"
	} else if report.Outcome == ExecutionCommittedPostVerifyFailed {
		result.PostVerificationStatus = "FAILED"
	}
	result.CleanupStatus = "CONFIRMED"
	if report.CleanupError != "" {
		result.CleanupStatus = "UNCERTAIN"
	}

	result.Unknown = report.Outcome == ExecutionCommitOutcomeUnknown || result.CleanupStatus == "UNCERTAIN"
	switch {
	case report.Outcome == ExecutionCommitOutcomeUnknown:
		result.UnknownReason = "commit-outcome-unknown"
	case result.CleanupStatus == "UNCERTAIN":
		result.UnknownReason = "cleanup-unknown"
	}
	result.RecoveryRequired = result.Unknown || report.Outcome == ExecutionCommittedPostVerifyFailed
	result.RecoveryEvidence.CommitKnown = result.CommitStatus != "COMMIT_UNKNOWN"
	result.RecoveryEvidence.PostVerifyKnown = result.PostVerificationStatus != "NOT_VERIFIED"
	result.RecoveryEvidence.CleanupUncertain = result.CleanupStatus == "UNCERTAIN"
	result.RecoveryEvidence.CorrelationDigest = safeCorrelationDigest(result)
	if result.RecoveryRequired {
		result.RetryDisposition = "DO_NOT_RETRY"
	} else {
		result.RetryDisposition = "RETRY_ONLY_IF_OWNER_PERMITS"
	}
	result.Disposition = "NON_SUCCESS"
	result.ResultClass = "NOT_COMMITTED"
	result.Certainty = "KNOWN"
	if report.Outcome == ExecutionCommittedAndVerified {
		result.Disposition = "SUCCESS"
		result.ResultClass = "COMMITTED_AND_VERIFIED"
	} else if report.Outcome == ExecutionCommitOutcomeUnknown {
		result.ResultClass = "AUTHORITATIVE_UNKNOWN"
		result.Certainty = "UNKNOWN"
	} else if report.Outcome == ExecutionCommittedPostVerifyFailed {
		result.ResultClass = "COMMITTED_POSTVERIFY_FAILED"
	}
	return result
}

func safeCorrelationDigest(result SafeSemanticResult) string {
	value := strings.Join([]string{result.OperationID, result.AttemptID.String(), result.TargetFingerprint,
		strconv.FormatInt(result.Generation, 10), result.FactsDigest, result.ProofDigest, result.PredicateIdentity,
		result.PredicateVersion, string(result.Outcome), string(result.Phase), result.CommitStatus,
		result.PostVerificationStatus, result.CleanupStatus, strconv.FormatBool(result.Unknown),
		strconv.FormatBool(result.RecoveryRequired)}, "|")
	// Correlation is deliberately a digest, not an authority identity.
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func (r PR1ToD4Result) String() string {
	encoded, _ := json.Marshal(r.Semantic)
	return string(encoded)
}

func (r PR1ToD4Result) MarshalJSON() ([]byte, error) { return json.Marshal(r.Semantic) }
