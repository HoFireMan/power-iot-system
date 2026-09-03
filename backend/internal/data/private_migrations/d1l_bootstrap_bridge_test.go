package migrations

import (
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type d1lFailingReader struct{}

func (d1lFailingReader) Read([]byte) (int, error) {
	return 0, errors.New("synthetic envelope read failure")
}

type d1lTruncatedReader struct{}

func (d1lTruncatedReader) Read(p []byte) (int, error) {
	const partial = "synthetic-truncated-envelope"
	copy(p, partial)
	return len(partial), io.ErrUnexpectedEOF
}

func validD1LInspectForTest(t *testing.T) (InspectResult, D1LBootstrapConfig) {
	t.Helper()
	cfg := D1LBootstrapConfig{
		OperationID:       uuid.NewString(),
		AttemptID:         uuid.NewString(),
		AuthorizationID:   uuid.NewString(),
		TargetFingerprint: []byte(strings.Repeat("t", 32)),
		EvidenceDigest:    []byte(strings.Repeat("e", 32)),
	}
	return InspectResult{
		Outcome:         OutcomeSuccess,
		AuthorizationID: cfg.AuthorizationID,
		IssuerRequestID: uuid.NewString(),
		AttemptID:       cfg.AttemptID,
		State:           AuthorizationIssued,
		Epoch:           1,
		Nonce:           "AAAAAAAAAAAAAAAAAAAAAA",
		ExpiresAt:       time.Now().UTC().Add(time.Hour),
		Scope:           ScopeControlCatalogInstall,
		Bindings: map[string]string{
			"operation":     cfg.OperationID,
			"attempt_id":    cfg.AttemptID,
			"target_id":     strings.Repeat("a", 64),
			"installer_id":  D1LInstallerDigestV1,
			"evidence_hash": hex.EncodeToString(cfg.EvidenceDigest),
		},
	}, cfg
}

func TestD1LProviderBindingDerivesPrivateAdmission(t *testing.T) {
	inspect, cfg := validD1LInspectForTest(t)
	validation, err := validateD1LInspect(inspect, cfg, D1LInstallerDigestV1, strings.Repeat("a", 64))
	if err != nil {
		t.Fatalf("valid provider binding rejected: %v", err)
	}
	if err := RequireExternalWriterAdmission(deriveExternalWriterAdmission(validation)); err != nil {
		t.Fatalf("validated provider binding did not derive admission: %v", err)
	}
	if _, err := validateD1LInspect(inspect, cfg, D1LInstallerDigestV1, strings.Repeat("b", 64)); !errors.Is(err, ErrD1LProviderBinding) {
		t.Fatalf("wrong target validation error=%v", err)
	}
	if err := RequireExternalWriterAdmission(deriveExternalWriterAdmission(d1LInspectValidation{})); !errors.Is(err, ErrExternalWriterAdmissionRequired) {
		t.Fatalf("zero validation marker authorized protected work: %v", err)
	}
}

func TestReadD1LEnvelopeFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   interface{ Read([]byte) (int, error) }
	}{
		{name: "empty", in: strings.NewReader("")},
		{name: "oversized", in: strings.NewReader(strings.Repeat("x", (1<<20)+1))},
		{name: "truncated", in: d1lTruncatedReader{}},
		{name: "read error", in: d1lFailingReader{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := readD1LEnvelope(tc.in); err == nil {
				t.Fatal("invalid protected envelope was accepted")
			}
		})
	}
}
