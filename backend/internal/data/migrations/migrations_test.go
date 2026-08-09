package migrations

import (
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func testDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set; database-backed migration test not run")
	}
	if err := Up(dsn); err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestMigrationAndTimescaleMetadata(t *testing.T) {
	db := testDatabase(t)
	var extensionVersion string
	if err := db.Raw("SELECT extversion FROM pg_extension WHERE extname = 'timescaledb'").Scan(&extensionVersion).Error; err != nil {
		t.Fatal(err)
	}
	if extensionVersion == "" {
		t.Fatal("timescaledb extension is not installed")
	}

	var hypertableCount int64
	if err := db.Raw(`
		SELECT count(*)
		FROM timescaledb_information.hypertables h
		JOIN timescaledb_information.dimensions d
		  ON d.hypertable_schema = h.hypertable_schema
		 AND d.hypertable_name = h.hypertable_name
		WHERE h.hypertable_name = 'power_readings'
		  AND d.column_name = 'recorded_at'
		  AND d.dimension_type = 'Time'`).Scan(&hypertableCount).Error; err != nil {
		t.Fatal(err)
	}
	if hypertableCount != 1 {
		t.Fatalf("power_readings is not a recorded_at hypertable: %d", hypertableCount)
	}

	var btreeGistCount int64
	if err := db.Raw("SELECT count(*) FROM pg_extension WHERE extname = 'btree_gist'").Scan(&btreeGistCount).Error; err != nil {
		t.Fatal(err)
	}
	if btreeGistCount != 1 {
		t.Fatal("btree_gist extension is not installed")
	}

	for _, table := range []string{"admin_binding_operations", "admin_binding_audits"} {
		var exists bool
		if err := db.Raw("SELECT to_regclass(?) IS NOT NULL", table).Scan(&exists).Error; err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("required persistence table %s is missing", table)
		}
	}

	var macLength int64
	if err := db.Raw(`
		SELECT character_maximum_length
		FROM information_schema.columns
		WHERE table_name = 'admin_binding_audits' AND column_name = 'device_mac'`).Scan(&macLength).Error; err != nil {
		t.Fatal(err)
	}
	if macLength != 12 {
		t.Fatalf("audit MAC snapshot length=%d, want 12", macLength)
	}

	var operationIDIndexCount int64
	if err := db.Raw(`
		SELECT count(*)
		FROM pg_indexes
		WHERE tablename = 'admin_binding_operations'
		  AND indexname = 'admin_binding_operations_operation_id_idx'`).Scan(&operationIDIndexCount).Error; err != nil {
		t.Fatal(err)
	}
	if operationIDIndexCount != 0 {
		t.Fatal("redundant plain operation_id index still exists")
	}
}

func TestDatabaseIdentityAndConstraints(t *testing.T) {
	db := testDatabase(t)
	suffix := uuid.NewString()
	shopCode := "migration-" + suffix[:8]
	hex := strings.ReplaceAll(suffix, "-", "")
	macA := strings.ToUpper(hex[:12])
	macB := strings.ToUpper(hex[12:24])

	var shopID int64
	if err := db.Raw(`INSERT INTO shops (code, name) VALUES (?, ?) RETURNING id`, shopCode, "Migration Test Shop").Scan(&shopID).Error; err != nil {
		t.Fatal(err)
	}
	var deviceA, deviceB int64
	if err := db.Raw(`INSERT INTO devices (shop_id, mac_address, name) VALUES (?, ?, ?) RETURNING id`, shopID, macA, "device-a").Scan(&deviceA).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw(`INSERT INTO devices (shop_id, mac_address, name) VALUES (?, ?, ?) RETURNING id`, shopID, macB, "device-b").Scan(&deviceB).Error; err != nil {
		t.Fatal(err)
	}
	serial := "MIG-SERIAL-" + suffix
	if err := db.Exec(`UPDATE devices SET serial_number = ? WHERE id = ?`, serial, deviceA).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO devices (shop_id, mac_address, serial_number, name) VALUES (?, ?, ?, ?)`, shopID, "223344556677", serial, "duplicate-serial").Error; err == nil {
		t.Fatal("database accepted a duplicate serial number")
	}
	// Nullable legacy serial ownership remains compatible with existing rows.
	var legacyDevice uint
	if err := db.Raw(`INSERT INTO devices (shop_id, mac_address, serial_number, name) VALUES (?, ?, NULL, ?) RETURNING id`, shopID, "334455667788", "legacy-no-serial").Scan(&legacyDevice).Error; err != nil {
		t.Fatal("database rejected a legacy NULL serial: ", err)
	}
	defer func() {
		db.Exec("DELETE FROM power_readings WHERE device_id IN (?, ?, ?)", deviceA, deviceB, legacyDevice)
		db.Exec("DELETE FROM telemetry_ingest_keys WHERE device_id IN (?, ?)", deviceA, deviceB)
		db.Exec("DELETE FROM device_assignments WHERE device_id IN (?, ?)", deviceA, deviceB)
		db.Exec("DELETE FROM measurement_points WHERE shop_id = ?", shopID)
		db.Exec("DELETE FROM devices WHERE id IN (?, ?, ?)", deviceA, deviceB, legacyDevice)
		db.Exec("DELETE FROM shops WHERE id = ?", shopID)
	}()

	colonMAC := fmt.Sprintf("%s:%s:%s:%s:%s:%s", macA[0:2], macA[2:4], macA[4:6], macA[6:8], macA[8:10], macA[10:12])
	if err := db.Exec(`INSERT INTO devices (shop_id, mac_address, name) VALUES (?, ?, ?)`, shopID, colonMAC, "duplicate-colon").Error; err == nil {
		t.Fatal("database accepted a non-canonical MAC")
	}
	if err := db.Exec(`INSERT INTO devices (shop_id, mac_address, name) VALUES (?, ?, ?)`, shopID, macA, "duplicate-canonical").Error; err == nil {
		t.Fatal("database accepted a canonical MAC duplicate")
	}
	var mp1Text, mp2Text string
	if err := db.Raw(`INSERT INTO measurement_points (shop_id, name) VALUES (?, ?) RETURNING id`, shopID, "MP-001").Scan(&mp1Text).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw(`INSERT INTO measurement_points (shop_id, name) VALUES (?, ?) RETURNING id`, shopID, "MP-002").Scan(&mp2Text).Error; err != nil {
		t.Fatal(err)
	}
	mp1, err := uuid.Parse(mp1Text)
	if err != nil {
		t.Fatal(err)
	}
	mp2, err := uuid.Parse(mp2Text)
	if err != nil {
		t.Fatal(err)
	}

	start := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	if err := db.Exec(`INSERT INTO device_assignments (device_id, measurement_point_id, valid_from, valid_to) VALUES (?, ?, ?, ?)`, deviceA, mp1, start, end).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO device_assignments (device_id, measurement_point_id, valid_from, valid_to) VALUES (?, ?, ?, ?)`, deviceA, mp2, end, end.Add(time.Hour)).Error; err != nil {
		t.Fatal("adjacent device assignments should be allowed: ", err)
	}
	// Replacement keeps the same logical measurement-point UUID while the
	// physical device changes after the prior assignment has ended.
	if err := db.Exec(`INSERT INTO device_assignments (device_id, measurement_point_id, valid_from, valid_to) VALUES (?, ?, ?, ?)`, deviceB, mp1, end.Add(2*time.Hour), end.Add(3*time.Hour)).Error; err != nil {
		t.Fatal("device replacement at the same measurement point should be allowed: ", err)
	}
	if err := db.Exec(`INSERT INTO device_assignments (device_id, measurement_point_id, valid_from) VALUES (?, ?, ?)`, deviceB, mp1, start.Add(30*time.Minute)).Error; err == nil {
		t.Fatal("overlapping measurement-point assignments were accepted")
	}
	if err := db.Exec(`INSERT INTO device_assignments (device_id, measurement_point_id, valid_from, valid_to) VALUES (?, ?, ?, ?)`, deviceA, mp1, start.Add(30*time.Minute), start.Add(45*time.Minute)).Error; err == nil {
		t.Fatal("overlapping device assignments were accepted")
	}

	for _, key := range []struct {
		device int64
		boot   int
		seq    int
	}{{deviceA, 1, 1}, {deviceB, 1, 1}} {
		if err := db.Exec(`INSERT INTO telemetry_ingest_keys (device_id, boot_counter, sequence) VALUES (?, ?, ?)`, key.device, key.boot, key.seq).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Exec(`INSERT INTO telemetry_ingest_keys (device_id, boot_counter, sequence) VALUES (?, ?, ?)`, deviceA, 1, 1).Error; err == nil {
		t.Fatal("duplicate telemetry ingest key was accepted")
	}

	recordedAt := start.Add(5 * time.Minute)
	if err := db.Exec(`
		INSERT INTO power_readings
		(time, recorded_at, received_at, measurement_point_id, device_id, voltage, current, active_power, kwh_total, boot_counter, sequence)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, recordedAt, recordedAt, recordedAt.Add(time.Minute), mp1, deviceA, 110, 1.2, 132, 12.3, 1, 1).Error; err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Raw(`SELECT count(*) FROM power_readings WHERE measurement_point_id = ? AND device_id = ? AND recorded_at = ?`, mp1, deviceA, recordedAt).Scan(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("power reading query returned %d rows", count)
	}
}

func migrationRollbackDatabase(t *testing.T) (*gorm.DB, string) {
	t.Helper()
	dsn := os.Getenv("TEST_MIGRATION_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_MIGRATION_DATABASE_URL is not set; rollback test requires a dedicated disposable database")
	}
	if err := Up(dsn); err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("TRUNCATE admin_binding_audits, admin_binding_operations").Error; err != nil {
		t.Fatal(err)
	}
	return db, dsn
}

func ensureMigrationRollbackActor(t *testing.T, db *gorm.DB) uint {
	t.Helper()
	var actorID uint
	if err := db.Raw("SELECT id FROM users ORDER BY id LIMIT 1").Scan(&actorID).Error; err != nil {
		t.Fatal(err)
	}
	if actorID != 0 {
		return actorID
	}
	if err := db.Raw(`INSERT INTO users (account, password_hash, name) VALUES (?, ?, ?) RETURNING id`, "migration-actor-"+uuid.NewString()[:8], "test-hash", "Migration Actor").Scan(&actorID).Error; err != nil {
		t.Fatal(err)
	}
	return actorID
}

func insertMigrationRollbackOperation(t *testing.T, db *gorm.DB, actorID uint, scope, operation string) uuid.UUID {
	t.Helper()
	operationID := uuid.New()
	hash := sha256.Sum256([]byte(operationID.String()))
	if err := db.Exec(`
		INSERT INTO admin_binding_operations
		(operation_id, idempotency_key, operation, scope_key, actor_id, canonical_request_hash)
		VALUES (?, ?, ?, ?, ?, ?)`, operationID, "migration-rollback-"+uuid.NewString(), operation, scope, actorID, hash[:]).Error; err != nil {
		t.Fatal(err)
	}
	return operationID
}

func waitForPendingLock(t *testing.T, db *gorm.DB, mode string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var count int64
		if err := db.Raw(`
			SELECT count(*)
			FROM pg_locks lock
			JOIN pg_class relation ON relation.oid = lock.relation
			WHERE relation.relname IN ('admin_binding_audits', 'admin_binding_operations')
			  AND lock.mode = ?
			  AND NOT lock.granted`, mode).Scan(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for pending %s", mode)
}

func restoreMigrationVersionFour(t *testing.T, db *gorm.DB, dsn string) {
	t.Helper()
	if err := db.Exec("TRUNCATE admin_binding_audits, admin_binding_operations").Error; err != nil {
		t.Fatal(err)
	}
	if err := Down(dsn); err != nil {
		t.Fatal(err)
	}
	if err := Up(dsn); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationRollbackOnEmptyDatabase(t *testing.T) {
	dsn := os.Getenv("TEST_MIGRATION_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_MIGRATION_DATABASE_URL is not set; rollback test requires a dedicated disposable database")
	}
	if err := Up(dsn); err != nil {
		t.Fatal(err)
	}
	if err := Down(dsn); err != nil {
		t.Fatal(err)
	}
	if err := Up(dsn); err != nil {
		t.Fatal(err)
	}
	version, dirty, err := Version(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if dirty || version != 4 {
		t.Fatal(fmt.Sprintf("unexpected migration state version=%d dirty=%t", version, dirty))
	}
}

func TestMigrationRollbackFailsClosedForAuditHistory(t *testing.T) {
	db, dsn := migrationRollbackDatabase(t)
	actorID := ensureMigrationRollbackActor(t, db)
	scope := "shop:migration"
	operationID := insertMigrationRollbackOperation(t, db, actorID, scope, "bind")
	if err := db.Exec(`
		INSERT INTO admin_binding_audits (operation_id, request_identity, actor_id, scope_key, action)
		VALUES (?, ?, ?, ?, ?)`, operationID, "migration-audit", actorID, scope, "bind").Error; err != nil {
		t.Fatal(err)
	}

	if downErr := Down(dsn); downErr == nil {
		t.Fatal("DOWN succeeded with durable audit history")
	}
	var auditCount, operationCount int64
	if err := db.Raw("SELECT count(*) FROM admin_binding_audits").Scan(&auditCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw("SELECT count(*) FROM admin_binding_operations").Scan(&operationCount).Error; err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 || operationCount != 1 {
		t.Fatalf("fail-closed rollback lost history: audits=%d operations=%d", auditCount, operationCount)
	}
	version, dirty, versionErr := Version(dsn)
	if versionErr != nil || dirty || version != 4 {
		t.Fatalf("guarded DOWN left unexpected migration state: version=%d dirty=%t err=%v", version, dirty, versionErr)
	}
	restoreMigrationVersionFour(t, db, dsn)
}

func TestMigrationRollbackFailsClosedForOperationHistory(t *testing.T) {
	db, dsn := migrationRollbackDatabase(t)
	actorID := ensureMigrationRollbackActor(t, db)
	insertMigrationRollbackOperation(t, db, actorID, "shop:migration", "bind")

	if err := Down(dsn); err == nil {
		t.Fatal("DOWN succeeded with durable operation history")
	}
	var operationCount int64
	if err := db.Raw("SELECT count(*) FROM admin_binding_operations").Scan(&operationCount).Error; err != nil {
		t.Fatal(err)
	}
	if operationCount != 1 {
		t.Fatalf("fail-closed rollback lost operation history: operations=%d", operationCount)
	}
	restoreMigrationVersionFour(t, db, dsn)
}

func TestMigrationRollbackProtectsConcurrentWriter(t *testing.T) {
	db, dsn := migrationRollbackDatabase(t)
	actorID := ensureMigrationRollbackActor(t, db)

	blocker := db.Begin()
	if err := blocker.Exec("LOCK TABLE admin_binding_operations, admin_binding_audits IN SHARE ROW EXCLUSIVE MODE").Error; err != nil {
		blocker.Rollback()
		t.Fatal(err)
	}

	downResult := make(chan error, 1)
	go func() { downResult <- Down(dsn) }()
	waitForPendingLock(t, db, "AccessExclusiveLock")

	writerResult := make(chan error, 1)
	go func() {
		writerResult <- db.Exec(`
			INSERT INTO admin_binding_operations
			(operation_id, idempotency_key, operation, scope_key, actor_id, canonical_request_hash)
			VALUES (?, ?, ?, ?, ?, ?)`, uuid.New(), "concurrent-writer-"+uuid.NewString(), "bind", "shop:migration", actorID, make([]byte, 32)).Error
	}()
	waitForPendingLock(t, db, "RowExclusiveLock")

	if err := blocker.Commit().Error; err != nil {
		t.Fatal(err)
	}
	if err := <-downResult; err != nil {
		t.Fatal("empty protected rollback failed:", err)
	}
	if err := <-writerResult; err == nil {
		t.Fatal("concurrent writer inserted after protected rollback dropped its table")
	}

	var auditsExists, operationsExists bool
	if err := db.Raw("SELECT to_regclass('admin_binding_audits') IS NOT NULL").Scan(&auditsExists).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw("SELECT to_regclass('admin_binding_operations') IS NOT NULL").Scan(&operationsExists).Error; err != nil {
		t.Fatal(err)
	}
	if auditsExists || operationsExists {
		t.Fatal("protected empty rollback did not drop both tables")
	}
	if err := Up(dsn); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationRollbackMatchesOperationAuditLockOrder(t *testing.T) {
	db, dsn := migrationRollbackDatabase(t)
	actorID := ensureMigrationRollbackActor(t, db)
	scope := "shop:migration"
	writer := db.Begin()
	operationID := insertMigrationRollbackOperation(t, writer, actorID, scope, "bind")

	downResult := make(chan error, 1)
	go func() { downResult <- Down(dsn) }()
	waitForPendingLock(t, db, "AccessExclusiveLock")

	if err := writer.Exec(`
		INSERT INTO admin_binding_audits (operation_id, request_identity, actor_id, scope_key, action)
		VALUES (?, ?, ?, ?, ?)`, operationID, "lock-order-audit", actorID, scope, "bind").Error; err != nil {
		writer.Rollback()
		t.Fatal(err)
	}
	if err := writer.Commit().Error; err != nil {
		t.Fatal(err)
	}
	if err := <-downResult; err == nil {
		t.Fatal("DOWN unexpectedly succeeded while writer committed durable history")
	}

	var auditCount, operationCount int64
	if err := db.Raw("SELECT count(*) FROM admin_binding_audits").Scan(&auditCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw("SELECT count(*) FROM admin_binding_operations").Scan(&operationCount).Error; err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 || operationCount != 1 {
		t.Fatalf("lock-order rollback lost committed writer rows: audits=%d operations=%d", auditCount, operationCount)
	}
	version, dirty, versionErr := Version(dsn)
	if versionErr != nil || dirty || version != 4 {
		t.Fatalf("lock-order rollback left unexpected migration state: version=%d dirty=%t err=%v", version, dirty, versionErr)
	}
	restoreMigrationVersionFour(t, db, dsn)
}
