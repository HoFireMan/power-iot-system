package reconciliation

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"time"

	"power-iot-backend/internal/data/migrations"

	"github.com/golang-migrate/migrate/v4"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// OperatorMode identifies the intentionally separate read-only and mutating
// operator paths.
type OperatorMode string

const (
	OperatorDiagnostic OperatorMode = "diagnostic"
	OperatorExecute    OperatorMode = "execute"
)

// DiagnosticReport contains evidence only. It never contains a Plan and is
// never accepted by ProtectedExecutor as write authorization.
type DiagnosticReport struct {
	Mode                     OperatorMode                       `json:"mode"`
	SourceFactsDigest        string                             `json:"source_facts_digest,omitempty"`
	MappingBasisDigest       string                             `json:"mapping_basis_digest,omitempty"`
	MappingDigest            string                             `json:"mapping_digest,omitempty"`
	PlanID                   string                             `json:"plan_id,omitempty"`
	PlanDigest               string                             `json:"plan_digest,omitempty"`
	FrozenAt                 time.Time                          `json:"frozen_at,omitempty"`
	ClassificationCounts     map[string]int                     `json:"classification_counts,omitempty"`
	ExpectedAffectedCounts   map[string]int                     `json:"expected_affected_counts,omitempty"`
	Blockers                 []string                           `json:"blockers,omitempty"`
	RequiredExplicitMappings []string                           `json:"required_explicit_mappings,omitempty"`
	BackendPID               int64                              `json:"-"`
	FenceState               migrations.ExclusiveOwnershipState `json:"-"`
	Error                    string                             `json:"error,omitempty"`
}

// OperatorReport is the stable, credential-free result rendered by the
// operator command. Outcome is meaningful for execute mode; diagnostic mode
// succeeds when evidence was collected, even if the evidence contains
// blockers.
type OperatorReport struct {
	Mode                     OperatorMode                       `json:"mode"`
	Outcome                  ExecutionOutcome                   `json:"outcome,omitempty"`
	Phase                    ExecutionPhase                     `json:"phase,omitempty"`
	SourceFactsDigest        string                             `json:"source_facts_digest,omitempty"`
	MappingBasisDigest       string                             `json:"mapping_basis_digest,omitempty"`
	MappingDigest            string                             `json:"mapping_digest,omitempty"`
	PlanID                   string                             `json:"plan_id,omitempty"`
	PlanDigest               string                             `json:"plan_digest,omitempty"`
	FrozenAt                 time.Time                          `json:"frozen_at,omitempty"`
	ClassificationCounts     map[string]int                     `json:"classification_counts,omitempty"`
	ExpectedAffectedCounts   map[string]int                     `json:"expected_affected_counts,omitempty"`
	AppliedAffectedCounts    map[string]int                     `json:"applied_affected_counts,omitempty"`
	Blockers                 []string                           `json:"blockers,omitempty"`
	RequiredExplicitMappings []string                           `json:"required_explicit_mappings,omitempty"`
	Committed                bool                               `json:"committed"`
	PostCommitVerified       bool                               `json:"post_commit_verified"`
	TriggerRestored          bool                               `json:"trigger_restored"`
	BackendPID               int64                              `json:"-"`
	FenceState               migrations.ExclusiveOwnershipState `json:"-"`
	CleanupError             string                             `json:"cleanup_error,omitempty"`
	D007Terminal             D007TerminalEvidence               `json:"d007_terminal,omitempty"`
	Error                    string                             `json:"error,omitempty"`
}

func DiagnoseV5(ctx context.Context, dsn string, mapping *MappingArtifact) (report DiagnosticReport, err error) {
	report.Mode = OperatorDiagnostic
	if ctx == nil {
		ctx = context.Background()
	}
	fence, err := migrations.OpenExclusiveWriterFence(ctx, dsn)
	if err != nil {
		report.Error = err.Error()
		return report, err
	}
	report.BackendPID = fence.BackendPID()
	defer func() {
		closeErr := fence.Close()
		report.FenceState = fence.State()
		if closeErr != nil {
			report.Error = closeErr.Error()
			if err == nil {
				err = closeErr
			} else {
				err = errors.Join(err, closeErr)
			}
		}
	}()

	driverURL, err := filteredPostgresURL(dsn)
	if err != nil {
		report.Error = safeOperatorError(err)
		return report, err
	}
	db, err := gorm.Open(postgres.Open(driverURL), &gorm.Config{})
	if err != nil {
		report.Error = err.Error()
		return report, err
	}
	defer func() {
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
	}()
	metadataTable, err := migrations.ConfiguredMigrationTable(ctx, dsn, fence.Conn())
	if err != nil {
		report.Error = safeOperatorError(err)
		return report, err
	}
	collector := NewPostgresFactCollectorWithMetadataTable(db, metadataTable)
	tx, err := fence.Conn().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		report.Error = err.Error()
		return report, err
	}
	rollback := func() error { return tx.Rollback() }
	defer func() {
		if rollbackErr := rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			if err == nil {
				err = rollbackErr
			} else {
				err = errors.Join(err, rollbackErr)
			}
			report.Error = err.Error()
		}
	}()

	if err = tx.QueryRowContext(ctx, "SELECT transaction_timestamp()").Scan(&report.FrozenAt); err != nil {
		report.Error = err.Error()
		return report, err
	}
	report.FrozenAt = report.FrozenAt.UTC()
	facts, err := collector.CollectV5Pinned(ctx, report.FrozenAt, tx)
	if err != nil {
		report.Error = err.Error()
		return report, err
	}
	_, sourceDigest, err := CanonicalSourceFacts(facts)
	if err != nil {
		report.Error = err.Error()
		return report, err
	}
	report.SourceFactsDigest = hex.EncodeToString(sourceDigest)
	mappingBasisDigest, err := MappingSourceFactsDigest(facts)
	if err != nil {
		report.Error = safeOperatorError(err)
		return report, err
	}
	report.MappingBasisDigest = hex.EncodeToString(mappingBasisDigest)
	if mapping != nil {
		report.MappingDigest, err = mapping.DigestHex()
		if err != nil {
			report.Error = err.Error()
			return report, err
		}
	}
	plan, err := BuildPlan(facts, mapping)
	if err != nil {
		report.Error = err.Error()
		return report, err
	}
	report.PlanID = plan.PlanID.String()
	report.PlanDigest = hex.EncodeToString(plan.Digest)
	report.ClassificationCounts = classificationCounts(plan.FactClassifications)
	report.ExpectedAffectedCounts = copyCounts(plan.ExpectedAffectedCounts)
	report.Blockers = append([]string(nil), plan.Blockers...)
	report.RequiredExplicitMappings = append([]string(nil), plan.RequiredExplicitMappings...)
	if mapping != nil && len(plan.RequiredExplicitMappings) != 0 {
		err = fmt.Errorf("mapping artifact has incomplete coverage: %v", plan.RequiredExplicitMappings)
		report.Error = err.Error()
		return report, err
	}
	return report, nil
}

func classificationCounts(classes []FactClassification) map[string]int {
	counts := make(map[string]int)
	for _, class := range classes {
		counts[string(class.Classification)]++
	}
	return counts
}

func OperatorReportFromDiagnostic(report DiagnosticReport, err error) OperatorReport {
	return OperatorReport{
		Mode: report.Mode, SourceFactsDigest: report.SourceFactsDigest,
		MappingDigest: report.MappingDigest, MappingBasisDigest: report.MappingBasisDigest, PlanID: report.PlanID, PlanDigest: report.PlanDigest,
		FrozenAt: report.FrozenAt, ClassificationCounts: report.ClassificationCounts,
		ExpectedAffectedCounts: report.ExpectedAffectedCounts, Blockers: report.Blockers,
		RequiredExplicitMappings: report.RequiredExplicitMappings, BackendPID: report.BackendPID,
		FenceState: report.FenceState, Error: diagnosticError(report.Error, err),
	}
}

func OperatorReportFromExecution(report ExecutionReport, err error) OperatorReport {
	return OperatorReport{
		Mode: OperatorExecute, Outcome: report.Outcome, Phase: report.Phase,
		SourceFactsDigest: report.SourceFactsDigest, MappingBasisDigest: report.MappingBasisDigest, MappingDigest: report.MappingDigest,
		PlanID: report.PlanID.String(), PlanDigest: report.PlanDigest, FrozenAt: report.FrozenAt,
		ExpectedAffectedCounts: report.ExpectedAffectedCounts, AppliedAffectedCounts: report.AppliedAffectedCounts,
		Committed: report.Committed, PostCommitVerified: report.PostCommitVerified,
		TriggerRestored: report.TriggerRestored, BackendPID: report.BackendPID,
		FenceState: report.FenceState, CleanupError: safeOperatorString(report.CleanupError),
		D007Terminal: report.D007Terminal,
		Error:        diagnosticError("", err),
	}
}

func safeOperatorError(err error) string {
	if err == nil {
		return ""
	}
	// Database-driver errors may echo arbitrary DSN syntax. The operator
	// surface deliberately reports only a safe generic diagnostic; outcome,
	// phase, and cleanup fields carry the actionable execution evidence.
	return "operator operation failed"
}

func diagnosticError(existing string, err error) string {
	if err != nil {
		return safeOperatorError(err)
	}
	return safeOperatorString(existing)
}

func safeOperatorString(value string) string {
	if value == "" {
		return ""
	}
	return "operator operation failed"
}

func filteredPostgresURL(dsn string) (string, error) {
	parsed, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	return migrate.FilterCustomQuery(parsed).String(), nil
}

func RenderOperatorReport(w io.Writer, report OperatorReport) error {
	if w == nil {
		return errors.New("operator report writer is required")
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

func OperatorExitCode(report OperatorReport, err error) int {
	if report.Mode == OperatorDiagnostic {
		if err != nil || report.Error != "" {
			return 2
		}
		return 0
	}
	if report.CleanupError != "" {
		return 13
	}
	if err != nil && report.Outcome == "" {
		return 2
	}
	switch report.Outcome {
	case ExecutionCommittedAndVerified:
		if err != nil {
			return 13
		}
		return 0
	case ExecutionNotCommitted:
		return 10
	case ExecutionCommitOutcomeUnknown:
		return 11
	case ExecutionCommittedPostVerifyFailed:
		return 12
	default:
		return 2
	}
}

func (r OperatorReport) String() string {
	var b []byte
	b, _ = json.Marshal(r)
	return string(b)
}
