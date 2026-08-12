package reconciliation

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestClassificationCountsAndOperatorExitCodes(t *testing.T) {
	counts := classificationCounts([]FactClassification{
		{Classification: AlreadyConsistent},
		{Classification: AlreadyConsistent},
		{Classification: ExplicitMappingRequired},
	})
	if counts[string(AlreadyConsistent)] != 2 || counts[string(ExplicitMappingRequired)] != 1 {
		t.Fatalf("counts=%v", counts)
	}
	cases := []struct {
		name     string
		report   OperatorReport
		err      error
		wantCode int
	}{
		{"diagnostic", OperatorReport{Mode: OperatorDiagnostic}, nil, 0},
		{"diagnostic error", OperatorReport{Mode: OperatorDiagnostic}, errors.New("read failed"), 2},
		{"not committed", OperatorReport{Mode: OperatorExecute, Outcome: ExecutionNotCommitted}, errors.New("blocked"), 10},
		{"unknown", OperatorReport{Mode: OperatorExecute, Outcome: ExecutionCommitOutcomeUnknown}, errors.New("ambiguous"), 11},
		{"postverify", OperatorReport{Mode: OperatorExecute, Outcome: ExecutionCommittedPostVerifyFailed}, errors.New("verify"), 12},
		{"cleanup", OperatorReport{Mode: OperatorExecute, Outcome: ExecutionCommittedAndVerified, CleanupError: "uncertain"}, errors.New("cleanup"), 13},
		{"success", OperatorReport{Mode: OperatorExecute, Outcome: ExecutionCommittedAndVerified}, nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := OperatorExitCode(tc.report, tc.err); got != tc.wantCode {
				t.Fatalf("exit=%d want %d", got, tc.wantCode)
			}
		})
	}
}

func TestSafeOperatorErrorRedactsURLAndKeywordCredentials(t *testing.T) {
	for _, message := range []string{
		`parse "postgres://alice:supersecret@%zz/nope": invalid URL escape`,
		`host=db user=alice password=supersecret dbname=security`,
	} {
		redacted := safeOperatorError(errors.New(message))
		if strings.Contains(redacted, "supersecret") || strings.Contains(redacted, "alice:") {
			t.Fatalf("credential leaked from %q: %q", message, redacted)
		}
	}
}

func TestFilteredPostgresURLRemovesMigrationOnlyOptions(t *testing.T) {
	filtered, err := filteredPostgresURL("postgres://user:pass@example.test/security?sslmode=disable&x-migrations-table=custom")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(filtered, "x-migrations-table") || !strings.Contains(filtered, "sslmode=disable") {
		t.Fatalf("filtered URL=%q", filtered)
	}
}

func TestRenderOperatorReportIsCredentialFreeAndStable(t *testing.T) {
	var out bytes.Buffer
	report := OperatorReport{
		Mode: OperatorExecute, Outcome: ExecutionCommitOutcomeUnknown,
		SourceFactsDigest: strings.Repeat("a", 64), Error: "commit acknowledgement was ambiguous",
	}
	if err := RenderOperatorReport(&out, report); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"outcome": "COMMIT_OUTCOME_UNKNOWN"`) {
		t.Fatalf("output=%s", out.String())
	}
	if strings.Contains(out.String(), "postgres://") || strings.Contains(out.String(), "password") {
		t.Fatalf("credential-like output=%s", out.String())
	}
}
