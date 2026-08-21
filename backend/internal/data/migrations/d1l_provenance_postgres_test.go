package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"power-iot-backend/internal/data/reconciliation/sourceowner"
	"power-iot-backend/internal/data/reconciliation/upstream"
)

func prepareD1LProvenanceDatabase(t *testing.T) (string, []byte) {
	t.Helper()
	_, dsn, _ := installD1LTestCatalog(t)
	target := d1lTargetForTest(t, dsn)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `UPDATE security_control.control_schema_migrations SET target_fingerprint=$1`, target); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	if _, err := D1LUpgradeLedger(context.Background(), dsn, target); err != nil {
		t.Fatal(err)
	}
	return dsn, target
}

func insertAvailableProvenance(t *testing.T, db *sql.DB, operationID, attemptID, provenanceID, _ uuid.UUID) D1LProvenanceRecord {
	t.Helper()
	target := []byte(strings.Repeat("t", 32))
	evidence := []byte(strings.Repeat("e", 32))
	if _, err := db.ExecContext(context.Background(), `INSERT INTO security_control.admission_provenance(provenance_id,provenance_version,owner_identity,owner_version,operation_id,attempt_id,target_fingerprint,evidence_digest,route_intent,state) VALUES($1,1,'trusted-post-d1l-upstream','owner-v1',$2,$3,$4,$5,'D1_ISSUE','AVAILABLE')`, provenanceID, operationID, attemptID, target, evidence); err != nil {
		t.Fatal(err)
	}
	var targetDigest, evidenceDigest [32]byte
	for i := range targetDigest {
		targetDigest[i], evidenceDigest[i] = 't', 'e'
	}
	return D1LProvenanceRecord{ID: provenanceID, Version: 1, OwnerIdentity: "trusted-post-d1l-upstream", OwnerVersion: "owner-v1", OperationID: operationID, AttemptID: attemptID, TargetFingerprint: targetDigest, EvidenceDigest: evidenceDigest, RouteIntent: "D1_ISSUE", State: D1LProvenanceAvailable}
}

func TestD1LProvenanceT0AndT1DurableLinkage(t *testing.T) {
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
	operationID, attemptID, provenanceID, issueID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	record := insertAvailableProvenance(t, db, operationID, attemptID, provenanceID, issueID)
	reservation, err := ledger.Reserve(context.Background(), record)
	if err != nil {
		t.Fatal(err)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM security_control.admission_provenance WHERE provenance_id=$1`, provenanceID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(D1LProvenanceReserved) {
		t.Fatalf("T0 state=%s", state)
	}
	if _, err := ledger.Reserve(context.Background(), record); err != ErrD1LProvenanceDuplicate {
		t.Fatalf("duplicate reserve err=%v", err)
	}
	lease, err := ledger.completeIssue(context.Background(), reservation)
	if err != nil {
		t.Fatal(err)
	}
	if lease.LeaseID == uuid.Nil || lease.Generation <= 0 || lease.Status != d1LLeaseStatusIssued {
		t.Fatalf("lease=%+v", lease)
	}
	var linkedLease uuid.UUID
	var linkedGeneration int64
	if err := db.QueryRow(`SELECT state,lease_id,lease_generation FROM security_control.admission_provenance WHERE provenance_id=$1`, provenanceID).Scan(&state, &linkedLease, &linkedGeneration); err != nil {
		t.Fatal(err)
	}
	if state != string(D1LProvenanceConsumed) || linkedLease != lease.LeaseID || linkedGeneration != lease.Generation {
		t.Fatalf("linkage state=%s lease=%s/%d want=%s/%d", state, linkedLease, linkedGeneration, lease.LeaseID, lease.Generation)
	}
	var secretColumn bool
	if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_attribute a JOIN pg_class c ON c.oid=a.attrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='security_control' AND c.relname='admission_provenance' AND a.attname IN ('activation','secret','presentation'))`).Scan(&secretColumn); err != nil {
		t.Fatal(err)
	}
	if secretColumn {
		t.Fatal("raw activation column exists in provenance ledger")
	}
}

func TestD1LReserveRejectsTamperedIssueIdentityPostgres(t *testing.T) {
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
	record := insertAvailableProvenance(t, db, uuid.New(), uuid.New(), uuid.New(), uuid.New())
	tampered := record
	tampered.IssueID = uuid.New()
	if _, err := ledger.Reserve(context.Background(), tampered); err != ErrD1LProvenance {
		t.Fatalf("caller-supplied issue reservation err=%v, want provenance rejection", err)
	}
	var state string
	var durableIssue interface{}
	if err := db.QueryRow(`SELECT state,issue_id FROM security_control.admission_provenance WHERE provenance_id=$1`, record.ID).Scan(&state, &durableIssue); err != nil {
		t.Fatal(err)
	}
	if state != string(D1LProvenanceAvailable) || durableIssue != nil {
		t.Fatalf("tampered reservation changed durable state=%s issue=%v", state, durableIssue)
	}
}

func TestD1LInvalidateMatchesExactReservationTuplePostgres(t *testing.T) {
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
	record := insertAvailableProvenance(t, db, uuid.New(), uuid.New(), uuid.New(), uuid.New())
	reservation, err := ledger.Reserve(context.Background(), record)
	if err != nil {
		t.Fatal(err)
	}
	tampered := reservation
	tampered.Record.OwnerVersion = "owner-v2-forged"
	if err := ledger.Invalidate(context.Background(), tampered, "T1_ROLLBACK"); err != ErrD1LProvenance {
		t.Fatalf("mismatched reservation err=%v", err)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM security_control.admission_provenance WHERE provenance_id=$1`, record.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(D1LProvenanceReserved) {
		t.Fatalf("mismatched invalidation changed state=%s", state)
	}
	if err := ledger.Invalidate(context.Background(), reservation, "T1_ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT state FROM security_control.admission_provenance WHERE provenance_id=$1`, record.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(D1LProvenanceInvalidated) {
		t.Fatalf("valid invalidation state=%s", state)
	}
}

func produceD1LPostgresProvenance(t *testing.T, dsn string, operationID, attemptID uuid.UUID) upstream.Provenance {
	t.Helper()
	ownerDB, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	owner := sourceowner.NewPostgresSourceOwner(ownerDB)
	binding := sourceowner.NewInvocationBinding(operationID, attemptID)
	evidence, err := owner.CollectTrustedV5(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	var target [32]byte
	target[0] = 1
	p, err := upstream.Produce(evidence, upstream.Binding{OperationID: operationID, AttemptID: attemptID, TargetFingerprint: target, RouteIntent: upstream.D1OwnerIssueRoute}, "owner-v1")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestD1LResolveProducerResponseLossAndExactDuplicateReadPostgres(t *testing.T) {
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
	p := produceD1LPostgresProvenance(t, dsn, uuid.New(), uuid.New())
	written, err := ledger.Record(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := ledger.ResolveProducer(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.IssueID != uuid.Nil || resolved.ID != written.ID || resolved.State != D1LProvenanceAvailable {
		t.Fatalf("response-loss resolution=%+v written=%+v", resolved, written)
	}
	if _, err := ledger.ResolveExact(context.Background(), resolved); err != ErrD1LProvenance {
		t.Fatalf("AVAILABLE exact resolution err=%v, want missing issue identity", err)
	}
	reservation, err := ledger.Reserve(context.Background(), written)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.ResolveExact(context.Background(), reservation.Record); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.ResolveReservedNonCommit(context.Background(), reservation, "RECOVERY_REQUIRED"); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM security_control.admission_provenance WHERE provenance_id=$1`, written.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(D1LProvenanceInvalidated) {
		t.Fatalf("proven noncommit state=%s", state)
	}
}

func TestD1LOwnerServiceActivationSafeOneShotPostgres(t *testing.T) {
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
	service, err := NewD1LOwnerService(ledger)
	if err != nil {
		t.Fatal(err)
	}
	p := produceD1LPostgresProvenance(t, dsn, uuid.New(), uuid.New())
	result, err := service.IssueAndActivate(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "ACTIVE" || result.Identity.LeaseID == uuid.Nil || result.ExpiresAt.IsZero() {
		t.Fatalf("unsafe/invalid activation result=%+v", result)
	}
	inspection, err := service.Inspect(context.Background(), result.Identity)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Status != "ACTIVE" || !inspection.ExpiresAt.Equal(result.ExpiresAt) {
		t.Fatalf("activation inspection=%+v result=%+v", inspection, result)
	}
	if strings.Contains(strings.ToLower(fmt.Sprintf("%+v", result)), "activation") || strings.Contains(strings.ToLower(fmt.Sprintf("%+v", result)), "verifier") {
		t.Fatal("safe issue result exposed secret field")
	}
	var rowJSON string
	if err := db.QueryRow(`SELECT to_jsonb(l)::text FROM security_control.admission_leases l WHERE lease_id=$1`, result.Identity.LeaseID).Scan(&rowJSON); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rowJSON, "activation") || strings.Contains(rowJSON, "presentation") {
		t.Fatalf("lease row contains raw activation column: %s", rowJSON)
	}
}

func TestD1LActivationMismatchReplayAndConcurrentWinnerPostgres(t *testing.T) {
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
	p := produceD1LPostgresProvenance(t, dsn, uuid.New(), uuid.New())
	record, err := ledger.Record(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := ledger.Reserve(context.Background(), record)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := ledger.completeIssue(context.Background(), reservation)
	if err != nil {
		t.Fatal(err)
	}
	id := d1lIdentityForTest(lease)
	bad := append([]byte(nil), lease.activation...)
	bad[0] ^= 1
	if err := ledger.activate(context.Background(), id, bad); err != ErrD1LLeaseBinding {
		t.Fatalf("mismatched activation err=%v", err)
	}
	first := append([]byte(nil), lease.activation...)
	if err := ledger.activate(context.Background(), id, first); err != nil {
		t.Fatal(err)
	}
	if err := ledger.activate(context.Background(), id, first); err != ErrD1LLeaseTerminal {
		t.Fatalf("activation replay err=%v", err)
	}

	p2 := produceD1LPostgresProvenance(t, dsn, uuid.New(), uuid.New())
	r2, err := ledger.Record(context.Background(), p2)
	if err != nil {
		t.Fatal(err)
	}
	v2, err := ledger.Reserve(context.Background(), r2)
	if err != nil {
		t.Fatal(err)
	}
	lease2, err := ledger.completeIssue(context.Background(), v2)
	if err != nil {
		t.Fatal(err)
	}
	id2 := d1lIdentityForTest(lease2)
	presentation := append([]byte(nil), lease2.activation...)
	results := make(chan error, 2)
	go func() { results <- ledger.activate(context.Background(), id2, append([]byte(nil), presentation...)) }()
	go func() { results <- ledger.activate(context.Background(), id2, append([]byte(nil), presentation...)) }()
	var wins, terminals int
	for i := 0; i < 2; i++ {
		err := <-results
		switch err {
		case nil:
			wins++
		case ErrD1LLeaseTerminal:
			terminals++
		default:
			t.Fatalf("concurrent activation err=%v", err)
		}
	}
	if wins != 1 || terminals != 1 {
		t.Fatalf("concurrent activation wins=%d terminals=%d", wins, terminals)
	}
}

func TestD1LActivationExpiryTerminalizesWithoutActivationPostgres(t *testing.T) {
	dsn, _ := prepareD1LProvenanceDatabase(t)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ledger, err := NewD1LProvenanceLedger(db, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	p := produceD1LPostgresProvenance(t, dsn, uuid.New(), uuid.New())
	record, err := ledger.Record(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := ledger.Reserve(context.Background(), record)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := ledger.completeIssue(context.Background(), reservation)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond)
	if err := ledger.activateIssuedLease(context.Background(), &lease); err != ErrD1LLeaseTerminal {
		t.Fatalf("expired activation err=%v", err)
	}
	var status, code string
	if err := db.QueryRow(`SELECT status,terminal_code FROM security_control.admission_leases WHERE lease_id=$1`, lease.LeaseID).Scan(&status, &code); err != nil {
		t.Fatal(err)
	}
	if status != "EXPIRED" || code != "EXPIRED" {
		t.Fatalf("expired activation status=%s code=%s", status, code)
	}
}

func TestD1LProvenanceRejectsUnmanifestedLedger(t *testing.T) {
	dsn, _ := prepareD1LProvenanceDatabase(t)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`DELETE FROM security_control.control_schema_migrations`); err != nil {
		t.Fatal(err)
	}
	ledger, err := NewD1LProvenanceLedger(db, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ledger.Resolve(context.Background(), uuid.New(), 1, uuid.New())
	if err != ErrD1LLeaseStoreUnavailable {
		t.Fatalf("unmanifested ledger err=%v", err)
	}
}
