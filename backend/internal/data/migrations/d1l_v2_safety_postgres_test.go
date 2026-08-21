package migrations

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestD1LV2ClosedWorldBindingsAndCodesRejectContradictionsPostgres(t *testing.T) {
	dsn, _ := prepareD1LProvenanceDatabase(t)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	insert := func(provenanceID, operationID, attemptID, issueID uuid.UUID, target []byte, route, state, terminal string, created, reserved, resolved time.Time) error {
		var issue any = issueID
		if state == "AVAILABLE" {
			issue = nil
		}
		_, err := db.ExecContext(ctx, `INSERT INTO security_control.admission_provenance(provenance_id,provenance_version,owner_identity,owner_version,operation_id,attempt_id,target_fingerprint,evidence_digest,route_intent,state,issue_id,created_at,reserved_at,resolved_at,terminal_code) VALUES($1,1,'trusted-post-d1l-upstream','owner-v1',$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, provenanceID, operationID, attemptID, target, []byte(strings.Repeat("e", 32)), route, state, issue, created, nullableTime(reserved), nullableTime(resolved), nullableString(terminal))
		return err
	}
	now := time.Now().UTC()
	var zeroComparison bool
	if err := db.QueryRowContext(ctx, `SELECT decode(repeat('00',64),'hex') <> decode(repeat('00',64),'hex')`).Scan(&zeroComparison); err != nil {
		t.Fatal(err)
	}
	if zeroComparison {
		t.Fatal("PostgreSQL zero comparison unexpectedly true")
	}
	validTarget := []byte(strings.Repeat("t", 32))
	if err := insert(uuid.New(), uuid.New(), uuid.New(), uuid.New(), make([]byte, 32), "D1_ISSUE", "AVAILABLE", "", now, time.Time{}, time.Time{}); err == nil {
		t.Fatal("zero target fingerprint was accepted")
	}
	if err := insert(uuid.New(), uuid.New(), uuid.New(), uuid.New(), validTarget, "PROVIDER", "AVAILABLE", "", now, time.Time{}, time.Time{}); err == nil {
		t.Fatal("unapproved route was accepted")
	}
	if err := insert(uuid.New(), uuid.New(), uuid.New(), uuid.New(), validTarget, "D1_ISSUE", "INVALIDATED", "raw provider error", now, now.Add(time.Second), now.Add(2*time.Second)); err == nil {
		t.Fatal("unapproved terminal code was accepted")
	}
	if err := insert(uuid.New(), uuid.New(), uuid.New(), uuid.New(), validTarget, "D1_ISSUE", "RESERVED", "", now, now.Add(-time.Second), time.Time{}); err == nil {
		t.Fatal("reserved timestamp before created_at was accepted")
	}
	operationID, attemptID := uuid.New(), uuid.New()
	if err := insert(uuid.New(), operationID, attemptID, uuid.New(), validTarget, "D1_ISSUE", "AVAILABLE", "", now, time.Time{}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := insert(uuid.New(), operationID, attemptID, uuid.New(), validTarget, "D1_ISSUE", "AVAILABLE", "", now, time.Time{}, time.Time{}); err == nil {
		t.Fatal("duplicate authoritative attempt identity was accepted")
	}
}

// These tiny adapters keep the INSERT helper's nullable fields explicit while
// allowing the test to exercise PostgreSQL's CHECK constraints directly.
func nullableTime(value time.Time) interface{} {
	if value.IsZero() {
		return nil
	}
	return value
}

func nullableString(value string) interface{} {
	if value == "" {
		return nil
	}
	return value
}
