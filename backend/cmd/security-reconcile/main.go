// Command security-reconcile provides a diagnostic-by-default operator
// surface. Mutation requires the explicit -execute flag.
package main

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"power-iot-backend/internal/data/private_migrations"
	"power-iot-backend/internal/data/reconciliation"
	"power-iot-backend/internal/data/reconciliation/sourceowner"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

const maxMappingArtifactBytes = 1 << 20

func invalidExecuteArgument(args []string) bool {
	invalid := false
	for _, arg := range args {
		for _, prefix := range []string{"-execute=", "--execute="} {
			if strings.HasPrefix(arg, prefix) {
				value := strings.TrimSpace(strings.TrimPrefix(arg, prefix))
				if value != "true" && value != "false" {
					invalid = true
				}
			}
		}
	}
	return invalid
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if invalidExecuteArgument(args) {
		_, _ = fmt.Fprintln(stderr, "invalid -execute value")
		return 2
	}
	flags := flag.NewFlagSet("security-reconcile", flag.ContinueOnError)
	flags.SetOutput(stderr)
	databaseURLFlag := flags.String("database-url", "", "PostgreSQL connection URL (defaults to DATABASE_URL)")
	mappingFile := flags.String("mapping-file", "", "strict v5 mapping artifact JSON file; stale artifacts fail closed")
	execute := flags.Bool("execute", false, "explicitly execute protected reconciliation; default is read-only diagnostic")
	d1OperationID := flags.String("d1-operation-id", "", "D1 lease lookup operation UUID (required with -execute)")
	d1AttemptID := flags.String("d1-attempt-id", "", "D1 lease lookup attempt UUID (required with -execute)")
	d1LeaseID := flags.String("d1-lease-id", "", "D1 lease lookup UUID (required with -execute)")
	d1Generation := flags.Int64("d1-generation", 0, "D1 lease lookup generation (required with -execute)")
	d1TargetFingerprint := flags.String("d1-target-fingerprint", "", "D1 lease lookup target fingerprint, 64 hex characters (required with -execute)")
	d1EvidenceDigest := flags.String("d1-evidence-digest", "", "D1 lease lookup digest, 64 hex characters (required with -execute)")
	targetID := flags.Uint("target-id", 0, "protected target device ID (required with -execute)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if len(flags.Args()) != 0 {
		_, _ = fmt.Fprintln(stderr, "unexpected positional arguments")
		return 2
	}
	databaseURL := strings.TrimSpace(*databaseURLFlag)
	if databaseURL == "" {
		databaseURL = strings.TrimSpace(os.Getenv("DATABASE_URL"))
	}
	if databaseURL == "" {
		_, _ = fmt.Fprintln(stderr, "DATABASE_URL or -database-url is required")
		return 2
	}

	var mapping *reconciliation.MappingArtifact
	if strings.TrimSpace(*mappingFile) != "" {
		var err error
		mapping, err = loadMappingArtifact(*mappingFile)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "failed to read mapping artifact")
			return 2
		}
	}

	var leaseIdentity migrations.D1LLeaseIdentity
	if *execute {
		var err error
		leaseIdentity, err = parseD1LeaseIdentity(*d1OperationID, *d1AttemptID, *d1LeaseID, *d1Generation, *d1TargetFingerprint, *d1EvidenceDigest)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "invalid D1 lease identity")
			return 2
		}
		if *targetID == 0 {
			_, _ = fmt.Fprintln(stderr, "target-id is required")
			return 2
		}
	}

	ctx := context.Background()
	if !*execute {
		diagnostic, err := reconciliation.DiagnoseV5(ctx, databaseURL, mapping)
		report := reconciliation.OperatorReportFromDiagnostic(diagnostic, err)
		if renderErr := reconciliation.RenderOperatorReport(stdout, report); renderErr != nil {
			_, _ = fmt.Fprintf(stderr, "render operator report: %v\n", renderErr)
			return 2
		}
		return reconciliation.OperatorExitCode(report, err)
	}

	d1DB, err := sql.Open("postgres", databaseURL)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "failed to open D1 ledger database")
		return 2
	}
	defer d1DB.Close()
	ledger, err := migrations.NewD1LProvenanceLedger(d1DB, time.Hour)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "failed to construct D1 ledger consumer")
		return 2
	}
	// D1 is an owner-mediated authority transaction. It is intentionally kept
	// distinct from the later D3 writer-fence session; the executor only
	// consumes this exact owner-issued lease after canonical admission.
	d1Owner, err := migrations.NewD1LOwnerService(ledger)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "failed to construct D1 owner")
		return 2
	}

	// Inspect the owner using the CLI identity flags only as a lookup key. The
	// returned identity is authoritative; caller-provided generation, digests,
	// and lifecycle fields never become execution or admission authority.
	inspection, err := d1Owner.Inspect(ctx, leaseIdentity)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "D1 lease admission inspection unavailable")
		return 2
	}
	ownerIdentity := inspection.Identity
	issued := migrations.D1LLeaseIssueResult{
		Identity: ownerIdentity, TargetFingerprint: append([]byte(nil), inspection.TargetFingerprint...),
		EvidenceDigest: append([]byte(nil), inspection.EvidenceDigest...), Status: inspection.Status,
		IssuedAt: inspection.IssuedAt, ExpiresAt: inspection.ExpiresAt, ActivatedAt: inspection.ActivatedAt,
	}

	// Collect fresh source evidence B independently through the owner-clock API;
	// it is never serialized, transported, or reused as D1 issuance evidence A.
	// The temporary GORM pool is closed before D3 starts; no source-owner handle
	// or transaction crosses the admission boundary.
	sourceDB, err := gorm.Open(postgres.Open(databaseURL), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "failed to open source owner database")
		return 2
	}
	sourceSQLDB, err := sourceDB.DB()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "failed to open source owner database")
		return 2
	}
	closeSourceDB := func() {
		_ = sourceSQLDB.Close()
	}
	binding := sourceowner.NewInvocationBinding(ownerIdentity.OperationID, ownerIdentity.AttemptID)
	source, err := sourceowner.NewPostgresSourceOwner(sourceDB).CollectTrustedV5(ctx, binding)
	closeSourceDB()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "source-owner admission evidence unavailable")
		return 2
	}
	eligibility, err := reconciliation.NewProtectedD1Eligibility(ctx, d1Owner, issued)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "D1 lease is not eligible for protected admission")
		return 2
	}
	admission := reconciliation.NewCanonicalProtectedAdmission()
	admissionResult := admission.Admit(reconciliation.ProtectedAdmissionContext{
		Route: reconciliation.ProtectedRouteCanonical, OperationID: ownerIdentity.OperationID,
		AttemptID: ownerIdentity.AttemptID, TargetID: uint(*targetID),
		// The complete binding comes from the exact D1 owner inspection. Admit
		// derives the authority generation from that inspection as well.
		Generation: uint64(ownerIdentity.Generation), ObservedAt: source.ObservedAt(),
		FreshUntil: source.FreshUntil(), Source: source, Eligibility: eligibility,
		CallerAuthorized: false,
	}, time.Now().UTC())
	if admissionResult.Status != reconciliation.AdmissionAllowed {
		_, _ = fmt.Fprintln(stderr, "canonical protected admission denied")
		return 2
	}
	// Execution consumes the identity returned by D1 owner inspection, never the
	// lookup/correlation flags supplied by the caller.
	executor := reconciliation.NewProtectedExecutorWithD1(nil, d1Owner, ownerIdentity)
	var execution reconciliation.ExecutionReport
	if mapping == nil {
		execution, err = executor.Execute(ctx, databaseURL, nil)
	} else {
		// The artifact remains bound to its recorded source digest. The
		// protected executor deliberately rejects it if fresh facts differ;
		// diagnostic evidence never authorizes rebinding or a blind retry.
		execution, err = executor.ExecuteWithMappingResolver(ctx, databaseURL, func(context.Context, reconciliation.FactSet) (*reconciliation.MappingArtifact, error) {
			return mapping, nil
		})
	}
	report := reconciliation.OperatorReportFromExecution(execution, err)
	if renderErr := reconciliation.RenderOperatorReport(stdout, report); renderErr != nil {
		_, _ = fmt.Fprintf(stderr, "render operator report: %v\n", renderErr)
		return 2
	}
	return reconciliation.OperatorExitCode(report, err)
}

func parseD1LeaseIdentity(operationID, attemptID, leaseID string, generation int64, targetFingerprint, evidenceDigest string) (migrations.D1LLeaseIdentity, error) {
	if generation <= 0 || strings.TrimSpace(operationID) == "" || strings.TrimSpace(attemptID) == "" || strings.TrimSpace(leaseID) == "" {
		return migrations.D1LLeaseIdentity{}, errors.New("incomplete D1 lease identity")
	}
	operation, err := uuid.Parse(strings.TrimSpace(operationID))
	if err != nil || operation == uuid.Nil {
		return migrations.D1LLeaseIdentity{}, errors.New("invalid D1 operation identity")
	}
	attempt, err := uuid.Parse(strings.TrimSpace(attemptID))
	if err != nil || attempt == uuid.Nil {
		return migrations.D1LLeaseIdentity{}, errors.New("invalid D1 attempt identity")
	}
	lease, err := uuid.Parse(strings.TrimSpace(leaseID))
	if err != nil || lease == uuid.Nil {
		return migrations.D1LLeaseIdentity{}, errors.New("invalid D1 lease identity")
	}
	decodeDigest := func(value string) ([]byte, error) {
		decoded, err := hex.DecodeString(strings.TrimSpace(value))
		if err != nil || len(decoded) != 32 {
			return nil, errors.New("invalid D1 digest")
		}
		return decoded, nil
	}
	target, err := decodeDigest(targetFingerprint)
	if err != nil {
		return migrations.D1LLeaseIdentity{}, err
	}
	evidence, err := decodeDigest(evidenceDigest)
	if err != nil {
		return migrations.D1LLeaseIdentity{}, err
	}
	return migrations.D1LLeaseIdentity{
		LeaseID: lease, OperationID: operation, AttemptID: attempt, Generation: generation,
		TargetFingerprint: target, EvidenceDigest: evidence,
	}, nil
}

func loadMappingArtifact(path string) (*reconciliation.MappingArtifact, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxMappingArtifactBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxMappingArtifactBytes {
		return nil, errors.New("mapping artifact exceeds 1 MiB limit")
	}
	artifact, err := reconciliation.ParseMappingArtifact(data)
	if err != nil {
		return nil, err
	}
	return &artifact, nil
}
