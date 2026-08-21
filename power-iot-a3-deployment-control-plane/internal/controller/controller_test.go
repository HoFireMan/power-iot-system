package controller

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"power-iot-a3-deployment-control-plane/internal/ledger"
	"power-iot-a3-deployment-control-plane/internal/store"
	"power-iot-a3-deployment-control-plane/internal/testsupport/providerdsn"
	"power-iot-a3-deployment-control-plane/migrations"
)

const (
	testAuthorizationID = "11111111-1111-4111-8111-111111111111"
	testIssuerRequestID = "22222222-2222-4222-8222-222222222222"
	testAttemptID       = "33333333-3333-4333-8333-333333333333"
	testOperationID     = "44444444-4444-4444-8444-444444444444"
	testTarget          = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testEvidence        = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestControllerHelperProcess(t *testing.T) {
	if os.Getenv("D1L_CONTROLLER_HELPER") != "1" {
		return
	}
	presentation := os.NewFile(uintptr(ProtectedPresentationFD), "presentation")
	result := os.NewFile(uintptr(ProtectedResultFD), "result")
	if presentation == nil || result == nil {
		os.Exit(31)
	}
	_, _ = io.ReadAll(presentation)
	_ = presentation.Close()
	if os.Getenv("D1L_CONTROLLER_HELPER_MODE") == "malformed" {
		_, _ = result.Write([]byte{0, 0, 0, 1, '{'})
		_ = result.Close()
		os.Exit(32)
	}
	frame := ResultFrame{
		Schema:            resultSchema,
		OperationID:       os.Getenv("D1L_CONTROLLER_OPERATION"),
		AttemptID:         os.Getenv("D1L_CONTROLLER_ATTEMPT"),
		AuthorizationID:   os.Getenv("D1L_CONTROLLER_AUTHORIZATION"),
		ConsumeRequestID:  "55555555-5555-4555-8555-555555555555",
		ProviderEpoch:     helperEpoch(),
		TargetFingerprint: testTarget,
		InstallerDigest:   d1lInstallerDigestV1,
		EvidenceDigest:    testEvidence,
		InstallState:      "COMMITTED_READY",
		CleanupState:      "CLEAN",
		Before:            "V5_BASE",
		After:             "EXACT_READY",
		Committed:         true,
		StartedAt:         time.Now().UTC().Format(time.RFC3339Nano),
		FinishedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		BackendPID:        int64(os.Getpid()),
		MigrationLockKey:  17,
	}
	payload, err := json.Marshal(frame)
	if err != nil {
		os.Exit(33)
	}
	encoded := make([]byte, resultPrefixSize+len(payload))
	binary.BigEndian.PutUint32(encoded[:resultPrefixSize], uint32(len(payload)))
	copy(encoded[resultPrefixSize:], payload)
	_, _ = result.Write(encoded)
	_ = result.Close()
	if os.Getenv("D1L_CONTROLLER_HELPER_MODE") == "nonzero" {
		os.Exit(34)
	}
	os.Exit(0)
}

func TestControllerRunsOneProtectedFD3FD4Handoff(t *testing.T) {
	runner := controllerHelperScript(t)
	secret := strings.Repeat("opaque-provider-envelope-", 8192)
	var issueCalls atomic.Int32
	controller, err := New(Config{
		RunnerPath:  runner,
		DatabaseURL: "postgres://target.invalid/security_test_target",
		Environment: controllerHelperEnvironment(t, "normal"),
		TTL:         time.Minute,
		Issue: func(_ context.Context, request store.RequestData, ttl time.Duration) (store.IssueResult, error) {
			issueCalls.Add(1)
			if request.Role != "deployment-runbook" || request.Scope != ledger.ScopeControlCatalogInstall || ttl != time.Minute {
				t.Fatal("controller did not use the existing Provider Issue contract")
			}
			if request.ID != testIssuerRequestID || request.AttemptID != testAttemptID || len(request.Bindings) != 5 || request.Bindings["operation"] != testOperationID || request.Bindings["target_id"] != testTarget || request.Bindings["evidence_hash"] != testEvidence {
				t.Fatalf("unexpected Provider Issue tuple: %#v", request)
			}
			return store.IssueResult{
				AuthorizationID: testAuthorizationID,
				IssuerRequestID: testIssuerRequestID,
				AttemptID:       testAttemptID,
				State:           ledger.Issued,
				Epoch:           7,
				ExpiresAt:       time.Now().Add(time.Minute),
				Scope:           ledger.ScopeControlCatalogInstall,
				Bindings:        request.Bindings,
				Envelope:        secret,
				SecretAvailable: true,
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := controller.Run(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("controller run: %v", err)
	}
	if issueCalls.Load() != 1 {
		t.Fatalf("Provider Issue calls=%d, want one", issueCalls.Load())
	}
	if result.Issue != IssueIssued || result.Launch != LaunchStarted || result.Presentation != PresentationSent || result.Delivery != ResultDelivered || result.Exit != ExitZero || result.Operation != OperationCommittedReady {
		t.Fatalf("unexpected independent statuses: %+v", result)
	}
	if result.Frame == nil || result.Frame.AuthorizationID != testAuthorizationID || result.Frame.InstallState != "COMMITTED_READY" {
		t.Fatalf("missing authoritative frame: %+v", result.Frame)
	}
	if strings.Contains(string(mustJSON(t, result)), secret) {
		t.Fatal("raw Provider envelope crossed controller result")
	}
}

func TestControllerSeparatesDeliveredOperationTruthFromExitStatus(t *testing.T) {
	runner := controllerHelperScript(t)
	controller, err := New(Config{
		RunnerPath:  runner,
		DatabaseURL: "postgres://target.invalid/security_test_target",
		Environment: controllerHelperEnvironment(t, "nonzero"),
		Issue:       testIssueFunc,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := controller.Run(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("delivered frame should remain consumable despite child exit: %v", err)
	}
	if result.Delivery != ResultDelivered || result.Operation != OperationCommittedReady || result.Exit != ExitNonZero {
		t.Fatalf("operation truth was coupled to exit status: %+v", result)
	}
}

func TestControllerRejectsMalformedResultWithoutRetryOrSecretDisclosure(t *testing.T) {
	runner := controllerHelperScript(t)
	secret := "do-not-leak-provider-envelope"
	var issueCalls atomic.Int32
	controller, err := New(Config{
		RunnerPath:  runner,
		DatabaseURL: "postgres://target.invalid/security_test_target",
		Environment: controllerHelperEnvironment(t, "malformed"),
		Issue: func(context.Context, store.RequestData, time.Duration) (store.IssueResult, error) {
			issueCalls.Add(1)
			return testIssued(secret), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := controller.Run(context.Background(), testRequest())
	if !errors.Is(err, ErrControllerResult) {
		t.Fatalf("malformed result error=%v, want protected result error", err)
	}
	if issueCalls.Load() != 1 || result.Delivery != ResultFailed || result.Operation != OperationUnavailable || result.Frame != nil {
		t.Fatalf("malformed result/replay statuses=%+v calls=%d", result, issueCalls.Load())
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(string(mustJSON(t, result)), secret) {
		t.Fatal("raw envelope disclosed by controller failure")
	}
}

func TestControllerUsesExistingProviderIssuedAuthorizationWhenConfigured(t *testing.T) {
	raw := strings.TrimSpace(os.Getenv("D1L_PROVIDER_DATABASE_URL"))
	if raw == "" {
		t.Skip("D1L_PROVIDER_DATABASE_URL is not configured; real Provider controller check skipped")
	}
	if err := providerdsn.ValidateProviderTestURL(raw); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.AcquireAuthorityWithBootstrap(context.Background(), migrations.Bootstrap); err != nil {
		t.Fatal(err)
	}
	defer s.ReleaseAuthority()
	issuerID := testIssuerRequestID
	issued, err := s.Issue(context.Background(), store.RequestData{
		ID: issuerID, AttemptID: testAttemptID, Role: "deployment-runbook", Scope: ledger.ScopeControlCatalogInstall,
		Bindings: map[string]string{"operation": testOperationID, "attempt_id": testAttemptID, "target_id": testTarget, "installer_id": d1lInstallerDigestV1, "evidence_hash": testEvidence},
	}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if issued.State != ledger.Issued || !issued.SecretAvailable || issued.Envelope == "" {
		t.Fatalf("real Provider did not return one usable authorization: %#v", issued)
	}
	controller, err := New(Config{
		RunnerPath: runnerForTest(t), DatabaseURL: "postgres://target.invalid/security_test_target",
		Environment: controllerHelperEnvironmentWithEpoch(t, "normal", issued.Epoch),
		Issue: func(_ context.Context, request store.RequestData, _ time.Duration) (store.IssueResult, error) {
			// The Provider authorization was issued once above. This callback is
			// an existing Provider API result adapter, not an issuer or retry.
			if request.ID != issuerID || request.AttemptID != testAttemptID {
				t.Fatalf("controller changed existing Provider request identity: %#v", request)
			}
			return issued, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := controller.Run(context.Background(), testRequest())
	if err != nil || result.Delivery != ResultDelivered || result.Operation != OperationCommittedReady {
		t.Fatalf("real Provider-issued controller run=%+v err=%v", result, err)
	}
	if result.Frame == nil || result.Frame.AuthorizationID != issued.AuthorizationID || result.Frame.ProviderEpoch != issued.Epoch {
		t.Fatalf("real Provider identity/epoch not preserved: %+v", result.Frame)
	}
}

func TestControllerLaunchFailureDoesNotIssueOrRetry(t *testing.T) {
	var issueCalls atomic.Int32
	controller, err := New(Config{
		RunnerPath:  filepath.Join(t.TempDir(), "missing-d1l-bootstrap"),
		DatabaseURL: "postgres://target.invalid/security_test_target",
		Issue: func(context.Context, store.RequestData, time.Duration) (store.IssueResult, error) {
			issueCalls.Add(1)
			return testIssued("secret"), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := controller.Run(context.Background(), testRequest())
	if !errors.Is(err, ErrControllerLaunch) || issueCalls.Load() != 1 || result.Launch != LaunchNotAttempted || result.Exit != ExitNotAttempted {
		t.Fatalf("launch failure statuses=%+v calls=%d err=%v", result, issueCalls.Load(), err)
	}
}

func TestControllerRejectsEnvelopeInResultProjection(t *testing.T) {
	frame := ResultFrame{Schema: resultSchema, OperationError: &ResultError{Class: "OPERATION_FAILED", Message: "prefix-secret-suffix"}, CleanupState: "UNKNOWN"}
	if err := validateResultFrame(frame, testRequest(), testIssued("secret"), []byte("secret")); !errors.Is(err, ErrControllerResult) {
		t.Fatalf("result projection containing envelope was accepted: %v", err)
	}
}

func TestControllerRejectsTrailingAndOversizedFrames(t *testing.T) {
	frame := ResultFrame{Schema: resultSchema, OperationID: testOperationID, AttemptID: testAttemptID, AuthorizationID: testAuthorizationID, ConsumeRequestID: uuid.NewString()}
	payload, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	encoded := make([]byte, resultPrefixSize+len(payload)+1)
	binary.BigEndian.PutUint32(encoded[:resultPrefixSize], uint32(len(payload)))
	copy(encoded[resultPrefixSize:], payload)
	encoded[len(encoded)-1] = 1
	if _, err := readResultFrame(bytes.NewReader(encoded)); !errors.Is(err, ErrControllerResult) {
		t.Fatalf("trailing frame accepted: %v", err)
	}
	var prefix [resultPrefixSize]byte
	binary.BigEndian.PutUint32(prefix[:], resultMaxFrameSize)
	if _, err := readResultFrame(bytes.NewReader(prefix[:])); !errors.Is(err, ErrControllerResult) {
		t.Fatalf("oversized frame accepted: %v", err)
	}
}

func testRequest() Request {
	return Request{IssuerRequestID: testIssuerRequestID, OperationID: testOperationID, AttemptID: testAttemptID, TargetFingerprint: testTarget, EvidenceDigest: testEvidence}
}

func testIssueFunc(context.Context, store.RequestData, time.Duration) (store.IssueResult, error) {
	return testIssued("test-envelope"), nil
}

func testIssued(envelope string) store.IssueResult {
	return store.IssueResult{
		AuthorizationID: testAuthorizationID,
		IssuerRequestID: testIssuerRequestID,
		AttemptID:       testAttemptID,
		State:           ledger.Issued,
		Epoch:           7,
		ExpiresAt:       time.Now().Add(time.Minute),
		Scope:           ledger.ScopeControlCatalogInstall,
		Bindings: map[string]string{
			"operation":     testOperationID,
			"attempt_id":    testAttemptID,
			"target_id":     testTarget,
			"installer_id":  d1lInstallerDigestV1,
			"evidence_hash": testEvidence,
		},
		Envelope:        envelope,
		SecretAvailable: true,
	}
}

func controllerHelperScript(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "d1l-bootstrap-helper")
	contents := "#!/bin/sh\noperation=; attempt=; authorization=\nwhile [ \"$#\" -gt 0 ]; do\n  case \"$1\" in\n    --operation-id) operation=\"$2\"; shift 2 ;;\n    --attempt-id) attempt=\"$2\"; shift 2 ;;\n    --authorization-id) authorization=\"$2\"; shift 2 ;;\n    *) shift ;;\n  esac\ndone\nexport D1L_CONTROLLER_OPERATION=\"$operation\"\nexport D1L_CONTROLLER_ATTEMPT=\"$attempt\"\nexport D1L_CONTROLLER_AUTHORIZATION=\"$authorization\"\nexec \"$D1L_CONTROLLER_HELPER_BINARY\" -test.run=TestControllerHelperProcess\n"
	if err := os.WriteFile(script, []byte(contents), 0700); err != nil {
		t.Fatal(err)
	}
	return script
}

func controllerHelperEnvironment(t *testing.T, mode string) []string {
	t.Helper()
	binary, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	return []string{
		"D1L_CONTROLLER_HELPER=1",
		"D1L_CONTROLLER_HELPER_BINARY=" + binary,
		"D1L_CONTROLLER_HELPER_MODE=" + mode,
		"D1L_CONTROLLER_OPERATION=" + testOperationID,
		"D1L_CONTROLLER_ATTEMPT=" + testAttemptID,
		"D1L_CONTROLLER_AUTHORIZATION=" + testAuthorizationID,
		"D1L_CONTROLLER_EPOCH=7",
	}
}

func controllerHelperEnvironmentWithEpoch(t *testing.T, mode string, epoch int64) []string {
	t.Helper()
	env := controllerHelperEnvironment(t, mode)
	for i, entry := range env {
		if strings.HasPrefix(entry, "D1L_CONTROLLER_EPOCH=") {
			env[i] = "D1L_CONTROLLER_EPOCH=" + strconv.FormatInt(epoch, 10)
			return env
		}
	}
	return append(env, "D1L_CONTROLLER_EPOCH="+strconv.FormatInt(epoch, 10))
}

func helperEpoch() int64 {
	epoch, err := strconv.ParseInt(os.Getenv("D1L_CONTROLLER_EPOCH"), 10, 64)
	if err != nil || epoch <= 0 {
		return 7
	}
	return epoch
}

func runnerForTest(t *testing.T) string {
	t.Helper()
	return controllerHelperScript(t)
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
