package migrations

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

func writerFenceDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_MIGRATION_DATABASE_URL")
	if dsn == "" {
		t.Fatal("TEST_MIGRATION_DATABASE_URL is required for writer-fence PostgreSQL tests")
	}
	return dsn
}

func writerFenceDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("postgres", writerFenceDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	if err := db.Ping(); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db
}

func assertExclusiveAvailable(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var got bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1::bigint)", WriterFenceKey).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Fatal("fresh connection could not acquire the canonical exclusive lock")
	}
	var unlocked bool
	if err := conn.QueryRowContext(ctx, unlockWriterFenceSQL, WriterFenceKey).Scan(&unlocked); err != nil {
		t.Fatal(err)
	}
	if !unlocked {
		t.Fatal("fresh connection failed to confirm canonical exclusive unlock")
	}
}

func TestWriterFenceSharedCommitReleasesLock(t *testing.T) {
	db := writerFenceDB(t)
	defer db.Close()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := AcquireSharedWriterFence(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	assertExclusiveAvailable(t, db)
}

func TestWriterFenceSharedRollbackReleasesLock(t *testing.T) {
	db := writerFenceDB(t)
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := AcquireSharedWriterFence(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertExclusiveAvailable(t, db)
}

func TestWriterFenceExistingSharedWriterDrainsBeforeExclusive(t *testing.T) {
	dsn := writerFenceDSN(t)
	db := writerFenceDB(t)
	defer db.Close()
	shared, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer shared.Rollback()
	if err := AcquireSharedWriterFence(context.Background(), shared); err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		fence, err := OpenExclusiveWriterFence(ctx, dsn)
		if err == nil {
			err = fence.Close()
		}
		result <- err
	}()
	select {
	case err := <-result:
		t.Fatalf("exclusive acquired before shared commit: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := shared.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("exclusive did not acquire after shared writer committed")
	}
}

func TestWriterFenceExclusiveBlocksNewSharedBeforeApplicationSQL(t *testing.T) {
	dsn := writerFenceDSN(t)
	fence, err := OpenExclusiveWriterFence(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()

	db := writerFenceDB(t)
	defer db.Close()
	started := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		tx, err := db.Begin()
		if err != nil {
			finished <- err
			return
		}
		close(started)
		err = AcquireSharedWriterFence(context.Background(), tx)
		if err == nil {
			_, err = tx.Exec("SELECT 1")
		}
		if err != nil {
			_ = tx.Rollback()
		} else {
			err = tx.Commit()
		}
		finished <- err
	}()
	<-started
	select {
	case err := <-finished:
		t.Fatalf("shared writer crossed exclusive fence early: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := fence.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shared writer did not proceed after exclusive release")
	}
}

func TestWriterFenceSecondExclusiveCannotEnterProtectedWindow(t *testing.T) {
	dsn := writerFenceDSN(t)
	first, err := OpenExclusiveWriterFence(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	result := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancel()
		second, err := OpenExclusiveWriterFence(ctx, dsn)
		if err == nil {
			err = second.Close()
		}
		result <- err
	}()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("second orchestrator entered protected window")
		}
	case <-time.After(100 * time.Millisecond):
	}
}

func TestWriterFenceExclusiveSurvivesTransactionCommitOnPinnedSession(t *testing.T) {
	fence, err := OpenExclusiveWriterFence(context.Background(), writerFenceDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	tx, err := fence.Conn().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var available bool
	probe := writerFenceDB(t)
	defer probe.Close()
	if err := probe.QueryRow("SELECT pg_try_advisory_lock($1::bigint)", WriterFenceKey).Scan(&available); err != nil {
		t.Fatal(err)
	}
	if available {
		t.Fatal("exclusive lock did not survive a transaction commit on the pinned session")
	}
}

func TestWriterFenceNonOwnerUnlockIsFalse(t *testing.T) {
	owner, err := OpenExclusiveWriterFence(context.Background(), writerFenceDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	probe := writerFenceDB(t)
	defer probe.Close()
	var unlocked bool
	if err := probe.QueryRow(unlockWriterFenceSQL, WriterFenceKey).Scan(&unlocked); err != nil {
		t.Fatal(err)
	}
	if unlocked {
		t.Fatal("non-owner unlock unexpectedly returned true")
	}
}

func TestWithExclusiveWriterFencePanicStillReleasesLock(t *testing.T) {
	dsn := writerFenceDSN(t)
	panicked := false
	func() {
		defer func() {
			if recover() != nil {
				panicked = true
			}
		}()
		_ = WithExclusiveWriterFence(context.Background(), dsn, func(*ExclusiveWriterFence) error {
			panic("protected work panic")
		})
	}()
	if !panicked {
		t.Fatal("protected work panic was unexpectedly swallowed")
	}
	probe := writerFenceDB(t)
	defer probe.Close()
	assertExclusiveAvailable(t, probe)
}

func TestWriterFenceUnknownOwnershipDiscardsBackendAndProvesFreshAcquisition(t *testing.T) {
	fence, err := OpenExclusiveWriterFence(context.Background(), writerFenceDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	pid := fence.BackendPID()
	if pid == 0 || fence.State() != ExclusiveOwned {
		t.Fatalf("owner pid/state=%d/%q", pid, fence.State())
	}
	fence.state = ExclusiveUnknown
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}
	if fence.State() != ExclusiveReleased {
		t.Fatalf("unknown cleanup state=%q", fence.State())
	}
	probe := writerFenceDB(t)
	defer probe.Close()
	assertExclusiveAvailable(t, probe)
}

func waitForWriterFenceBackendGone(t *testing.T, db *sql.DB, pid int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		var present bool
		err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE pid = $1)`, pid).Scan(&present)
		if err != nil {
			t.Fatal(err)
		}
		if !present {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("backend PID %d remained in pg_stat_activity: %v", pid, ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestWriterFenceConnectionLossReleasesSessionLockAndDiscardsUnknownBackend(t *testing.T) {
	dsn := writerFenceDSN(t)
	fence, err := OpenExclusiveWriterFence(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	pid := fence.BackendPID()
	if pid == 0 || fence.State() != ExclusiveOwned {
		t.Fatalf("owner pid/state=%d/%q", pid, fence.State())
	}

	observer := writerFenceDB(t)
	defer observer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var terminated bool
	if err := observer.QueryRowContext(ctx, "SELECT pg_terminate_backend($1)", pid).Scan(&terminated); err != nil {
		t.Fatal(err)
	}
	if !terminated {
		t.Fatalf("pg_terminate_backend(%d) did not terminate the owner backend", pid)
	}
	waitForWriterFenceBackendGone(t, observer, pid)

	// The owner session was actually lost. Close must first surface the failed
	// release as uncertain, then complete bounded physical-session discard and
	// prove that the advisory lock is available again. Do not pre-classify the
	// state: this exercises the production owned-to-unknown transition.
	closeErr := fence.Close()
	if closeErr == nil || !errors.Is(closeErr, ErrPhysicalConnectionDiscardRequired) {
		t.Fatalf("connection-loss cleanup error=%v, want physical discard uncertainty", closeErr)
	}
	if fence.State() != ExclusiveReleased {
		t.Fatalf("connection-loss cleanup state=%q, want released", fence.State())
	}
	assertExclusiveAvailable(t, observer)
}

func TestWriterFenceCapabilityRequiresLiveOwnership(t *testing.T) {
	fence, err := OpenExclusiveWriterFence(context.Background(), writerFenceDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	capability, err := fence.Capability()
	if err != nil {
		t.Fatal(err)
	}
	if err := RequireProtectedWork(capability); err != nil {
		t.Fatal(err)
	}
	if err := fence.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := RequireProtectedWork(capability); !errors.Is(err, ErrWriterFenceDecisionRequired) {
		t.Fatalf("released capability error=%v", err)
	}
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}
}
