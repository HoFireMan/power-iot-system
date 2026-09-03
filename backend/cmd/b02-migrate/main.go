// Command b02-migrate is the LOCAL/REHEARSAL-only B-02 protected migration
// operator. It has no production target or production execution path.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"power-iot-backend/internal/data/private_migrations"
)

type drainEvidence struct {
	Schema                int    `json:"schema"`
	Target                string `json:"target"`
	HTTPWritesBlocked     bool   `json:"http_writes_blocked"`
	MQTTIngestionBlocked  bool   `json:"mqtt_ingestion_blocked"`
	RestartSuppressed     bool   `json:"restart_suppressed"`
	DirectSQLControlled   bool   `json:"direct_sql_controlled"`
	InFlightWritesDrained bool   `json:"in_flight_writes_drained"`
	QuiescenceProven      bool   `json:"quiescence_proven"`
	ObservedAt            string `json:"observed_at"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("b02-migrate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	databaseURL := flags.String("database-url", strings.TrimSpace(os.Getenv("DATABASE_URL")), "application PostgreSQL URL")
	evidencePath := flags.String("drain-evidence", "", "rehearsal-only drain evidence JSON path")
	execute := flags.Bool("execute", false, "invoke the protected B-02 entry")
	rehearsal := flags.Bool("rehearsal", false, "constrain this command to an isolated rehearsal")
	targetIdentityFile := flags.String("target-identity-file", "", "container-visible host-managed target identity file")
	if err := flags.Parse(args); err != nil || len(flags.Args()) != 0 {
		return 2
	}
	if !*rehearsal {
		_, _ = fmt.Fprintln(stderr, "-rehearsal is required; b02-migrate has no production mode")
		return 2
	}
	if !*execute {
		_, _ = fmt.Fprintln(stdout, "B-02 rehearsal operator ready; no migration executed")
		return 0
	}
	if strings.TrimSpace(*databaseURL) == "" {
		_, _ = fmt.Fprintln(stderr, "-database-url is required with -execute")
		return 2
	}
	if err := validateRehearsalDatabaseURL(*databaseURL); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	if err := verifyTargetIdentity(*targetIdentityFile); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	if strings.TrimSpace(*evidencePath) == "" {
		_, _ = fmt.Fprintln(stderr, "-drain-evidence is required with -rehearsal -execute")
		return 2
	}
	evidence, err := loadDrainEvidence(*evidencePath)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "drain evidence rejected")
		return 2
	}

	check := rehearsalDrainAdmission(evidence)
	report, err := migrations.RunB02ProtectedMigrationOperator(context.Background(), *databaseURL, check)
	encoded, marshalErr := json.MarshalIndent(report, "", "  ")
	if marshalErr == nil {
		_, _ = fmt.Fprintln(stdout, string(encoded))
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "protected B-02 migration failed:", err)
		return 1
	}
	return 0
}

func rehearsalDrainAdmission(evidence drainEvidence) migrations.TrustedDrainAdmissionCheck {
	return func(context.Context) error {
		if evidence.Target != "rehearsal" {
			return errors.New("drain evidence target is not rehearsal")
		}
		if !evidence.HTTPWritesBlocked || !evidence.MQTTIngestionBlocked || !evidence.RestartSuppressed || !evidence.DirectSQLControlled || !evidence.InFlightWritesDrained || !evidence.QuiescenceProven {
			return errors.New("drain evidence is incomplete")
		}
		return nil
	}
}

func validateRehearsalDatabaseURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		return errors.New("rehearsal database URL must use PostgreSQL")
	}
	if parsed.Hostname() != "127.0.0.1" {
		return errors.New("rehearsal database URL must target local 127.0.0.1")
	}
	port := parsed.Port()
	if port == "" {
		return errors.New("rehearsal database URL must specify a local port")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return errors.New("rehearsal database URL must specify a valid local port")
	}
	if portNumber == 5432 {
		return errors.New("rehearsal database URL must not use the legacy PostgreSQL port")
	}
	for key := range parsed.Query() {
		switch strings.ToLower(key) {
		case "host", "hostaddr", "port", "dbname", "service", "x-migrations-table", "x-migrations-table-quoted":
			return errors.New("rehearsal database URL must not contain PostgreSQL connection overrides")
		}
	}
	return nil
}

func verifyTargetIdentity(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("-target-identity-file is required with -execute")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("target identity load failed: %w", err)
	}
	const expected = "target=rehearsal\nrole=power-iot-a3-rehearsal-operator\n"
	if string(data) != expected {
		return errors.New("target identity verification failed")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&022 != 0 {
		return errors.New("target identity verification failed: non-writable regular file required")
	}
	return nil
}

func loadDrainEvidence(path string) (drainEvidence, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return drainEvidence{}, err
	}
	var evidence drainEvidence
	if err := json.Unmarshal(data, &evidence); err != nil {
		return drainEvidence{}, err
	}
	if evidence.Schema != 1 || evidence.Target != "rehearsal" {
		return drainEvidence{}, errors.New("unsupported drain evidence")
	}
	observed, err := time.Parse(time.RFC3339, evidence.ObservedAt)
	if err != nil || observed.IsZero() {
		return drainEvidence{}, errors.New("drain evidence timestamp is invalid")
	}
	return evidence, nil
}
