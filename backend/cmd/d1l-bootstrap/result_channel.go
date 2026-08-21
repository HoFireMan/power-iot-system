package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"

	"power-iot-backend/internal/data/migrations"
)

// The result channel is a protected local inherited descriptor selected by
// the command invocation contract. It is deliberately not the presentation
// descriptor (FD 3), stdout, or stderr.
const (
	d1lProtectedPresentationFD = 3
	d1lProtectedResultFD       = 4

	// A result is one bounded frame: a four-byte length prefix followed by a
	// UTF-8 JSON object. The bound includes both prefix and payload.
	d1lResultMaxFrameSize = 64 * 1024
	d1lResultPrefixSize   = 4
)

var (
	errD1LResultSink       = errors.New("D1-L protected result sink is unavailable")
	errD1LResultTooLarge   = errors.New("D1-L protected result exceeds frame limit")
	errD1LResultNoProgress = errors.New("D1-L protected result sink made no progress")
)

type d1LResultWriter interface {
	Write([]byte) (int, error)
}

// runD1LBootstrapAndDeliver invokes the operation once and then performs the
// one-shot delivery. Delivery failure is returned separately and never causes
// the operation callback to be called again.
func runD1LBootstrapAndDeliver(run func() (migrations.D1LBootstrapReport, error), sink d1LResultWriter) (migrations.D1LBootstrapReport, error, error) {
	report, operationErr := run()
	return report, operationErr, writeD1LResult(sink, d1LResultFrameFromReport(report, operationErr))
}

// d1LResultFrame is intentionally a separate allow-list DTO rather than a
// JSON-tagged migration report. This prevents protected input, provider
// authorization material, and ExternalWriterAdmission from entering the
// controller channel if D1LBootstrapReport gains fields in the future.
type d1LResultFrame struct {
	Schema            string                              `json:"schema"`
	OperationID       string                              `json:"operation_id"`
	AttemptID         string                              `json:"attempt_id"`
	AuthorizationID   string                              `json:"authorization_id"`
	ConsumeRequestID  string                              `json:"consume_request_id"`
	ProviderEpoch     int64                               `json:"provider_epoch"`
	TargetFingerprint string                              `json:"target_fingerprint"`
	InstallerDigest   string                              `json:"installer_digest"`
	EvidenceDigest    string                              `json:"evidence_digest"`
	InstallState      migrations.D1LBootstrapInstallState `json:"install_state"`
	CleanupState      migrations.D1LCleanupState          `json:"cleanup_state"`
	Before            migrations.D1LCatalogState          `json:"before"`
	After             migrations.D1LCatalogState          `json:"after"`
	Committed         bool                                `json:"committed"`
	StartedAt         string                              `json:"started_at"`
	FinishedAt        string                              `json:"finished_at"`
	BackendPID        int64                               `json:"backend_pid"`
	MigrationLockKey  int64                               `json:"migration_lock_key"`
	OperationError    *d1LResultError                     `json:"operation_error,omitempty"`
}

type d1LResultError struct {
	Class   string `json:"class"`
	Message string `json:"message"`
}

func d1LResultFrameFromReport(report migrations.D1LBootstrapReport, operationErr error) d1LResultFrame {
	frame := d1LResultFrame{
		Schema:            "d1l-bootstrap-result-v1",
		OperationID:       report.OperationID,
		AttemptID:         report.AttemptID,
		AuthorizationID:   report.AuthorizationID,
		ConsumeRequestID:  report.ConsumeRequestID,
		ProviderEpoch:     report.ProviderEpoch,
		TargetFingerprint: report.TargetFingerprint,
		InstallerDigest:   report.InstallerDigest,
		EvidenceDigest:    report.EvidenceDigest,
		InstallState:      report.InstallState,
		CleanupState:      report.CleanupState,
		Before:            report.Before,
		After:             report.After,
		Committed:         report.Committed,
		StartedAt:         d1lResultTime(report.StartedAt),
		FinishedAt:        d1lResultTime(report.FinishedAt),
		BackendPID:        report.BackendPID,
		MigrationLockKey:  report.MigrationLockKey,
	}
	if operationErr != nil {
		frame.OperationError = &d1LResultError{
			Class:   d1lResultErrorClass(operationErr),
			Message: "D1-L bootstrap operation failed",
		}
	}
	return frame
}

func d1lResultTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// d1lResultErrorClass is a bounded, non-secret classification. In
// particular, the underlying error is never serialized: database URLs,
// provider responses, and envelope bytes must not cross the result channel.
func d1lResultErrorClass(err error) string {
	switch {
	case errors.Is(err, migrations.ErrD1LCommitUnknown):
		return "COMMIT_UNKNOWN"
	case errors.Is(err, migrations.ErrD1LNoRetry):
		return "NO_RETRY"
	case errors.Is(err, migrations.ErrD1LProviderBinding):
		return "PROVIDER_BINDING"
	case errors.Is(err, migrations.ErrD1LBootstrapState):
		return "BOOTSTRAP_STATE"
	case errors.Is(err, migrations.ErrD1LCatalog):
		return "CATALOG"
	case errors.Is(err, migrations.ErrD1LArtifactDigest):
		return "ARTIFACT_DIGEST"
	default:
		return "OPERATION_FAILED"
	}
}

func marshalD1LResultFrame(frame d1LResultFrame) ([]byte, error) {
	payload, err := json.Marshal(frame)
	if err != nil {
		return nil, fmt.Errorf("marshal D1-L result: %w", err)
	}
	if len(payload) > d1lResultMaxFrameSize-d1lResultPrefixSize {
		return nil, errD1LResultTooLarge
	}
	result := make([]byte, d1lResultPrefixSize+len(payload))
	binary.BigEndian.PutUint32(result[:d1lResultPrefixSize], uint32(len(payload)))
	copy(result[d1lResultPrefixSize:], payload)
	return result, nil
}

// writeD1LResult writes exactly one complete frame. A short write, a closed
// sink, or a writer that makes no progress is a delivery failure; no retry of
// the bootstrap operation is performed by this helper or its caller.
func writeD1LResult(w d1LResultWriter, frame d1LResultFrame) error {
	if w == nil {
		return errD1LResultSink
	}
	encoded, err := marshalD1LResultFrame(frame)
	if err != nil {
		return err
	}
	for len(encoded) > 0 {
		n, writeErr := w.Write(encoded)
		if n < 0 || n > len(encoded) {
			return fmt.Errorf("%w: invalid write count %d", errD1LResultSink, n)
		}
		if n > 0 {
			encoded = encoded[n:]
		}
		if writeErr != nil {
			return fmt.Errorf("%w: %v", errD1LResultSink, writeErr)
		}
		if n == 0 {
			return errD1LResultNoProgress
		}
	}
	return nil
}

// openD1LResultSink validates the inherited endpoint before bootstrap starts.
// F_GETFL catches closed and read-only descriptors without writing a probe byte
// into the one-shot result stream.
func openD1LResultSink() (*os.File, error) {
	if d1lProtectedResultFD == d1lProtectedPresentationFD || d1lProtectedResultFD <= 2 {
		return nil, errD1LResultSink
	}
	sink := os.NewFile(uintptr(d1lProtectedResultFD), "d1l-protected-result")
	if sink == nil {
		return nil, errD1LResultSink
	}
	if _, err := sink.Stat(); err != nil {
		_ = sink.Close()
		return nil, fmt.Errorf("%w: %v", errD1LResultSink, err)
	}
	flags, err := unix.FcntlInt(uintptr(d1lProtectedResultFD), unix.F_GETFL, 0)
	if err != nil {
		_ = sink.Close()
		return nil, fmt.Errorf("%w: %v", errD1LResultSink, err)
	}
	if flags&unix.O_ACCMODE == unix.O_RDONLY {
		_ = sink.Close()
		return nil, fmt.Errorf("%w: descriptor is not writable", errD1LResultSink)
	}
	return sink, nil
}
