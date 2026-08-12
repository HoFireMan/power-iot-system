// Command security-reconcile provides a diagnostic-by-default operator
// surface. Mutation requires the explicit -execute flag.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"power-iot-backend/internal/data/reconciliation"
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

	executor := reconciliation.NewProtectedExecutor(nil)
	var execution reconciliation.ExecutionReport
	var err error
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
