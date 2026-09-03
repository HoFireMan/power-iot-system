package migrations

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestD1LLeaseLifecycleRejectsWithoutNextLedgerProofPostgres(t *testing.T) {
	setups := []struct {
		name  string
		setup func(*testing.T) string
	}{
		{
			name: "v1_manifest",
			setup: func(t *testing.T) string {
				_, dsn, _ := installD1LTestCatalog(t)
				return dsn
			},
		},
		{
			name: "unmanifested_ledger",
			setup: func(t *testing.T) string {
				_, dsn, _ := installD1LTestCatalog(t)
				target := d1lTargetForTest(t, dsn)
				db, err := sql.Open("postgres", dsn)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`UPDATE security_control.control_schema_migrations SET target_fingerprint=$1`, target); err != nil {
					db.Close()
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				if _, err := D1LUpgradeLedger(context.Background(), dsn, target); err != nil {
					t.Fatal(err)
				}
				db, err = sql.Open("postgres", dsn)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`DELETE FROM security_control.control_schema_migrations`); err != nil {
					db.Close()
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				return dsn
			},
		},
		{
			name: "next_manifest_without_ledger",
			setup: func(t *testing.T) string {
				_, dsn, _ := installD1LTestCatalog(t)
				target := d1lTargetForTest(t, dsn)
				db, err := sql.Open("postgres", dsn)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`UPDATE security_control.control_schema_migrations SET target_fingerprint=$1`, target); err != nil {
					db.Close()
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				if _, err := D1LUpgradeLedger(context.Background(), dsn, target); err != nil {
					t.Fatal(err)
				}
				db, err = sql.Open("postgres", dsn)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`DROP TABLE security_control.admission_provenance CASCADE`); err != nil {
					db.Close()
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				return dsn
			},
		},
	}
	for _, tc := range setups {
		t.Run(tc.name, func(t *testing.T) {
			db, err := sql.Open("postgres", tc.setup(t))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ledger, err := NewD1LProvenanceLedger(db, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			id := D1LLeaseIdentity{
				LeaseID: uuid.New(), OperationID: uuid.New(), AttemptID: uuid.New(), Generation: 1,
				TargetFingerprint: bytes32ForD1LTest(1), EvidenceDigest: bytes32ForD1LTest(2),
			}
			ctx := context.Background()
			checks := []struct {
				name string
				call func() error
			}{
				{name: "inspect", call: func() error { _, err := ledger.InspectLease(ctx, id); return err }},
				{name: "activate", call: func() error { return ledger.activate(ctx, id, make([]byte, d1LActivationSize)) }},
				{name: "consume", call: func() error { return ledger.ConsumeLease(ctx, id) }},
				{name: "revoke", call: func() error { return ledger.RevokeLease(ctx, id, "OWNER_INVALIDATED") }},
				{name: "begin_quarantine", call: func() error { return ledger.BeginQuarantineLease(ctx, id) }},
				{name: "quarantine", call: func() error { return ledger.QuarantineLease(ctx, id, "OWNER_INVALIDATED") }},
				{name: "recover", call: func() error {
					_, err := ledger.RecoverLease(ctx, id, bytes32ForD1LTest(3), "RECOVERY_REQUIRED")
					return err
				}},
			}
			for _, check := range checks {
				t.Run(check.name, func(t *testing.T) {
					if err := check.call(); !errors.Is(err, ErrD1LLeaseStoreUnavailable) {
						t.Fatalf("err=%v, want fail-closed authority rejection", err)
					}
				})
			}
		})
	}
}

func TestD1LExpiryWinsAgainstQuarantineAndRevokePostgres(t *testing.T) {
	dsn, _ := prepareD1LProvenanceDatabase(t)
	db := openD1LLeaseTestDB(t, dsn)
	store, err := newD1LLeaseBoundaryStore(db)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := NewD1LProvenanceLedger(db, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	lease := issueD1LLeaseForTest(t, store, time.Now().UTC().Add(500*time.Millisecond))
	activateD1LLeaseFixture(t, db, lease)
	id := d1lIdentityForTest(lease)
	time.Sleep(600 * time.Millisecond)
	if err := ledger.BeginQuarantineLease(context.Background(), id); !errors.Is(err, ErrD1LLeaseTerminal) {
		t.Fatalf("expired active quarantine err=%v", err)
	}
	var status, code string
	if err := db.QueryRow(`SELECT status,terminal_code FROM security_control.admission_leases WHERE lease_id=$1`, lease.LeaseID).Scan(&status, &code); err != nil {
		t.Fatal(err)
	}
	if status != "REVOKED" || code != "EXPIRED" {
		t.Fatalf("expired active status=%s code=%s", status, code)
	}

	lease2 := issueD1LLeaseForTest(t, store, time.Now().UTC().Add(500*time.Millisecond))
	activateD1LLeaseFixture(t, db, lease2)
	id2 := d1lIdentityForTest(lease2)
	if err := ledger.BeginQuarantineLease(context.Background(), id2); err != nil {
		t.Fatal(err)
	}
	time.Sleep(600 * time.Millisecond)
	if err := ledger.QuarantineLease(context.Background(), id2, "OWNER_INVALIDATED"); !errors.Is(err, ErrD1LLeaseTerminal) {
		t.Fatalf("expired pending quarantine err=%v", err)
	}
	if err := db.QueryRow(`SELECT status,terminal_code FROM security_control.admission_leases WHERE lease_id=$1`, lease2.LeaseID).Scan(&status, &code); err != nil {
		t.Fatal(err)
	}
	if status != "REVOKED" || code != "EXPIRED" {
		t.Fatalf("expired pending status=%s code=%s", status, code)
	}
}

func TestD1LConsumeExpiredActiveTerminalizesWithoutConsumePostgres(t *testing.T) {
	dsn, _ := prepareD1LProvenanceDatabase(t)
	db := openD1LLeaseTestDB(t, dsn)
	store, err := newD1LLeaseBoundaryStore(db)
	if err != nil {
		t.Fatal(err)
	}
	lease := issueD1LLeaseForTest(t, store, time.Now().UTC().Add(500*time.Millisecond))
	activateD1LLeaseFixture(t, db, lease)
	time.Sleep(600 * time.Millisecond)
	ledger, err := NewD1LProvenanceLedger(db, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	id := d1lIdentityForTest(lease)
	if err := ledger.ConsumeLease(context.Background(), id); !errors.Is(err, ErrD1LLeaseTerminal) {
		t.Fatalf("expired consume err=%v, want terminal", err)
	}
	var status, code string
	if err := db.QueryRow(`SELECT status,terminal_code FROM security_control.admission_leases WHERE lease_id=$1`, lease.LeaseID).Scan(&status, &code); err != nil {
		t.Fatal(err)
	}
	if status != "REVOKED" || code != "EXPIRED" {
		t.Fatalf("expired active status=%s code=%s", status, code)
	}
}

func TestD1LQuarantineLifecyclePreservesTerminalFieldsUntilRevokePostgres(t *testing.T) {
	dsn, _ := prepareD1LProvenanceDatabase(t)
	db := openD1LLeaseTestDB(t, dsn)
	store, err := newD1LLeaseBoundaryStore(db)
	if err != nil {
		t.Fatal(err)
	}
	lease := issueD1LLeaseForTest(t, store, time.Now().UTC().Add(time.Hour))
	activateD1LLeaseFixture(t, db, lease)
	ledger, err := NewD1LProvenanceLedger(db, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	id := d1lIdentityForTest(lease)
	if err := ledger.BeginQuarantineLease(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	var status string
	var terminalAt, quarantinedAt *time.Time
	var terminalCode, quarantineCode *string
	read := func() {
		t.Helper()
		if err := db.QueryRow(`SELECT status,terminal_at,terminal_code,quarantined_at,quarantine_code FROM security_control.admission_leases WHERE lease_id=$1`, lease.LeaseID).Scan(&status, &terminalAt, &terminalCode, &quarantinedAt, &quarantineCode); err != nil {
			t.Fatal(err)
		}
	}
	read()
	if status != "QUARANTINE_PENDING" || terminalAt != nil || terminalCode != nil || quarantinedAt != nil || quarantineCode != nil {
		t.Fatalf("pending lifecycle status=%s terminal=%v/%v quarantine=%v/%v", status, terminalAt, terminalCode, quarantinedAt, quarantineCode)
	}
	if err := ledger.QuarantineLease(context.Background(), id, "OWNER_INVALIDATED"); err != nil {
		t.Fatal(err)
	}
	read()
	if status != "QUARANTINED" || terminalAt != nil || terminalCode != nil || quarantinedAt == nil || quarantineCode == nil || *quarantineCode != "OWNER_INVALIDATED" {
		t.Fatalf("quarantined lifecycle status=%s terminal=%v/%v quarantine=%v/%v", status, terminalAt, terminalCode, quarantinedAt, quarantineCode)
	}
	inspection, err := ledger.InspectLease(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Status != "QUARANTINED" {
		t.Fatalf("inspection terminalized quarantine: %+v", inspection)
	}
	if err := ledger.RevokeLease(context.Background(), id, "REVOKED_BY_OWNER"); !errors.Is(err, ErrD1LLeaseTerminal) {
		t.Fatalf("ordinary revoke quarantined err=%v, want terminal/recovery rejection", err)
	}
	if _, err := ledger.RecoverLease(context.Background(), id, bytes32ForD1LTest(9), "RECOVERY_REQUIRED"); err != nil {
		t.Fatal(err)
	}
	read()
	if status != "REVOKED" || terminalAt == nil || terminalCode == nil || *terminalCode != "RECOVERY_REQUIRED" || quarantinedAt == nil || quarantineCode == nil {
		t.Fatalf("revoked lifecycle status=%s terminal=%v/%v quarantine=%v/%v", status, terminalAt, terminalCode, quarantinedAt, quarantineCode)
	}
}

func TestD1LLeaseTerminalStatesCannotReopenPostgres(t *testing.T) {
	dsn, _ := prepareD1LProvenanceDatabase(t)
	db := openD1LLeaseTestDB(t, dsn)
	store, err := newD1LLeaseBoundaryStore(db)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := NewD1LProvenanceLedger(db, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, terminal := range []string{"CONSUMED", "REVOKED"} {
		lease := issueD1LLeaseForTest(t, store, time.Now().UTC().Add(time.Hour))
		activateD1LLeaseFixture(t, db, lease)
		if terminal == "CONSUMED" {
			if err := ledger.ConsumeLease(context.Background(), d1lIdentityForTest(lease)); err != nil {
				t.Fatal(err)
			}
		} else if err := ledger.RevokeLease(context.Background(), d1lIdentityForTest(lease), "OWNER_INVALIDATED"); err != nil {
			t.Fatal(err)
		}
		id := d1lIdentityForTest(lease)
		if err := ledger.BeginQuarantineLease(context.Background(), id); !errors.Is(err, ErrD1LLeaseTerminal) {
			t.Fatalf("%s reopen pending err=%v", terminal, err)
		}
	}
}

func d1lIdentityForTest(lease d1LLease) D1LLeaseIdentity {
	return D1LLeaseIdentity{
		LeaseID: lease.LeaseID, OperationID: lease.OperationID, AttemptID: lease.AttemptID,
		Generation: lease.Generation, TargetFingerprint: append([]byte(nil), lease.TargetFingerprint...),
		EvidenceDigest: append([]byte(nil), lease.EvidenceDigest...),
	}
}

func TestD1LConcurrentConsumeHasOneWinnerPostgres(t *testing.T) {
	dsn, _ := prepareD1LProvenanceDatabase(t)
	db := openD1LLeaseTestDB(t, dsn)
	store, err := newD1LLeaseBoundaryStore(db)
	if err != nil {
		t.Fatal(err)
	}
	lease := issueD1LLeaseForTest(t, store, time.Now().UTC().Add(time.Hour))
	activateD1LLeaseFixture(t, db, lease)
	ledger, err := NewD1LProvenanceLedger(db, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	id := d1lIdentityForTest(lease)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- ledger.ConsumeLease(context.Background(), id)
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	var winners, terminals int
	for err := range results {
		if err == nil {
			winners++
		} else if errors.Is(err, ErrD1LLeaseTerminal) {
			terminals++
		} else {
			t.Fatalf("concurrent consume error=%v", err)
		}
	}
	if winners != 1 || terminals != 1 {
		t.Fatalf("concurrent consume winners=%d terminals=%d", winners, terminals)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM security_control.admission_leases WHERE lease_id=$1`, lease.LeaseID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(status, "CONSUMED") {
		t.Fatalf("final status=%s", status)
	}
}
