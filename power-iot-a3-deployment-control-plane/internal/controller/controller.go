// Package controller owns the bounded, one-shot local handoff from the
// Provider runbook authority to the accepted D1-L runner. It deliberately does
// not mint authority itself: Issue is injected from the existing Provider
// authority/API, and the returned opaque envelope is transferred only through
// inherited FD 3.
package controller

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"power-iot-a3-deployment-control-plane/internal/ledger"
	"power-iot-a3-deployment-control-plane/internal/store"
)

const (
	// ProtectedPresentationFD and ProtectedResultFD are the accepted runner
	// contract. ExtraFiles maps the first and second child descriptors to 3 and
	// 4 respectively; stdout/stderr are never used as authority channels.
	ProtectedPresentationFD = 3
	ProtectedResultFD       = 4

	resultSchema       = "d1l-bootstrap-result-v1"
	resultPrefixSize   = 4
	resultMaxFrameSize = 64 * 1024

	// This is the fixed D1-L artifact identity accepted by the runner/provider
	// binding. The controller never accepts a caller-selected installer.
	d1lInstallerDigestV1 = "d5a2446fb6082d51f67f2f52c54c21afdecf679ad9288854639d2ba9d4d45a0f"
)

var (
	ErrControllerConfig        = errors.New("D1-L controller configuration rejected")
	ErrControllerIssue         = errors.New("D1-L Provider issue failed")
	ErrControllerAuthorization = errors.New("D1-L Provider authorization rejected")
	ErrControllerLaunch        = errors.New("D1-L runner launch failed")
	ErrControllerPresentation  = errors.New("D1-L protected presentation delivery failed")
	ErrControllerResult        = errors.New("D1-L protected result delivery failed")
)

// IssueFunc is the existing Provider Issue authority/API adapted to the
// controller. The controller supplies the fixed runbook role and exact binding
// tuple; it never implements a second issuer or retries this call.
type IssueFunc func(context.Context, store.RequestData, time.Duration) (store.IssueResult, error)

// Config identifies an already installed accepted runner and the target it is
// allowed to inspect. Environment is inherited only for non-secret runner
// configuration (Provider endpoint and TLS file paths); the opaque envelope is
// never inserted into it.
type Config struct {
	RunnerPath  string
	DatabaseURL string
	Environment []string
	Issue       IssueFunc
	TTL         time.Duration
}

// Request is caller/runbook metadata. TargetFingerprint is checked by the
// runner against its independently derived pinned target identity; it is not
// treated as authority by this package.
type Request struct {
	IssuerRequestID   string
	OperationID       string
	AttemptID         string
	TargetFingerprint string
	EvidenceDigest    string
}

// OperationTruth is the runner's operation state, not a process or transport
// state. UNAVAILABLE means no trusted result frame was delivered and is not a
// fourth runner operation state.
type OperationTruth string

const (
	OperationNotInstalled   OperationTruth = "NOT_INSTALLED"
	OperationCommittedReady OperationTruth = "COMMITTED_READY"
	OperationUnknown        OperationTruth = "UNKNOWN"
	OperationUnavailable    OperationTruth = "UNAVAILABLE"
)

type IssueStatus string

type LaunchStatus string

type PresentationStatus string

type ResultDeliveryStatus string

type ExitStatus string

type ResultError struct {
	Class   string `json:"class"`
	Message string `json:"message"`
}

const (
	IssueNotAttempted IssueStatus = "NOT_ATTEMPTED"
	IssueIssued       IssueStatus = "ISSUED"
	IssueFailed       IssueStatus = "FAILED"
	IssueRejected     IssueStatus = "REJECTED"

	LaunchNotAttempted LaunchStatus = "NOT_ATTEMPTED"
	LaunchStarted      LaunchStatus = "STARTED"
	LaunchFailed       LaunchStatus = "FAILED"

	PresentationNotAttempted PresentationStatus = "NOT_ATTEMPTED"
	PresentationSent         PresentationStatus = "SENT"
	PresentationFailed       PresentationStatus = "FAILED"

	ResultNotAttempted ResultDeliveryStatus = "NOT_ATTEMPTED"
	ResultDelivered    ResultDeliveryStatus = "DELIVERED"
	ResultFailed       ResultDeliveryStatus = "FAILED"

	ExitNotAttempted ExitStatus = "NOT_ATTEMPTED"
	ExitZero         ExitStatus = "EXITED_ZERO"
	ExitNonZero      ExitStatus = "EXITED_NONZERO"
	ExitSignaled     ExitStatus = "SIGNALED"
	ExitUnknown      ExitStatus = "UNKNOWN"
)

// ResultFrame is the exact non-secret allow-list projection emitted by the
// accepted runner H7-B channel. No envelope, Provider credential, database
// credential, or ExternalWriterAdmission crosses this type.
type ResultFrame struct {
	Schema            string       `json:"schema"`
	OperationID       string       `json:"operation_id"`
	AttemptID         string       `json:"attempt_id"`
	AuthorizationID   string       `json:"authorization_id"`
	ConsumeRequestID  string       `json:"consume_request_id"`
	ProviderEpoch     int64        `json:"provider_epoch"`
	TargetFingerprint string       `json:"target_fingerprint"`
	InstallerDigest   string       `json:"installer_digest"`
	EvidenceDigest    string       `json:"evidence_digest"`
	InstallState      string       `json:"install_state"`
	CleanupState      string       `json:"cleanup_state"`
	Before            string       `json:"before"`
	After             string       `json:"after"`
	Committed         bool         `json:"committed"`
	StartedAt         string       `json:"started_at"`
	FinishedAt        string       `json:"finished_at"`
	BackendPID        int64        `json:"backend_pid"`
	MigrationLockKey  int64        `json:"migration_lock_key"`
	OperationError    *ResultError `json:"operation_error,omitempty"`
}

// RunResult intentionally keeps operation truth, admission delivery, result
// delivery, launch, and exit status independent. A delivered COMMITTED_READY
// frame remains COMMITTED_READY even when the child exits non-zero after
// reporting an operation error; delivery never rewrites truth.
type RunResult struct {
	IssuerRequestID string               `json:"issuer_request_id,omitempty"`
	Issue           IssueStatus          `json:"issue_status"`
	Launch          LaunchStatus         `json:"launch_status"`
	Presentation    PresentationStatus   `json:"presentation_status"`
	Delivery        ResultDeliveryStatus `json:"result_delivery_status"`
	Exit            ExitStatus           `json:"exit_status"`
	Operation       OperationTruth       `json:"operation_truth"`
	Frame           *ResultFrame         `json:"frame,omitempty"`
}

// Controller is a one-shot controller. A Controller value has no retry or
// replay method; each Run performs at most one Issue callback, one process
// launch, one FD3 presentation write, one FD4 frame read, and one Wait.
type Controller struct {
	cfg Config
}

func New(cfg Config) (*Controller, error) {
	if strings.TrimSpace(cfg.RunnerPath) == "" || !filepath.IsAbs(cfg.RunnerPath) || strings.TrimSpace(cfg.DatabaseURL) == "" || cfg.Issue == nil {
		return nil, ErrControllerConfig
	}
	if cfg.TTL < 0 || cfg.TTL > 24*time.Hour {
		return nil, ErrControllerConfig
	}
	if cfg.TTL == 0 {
		cfg.TTL = 10 * time.Minute
	}
	return &Controller{cfg: cfg}, nil
}

func (c *Controller) Run(ctx context.Context, req Request) (RunResult, error) {
	result := RunResult{
		Issue:        IssueNotAttempted,
		Launch:       LaunchNotAttempted,
		Presentation: PresentationNotAttempted,
		Delivery:     ResultNotAttempted,
		Exit:         ExitNotAttempted,
		Operation:    OperationUnavailable,
	}
	if c == nil || c.cfg.Issue == nil || strings.TrimSpace(c.cfg.RunnerPath) == "" {
		return result, ErrControllerConfig
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateRequest(req); err != nil {
		return result, ErrControllerConfig
	}
	issuerRequestID := strings.TrimSpace(req.IssuerRequestID)
	if issuerRequestID == "" {
		issuerRequestID = uuid.NewString()
	}
	result.IssuerRequestID = issuerRequestID
	issued, err := c.cfg.Issue(ctx, store.RequestData{
		ID: issuerRequestID, AttemptID: req.AttemptID, Role: "deployment-runbook",
		Scope: ledger.ScopeControlCatalogInstall,
		Bindings: map[string]string{
			"operation":     req.OperationID,
			"attempt_id":    req.AttemptID,
			"target_id":     req.TargetFingerprint,
			"installer_id":  d1lInstallerDigestV1,
			"evidence_hash": req.EvidenceDigest,
		},
	}, c.cfg.TTL)
	if err != nil {
		result.Issue = IssueFailed
		return result, ErrControllerIssue
	}
	result.Issue = IssueIssued
	if err := validateIssuedAuthorization(issued, req, issuerRequestID); err != nil {
		result.Issue = IssueRejected
		return result, ErrControllerAuthorization
	}
	// Keep only the byte copy needed for the one protected write. The provider
	// IssueResult is deliberately not returned, and no secret-bearing value is
	// copied into RunResult or an error.
	envelope := []byte(issued.Envelope)
	issued.Envelope = ""
	defer zeroBytes(envelope)

	env, err := runnerEnvironment(c.cfg.Environment, c.cfg.DatabaseURL, string(envelope))
	if err != nil {
		return result, ErrControllerConfig
	}
	cmd := exec.CommandContext(ctx, c.cfg.RunnerPath,
		"--target-fingerprint", req.TargetFingerprint,
		"--evidence-digest", req.EvidenceDigest,
		"--operation-id", req.OperationID,
		"--attempt-id", req.AttemptID,
		"--authorization-id", issued.AuthorizationID,
	)
	cmd.Env = env
	// Human-readable streams are explicitly non-authoritative and discarded;
	// the only accepted result source is the dedicated inherited FD 4.
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard

	presentationRead, presentationWrite, err := os.Pipe()
	if err != nil {
		return result, ErrControllerLaunch
	}
	resultRead, resultWrite, err := os.Pipe()
	if err != nil {
		_ = presentationRead.Close()
		_ = presentationWrite.Close()
		return result, ErrControllerLaunch
	}
	cmd.ExtraFiles = []*os.File{presentationRead, resultWrite}
	if err := cmd.Start(); err != nil {
		_ = presentationRead.Close()
		_ = presentationWrite.Close()
		_ = resultRead.Close()
		_ = resultWrite.Close()
		return result, ErrControllerLaunch
	}
	result.Launch = LaunchStarted
	// Parent copies must close the child endpoints immediately after Start. The
	// child receives exactly FD3=read end and FD4=write end; ExtraFiles applies
	// close-on-exec to all unrelated descriptors.
	_ = presentationRead.Close()
	_ = resultWrite.Close()

	presentationDone := make(chan error, 1)
	go func() {
		presentationDone <- writePresentation(presentationWrite, envelope)
		_ = presentationWrite.Close()
	}()
	frameDone := make(chan struct {
		frame *ResultFrame
		err   error
	}, 1)
	go func() {
		frame, readErr := readResultFrame(resultRead)
		_ = resultRead.Close()
		frameDone <- struct {
			frame *ResultFrame
			err   error
		}{frame: frame, err: readErr}
	}()
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	presentationErr := <-presentationDone
	if presentationErr == nil {
		result.Presentation = PresentationSent
	} else {
		result.Presentation = PresentationFailed
	}
	frameResult := <-frameDone
	waitErr := <-waitDone
	result.Exit = classifyExit(waitErr)
	if frameResult.err == nil {
		if err := validateResultFrame(*frameResult.frame, req, issued, envelope); err != nil {
			result.Delivery = ResultFailed
		} else {
			result.Delivery = ResultDelivered
			result.Frame = frameResult.frame
			result.Operation = operationTruth(frameResult.frame.InstallState)
		}
	} else {
		result.Delivery = ResultFailed
	}
	if presentationErr != nil {
		return result, ErrControllerPresentation
	}
	if frameResult.err != nil || result.Delivery != ResultDelivered {
		return result, ErrControllerResult
	}
	return result, nil
}

func validateRequest(req Request) error {
	if req.IssuerRequestID != "" {
		if _, err := uuid.Parse(req.IssuerRequestID); err != nil {
			return err
		}
	}
	if _, err := uuid.Parse(req.OperationID); err != nil {
		return err
	}
	if _, err := uuid.Parse(req.AttemptID); err != nil {
		return err
	}
	if !lowerHexDigest(req.TargetFingerprint) || !lowerHexDigest(req.EvidenceDigest) {
		return ErrControllerConfig
	}
	return nil
}

func lowerHexDigest(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	for _, r := range value {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

func validateIssuedAuthorization(issued store.IssueResult, req Request, issuerRequestID string) error {
	if issued.State != ledger.Issued || !issued.SecretAvailable || strings.TrimSpace(issued.Envelope) == "" || issued.Epoch <= 0 || issued.ExpiresAt.IsZero() || !issued.ExpiresAt.After(time.Now()) {
		return ErrControllerAuthorization
	}
	if issued.IssuerRequestID != issuerRequestID || issued.AttemptID != req.AttemptID || issued.Scope != ledger.ScopeControlCatalogInstall {
		return ErrControllerAuthorization
	}
	if _, err := uuid.Parse(issued.AuthorizationID); err != nil {
		return ErrControllerAuthorization
	}
	if _, err := uuid.Parse(issued.IssuerRequestID); err != nil {
		return ErrControllerAuthorization
	}
	if len(issued.Bindings) != 5 || issued.Bindings["operation"] != req.OperationID || issued.Bindings["attempt_id"] != req.AttemptID || issued.Bindings["target_id"] != req.TargetFingerprint || issued.Bindings["installer_id"] != d1lInstallerDigestV1 || issued.Bindings["evidence_hash"] != req.EvidenceDigest {
		return ErrControllerAuthorization
	}
	return nil
}

func runnerEnvironment(base []string, databaseURL, envelope string) ([]string, error) {
	if strings.TrimSpace(databaseURL) == "" {
		return nil, ErrControllerConfig
	}
	if envelope == "" {
		return nil, ErrControllerAuthorization
	}
	if base == nil {
		base = os.Environ()
	}
	env := make([]string, 0, len(base)+1)
	for _, entry := range base {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			return nil, ErrControllerConfig
		}
		// The presentation has no supported environment spelling. Remove the
		// obvious spelling and refuse any accidental exact bearer copy instead
		// of passing it to a child environment.
		if key == "D1L_BOOTSTRAP_AUTHORIZATION" {
			continue
		}
		if strings.Contains(value, envelope) {
			return nil, ErrControllerConfig
		}
		if key == "DATABASE_URL" {
			continue
		}
		env = append(env, entry)
	}
	env = append(env, "DATABASE_URL="+databaseURL)
	return env, nil
}

func writePresentation(w *os.File, presentation []byte) error {
	if w == nil || len(presentation) == 0 {
		return ErrControllerPresentation
	}
	for len(presentation) > 0 {
		n, err := w.Write(presentation)
		if n < 0 || n > len(presentation) {
			return ErrControllerPresentation
		}
		presentation = presentation[n:]
		if err != nil || n == 0 {
			return ErrControllerPresentation
		}
	}
	return nil
}

func readResultFrame(r io.Reader) (*ResultFrame, error) {
	if r == nil {
		return nil, ErrControllerResult
	}
	var prefix [resultPrefixSize]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return nil, ErrControllerResult
	}
	length := int(binary.BigEndian.Uint32(prefix[:]))
	if length <= 0 || length > resultMaxFrameSize-resultPrefixSize {
		return nil, ErrControllerResult
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, ErrControllerResult
	}
	// Exactly one invocation frame is accepted. A trailing byte is a framing
	// failure, not a second result to consume.
	var trailing [1]byte
	n, err := r.Read(trailing[:])
	if n != 0 || !errors.Is(err, io.EOF) {
		return nil, ErrControllerResult
	}
	var frame ResultFrame
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&frame); err != nil {
		return nil, ErrControllerResult
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, ErrControllerResult
	}
	if frame.Schema != resultSchema {
		return nil, ErrControllerResult
	}
	return &frame, nil
}

func validateResultFrame(frame ResultFrame, req Request, issued store.IssueResult, envelope []byte) error {
	if frame.Schema != resultSchema {
		return ErrControllerResult
	}
	// D1LBootstrap deliberately returns proof-bearing partial/UNKNOWN reports
	// for failures before pinning, Inspect, or Consume. Empty optional fields
	// therefore remain valid; a populated binding field must agree with the
	// invocation and the Provider issue result.
	if (frame.OperationID != "" && frame.OperationID != req.OperationID) ||
		(frame.AttemptID != "" && frame.AttemptID != req.AttemptID) ||
		(frame.AuthorizationID != "" && frame.AuthorizationID != issued.AuthorizationID) ||
		(frame.ProviderEpoch != 0 && frame.ProviderEpoch != issued.Epoch) ||
		(frame.TargetFingerprint != "" && frame.TargetFingerprint != req.TargetFingerprint) ||
		(frame.InstallerDigest != "" && frame.InstallerDigest != d1lInstallerDigestV1) ||
		(frame.EvidenceDigest != "" && frame.EvidenceDigest != req.EvidenceDigest) {
		return ErrControllerResult
	}
	if frame.InstallState != "" && frame.InstallState != string(OperationNotInstalled) && frame.InstallState != string(OperationCommittedReady) && frame.InstallState != string(OperationUnknown) {
		return ErrControllerResult
	}
	if frame.CleanupState != "CLEAN" && frame.CleanupState != "CLEANUP_INCOMPLETE" && frame.CleanupState != "UNKNOWN" {
		return ErrControllerResult
	}
	if frameContainsEnvelope(frame, envelope) {
		return ErrControllerResult
	}
	if frame.OperationError != nil {
		switch frame.OperationError.Class {
		case "COMMIT_UNKNOWN", "NO_RETRY", "PROVIDER_BINDING", "BOOTSTRAP_STATE", "CATALOG", "ARTIFACT_DIGEST", "OPERATION_FAILED":
		default:
			return ErrControllerResult
		}
		if frame.OperationError.Message != "D1-L bootstrap operation failed" {
			return ErrControllerResult
		}
	}
	return nil
}

func frameContainsEnvelope(frame ResultFrame, envelope []byte) bool {
	if len(envelope) == 0 {
		return false
	}
	secret := string(envelope)
	for _, value := range []string{
		frame.Schema, frame.OperationID, frame.AttemptID, frame.AuthorizationID,
		frame.ConsumeRequestID, frame.TargetFingerprint, frame.InstallerDigest,
		frame.EvidenceDigest, frame.InstallState, frame.CleanupState, frame.Before,
		frame.After, frame.StartedAt, frame.FinishedAt,
	} {
		if strings.Contains(value, secret) {
			return true
		}
	}
	if frame.OperationError != nil && (strings.Contains(frame.OperationError.Class, secret) || strings.Contains(frame.OperationError.Message, secret)) {
		return true
	}
	return false
}

func operationTruth(state string) OperationTruth {
	switch state {
	case "NOT_INSTALLED":
		return OperationNotInstalled
	case "COMMITTED_READY":
		return OperationCommittedReady
	default:
		return OperationUnknown
	}
}

func classifyExit(err error) ExitStatus {
	if err == nil {
		return ExitZero
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return ExitSignaled
		}
		return ExitNonZero
	}
	return ExitUnknown
}

func zeroBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
