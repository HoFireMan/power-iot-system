//go:build securityintegration

package reconciliation

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// This suite requires a dedicated migration database supplied by repository
// validation. It never runs migrations and every transaction rolls back.
func TestPostgresFactCollectionIsReadOnly(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_MIGRATION_DATABASE_URL")
	}
	if dsn == "" {
		t.Fatal("TEST_DATABASE_URL or TEST_MIGRATION_DATABASE_URL is required")
	}
	if strings.Contains(strings.ToLower(dsn), "power_iot") {
		t.Fatal("dedicated migration database must not point at power_iot")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	if err := tx.Exec("SET TRANSACTION READ ONLY").Error; err != nil {
		t.Fatal(err)
	}
	facts, err := NewPostgresFactCollector(tx).CollectV5(context.Background(), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if facts.SchemaVersion != SchemaVersion {
		t.Fatalf("schema=%q", facts.SchemaVersion)
	}
	if _, _, err := CanonicalSourceFacts(facts); err != nil {
		t.Fatal(err)
	}
}

type postgresCallerOwnedFence struct{ tx *sql.Tx }

func (postgresCallerOwnedFence) ExclusiveReconciliationFence() bool { return true }
func (f postgresCallerOwnedFence) PinnedTransaction() ReadOnlyConnection {
	return f.tx
}

func TestFencedRecheckUsesCallerOwnedReadWriteRepeatableReadTransaction(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_MIGRATION_DATABASE_URL")
	}
	if dsn == "" {
		t.Fatal("TEST_DATABASE_URL or TEST_MIGRATION_DATABASE_URL is required")
	}
	if strings.Contains(strings.ToLower(dsn), "power_iot") {
		t.Fatal("dedicated migration database must not point at power_iot")
	}
	base, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	db, err := base.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	var isolation, readOnly string
	var beforePID int
	if err := tx.QueryRowContext(ctx, "SHOW transaction_isolation").Scan(&isolation); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRowContext(ctx, "SHOW transaction_read_only").Scan(&readOnly); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&beforePID); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(isolation, "repeatable read") || !strings.EqualFold(readOnly, "off") {
		t.Fatalf("caller transaction isolation=%q read_only=%q, want repeatable read/off", isolation, readOnly)
	}

	rechecker := FencedRechecker{
		Collector: NewPostgresFactCollector(base),
		Now:       func() time.Time { return time.Unix(1, 0) },
	}
	facts, err := rechecker.RecheckV5(ctx, postgresCallerOwnedFence{tx: tx})
	if err != nil {
		t.Fatal(err)
	}
	if facts.SchemaVersion != SchemaVersion {
		t.Fatalf("schema=%q", facts.SchemaVersion)
	}
	var afterPID int
	if err := tx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&afterPID); err != nil {
		t.Fatal(err)
	}
	if beforePID != afterPID {
		t.Fatalf("backend changed across recheck: before=%d after=%d", beforePID, afterPID)
	}
	if err := tx.QueryRowContext(ctx, "SHOW transaction_read_only").Scan(&readOnly); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(readOnly, "off") {
		t.Fatalf("transaction became read-only after recheck: %q", readOnly)
	}
}
