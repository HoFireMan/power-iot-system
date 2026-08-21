//go:build d4referencepostgres

package reconciliation

// This file is deliberately a reference conformance harness, not a
// production repository. It creates and drops arbitrary test-local objects,
// never invokes migrations.Up, and does not establish D4 table names.

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

var d4ReferenceIdentifier = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

func TestD4ReferencePostgresDurableCASAndRecovery(t *testing.T) {
	dsn := os.Getenv("D4_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("dedicated D4_TEST_POSTGRES_URL is not configured")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	name := "d4_ref_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if !d4ReferenceIdentifier.MatchString(name) {
		t.Fatal("invalid test identifier")
	}
	defer func() { _, _ = db.ExecContext(context.Background(), `DROP TABLE IF EXISTS `+name) }()
	if _, err := db.ExecContext(ctx, `CREATE TABLE `+name+` (operation_id uuid NOT NULL, attempt_id uuid NOT NULL, target bytea NOT NULL, generation bigint NOT NULL, state text NOT NULL, claim_id uuid, recovery text NOT NULL DEFAULT '', safe_result jsonb, PRIMARY KEY (operation_id, attempt_id, target, generation))`); err != nil {
		t.Fatal(err)
	}
	tuple := d4TestTuple(t)
	if _, err := db.ExecContext(ctx, `INSERT INTO `+name+` (operation_id, attempt_id, target, generation, state) VALUES ($1,$2,$3,$4,'RECEIVED')`, tuple.OperationID(), tuple.AttemptID(), tuple.targetFingerprint[:], tuple.Generation()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO `+name+` (operation_id, attempt_id, target, generation, state) VALUES ($1,$2,$3,$4,'RECEIVED') ON CONFLICT (operation_id, attempt_id, target, generation) DO NOTHING`, tuple.OperationID(), tuple.AttemptID(), tuple.targetFingerprint[:], tuple.Generation()); err != nil {
		t.Fatal(err)
	}
	var duplicateCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM `+name+` WHERE operation_id=$1 AND attempt_id=$2 AND target=$3 AND generation=$4`, tuple.OperationID(), tuple.AttemptID(), tuple.targetFingerprint[:], tuple.Generation()).Scan(&duplicateCount); err != nil || duplicateCount != 1 {
		t.Fatalf("duplicate count=%d err=%v", duplicateCount, err)
	}
	const workers = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners, errs := 0, 0
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			worker := uuid.New()
			tx, beginErr := db.BeginTx(ctx, nil)
			if beginErr != nil {
				mu.Lock()
				errs++
				mu.Unlock()
				return
			}
			result, updateErr := tx.ExecContext(ctx, `UPDATE `+name+` SET state='ADMITTED', claim_id=$5 WHERE operation_id=$1 AND attempt_id=$2 AND target=$3 AND generation=$4 AND state='RECEIVED' AND claim_id IS NULL`, tuple.OperationID(), tuple.AttemptID(), tuple.targetFingerprint[:], tuple.Generation(), worker)
			if updateErr == nil {
				affected, rowsErr := result.RowsAffected()
				updateErr = rowsErr
				if updateErr == nil && affected == 1 {
					mu.Lock()
					winners++
					mu.Unlock()
				}
			}
			if updateErr != nil {
				_ = tx.Rollback()
				mu.Lock()
				errs++
				mu.Unlock()
				return
			}
			if commitErr := tx.Commit(); commitErr != nil {
				mu.Lock()
				errs++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if winners != 1 || errs != 0 {
		t.Fatalf("CAS winners=%d errors=%d", winners, errs)
	}
	var winningClaim uuid.UUID
	if err := db.QueryRowContext(ctx, `SELECT claim_id FROM `+name+` WHERE operation_id=$1 AND attempt_id=$2 AND target=$3 AND generation=$4`, tuple.OperationID(), tuple.AttemptID(), tuple.targetFingerprint[:], tuple.Generation()).Scan(&winningClaim); err != nil {
		t.Fatal(err)
	}
	generationMismatch, err := db.ExecContext(ctx, `UPDATE `+name+` SET state='EXECUTING' WHERE operation_id=$1 AND attempt_id=$2 AND target=$3 AND generation=$4 AND state='ADMITTED' AND claim_id=$5`, tuple.OperationID(), tuple.AttemptID(), tuple.targetFingerprint[:], tuple.Generation()+1, winningClaim)
	if err != nil {
		t.Fatal(err)
	}
	if count, _ := generationMismatch.RowsAffected(); count != 0 {
		t.Fatal("generation-mismatched worker changed state")
	}
	claimMismatch, err := db.ExecContext(ctx, `UPDATE `+name+` SET state='EXECUTING' WHERE operation_id=$1 AND attempt_id=$2 AND target=$3 AND generation=$4 AND state='ADMITTED' AND claim_id=$5`, tuple.OperationID(), tuple.AttemptID(), tuple.targetFingerprint[:], tuple.Generation(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if count, _ := claimMismatch.RowsAffected(); count != 0 {
		t.Fatal("stale worker changed state")
	}
	result := `{"operation_id":"` + tuple.OperationID().String() + `","attempt_id":"` + tuple.AttemptID().String() + `","target_fingerprint_digest":"` + tuple.TargetFingerprintHex() + `","generation":7,"disposition":"NON_SUCCESS","commit_status":"COMMIT_UNKNOWN","post_verification_status":"NOT_VERIFIED","cleanup_status":"CONFIRMED","certainty":"UNKNOWN","unknown":true,"recovery_required":true,"recovery_class":"UNKNOWN_COMMIT_OR_CLEANUP","replay_disposition":"NOT_REPLAYABLE"}`
	if _, err := db.ExecContext(ctx, `UPDATE `+name+` SET state='RECOVERY_REQUIRED', recovery='UNKNOWN_COMMIT_OR_CLEANUP', safe_result=$1::jsonb WHERE operation_id=$2 AND attempt_id=$3 AND target=$4 AND generation=$5 AND claim_id=$6 AND state='ADMITTED'`, result, tuple.OperationID(), tuple.AttemptID(), tuple.targetFingerprint[:], tuple.Generation(), winningClaim); err != nil {
		t.Fatal(err)
	}
	restarted, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	var state, recovery string
	var safeResult []byte
	if err := restarted.QueryRowContext(ctx, `SELECT state, recovery, safe_result FROM `+name+` WHERE operation_id=$1 AND attempt_id=$2 AND target=$3 AND generation=$4`, tuple.OperationID(), tuple.AttemptID(), tuple.targetFingerprint[:], tuple.Generation()).Scan(&state, &recovery, &safeResult); err != nil {
		t.Fatal(err)
	}
	if state != "RECOVERY_REQUIRED" || recovery != "UNKNOWN_COMMIT_OR_CLEANUP" {
		t.Fatalf("restart state=%s recovery=%s", state, recovery)
	}
	var decoded D4SafeResult
	if err := json.Unmarshal(safeResult, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := decoded.ValidateFor(tuple); err != nil {
		t.Fatalf("restart safe result validation: %v", err)
	}
	if _, err := restarted.ExecContext(ctx, `UPDATE `+name+` SET state='TERMINAL' WHERE operation_id=$1 AND attempt_id=$2 AND target=$3 AND generation=$4 AND claim_id=$5 AND state='RECOVERY_REQUIRED'`, tuple.OperationID(), tuple.AttemptID(), tuple.targetFingerprint[:], tuple.Generation(), winningClaim); err != nil {
		t.Fatal(err)
	}
	immutable, err := restarted.ExecContext(ctx, `UPDATE `+name+` SET state='RECEIVED' WHERE operation_id=$1 AND attempt_id=$2 AND target=$3 AND generation=$4 AND claim_id=$5 AND state <> 'TERMINAL'`, tuple.OperationID(), tuple.AttemptID(), tuple.targetFingerprint[:], tuple.Generation(), winningClaim)
	if err != nil {
		t.Fatal(err)
	}
	if count, _ := immutable.RowsAffected(); count != 0 {
		t.Fatal("terminal record was reset")
	}
}
