package migrations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

func securityMigrationDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_MIGRATION_DATABASE_URL")
	if dsn == "" {
		t.Fatal("TEST_MIGRATION_DATABASE_URL is required for Security Schema Stage A PostgreSQL tests")
	}
	return dsn
}

func securityMigrationDB(t *testing.T, dsn string) *sql.DB {
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

// securityResetToV4 returns the dedicated database to a clean Stage A input
// version through the migration runner. It never bypasses writer admission.
func securityResetToV4(t *testing.T, dsn string) {
	t.Helper()
	if err := Up(dsn); err != nil {
		t.Fatal(err)
	}
	db := securityMigrationDB(t, dsn)
	if _, err := db.Exec(`
		DELETE FROM refresh_tokens;
		DELETE FROM refresh_sessions;
		TRUNCATE admin_binding_audits, admin_binding_operations;
		UPDATE users SET auth_enabled = false;
		UPDATE devices SET inventory_owner_client_id = NULL;
		UPDATE admin_binding_operations SET client_id = NULL`); err != nil {
		t.Fatal(err)
	}
	if err := Down(dsn); err != nil {
		t.Fatal(err)
	}
	version, dirty, err := Version(dsn)
	if err != nil || dirty || version != 4 {
		t.Fatalf("security reset version=%d dirty=%t err=%v", version, dirty, err)
	}
}

func securityAssertMigrationState(t *testing.T, dsn string, want uint) {
	t.Helper()
	version, dirty, err := Version(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if dirty || version != want {
		t.Fatalf("migration state version=%d dirty=%t, want version=%d dirty=false", version, dirty, want)
	}
}

func securityColumn(t *testing.T, db *sql.DB, table, column string) (dataType, udtName, defaultValue, nullable string) {
	t.Helper()
	var result struct {
		DataType     string
		UDTName      string
		DefaultValue sql.NullString
		Nullable     string
	}
	err := db.QueryRow(`
		SELECT data_type, udt_name, column_default, is_nullable
		FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2`, table, column).
		Scan(&result.DataType, &result.UDTName, &result.DefaultValue, &result.Nullable)
	if errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing %s.%s", table, column)
	}
	if err != nil {
		t.Fatal(err)
	}
	return result.DataType, result.UDTName, result.DefaultValue.String, result.Nullable
}

func securityConstraint(t *testing.T, db *sql.DB, name string) (validated bool, deleteAction string, definition string) {
	t.Helper()
	var result struct {
		Validated    bool
		DeleteAction string
		Definition   string
	}
	err := db.QueryRow(`
		SELECT convalidated, confdeltype, pg_get_constraintdef(oid)
		FROM pg_constraint
		WHERE conname = $1`, name).
		Scan(&result.Validated, &result.DeleteAction, &result.Definition)
	if err != nil {
		t.Fatalf("constraint %s: %v", name, err)
	}
	return result.Validated, result.DeleteAction, result.Definition
}

func securityIndexDefinition(t *testing.T, db *sql.DB, name string) string {
	t.Helper()
	var definition string
	if err := db.QueryRow(`
		SELECT indexdef FROM pg_indexes
		WHERE schemaname = current_schema() AND indexname = $1`, name).Scan(&definition); err != nil {
		t.Fatalf("index %s: %v", name, err)
	}
	return definition
}

func securityTableExists(t *testing.T, db *sql.DB, table string) bool {
	t.Helper()
	var exists bool
	if err := db.QueryRow(`SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

func TestSecuritySchemaFoundationCatalogAndLegacySafety(t *testing.T) {
	dsn := securityMigrationDSN(t)
	securityResetToV4(t, dsn)
	db := securityMigrationDB(t, dsn)
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")

	var clientID, compatShopID, ownerShopID, userID, deviceID int64
	if err := db.QueryRow(`INSERT INTO clients (code, name) VALUES ($1, $2) RETURNING id`, "stage-a-client-"+suffix[:12], "Stage A Client").Scan(&clientID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO shops (client_id, code, name) VALUES (NULL, $1, $2) RETURNING id`, "stage-a-null-shop-"+suffix[:12], "Unresolved Shop").Scan(&compatShopID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO shops (client_id, code, name) VALUES ($1, $2, $3) RETURNING id`, clientID, "stage-a-owner-shop-"+suffix[:12], "Owner Shop").Scan(&ownerShopID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO users (account, password_hash, name) VALUES ($1, $2, $3) RETURNING id`, "stage-a-user-"+suffix[:12], "hash-not-a-token", "Stage A User").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO user_shop_relations (user_id, shop_id) VALUES ($1, $2), ($3, $2)`, 9223372036854770000, ownerShopID, userID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO devices (shop_id, mac_address, name) VALUES ($1, $2, $3) RETURNING id`, compatShopID, strings.ToUpper(suffix[:12]), "Stage A Device").Scan(&deviceID); err != nil {
		t.Fatal(err)
	}
	var measurementPointID, assignmentID string
	if err := db.QueryRow(`INSERT INTO measurement_points (shop_id, name) VALUES ($1, $2) RETURNING id`, ownerShopID, "Stage A MP").Scan(&measurementPointID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO device_assignments (device_id, measurement_point_id, valid_from) VALUES ($1, $2, $3) RETURNING id`, deviceID, measurementPointID, time.Now().UTC().Add(-time.Minute)).Scan(&assignmentID); err != nil {
		t.Fatal(err)
	}
	opID := uuid.New()
	hash := sha256.Sum256([]byte("stage-a-" + suffix))
	if _, err := db.Exec(`
		INSERT INTO admin_binding_operations
		(operation_id, idempotency_key, operation, scope_key, actor_id, canonical_request_hash)
		VALUES ($1, $2, 'bind', $3, $4, $5)`, opID, "stage-a-key-"+suffix[:12], "shop:"+suffix[:12], userID, hash[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO admin_binding_audits (operation_id, request_identity, actor_id, scope_key, action)
		VALUES ($1, $2, $3, $4, 'bind')`, opID, "stage-a-request-"+suffix[:12], userID, "shop:"+suffix[:12]); err != nil {
		t.Fatal(err)
	}

	if err := Up(dsn); err != nil {
		t.Fatal(err)
	}
	securityAssertMigrationState(t, dsn, 5)

	dataType, udtName, defaultValue, nullable := securityColumn(t, db, "users", "auth_enabled")
	if dataType != "boolean" || udtName != "bool" || !strings.Contains(defaultValue, "false") || nullable != "NO" {
		t.Fatalf("users.auth_enabled type=%s/%s default=%q nullable=%s", dataType, udtName, defaultValue, nullable)
	}
	var authEnabled bool
	if err := db.QueryRow(`SELECT auth_enabled FROM users WHERE id = $1`, userID).Scan(&authEnabled); err != nil {
		t.Fatal(err)
	}
	if authEnabled {
		t.Fatal("existing user was enabled by Stage A")
	}

	for table, column := range map[string]string{
		"shops":                    "client_id",
		"devices":                  "inventory_owner_client_id",
		"admin_binding_operations": "client_id",
		"admin_binding_audits":     "client_id",
	} {
		dataType, udtName, _, nullable = securityColumn(t, db, table, column)
		if dataType != "bigint" || udtName != "int8" || nullable != "YES" {
			t.Fatalf("%s.%s type=%s/%s nullable=%s", table, column, dataType, udtName, nullable)
		}
	}

	for name, want := range map[string]string{
		"security_shops_client_id_fkey":                        "r",
		"security_devices_inventory_owner_client_id_fkey":      "r",
		"security_user_shop_relations_user_id_fkey":            "r",
		"security_user_shop_relations_shop_id_fkey":            "r",
		"security_admin_binding_operations_client_id_fkey":     "r",
		"security_admin_binding_audits_client_id_fkey":         "r",
		"security_admin_binding_audits_client_provenance_fkey": "r",
	} {
		validated, deleteAction, _ := securityConstraint(t, db, name)
		if validated {
			t.Fatalf("legacy-safe constraint %s unexpectedly validated", name)
		}
		if deleteAction != want {
			t.Fatalf("constraint %s delete action=%q, want %q", name, deleteAction, want)
		}
	}
	for name := range map[string]struct{}{
		"refresh_sessions_user_id_fkey":  {},
		"refresh_tokens_session_id_fkey": {},
	} {
		validated, deleteAction, _ := securityConstraint(t, db, name)
		if !validated || deleteAction != "c" {
			t.Fatalf("refresh FK %s validated=%t delete action=%q", name, validated, deleteAction)
		}
	}

	for _, index := range []string{
		"security_shops_client_id_idx",
		"security_devices_inventory_owner_client_id_idx",
		"security_user_shop_relations_shop_user_idx",
		"refresh_sessions_user_created_idx",
		"refresh_sessions_expires_idx",
		"refresh_tokens_session_issued_idx",
		"refresh_tokens_expires_idx",
		"refresh_tokens_replaced_by_token_idx",
		"refresh_tokens_one_current_per_session_idx",
		"security_admin_binding_operations_client_time_idx",
		"security_admin_binding_audits_client_time_idx",
	} {
		_ = securityIndexDefinition(t, db, index)
	}
	currentIndex := securityIndexDefinition(t, db, "refresh_tokens_one_current_per_session_idx")
	if !strings.Contains(currentIndex, "consumed_at IS NULL") || !strings.Contains(currentIndex, "revoked_at IS NULL") {
		t.Fatal("current refresh-token uniqueness predicate is missing")
	}

	var uniqueExists bool
	if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'user_shop_relations_user_shop_key')`).Scan(&uniqueExists); err != nil {
		t.Fatal(err)
	}
	if !uniqueExists {
		t.Fatal("existing User/Shop uniqueness was removed")
	}
	if _, err := db.Exec(`INSERT INTO user_shop_relations (user_id, shop_id) VALUES ($1, $2)`, userID, ownerShopID); err == nil {
		t.Fatal("duplicate User/Shop relation was accepted")
	}
	if _, err := db.Exec(`INSERT INTO shops (client_id, code, name) VALUES (9223372036854770001, $1, 'invalid')`, "stage-a-invalid-shop-"+suffix[:12]); err == nil {
		t.Fatal("new orphan Shop Client reference was accepted")
	}
	if _, err := db.Exec(`INSERT INTO devices (shop_id, mac_address, name, inventory_owner_client_id) VALUES ($1, $2, 'invalid-owner', 9223372036854770001)`, ownerShopID, strings.ToUpper(suffix[12:24])); err == nil {
		t.Fatal("new orphan inventory owner was accepted")
	}

	var owner sql.NullInt64
	if err := db.QueryRow(`SELECT inventory_owner_client_id FROM devices WHERE id = $1`, deviceID).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner.Valid {
		t.Fatalf("Stage A backfilled inventory ownership from Device.ShopID: %d", owner.Int64)
	}
	var legacyNullShop int64
	if err := db.QueryRow(`SELECT count(*) FROM shops WHERE id = $1 AND client_id IS NULL`, compatShopID).Scan(&legacyNullShop); err != nil {
		t.Fatal(err)
	}
	if legacyNullShop != 1 {
		t.Fatal("Stage A changed unresolved Shop Client ownership")
	}
	var auditClient, operationClient sql.NullInt64
	if err := db.QueryRow(`SELECT client_id FROM admin_binding_audits WHERE id = (SELECT id FROM admin_binding_audits WHERE operation_id = $1)`, opID).Scan(&auditClient); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT client_id FROM admin_binding_operations WHERE operation_id = $1`, opID).Scan(&operationClient); err != nil {
		t.Fatal(err)
	}
	if auditClient.Valid || operationClient.Valid {
		t.Fatal("Stage A fabricated Admin tenant provenance")
	}

	columns, err := db.Query(`SELECT column_name FROM information_schema.columns WHERE table_name = 'refresh_tokens' ORDER BY ordinal_position`)
	if err != nil {
		t.Fatal(err)
	}
	defer columns.Close()
	var refreshColumns []string
	for columns.Next() {
		var name string
		if err := columns.Scan(&name); err != nil {
			t.Fatal(err)
		}
		refreshColumns = append(refreshColumns, name)
	}
	if err := columns.Err(); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"token", "raw_token", "plaintext_token", "refresh_token"} {
		for _, column := range refreshColumns {
			if column == forbidden {
				t.Fatalf("refresh_tokens contains forbidden plaintext column %q", column)
			}
		}
	}
	var hashColumnType string
	if err := db.QueryRow(`SELECT udt_name FROM information_schema.columns WHERE table_name = 'refresh_tokens' AND column_name = 'token_hash'`).Scan(&hashColumnType); err != nil {
		t.Fatal(err)
	}
	if hashColumnType != "bytea" {
		t.Fatalf("refresh_tokens.token_hash type=%q, want bytea", hashColumnType)
	}

	// Existing 000004 idempotency uniqueness remains the same after adding the
	// nullable structured Client snapshot.
	if _, err := db.Exec(`
		INSERT INTO admin_binding_operations
		(operation_id, idempotency_key, operation, scope_key, actor_id, canonical_request_hash)
		VALUES ($1, $2, 'bind', $3, $4, $5)`, uuid.New(), "stage-a-key-"+suffix[:12], "shop:"+suffix[:12], userID, hash[:]); err == nil {
		t.Fatal("existing Admin idempotency uniqueness changed")
	}
	matchingOperationID := uuid.New()
	provenanceHash := sha256.Sum256([]byte("stage-a-provenance-" + suffix))
	if _, err := db.Exec(`
		INSERT INTO admin_binding_operations
		(operation_id, idempotency_key, operation, scope_key, actor_id, client_id, canonical_request_hash)
		VALUES ($1, $2, 'bind', $3, $4, $5, $6)`, matchingOperationID, "stage-a-provenance-key-"+suffix[:12], "shop:"+suffix[:12], userID, clientID, provenanceHash[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO admin_binding_audits (operation_id, request_identity, actor_id, scope_key, action, client_id)
		VALUES ($1, $2, $3, $4, 'bind', $5)`, matchingOperationID, "stage-a-provenance-request-"+suffix[:12], userID, "shop:"+suffix[:12], clientID); err != nil {
		t.Fatal("matching non-NULL Admin provenance was rejected:", err)
	}
	provenanceOperationID := uuid.New()
	if _, err := db.Exec(`
		INSERT INTO admin_binding_operations
		(operation_id, idempotency_key, operation, scope_key, actor_id, client_id, canonical_request_hash)
		VALUES ($1, $2, 'bind', $3, $4, $5, $6)`, provenanceOperationID, "stage-a-provenance-mismatch-key-"+suffix[:12], "shop:"+suffix[:12], userID, clientID, hash[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO admin_binding_audits (operation_id, request_identity, actor_id, scope_key, action)
		VALUES ($1, $2, $3, $4, 'bind')`, provenanceOperationID, "stage-a-provenance-mismatch-request-"+suffix[:12], userID, "shop:"+suffix[:12]); err == nil {
		t.Fatal("Admin operation/audit provenance allowed a NULL/non-NULL Client mismatch")
	}

	// Existing Admin audits are immutable; the dedicated migration database is
	// reset as a test fixture with TRUNCATE rather than mutating an audit row.
	if _, err := db.Exec(`TRUNCATE admin_binding_audits, admin_binding_operations`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM device_assignments WHERE id = $1`, assignmentID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM measurement_points WHERE id = $1`, measurementPointID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM user_shop_relations WHERE user_id IN (9223372036854770000, $1)`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM devices WHERE id = $1`, deviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM users WHERE id = $1`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM shops WHERE id IN ($1, $2)`, compatShopID, ownerShopID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM clients WHERE id = $1`, clientID); err != nil {
		t.Fatal(err)
	}
}

func TestSecuritySchemaFoundationRefreshHashAndFamilySemantics(t *testing.T) {
	dsn := securityMigrationDSN(t)
	securityResetToV4(t, dsn)
	if err := Up(dsn); err != nil {
		t.Fatal(err)
	}
	db := securityMigrationDB(t, dsn)
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	var userID int64
	if err := db.QueryRow(`INSERT INTO users (account, password_hash, name) VALUES ($1, 'hash', 'Refresh User') RETURNING id`, "refresh-user-"+suffix[:12]).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM users WHERE id = $1`, userID)

	sessionID := uuid.New()
	issued := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := db.Exec(`INSERT INTO refresh_sessions (id, user_id, created_at, expires_at) VALUES ($1, $2, $3, $4)`, sessionID, userID, issued, issued.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	hashA := sha256.Sum256([]byte("opaque-refresh-a-" + suffix))
	hashB := sha256.Sum256([]byte("opaque-refresh-b-" + suffix))
	tokenA := uuid.New()
	if _, err := db.Exec(`INSERT INTO refresh_tokens (id, session_id, token_hash, issued_at, expires_at) VALUES ($1, $2, $3, $4, $5)`, tokenA, sessionID, hashA[:], issued, issued.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO refresh_tokens (id, session_id, token_hash, issued_at, expires_at) VALUES ($1, $2, $3, $4, $5)`, uuid.New(), sessionID, hashA[:], issued, issued.Add(time.Hour)); err == nil {
		t.Fatal("duplicate refresh-token hash was accepted")
	}
	if _, err := db.Exec(`INSERT INTO refresh_tokens (id, session_id, token_hash, issued_at, expires_at) VALUES ($1, $2, $3, $4, $5)`, uuid.New(), sessionID, make([]byte, 31), issued, issued.Add(time.Hour)); err == nil {
		t.Fatal("refresh-token hash with the wrong length was accepted")
	}
	if _, err := db.Exec(`INSERT INTO refresh_tokens (id, session_id, token_hash, issued_at, expires_at) VALUES ($1, $2, $3, $4, $5)`, uuid.New(), sessionID, hashB[:], issued, issued.Add(time.Hour)); err == nil {
		t.Fatal("two current refresh tokens in one family were accepted")
	}
	if _, err := db.Exec(`UPDATE refresh_tokens SET consumed_at = $1 WHERE id = $2`, issued.Add(10*time.Minute), tokenA); err != nil {
		t.Fatal(err)
	}
	tokenB := uuid.New()
	if _, err := db.Exec(`INSERT INTO refresh_tokens (id, session_id, token_hash, issued_at, expires_at) VALUES ($1, $2, $3, $4, $5)`, tokenB, sessionID, hashB[:], issued.Add(10*time.Minute), issued.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE refresh_tokens SET replaced_by_token_id = $1 WHERE id = $2`, tokenB, tokenA); err != nil {
		t.Fatal(err)
	}
	secondSession := uuid.New()
	if _, err := db.Exec(`INSERT INTO refresh_sessions (id, user_id, created_at, expires_at) VALUES ($1, $2, $3, $4)`, secondSession, userID, issued, issued.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	hashC := sha256.Sum256([]byte("opaque-refresh-c-" + suffix))
	secondToken := uuid.New()
	if _, err := db.Exec(`INSERT INTO refresh_tokens (id, session_id, token_hash, issued_at, expires_at) VALUES ($1, $2, $3, $4, $5)`, secondToken, secondSession, hashC[:], issued, issued.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE refresh_tokens SET replaced_by_token_id = $1 WHERE id = $2`, secondToken, tokenA); err == nil {
		t.Fatal("refresh-token rotation linked two different session families")
	}
	var replacedBy uuid.UUID
	if err := db.QueryRow(`SELECT replaced_by_token_id FROM refresh_tokens WHERE id = $1`, tokenA).Scan(&replacedBy); err != nil {
		t.Fatal(err)
	}
	if replacedBy != tokenB {
		t.Fatalf("replacement link=%s, want %s", replacedBy, tokenB)
	}
}

func TestSecuritySchemaFoundationDownReupAndWriterAdmission(t *testing.T) {
	dsn := securityMigrationDSN(t)
	securityResetToV4(t, dsn)
	db := securityMigrationDB(t, dsn)
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	var shopID int64
	if err := db.QueryRow(`INSERT INTO shops (code, name) VALUES ($1, 'v4 preservation shop') RETURNING id`, "stage-a-down-"+suffix[:12]).Scan(&shopID); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM shops WHERE id = $1`, shopID)

	shared, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := AcquireSharedWriterFence(context.Background(), shared); err != nil {
		shared.Rollback()
		t.Fatal(err)
	}
	upResult := make(chan error, 1)
	go func() { upResult <- Up(dsn) }()
	select {
	case err := <-upResult:
		t.Fatalf("000005 Up crossed shared writer admission: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := shared.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-upResult; err != nil {
		t.Fatal(err)
	}
	securityAssertMigrationState(t, dsn, 5)

	shared, err = db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := AcquireSharedWriterFence(context.Background(), shared); err != nil {
		shared.Rollback()
		t.Fatal(err)
	}
	downResult := make(chan error, 1)
	go func() { downResult <- Down(dsn) }()
	select {
	case err := <-downResult:
		t.Fatalf("000005 Down crossed shared writer admission: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := shared.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-downResult; err != nil {
		t.Fatal(err)
	}
	securityAssertMigrationState(t, dsn, 4)
	if !securityTableExists(t, db, "admin_binding_operations") || !securityTableExists(t, db, "user_shop_relations") {
		t.Fatal("v4 objects were removed by Stage A DOWN")
	}
	var preserved int64
	if err := db.QueryRow(`SELECT count(*) FROM shops WHERE id = $1`, shopID).Scan(&preserved); err != nil {
		t.Fatal(err)
	}
	if preserved != 1 {
		t.Fatal("pre-existing v4 Shop data was not preserved")
	}
	if securityTableExists(t, db, "refresh_sessions") || securityTableExists(t, db, "refresh_tokens") {
		t.Fatal("Stage A refresh tables survived DOWN")
	}
	if err := Up(dsn); err != nil {
		t.Fatal(err)
	}
	securityAssertMigrationState(t, dsn, 5)
}

func TestSecuritySchemaFoundationGuardedDownPreservesRefreshState(t *testing.T) {
	dsn := securityMigrationDSN(t)
	securityResetToV4(t, dsn)
	if err := Up(dsn); err != nil {
		t.Fatal(err)
	}
	db := securityMigrationDB(t, dsn)
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	var userID int64
	if err := db.QueryRow(`INSERT INTO users (account, password_hash, name) VALUES ($1, 'hash', 'Guard User') RETURNING id`, "guard-user-"+suffix[:12]).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	var sessionID uuid.UUID
	if err := db.QueryRow(`INSERT INTO refresh_sessions (user_id, expires_at) VALUES ($1, now() + interval '1 day') RETURNING id`, userID).Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("guard-token-" + suffix))
	if _, err := db.Exec(`INSERT INTO refresh_tokens (session_id, token_hash, expires_at) VALUES ($1, $2, now() + interval '1 hour')`, sessionID, hash[:]); err != nil {
		t.Fatal(err)
	}
	if err := Down(dsn); !errors.Is(err, ErrGuardedDown) {
		t.Fatalf("expected guarded v5 DOWN, got %v", err)
	}
	securityAssertMigrationState(t, dsn, 5)
	var tokenCount int64
	if err := db.QueryRow(`SELECT count(*) FROM refresh_tokens`).Scan(&tokenCount); err != nil {
		t.Fatal(err)
	}
	if tokenCount != 1 {
		t.Fatal("guarded DOWN removed refresh-token state")
	}
	if _, err := db.Exec(`DELETE FROM refresh_tokens`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM refresh_sessions`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM users WHERE id = $1`, userID); err != nil {
		t.Fatal(err)
	}
	if err := Down(dsn); err != nil {
		t.Fatal(err)
	}
	securityAssertMigrationState(t, dsn, 4)
	if err := Up(dsn); err != nil {
		t.Fatal(err)
	}
	securityAssertMigrationState(t, dsn, 5)
}

func TestSecuritySchemaFoundationFreshDatabase(t *testing.T) {
	baseDSN := securityMigrationDSN(t)
	schema := "stage_a_fresh_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	baseDB := securityMigrationDB(t, baseDSN)
	if _, err := baseDB.Exec(`CREATE SCHEMA "` + schema + `"`); err != nil {
		t.Fatal(err)
	}
	defer baseDB.Exec(`DROP SCHEMA "` + schema + `" CASCADE`)

	parsed, err := url.Parse(baseDSN)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("options", "-csearch_path="+schema+",public")
	parsed.RawQuery = query.Encode()
	freshDSN := parsed.String()
	if err := Up(freshDSN); err != nil {
		t.Fatal(err)
	}
	securityAssertMigrationState(t, freshDSN, 5)
	freshDB := securityMigrationDB(t, freshDSN)
	for _, table := range []string{"users", "shops", "devices", "user_shop_relations", "refresh_sessions", "refresh_tokens", "admin_binding_operations", "admin_binding_audits"} {
		if !securityTableExists(t, freshDB, table) {
			t.Fatalf("fresh migration did not create %s", table)
		}
	}
}

func TestSecuritySchemaFoundationCatalogProofIsVerboseDatabaseTest(t *testing.T) {
	// This named smoke test intentionally performs a catalog query so the
	// focused -v run has an unmistakable non-skipped database case.
	dsn := securityMigrationDSN(t)
	if err := Up(dsn); err != nil {
		t.Fatal(err)
	}
	db := securityMigrationDB(t, dsn)
	var version int
	var dirty bool
	if err := db.QueryRow(`SELECT version, dirty FROM schema_migrations`).Scan(&version, &dirty); err != nil {
		t.Fatal(err)
	}
	if version != 5 || dirty {
		t.Fatalf("schema_migrations version=%d dirty=%t", version, dirty)
	}
	var columns int
	if err := db.QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_name IN ('refresh_sessions', 'refresh_tokens')`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if columns < 13 {
		t.Fatalf("refresh schema catalog columns=%d, want at least 13", columns)
	}
}
