//go:build securityintegration && d5referencepostgres

package reconciliation

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"power-iot-backend/internal/data/private_migrations"
	"power-iot-backend/internal/testsupport"
)

func newD5TestDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	source := os.Getenv("TEST_MIGRATION_DATABASE_URL")
	if source == "" {
		t.Skip("TEST_MIGRATION_DATABASE_URL is required")
	}
	isolated, err := testsupport.New(context.Background(), source, migrations.Up)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", isolated.DSN())
	if err != nil {
		isolated.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close(); _ = isolated.Close() })
	return db, isolated.DSN()
}

func installD5TestSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`DROP TABLE IF EXISTS d4_operation_journal, d4_operation_ledger CASCADE`); err != nil {
		t.Fatal(err)
	}
	body, err := migrations.Files.ReadFile("sql/000006_d4_reconciliation.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(body)); err != nil {
		t.Fatal(err)
	}
}

func TestD5PostgresStoreCASJournalAndClaimInvariant(t *testing.T) {
	db, _ := newD5TestDB(t)
	installD5TestSchema(t, db)
	store := NewPostgresD4Store(db)
	tuple := d4TestTuple(t)
	if _, err := store.Receive(context.Background(), D4ReceiveRequest{Tuple: tuple, ReceivedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	const workers = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	failures := 0
	var firstErr error
	var winner uuid.UUID
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := store.Claim(context.Background(), D4ClaimRequest{Tuple: tuple, Approval: d4Approval(t, tuple, D4EventAdmit), WorkerID: uuid.New()})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failures++
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			if result.Won {
				winners++
				winner = result.ClaimID
			}
		}()
	}
	wg.Wait()
	if winners != 1 || failures != 0 || winner == uuid.Nil {
		t.Fatalf("winners=%d failures=%d winner=%s first=%v", winners, failures, winner, firstErr)
	}
	begin := D4OwnerEvent{Kind: D4EventBeginExecution, Tuple: tuple, Approval: d4Approval(t, tuple, D4EventBeginExecution)}
	if _, err := store.Transition(context.Background(), D4TransitionRequest{Tuple: tuple, Expected: D4Admitted, ClaimID: winner, Event: begin}); err != nil {
		t.Fatal(err)
	}
	result := d4TestResult(tuple)
	record, err := store.Transition(context.Background(), D4TransitionRequest{Tuple: tuple, Expected: D4Executing, ClaimID: winner, Event: D4OwnerEvent{Kind: D4EventTerminal, Tuple: tuple, Approval: d4Approval(t, tuple, D4EventTerminal), Result: &result}})
	if err != nil || record.State != D4Terminal || record.ClaimID != winner {
		t.Fatalf("terminal=%+v err=%v", record, err)
	}
	if _, err := store.Transition(context.Background(), D4TransitionRequest{Tuple: tuple, Expected: D4Terminal, ClaimID: uuid.Nil, Event: D4OwnerEvent{Kind: D4EventTerminal, Tuple: tuple, Approval: d4Approval(t, tuple, D4EventTerminal), Result: &result}}); err == nil {
		t.Fatal("terminal mutation accepted")
	}
}

func TestD5PostgresJournalExactDuplicateAndConflict(t *testing.T) {
	db, _ := newD5TestDB(t)
	installD5TestSchema(t, db)
	store := NewPostgresD4Store(db)
	tuple := d4TestTuple(t)
	if _, err := store.Receive(context.Background(), D4ReceiveRequest{Tuple: tuple, ReceivedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	result := d4TestResult(tuple)
	event := D4JournalEvent{EventID: uuid.New(), Version: 1, Tuple: tuple, From: D4ResultRecorded, To: D4Terminal, Result: &result, OccurredAt: time.Now().UTC()}
	if err := store.Append(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	conflict := event
	conflict.Correlation = "conflicting-safe-correlation"
	if err := store.Append(context.Background(), conflict); err == nil {
		t.Fatal("conflicting duplicate accepted")
	}
}

func TestD5PostgresReplayRejectsUnverifiedPayload(t *testing.T) {
	db, _ := newD5TestDB(t)
	installD5TestSchema(t, db)
	tuple := d4TestTuple(t)
	if _, err := db.Exec(`INSERT INTO d4_operation_ledger (operation_id,attempt_id,target_fingerprint,generation,state,updated_at) VALUES ($1,$2,$3,$4,'RECEIVED',now())`, tuple.OperationID(), tuple.AttemptID(), targetBytes(tuple), tuple.Generation()); err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{}`)
	digest := sha256.Sum256(payload)
	if _, err := db.Exec(`INSERT INTO d4_operation_journal (event_id,event_version,operation_id,attempt_id,target_fingerprint,generation,from_state,to_state,safe_payload,payload_digest,occurred_at) VALUES ($1,1,$2,$3,$4,$5,'RECEIVED','ADMITTED',$6::jsonb,$7,now())`, uuid.New(), tuple.OperationID(), tuple.AttemptID(), targetBytes(tuple), tuple.Generation(), payload, digest[:]); err != nil {
		t.Fatal(err)
	}
	if err := NewPostgresD4Store(db).Replay(context.Background(), func(D4JournalEvent) error { return nil }); err == nil {
		t.Fatal("replay accepted a payload without a valid event identity")
	}
}

func TestD5PostgresBundleCorrelationCASAndConflict(t *testing.T) {
	db, _ := newD5TestDB(t)
	installD5TestSchema(t, db)
	store := NewPostgresD4Store(db)
	tuple := d4TestTuple(t)
	correlation := D4SafeCorrelation{FactsDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ProofDigest: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", PredicateIdentity: "predicate-v1", PredicateVersion: "1", ProvenanceDigest: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}
	record := D4Record{Tuple: tuple, State: D4Received, UpdatedAt: time.Now().UTC()}
	first, err := NewD4ToD5BundleV2(record, correlation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcceptD4ToD5Bundle(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcceptD4ToD5Bundle(context.Background(), first); err != nil {
		t.Fatalf("exact bundle duplicate rejected: %v", err)
	}
	conflictingCorrelation := correlation
	conflictingCorrelation.PredicateVersion = "2"
	conflicting, err := NewD4ToD5BundleV2(record, conflictingCorrelation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcceptD4ToD5Bundle(context.Background(), conflicting); err == nil {
		t.Fatal("conflicting bundle correlation accepted")
	}
	result := d4TestResult(tuple)
	result.Disposition = D4NonSuccess
	result.CommitStatus = D4CommitNotCommitted
	result.PostVerificationStatus = D4PostNotVerified
	result.ReplayDisposition = D4RetryOwnerOnly
	terminal, err := store.Transition(context.Background(), D4TransitionRequest{Tuple: tuple, Expected: D4Received, Event: D4OwnerEvent{Kind: D4EventTerminal, Tuple: tuple, Approval: d4Approval(t, tuple, D4EventTerminal), Result: &result}})
	if err != nil {
		t.Fatal(err)
	}
	terminalBundle, err := NewD4ToD5BundleV2(terminal, conflictingCorrelation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcceptD4ToD5Bundle(context.Background(), terminalBundle); err == nil {
		t.Fatal("terminal bundle replaced established correlation")
	}
}

func TestD5PostgresConcurrentBundleCorrelationHasOneWinner(t *testing.T) {
	db, _ := newD5TestDB(t)
	installD5TestSchema(t, db)
	store := NewPostgresD4Store(db)
	tuple := d4TestTuple(t)
	record := D4Record{Tuple: tuple, State: D4Received, UpdatedAt: time.Now().UTC()}
	correlations := []D4SafeCorrelation{
		{FactsDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ProofDigest: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", PredicateIdentity: "predicate-v1", PredicateVersion: "1", ProvenanceDigest: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},
		{FactsDigest: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd", ProofDigest: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", PredicateIdentity: "predicate-v1", PredicateVersion: "2", ProvenanceDigest: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"},
	}
	bundles := make([]D4ToD5Bundle, len(correlations))
	for i := range correlations {
		var err error
		bundles[i], err = NewD4ToD5BundleV2(record, correlations[i])
		if err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	results := make(chan error, len(bundles))
	for _, bundle := range bundles {
		wg.Add(1)
		go func(bundle D4ToD5Bundle) {
			defer wg.Done()
			_, err := store.AcceptD4ToD5Bundle(context.Background(), bundle)
			results <- err
		}(bundle)
	}
	wg.Wait()
	close(results)
	var successes, failures int
	for err := range results {
		if err == nil {
			successes++
		} else {
			failures++
		}
	}
	if successes != 1 || failures != 1 {
		t.Fatalf("bundle correlation winners=%d failures=%d", successes, failures)
	}
}

func TestD5PostgresTransitionRollbackAndRestartDurability(t *testing.T) {
	db, dsn := newD5TestDB(t)
	installD5TestSchema(t, db)
	store := NewPostgresD4Store(db)
	tuple := d4TestTuple(t)
	if _, err := store.Receive(context.Background(), D4ReceiveRequest{Tuple: tuple, ReceivedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(context.Background(), D4ClaimRequest{Tuple: tuple, Approval: d4Approval(t, tuple, D4EventAdmit), WorkerID: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	begin := D4OwnerEvent{Kind: D4EventBeginExecution, Tuple: tuple, Approval: d4Approval(t, tuple, D4EventBeginExecution)}
	if _, err := store.Transition(context.Background(), D4TransitionRequest{Tuple: tuple, Expected: D4Admitted, ClaimID: claim.ClaimID, Event: begin}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE OR REPLACE FUNCTION d5_test_reject_journal() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test journal rejection'; END; $$; CREATE TRIGGER d5_test_reject_journal BEFORE INSERT ON d4_operation_journal FOR EACH ROW EXECUTE FUNCTION d5_test_reject_journal()`); err != nil {
		t.Fatal(err)
	}
	result := d4TestResult(tuple)
	if _, err := store.Transition(context.Background(), D4TransitionRequest{Tuple: tuple, Expected: D4Executing, ClaimID: claim.ClaimID, Event: D4OwnerEvent{Kind: D4EventTerminal, Tuple: tuple, Approval: d4Approval(t, tuple, D4EventTerminal), Result: &result}}); err == nil {
		t.Fatal("journal failure did not fail transition")
	}
	if _, err := db.Exec(`DROP TRIGGER d5_test_reject_journal ON d4_operation_journal; DROP FUNCTION d5_test_reject_journal()`); err != nil {
		t.Fatal(err)
	}
	record, err := store.Get(context.Background(), tuple)
	if err != nil || record.State != D4Executing {
		t.Fatalf("rollback record=%+v err=%v", record, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	persisted, err := NewPostgresD4Store(reopened).Get(context.Background(), tuple)
	if err != nil || persisted.State != D4Executing || persisted.ClaimID != claim.ClaimID {
		t.Fatalf("reopen record=%+v err=%v", persisted, err)
	}
}

func TestD5PostgresServiceDuplicateDeliveryInvokesD3Once(t *testing.T) {
	db, _ := newD5TestDB(t)
	installD5TestSchema(t, db)
	store := NewPostgresD4Store(db)
	tuple := d4TestTuple(t)
	invoker := &d4ServiceInvoker{results: []D4CompositeResult{{Safe: d4TestResult(tuple)}}}
	service := NewD4Service(store, d4TestAuthorizer(), invoker)
	admission := d4AdmitEvent(t, tuple)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = service.Process(context.Background(), admission) }()
	}
	wg.Wait()
	if invoker.Calls() != 1 {
		t.Fatalf("D3 calls=%d", invoker.Calls())
	}
	record, err := store.Get(context.Background(), tuple)
	if err != nil || record.State != D4Terminal || record.ClaimID == uuid.Nil {
		t.Fatalf("record=%+v err=%v", record, err)
	}
}

func TestD5PostgresStoreRejectsStaleGenerationAndAllowsPreClaimRecovery(t *testing.T) {
	db, _ := newD5TestDB(t)
	installD5TestSchema(t, db)
	store := NewPostgresD4Store(db)
	tuple := d4TestTuple(t)
	if _, err := store.Receive(context.Background(), D4ReceiveRequest{Tuple: tuple, ReceivedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	wrong := tuple
	wrong.generation++
	if _, err := store.Get(context.Background(), wrong); err == nil {
		t.Fatal("wrong generation read accepted")
	}
	record, err := store.Transition(context.Background(), D4TransitionRequest{Tuple: tuple, Expected: D4Received, Event: D4OwnerEvent{Kind: D4EventRequireRecovery, Tuple: tuple, Approval: d4Approval(t, tuple, D4EventRequireRecovery), Recovery: D4RecoveryUnknown}})
	if err != nil || record.State != D4RecoveryRequired || record.ClaimID != uuid.Nil {
		t.Fatalf("record=%+v err=%v", record, err)
	}
}

func TestD5PostgresStoreAllowsPreClaimDispositions(t *testing.T) {
	db, _ := newD5TestDB(t)
	installD5TestSchema(t, db)
	store := NewPostgresD4Store(db)
	tuple := d4TestTuple(t)
	if _, err := store.Receive(context.Background(), D4ReceiveRequest{Tuple: tuple, ReceivedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	result := d4TestResult(tuple)
	result.Disposition = D4NonSuccess
	result.CommitStatus = D4CommitNotCommitted
	record, err := store.Transition(context.Background(), D4TransitionRequest{Tuple: tuple, Expected: D4Received, Event: D4OwnerEvent{Kind: D4EventTerminal, Tuple: tuple, Approval: d4Approval(t, tuple, D4EventTerminal), Result: &result}})
	if err != nil || record.State != D4Terminal || record.ClaimID != uuid.Nil {
		t.Fatalf("record=%+v err=%v", record, err)
	}
}
