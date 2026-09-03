// Command d1l-bootstrap is the sole explicit D1-L bootstrap entrypoint.
// The opaque authorization presentation is read from inherited file descriptor
// 3; it is never accepted in argv, environment, logs, or SQL.
package main

import (
	"context"
	"encoding/hex"
	"flag"
	"log"
	"os"
	"strings"

	"power-iot-backend/internal/data/private_migrations"
)

func main() {
	dsn := flag.String("database-url", strings.TrimSpace(os.Getenv("DATABASE_URL")), "PostgreSQL connection URL")
	target := flag.String("target-fingerprint", "", "lower-case hexadecimal target fingerprint")
	evidence := flag.String("evidence-digest", "", "lower-case hexadecimal evidence digest")
	operation := flag.String("operation-id", "", "operation UUID")
	attempt := flag.String("attempt-id", "", "attempt UUID")
	authorization := flag.String("authorization-id", "", "authorization UUID")
	flag.Parse()
	if strings.TrimSpace(*dsn) == "" || *target == "" || *evidence == "" || *operation == "" || *attempt == "" || *authorization == "" {
		log.Fatal("database, digest, operation, attempt, and authorization bindings are required")
	}
	targetBytes, err := hex.DecodeString(*target)
	if err != nil || len(targetBytes) != 32 {
		log.Fatal("target-fingerprint must be 32-byte lower-case hexadecimal")
	}
	evidenceBytes, err := hex.DecodeString(*evidence)
	if err != nil || len(evidenceBytes) != 32 {
		log.Fatal("evidence-digest must be 32-byte lower-case hexadecimal")
	}
	// Validate the dedicated result endpoint before constructing the Provider or
	// starting bootstrap. FD 4 is the selected protected local controller
	// channel; FD 3 remains the H7-A protected presentation input.
	resultSink, err := openD1LResultSink()
	if err != nil {
		log.Fatal(err)
	}
	defer resultSink.Close()

	providerConfig, err := migrations.LoadD1LAuthorizationClientConfig(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	provider, err := migrations.NewD1LAuthorizationClient(providerConfig)
	if err != nil {
		log.Fatal(err)
	}
	presentation := os.NewFile(uintptr(d1lProtectedPresentationFD), "d1l-protected-presentation")
	if presentation == nil {
		log.Fatal("protected presentation fd 3 is required")
	}
	defer presentation.Close()
	_, bootstrapErr, deliveryErr := runD1LBootstrapAndDeliver(func() (migrations.D1LBootstrapReport, error) {
		return migrations.D1LBootstrapAndUpgrade(context.Background(), migrations.D1LBootstrapConfig{DatabaseURL: *dsn, TargetFingerprint: targetBytes, EvidenceDigest: evidenceBytes, OperationID: *operation, AttemptID: *attempt, AuthorizationID: *authorization, Envelope: presentation, Provider: provider})
	}, resultSink)
	if deliveryErr != nil {
		// Delivery truth is deliberately separate from operation truth. A failed
		// write never retries Consume, DDL, COMMIT, or bootstrap.
		log.Printf("D1-L result delivery failed: %v", deliveryErr)
		if bootstrapErr == nil {
			log.Fatal(deliveryErr)
		}
	}
	if bootstrapErr != nil {
		log.Fatal(bootstrapErr)
	}
}
