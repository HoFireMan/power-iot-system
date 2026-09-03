// Command d6-migrate is the D6 protected V5->V6 operator entry. Rehearsal
// mode remains explicitly isolated; production mode requires an exact target,
// an explicit execute flag, and a trusted inherited admission pipe. It never
// calls generic migrations.Up.
package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
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
	flags := flag.NewFlagSet("d6-migrate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	databaseURL := flags.String("database-url", strings.TrimSpace(os.Getenv("DATABASE_URL")), "application PostgreSQL URL")
	evidencePath := flags.String("drain-evidence", "", "rehearsal-only drain evidence JSON path")
	execute := flags.Bool("execute", false, "invoke the protected D5 entry")
	rehearsal := flags.Bool("rehearsal", false, "constrain this command to an isolated rehearsal")
	production := flags.Bool("production", false, "enable the explicit production operator entry")
	target := flags.String("target", "", "exact execution target; production requires tcrfid01")
	admissionFD := flags.Int("trusted-drain-admission-fd", -1, "inherited trusted operator admission pipe")
	publicKeyFile := flags.String("admission-public-key", "", "trusted Ed25519 admission verification key")
	targetIdentityFile := flags.String("target-identity-file", "", "container-visible host-managed target identity file")
	if err := flags.Parse(args); err != nil || len(flags.Args()) != 0 {
		return 2
	}
	if *rehearsal == *production {
		_, _ = fmt.Fprintln(stderr, "exactly one of -rehearsal or -production is required")
		return 2
	}
	if *production && *target != "tcrfid01" {
		_, _ = fmt.Fprintln(stderr, "production requires exact target tcrfid01")
		return 2
	}
	if *rehearsal && *target != "" && *target != "rehearsal" {
		_, _ = fmt.Fprintln(stderr, "rehearsal target must be rehearsal")
		return 2
	}
	if !*execute {
		if *production {
			_, _ = fmt.Fprintln(stdout, "D6 production operator ready; no migration executed")
		} else {
			_, _ = fmt.Fprintln(stdout, "D6 rehearsal operator ready; no migration executed")
		}
		return 0
	}
	if strings.TrimSpace(*databaseURL) == "" {
		_, _ = fmt.Fprintln(stderr, "-database-url is required with -execute")
		return 2
	}
	if err := verifyTargetIdentity(*targetIdentityFile, *production, *target); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}

	var check migrations.TrustedDrainAdmissionCheck
	if *admissionFD >= 0 {
		publicKey, keyErr := loadAdmissionPublicKey(*publicKeyFile)
		if keyErr != nil {
			_, _ = fmt.Fprintln(stderr, keyErr)
			return 2
		}
		file := os.NewFile(uintptr(*admissionFD), "trusted-drain-admission")
		if file == nil {
			_, _ = fmt.Fprintln(stderr, "trusted drain admission fd is unavailable")
			return 2
		}
		defer file.Close()
		check = trustedPipeAdmission(file, *target, publicKey)
	} else if *rehearsal {
		if strings.TrimSpace(*evidencePath) == "" {
			_, _ = fmt.Fprintln(stderr, "-drain-evidence or -trusted-drain-admission-fd is required with -rehearsal -execute")
			return 2
		}
		evidence, err := loadDrainEvidence(*evidencePath)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "drain evidence rejected")
			return 2
		}
		check = rehearsalDrainAdmission(evidence)
	} else {
		_, _ = fmt.Fprintln(stderr, "-trusted-drain-admission-fd is required with -production -execute")
		return 2
	}

	report, err := migrations.RunD6ProtectedMigrationOperator(context.Background(), *databaseURL, check)
	encoded, marshalErr := json.MarshalIndent(report, "", "  ")
	if marshalErr == nil {
		_, _ = fmt.Fprintln(stdout, string(encoded))
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "protected D5 migration failed:", err)
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

// trustedPipeAdmission is deliberately an inherited-FD seam rather than a
// user-selected JSON file. A target-specific operator process owns the pipe,
// performs the real drain/inspection sequence, and writes exactly one bounded
// admission line only after quiescence succeeds.
func trustedPipeAdmission(reader io.Reader, target string, publicKey ed25519.PublicKey) migrations.TrustedDrainAdmissionCheck {
	return func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, err := bufio.NewReader(reader).ReadString('\n')
		if err != nil {
			return fmt.Errorf("trusted drain admission unavailable: %w", err)
		}
		prefix := "D6-DRAIN-ADMISSION-V2 target=" + target + " result=PASS sig="
		if !strings.HasPrefix(line, prefix) {
			return errors.New("trusted drain admission did not prove the exact target")
		}
		signature, err := base64.RawStdEncoding.DecodeString(strings.TrimSuffix(strings.TrimPrefix(line, prefix), "\n"))
		if err != nil || !ed25519.Verify(publicKey, []byte("D6-DRAIN-ADMISSION-V2 target="+target+" result=PASS\n"), signature) {
			return errors.New("trusted drain admission signature verification failed")
		}
		return nil
	}
}

func verifyTargetIdentity(path string, production bool, target string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("-target-identity-file is required with -execute")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("target identity load failed: %w", err)
	}
	mode := "rehearsal"
	if production {
		mode = "production"
	}
	expected := "target=" + target + "\nrole=power-iot-a3-" + mode + "-operator\n"
	if string(data) != expected {
		return errors.New("target identity verification failed")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&022 != 0 {
		return errors.New("target identity verification failed: non-writable regular file required")
	}
	return nil
}

func loadAdmissionPublicKey(path string) (ed25519.PublicKey, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("-admission-public-key is required with trusted admission")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("admission public key load failed: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("admission public key is not PEM")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("admission public key parse failed: %w", err)
	}
	publicKey, ok := key.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("admission public key is not Ed25519")
	}
	return publicKey, nil
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
