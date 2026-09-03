package migrations

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestD1LT0RollbackLeavesAvailableWithoutIssuePostgres(t *testing.T) {
	dsn, _ := prepareD1LProvenanceDatabase(t)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ledger, err := NewD1LProvenanceLedger(db, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	record, err := ledger.Record(context.Background(), produceD1LPostgresProvenance(t, dsn, uuid.New(), uuid.New()))
	if err != nil {
		t.Fatal(err)
	}
	if record.IssueID != uuid.Nil {
		t.Fatalf("T0 record returned issue identity %s", record.IssueID)
	}
	injected := errors.New("injected T0 rollback")
	if _, err := ledger.reserve(context.Background(), record, func(*sql.Tx) error { return injected }); !errors.Is(err, injected) {
		t.Fatalf("T0 rollback err=%v, want %v", err, injected)
	}
	var state string
	var issue sql.NullString
	if err := db.QueryRow(`SELECT state,issue_id FROM security_control.admission_provenance WHERE provenance_id=$1`, record.ID).Scan(&state, &issue); err != nil {
		t.Fatal(err)
	}
	if state != string(D1LProvenanceAvailable) || issue.Valid {
		t.Fatalf("T0 rollback state=%s issue=%v, want AVAILABLE/NULL", state, issue)
	}
}

func TestD1LConcurrentT0HasOneWinnerAndOneDurableIssuePostgres(t *testing.T) {
	dsn, _ := prepareD1LProvenanceDatabase(t)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ledger, err := NewD1LProvenanceLedger(db, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	record, err := ledger.Record(context.Background(), produceD1LPostgresProvenance(t, dsn, uuid.New(), uuid.New()))
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, reserveErr := ledger.Reserve(context.Background(), record)
			results <- reserveErr
		}()
	}
	wg.Wait()
	close(results)
	var winners, duplicates int
	for reserveErr := range results {
		switch {
		case reserveErr == nil:
			winners++
		case errors.Is(reserveErr, ErrD1LProvenanceDuplicate):
			duplicates++
		default:
			t.Fatalf("concurrent T0 err=%v", reserveErr)
		}
	}
	if winners != 1 || duplicates != 1 {
		t.Fatalf("concurrent T0 winners=%d duplicates=%d, want 1/1", winners, duplicates)
	}
	var state string
	var issue uuid.UUID
	if err := db.QueryRow(`SELECT state,issue_id FROM security_control.admission_provenance WHERE provenance_id=$1`, record.ID).Scan(&state, &issue); err != nil {
		t.Fatal(err)
	}
	if state != string(D1LProvenanceReserved) || issue == uuid.Nil {
		t.Fatalf("concurrent T0 state=%s issue=%s, want RESERVED/non-null", state, issue)
	}
}

func TestD1LT1RollbackLeavesReservedWithoutLeasePostgres(t *testing.T) {
	dsn, _ := prepareD1LProvenanceDatabase(t)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ledger, err := NewD1LProvenanceLedger(db, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	record, err := ledger.Record(context.Background(), produceD1LPostgresProvenance(t, dsn, uuid.New(), uuid.New()))
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := ledger.Reserve(context.Background(), record)
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected T1 rollback")
	if _, err := ledger.completeIssueWithHook(context.Background(), reservation, func(*sql.Tx) error { return injected }); !errors.Is(err, injected) {
		t.Fatalf("T1 rollback err=%v, want %v", err, injected)
	}
	var state string
	var lease sql.NullString
	var generation sql.NullInt64
	if err := db.QueryRow(`SELECT state,lease_id,lease_generation FROM security_control.admission_provenance WHERE provenance_id=$1`, record.ID).Scan(&state, &lease, &generation); err != nil {
		t.Fatal(err)
	}
	if state != string(D1LProvenanceReserved) || lease.Valid || generation.Valid {
		t.Fatalf("T1 rollback state=%s lease=%v generation=%v, want RESERVED/NULL/NULL", state, lease, generation)
	}
	var leases int
	if err := db.QueryRow(`SELECT count(*) FROM security_control.admission_leases WHERE operation_id=$1`, record.OperationID).Scan(&leases); err != nil {
		t.Fatal(err)
	}
	if leases != 0 {
		t.Fatalf("T1 rollback left %d lease rows", leases)
	}
}

func TestD1LRestartResolutionPreservesReservedIssueIdentityPostgres(t *testing.T) {
	dsn, _ := prepareD1LProvenanceDatabase(t)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := NewD1LProvenanceLedger(db, time.Hour)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	record, err := ledger.Record(context.Background(), produceD1LPostgresProvenance(t, dsn, uuid.New(), uuid.New()))
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	reservation, err := ledger.Reserve(context.Background(), record)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	reservedIssue := reservation.Record.IssueID
	if reservedIssue == uuid.Nil {
		db.Close()
		t.Fatal("T0 returned no durable issue identity")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopenedLedger, err := NewD1LProvenanceLedger(reopened, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := reopenedLedger.ResolveExact(context.Background(), reservation.Record)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.State != D1LProvenanceReserved || resolved.IssueID != reservedIssue {
		t.Fatalf("reopened resolution=%+v, want RESERVED/%s", resolved, reservedIssue)
	}
}
