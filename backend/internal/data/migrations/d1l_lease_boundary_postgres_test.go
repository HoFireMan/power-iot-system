package migrations

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

func TestD1LLeaseBoundaryCreateAndExpiryEnforcementPostgres(t *testing.T) {
	cases := []struct {
		name       string
		childUntil func(time.Time) time.Time
		wantErr    error
	}{
		{name: "child expires before parent", childUntil: func(parent time.Time) time.Time { return parent.Add(-5 * time.Minute) }},
		{name: "child expires at parent", childUntil: func(parent time.Time) time.Time { return parent }},
		{name: "child expires after parent", childUntil: func(parent time.Time) time.Time { return parent.Add(time.Minute) }, wantErr: ErrD1LBoundaryExpiry},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			database, _, _ := installD1LTestCatalog(t)
			t.Logf("database=%s endpoint=127.0.0.1:55434", database.Name())
			db := openD1LLeaseTestDB(t, database.DSN())
			store, err := newD1LLeaseBoundaryStore(db)
			if err != nil {
				t.Fatal(err)
			}

			parentExpiry := time.Now().UTC().Add(30 * time.Minute)
			lease := issueD1LLeaseForTest(t, store, parentExpiry)
			activateD1LLeaseFixture(t, db, lease)
			boundaryID := uuid.New()
			boundaryExpiry := tc.childUntil(lease.ExpiresAt)
			_, err = store.CreateBoundary(context.Background(), d1LBoundaryCreateInput{
				BoundaryID: boundaryID, LeaseID: lease.LeaseID, AttemptID: lease.AttemptID,
				Generation: lease.Generation, BoundaryNonce: uuid.New(), BoundaryName: "A2_COMMIT",
				StartedAt: time.Now().UTC().Add(-time.Minute), ExpiresAt: boundaryExpiry,
			})
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("CreateBoundary() error = %v", err)
				}
			} else if !errors.Is(err, tc.wantErr) {
				t.Fatalf("CreateBoundary() error = %v, want %v", err, tc.wantErr)
			}
			var count int
			if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM security_control.admission_boundaries WHERE boundary_id = $1`, boundaryID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			wantCount := 1
			if tc.wantErr != nil {
				wantCount = 0
			}
			if count != wantCount {
				t.Fatalf("boundary row count=%d, want %d", count, wantCount)
			}
		})
	}
}

func TestD1LLeaseCreateUsesDatabaseIssueTimeAndRejectsInvalidDurabilityPostgres(t *testing.T) {
	database, _, _ := installD1LTestCatalog(t)
	t.Logf("database=%s endpoint=127.0.0.1:55434", database.Name())
	db := openD1LLeaseTestDB(t, database.DSN())
	store, err := newD1LLeaseBoundaryStore(db)
	if err != nil {
		t.Fatal(err)
	}

	lease := issueD1LLeaseForTest(t, store, time.Now().UTC().Add(10*time.Minute))
	if lease.Status != d1LLeaseStatusIssued || !lease.ExpiresAt.After(lease.IssuedAt) {
		t.Fatalf("lease=%+v, want ISSUED with database issued_at before expiry", lease)
	}
	var persistedExpiry, persistedIssued time.Time
	if err := db.QueryRow(`SELECT issued_at, expires_at FROM security_control.admission_leases WHERE lease_id = $1`, lease.LeaseID).Scan(&persistedIssued, &persistedExpiry); err != nil {
		t.Fatal(err)
	}
	if !persistedExpiry.Equal(lease.ExpiresAt) || !persistedIssued.Equal(lease.IssuedAt) {
		t.Fatalf("returned lease times issued=%s/%s expiry=%s/%s", lease.IssuedAt, persistedIssued, lease.ExpiresAt, persistedExpiry)
	}

	invalidID := uuid.New()
	_, err = store.CreateLease(context.Background(), d1LLeaseCreateInput{
		LeaseID: invalidID, OperationID: uuid.New(), AttemptID: uuid.New(),
		TargetFingerprint: bytes32ForD1LTest(1), EvidenceDigest: bytes32ForD1LTest(2),
		CapabilityVerifierDigest: bytes32ForD1LTest(3), ExpiresAt: time.Now().UTC().Add(-time.Minute),
	})
	if !errors.Is(err, ErrD1LLeaseExpiry) {
		t.Fatalf("past expiry error=%v, want %v", err, ErrD1LLeaseExpiry)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM security_control.admission_leases WHERE lease_id = $1`, invalidID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("invalid lease left %d durable rows", count)
	}
}

func TestD1LBoundaryRequiresPersistedActiveParentPostgres(t *testing.T) {
	database, _, _ := installD1LTestCatalog(t)
	t.Logf("database=%s endpoint=127.0.0.1:55434", database.Name())
	db := openD1LLeaseTestDB(t, database.DSN())
	store, err := newD1LLeaseBoundaryStore(db)
	if err != nil {
		t.Fatal(err)
	}
	lease := issueD1LLeaseForTest(t, store, time.Now().UTC().Add(20*time.Minute))
	boundaryID := uuid.New()
	_, err = store.CreateBoundary(context.Background(), d1LBoundaryCreateInput{
		BoundaryID: boundaryID, LeaseID: lease.LeaseID, AttemptID: lease.AttemptID,
		Generation: lease.Generation, BoundaryNonce: uuid.New(), BoundaryName: "A2_COMMIT",
		StartedAt: time.Now().UTC().Add(-time.Minute), ExpiresAt: time.Now().UTC().Add(time.Minute),
	})
	if !errors.Is(err, ErrD1LBoundaryParentInactive) {
		t.Fatalf("ISSUED parent error=%v, want %v", err, ErrD1LBoundaryParentInactive)
	}
	assertD1LBoundaryAbsent(t, db, boundaryID)
}

func TestD1LBoundaryUsesPersistedParentExpiryAndMissingParentCannotOrphanPostgres(t *testing.T) {
	database, _, _ := installD1LTestCatalog(t)
	t.Logf("database=%s endpoint=127.0.0.1:55434", database.Name())
	db := openD1LLeaseTestDB(t, database.DSN())
	store, err := newD1LLeaseBoundaryStore(db)
	if err != nil {
		t.Fatal(err)
	}
	parentExpiry := time.Now().UTC().Add(20 * time.Minute)
	lease := issueD1LLeaseForTest(t, store, parentExpiry)
	activateD1LLeaseFixture(t, db, lease)

	// The caller has no parent-expiry field.  An alternate, later value cannot
	// authorize this child: the persisted lease expiry is the only authority.
	boundaryID := uuid.New()
	_, err = store.CreateBoundary(context.Background(), d1LBoundaryCreateInput{
		BoundaryID: boundaryID, LeaseID: lease.LeaseID, AttemptID: lease.AttemptID,
		Generation: lease.Generation, BoundaryNonce: uuid.New(), BoundaryName: "HANDOFF",
		StartedAt: time.Now().UTC().Add(-time.Minute), ExpiresAt: parentExpiry.Add(time.Minute),
	})
	if !errors.Is(err, ErrD1LBoundaryExpiry) {
		t.Fatalf("alternate parent expiry error=%v, want %v", err, ErrD1LBoundaryExpiry)
	}
	assertD1LBoundaryAbsent(t, db, boundaryID)
	assertD1LLeaseExpiryUnchanged(t, db, lease.LeaseID, parentExpiry)

	missingLeaseID, missingAttemptID, missingBoundaryID := uuid.New(), uuid.New(), uuid.New()
	_, err = store.CreateBoundary(context.Background(), d1LBoundaryCreateInput{
		BoundaryID: missingBoundaryID, LeaseID: missingLeaseID, AttemptID: missingAttemptID,
		Generation: 1, BoundaryNonce: uuid.New(), BoundaryName: "A2_COMMIT",
		StartedAt: time.Now().UTC().Add(-time.Minute), ExpiresAt: time.Now().UTC().Add(time.Minute),
	})
	if !errors.Is(err, ErrD1LBoundaryParentMissing) {
		t.Fatalf("missing parent error=%v, want %v", err, ErrD1LBoundaryParentMissing)
	}
	assertD1LBoundaryAbsent(t, db, missingBoundaryID)
}

func TestD1LBoundaryIdentitySerializationAndDistinctClosedBoundariesPostgres(t *testing.T) {
	database, _, _ := installD1LTestCatalog(t)
	t.Logf("database=%s endpoint=127.0.0.1:55434", database.Name())
	db := openD1LLeaseTestDB(t, database.DSN())
	store, err := newD1LLeaseBoundaryStore(db)
	if err != nil {
		t.Fatal(err)
	}
	parentExpiry := time.Now().UTC().Add(30 * time.Minute)
	lease := issueD1LLeaseForTest(t, store, parentExpiry)
	activateD1LLeaseFixture(t, db, lease)

	// Hold the same parent row, then release two contenders together.  The
	// lease identity lock and the catalog's one-OPEN index choose one winner;
	// no timing sleep is used to establish the ordering.
	holder, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Exec(`SELECT lease_id FROM security_control.admission_leases WHERE lease_id = $1 AND generation = $2 AND attempt_id = $3 FOR UPDATE`, lease.LeaseID, lease.Generation, lease.AttemptID); err != nil {
		_ = holder.Rollback()
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := store.CreateBoundary(context.Background(), d1LBoundaryCreateInput{
				BoundaryID: uuid.New(), LeaseID: lease.LeaseID, AttemptID: lease.AttemptID,
				Generation: lease.Generation, BoundaryNonce: uuid.New(), BoundaryName: "A2_COMMIT",
				StartedAt: time.Now().UTC().Add(-time.Minute), ExpiresAt: lease.ExpiresAt,
			})
			results <- err
		}(i)
	}
	close(start)
	if err := holder.Rollback(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	close(results)
	var success, uniqueFailure int
	for err := range results {
		if err == nil {
			success++
			continue
		}
		var pqErr *pq.Error
		if !errors.As(err, &pqErr) || pqErr.Code != "23505" {
			t.Fatalf("contender error=%v, want unique identity/open serialization error", err)
		}
		uniqueFailure++
	}
	if success != 1 || uniqueFailure != 1 {
		t.Fatalf("contenders success=%d unique_failure=%d, want one each", success, uniqueFailure)
	}
	var openCount int
	if err := db.QueryRow(`SELECT count(*) FROM security_control.admission_boundaries WHERE lease_id = $1 AND generation = $2 AND status = 'OPEN'`, lease.LeaseID, lease.Generation).Scan(&openCount); err != nil {
		t.Fatal(err)
	}
	if openCount != 1 {
		t.Fatalf("open boundary count=%d, want 1", openCount)
	}

	// The catalog allows another distinct identity after the first boundary is
	// closed.  Closing here is test fixture setup; no closure API is introduced
	// by the H4 writer.
	if _, err := db.Exec(`UPDATE security_control.admission_boundaries SET status='COMMITTED', closed_at=clock_timestamp(), outcome_code='TEST_COMMITTED' WHERE lease_id = $1 AND generation = $2 AND status='OPEN'`, lease.LeaseID, lease.Generation); err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateBoundary(context.Background(), d1LBoundaryCreateInput{
		BoundaryID: uuid.New(), LeaseID: lease.LeaseID, AttemptID: lease.AttemptID,
		Generation: lease.Generation, BoundaryNonce: uuid.New(), BoundaryName: "HANDOFF",
		StartedAt: time.Now().UTC().Add(-time.Minute), ExpiresAt: lease.ExpiresAt,
	})
	if err != nil {
		t.Fatalf("distinct boundary after closed first boundary: %v", err)
	}
	if second.BoundaryName != "HANDOFF" {
		t.Fatalf("second boundary=%+v", second)
	}
	if err := db.QueryRow(`SELECT count(*) FROM security_control.admission_boundaries WHERE lease_id = $1`, lease.LeaseID).Scan(&openCount); err != nil {
		t.Fatal(err)
	}
	if openCount != 2 {
		t.Fatalf("boundary count=%d, want 2 retained identities", openCount)
	}
}

func TestD1LLeaseBoundaryStoreHasNoExpiryMutationSurface(t *testing.T) {
	database, _, _ := installD1LTestCatalog(t)
	db := openD1LLeaseTestDB(t, database.DSN())
	store, err := newD1LLeaseBoundaryStore(db)
	if err != nil {
		t.Fatal(err)
	}
	methods := reflect.TypeOf(store).NumMethod()
	if methods != 2 {
		t.Fatalf("private store exposes %d exported methods, want create-only CreateLease/CreateBoundary", methods)
	}
	for _, name := range []string{"CreateLease", "CreateBoundary"} {
		if _, ok := reflect.TypeOf(store).MethodByName(name); !ok {
			t.Fatalf("missing typed create method %s", name)
		}
	}
}

func openD1LLeaseTestDB(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	return db
}

func issueD1LLeaseForTest(t *testing.T, store *d1LLeaseBoundaryStore, expiresAt time.Time) d1LLease {
	t.Helper()
	lease, err := store.CreateLease(context.Background(), d1LLeaseCreateInput{
		LeaseID: uuid.New(), OperationID: uuid.New(), AttemptID: uuid.New(),
		TargetFingerprint: bytes32ForD1LTest(1), EvidenceDigest: bytes32ForD1LTest(2),
		CapabilityVerifierDigest: bytes32ForD1LTest(3), ExpiresAt: expiresAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return lease
}

func activateD1LLeaseFixture(t *testing.T, db *sql.DB, lease d1LLease) {
	t.Helper()
	if _, err := db.Exec(`UPDATE security_control.admission_leases SET status='ACTIVE', activated_at=issued_at WHERE lease_id = $1 AND generation = $2 AND attempt_id = $3`, lease.LeaseID, lease.Generation, lease.AttemptID); err != nil {
		t.Fatal(err)
	}
}

func bytes32ForD1LTest(value byte) []byte {
	return []byte(strings.Repeat(string([]byte{value}), 32))
}

func assertD1LBoundaryAbsent(t *testing.T, db *sql.DB, boundaryID uuid.UUID) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM security_control.admission_boundaries WHERE boundary_id = $1`, boundaryID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("boundary %s has %d rows, want absent", boundaryID, count)
	}
}

func assertD1LLeaseExpiryUnchanged(t *testing.T, db *sql.DB, leaseID uuid.UUID, want time.Time) {
	t.Helper()
	var got time.Time
	if err := db.QueryRow(`SELECT expires_at FROM security_control.admission_leases WHERE lease_id = $1`, leaseID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if delta := got.Sub(want); delta > time.Microsecond || delta < -time.Microsecond {
		t.Fatalf("persisted parent expiry=%s, want immutable %s", got, want)
	}
}
