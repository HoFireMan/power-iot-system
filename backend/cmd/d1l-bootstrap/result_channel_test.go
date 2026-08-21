package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"power-iot-backend/internal/data/migrations"
)

func testD1LResultReport() migrations.D1LBootstrapReport {
	return migrations.D1LBootstrapReport{
		InstallState:      migrations.D1LInstallCommittedReady,
		CleanupState:      migrations.D1LCleanupClean,
		Before:            migrations.D1LV5Base,
		After:             migrations.D1LExactReady,
		AuthorizationID:   "authorization-uuid",
		ConsumeRequestID:  "consume-uuid",
		OperationID:       "operation-uuid",
		AttemptID:         "attempt-uuid",
		ProviderEpoch:     17,
		TargetFingerprint: strings.Repeat("a", 64),
		InstallerDigest:   strings.Repeat("b", 64),
		EvidenceDigest:    strings.Repeat("c", 64),
		BackendPID:        1234,
		MigrationLockKey:  5678,
		Committed:         true,
		StartedAt:         time.Date(2026, time.January, 2, 3, 4, 5, 123456789, time.FixedZone("test", 3600)),
		FinishedAt:        time.Date(2026, time.January, 2, 3, 4, 6, 987654321, time.FixedZone("test", 3600)),
	}
}

func decodeD1LResultFrame(t *testing.T, encoded []byte) d1LResultFrame {
	t.Helper()
	if len(encoded) < d1lResultPrefixSize {
		t.Fatalf("encoded frame length=%d", len(encoded))
	}
	length := int(binary.BigEndian.Uint32(encoded[:d1lResultPrefixSize]))
	if length != len(encoded)-d1lResultPrefixSize {
		t.Fatalf("prefix length=%d, payload bytes=%d", length, len(encoded)-d1lResultPrefixSize)
	}
	var got d1LResultFrame
	if err := json.Unmarshal(encoded[d1lResultPrefixSize:], &got); err != nil {
		t.Fatalf("decode frame: %v", err)
	}
	return got
}

func TestD1LResultFramePreservesAuthoritativeFields(t *testing.T) {
	report := testD1LResultReport()
	var sink bytes.Buffer
	if err := writeD1LResult(&sink, d1LResultFrameFromReport(report, nil)); err != nil {
		t.Fatalf("write result: %v", err)
	}
	got := decodeD1LResultFrame(t, sink.Bytes())
	if got.Schema != "d1l-bootstrap-result-v1" || got.OperationID != report.OperationID || got.AttemptID != report.AttemptID || got.AuthorizationID != report.AuthorizationID || got.ConsumeRequestID != report.ConsumeRequestID {
		t.Fatalf("identity fields not preserved: %+v", got)
	}
	if got.ProviderEpoch != report.ProviderEpoch || got.TargetFingerprint != report.TargetFingerprint || got.InstallerDigest != report.InstallerDigest || got.EvidenceDigest != report.EvidenceDigest {
		t.Fatalf("binding fields not preserved: %+v", got)
	}
	if got.InstallState != report.InstallState || got.CleanupState != report.CleanupState || got.Before != report.Before || got.After != report.After || !got.Committed {
		t.Fatalf("state fields not preserved: %+v", got)
	}
	if got.BackendPID != report.BackendPID || got.MigrationLockKey != report.MigrationLockKey || got.StartedAt != report.StartedAt.UTC().Format(time.RFC3339Nano) || got.FinishedAt != report.FinishedAt.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("execution fields not preserved: %+v", got)
	}
}

func TestD1LResultFramePreservesAllInstallStatesAndCleanupStates(t *testing.T) {
	for _, state := range []migrations.D1LBootstrapInstallState{migrations.D1LInstallNotInstalled, migrations.D1LInstallCommittedReady, migrations.D1LInstallUnknown} {
		for _, cleanup := range []migrations.D1LCleanupState{migrations.D1LCleanupClean, migrations.D1LCleanupIncomplete, migrations.D1LCleanupUnknown} {
			report := testD1LResultReport()
			report.InstallState, report.CleanupState = state, cleanup
			var sink bytes.Buffer
			if err := writeD1LResult(&sink, d1LResultFrameFromReport(report, nil)); err != nil {
				t.Fatalf("state=%s cleanup=%s: %v", state, cleanup, err)
			}
			got := decodeD1LResultFrame(t, sink.Bytes())
			if got.InstallState != state || got.CleanupState != cleanup {
				t.Fatalf("got state=%s cleanup=%s, want state=%s cleanup=%s", got.InstallState, got.CleanupState, state, cleanup)
			}
		}
	}
}

func TestD1LResultFrameExcludesSecretsAndRawOperationError(t *testing.T) {
	secretEnvelope := "raw-provider-envelope-secret"
	secretAdmission := "external-writer-admission-secret"
	secretCredential := "database-password-secret"
	report := testD1LResultReport()
	report.AuthorizationID = "non-secret-authorization-id"
	var sink bytes.Buffer
	operationErr := errors.New("dsn=postgres://user:password@host/db envelope=" + secretEnvelope + " admission=" + secretAdmission + " credential=" + secretCredential)
	if err := writeD1LResult(&sink, d1LResultFrameFromReport(report, operationErr)); err != nil {
		t.Fatalf("write result: %v", err)
	}
	encoded := string(sink.Bytes())
	for _, secret := range []string{secretEnvelope, secretAdmission, secretCredential, "postgres://", "password"} {
		if strings.Contains(encoded, secret) {
			t.Fatalf("result contains secret material %q: %s", secret, encoded)
		}
	}
	got := decodeD1LResultFrame(t, sink.Bytes())
	if got.OperationError == nil || got.OperationError.Class != "OPERATION_FAILED" || got.OperationError.Message != "D1-L bootstrap operation failed" {
		t.Fatalf("unexpected bounded operation error: %+v", got.OperationError)
	}
}

type chunkWriter struct {
	buffer bytes.Buffer
	chunk  int
	fail   error
}

func (w *chunkWriter) Write(p []byte) (int, error) {
	if w.fail != nil {
		n := len(p)
		if w.chunk > 0 && n > w.chunk {
			n = w.chunk
		}
		_, _ = w.buffer.Write(p[:n])
		return n, w.fail
	}
	n := len(p)
	if w.chunk > 0 && n > w.chunk {
		n = w.chunk
	}
	return w.buffer.Write(p[:n])
}

func TestD1LResultWriterCompletesShortWrites(t *testing.T) {
	writer := &chunkWriter{chunk: 3}
	if err := writeD1LResult(writer, d1LResultFrameFromReport(testD1LResultReport(), nil)); err != nil {
		t.Fatalf("short writes should be completed: %v", err)
	}
	got := decodeD1LResultFrame(t, writer.buffer.Bytes())
	if got.InstallState != migrations.D1LInstallCommittedReady || !got.Committed {
		t.Fatalf("decoded frame=%+v", got)
	}
}

func TestD1LDeliveryFailureDoesNotReplayOperation(t *testing.T) {
	want := testD1LResultReport()
	operationCalls := 0
	report, operationErr, deliveryErr := runD1LBootstrapAndDeliver(func() (migrations.D1LBootstrapReport, error) {
		operationCalls++
		return want, errors.New("synthetic operation failure")
	}, &chunkWriter{chunk: 3, fail: io.ErrClosedPipe})
	if operationCalls != 1 {
		t.Fatalf("operation calls=%d, want exactly one", operationCalls)
	}
	if operationErr == nil || deliveryErr == nil {
		t.Fatalf("operationErr=%v deliveryErr=%v, want both errors", operationErr, deliveryErr)
	}
	if report.InstallState != want.InstallState || report.After != want.After || report.Committed != want.Committed {
		t.Fatalf("operation report changed during delivery failure: got=%+v want=%+v", report, want)
	}
}

func TestD1LResultWriterRejectsPartialFailedWriteAndNoProgress(t *testing.T) {
	failed := &chunkWriter{chunk: 3, fail: io.ErrClosedPipe}
	if err := writeD1LResult(failed, d1LResultFrameFromReport(testD1LResultReport(), nil)); err == nil {
		t.Fatal("partial failed write reported success")
	}
	noProgress := &chunkWriter{chunk: 0}
	noProgress.fail = nil
	// A writer with chunk zero writes its complete input, so use an explicit
	// no-progress writer for the no-progress contract.
	if err := writeD1LResult(noProgressWriter{}, d1LResultFrameFromReport(testD1LResultReport(), nil)); !errors.Is(err, errD1LResultNoProgress) {
		t.Fatalf("no-progress error=%v", err)
	}
}

type noProgressWriter struct{}

func (noProgressWriter) Write([]byte) (int, error) { return 0, nil }

func TestD1LResultChannelUsesDedicatedDescriptor(t *testing.T) {
	if d1lProtectedResultFD == d1lProtectedPresentationFD || d1lProtectedResultFD <= 2 {
		t.Fatalf("result FD=%d is not dedicated from presentation FD=%d/stdout/stderr", d1lProtectedResultFD, d1lProtectedPresentationFD)
	}
	if d1lResultMaxFrameSize <= d1lResultPrefixSize {
		t.Fatal("invalid bounded frame size")
	}
}

func TestD1LResultSinkRejectsMissingAndReadOnlyWithoutFallback(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		withD1LResultFD(t, nil)
		if _, err := openD1LResultSink(); err == nil {
			t.Fatal("missing result endpoint was accepted")
		}
	})
	t.Run("read-only", func(t *testing.T) {
		file, err := os.CreateTemp(t.TempDir(), "d1l-result-")
		if err != nil {
			t.Fatal(err)
		}
		name := file.Name()
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		readOnly, err := os.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		defer readOnly.Close()
		withD1LResultFD(t, readOnly)
		if _, err := openD1LResultSink(); err == nil {
			t.Fatal("read-only result endpoint was accepted")
		}
	})
}

// withD1LResultFD changes only the child test process's FD 4 and restores it
// before the test ends. The production seam remains the inherited descriptor;
// no stdout descriptor is substituted.
func withD1LResultFD(t *testing.T, replacement *os.File) {
	t.Helper()
	saved, err := unix.FcntlInt(uintptr(d1lProtectedResultFD), unix.F_DUPFD_CLOEXEC, 100)
	hadSaved := err == nil
	if err != nil && !errors.Is(err, unix.EBADF) {
		t.Fatalf("save result fd: %v", err)
	}
	if replacement == nil {
		_ = unix.Close(d1lProtectedResultFD)
	} else {
		dup, err := unix.FcntlInt(replacement.Fd(), unix.F_DUPFD_CLOEXEC, 100)
		if err != nil {
			t.Fatalf("duplicate replacement result fd: %v", err)
		}
		if err := unix.Dup2(int(dup), d1lProtectedResultFD); err != nil {
			_ = unix.Close(int(dup))
			t.Fatalf("install replacement result fd: %v", err)
		}
		_ = unix.Close(int(dup))
	}
	t.Cleanup(func() {
		_ = unix.Close(d1lProtectedResultFD)
		if hadSaved {
			_ = unix.Dup2(saved, d1lProtectedResultFD)
			_ = unix.Close(saved)
		}
	})
}

func TestD1LResultFrameIsBounded(t *testing.T) {
	report := testD1LResultReport()
	report.AuthorizationID = strings.Repeat("x", d1lResultMaxFrameSize)
	if _, err := marshalD1LResultFrame(d1LResultFrameFromReport(report, nil)); !errors.Is(err, errD1LResultTooLarge) {
		t.Fatalf("oversized result error=%v", err)
	}
}
