package migrations

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

func preflightDatabase(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dsn := os.Getenv("TEST_MIGRATION_DATABASE_URL")
	if dsn == "" {
		t.Fatal("TEST_MIGRATION_DATABASE_URL is required for Security Schema preflight PostgreSQL tests")
	}
	if err := Up(dsn); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db, dsn
}

func TestSecuritySchemaPreflightCleanSnapshotIsReadOnly(t *testing.T) {
	db, dsn := preflightDatabase(t)
	defer db.Close()

	before := preflightCounts(t, db)
	result, err := RunSecuritySchemaPreflight(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	if result.ObservedAt.IsZero() || result.ObservedAt.Location() != time.UTC {
		t.Fatalf("ObservedAt=%v, want a non-zero UTC timestamp", result.ObservedAt)
	}
	if result.SnapshotMode != SnapshotRepeatableReadOnly || !result.ReadOnlyVerified {
		t.Fatalf("snapshot mode=%q read_only=%t, want repeatable-read/read-only", result.SnapshotMode, result.ReadOnlyVerified)
	}
	if result.Migration.ExpectedVersion != 4 || result.Migration.ActualVersion != 4 || result.Migration.Dirty {
		t.Fatalf("unexpected migration state: %+v", result.Migration)
	}
	if result.WriterFence != WriterFenceRequiresMigrationOrchestration {
		t.Fatalf("writer fence=%q, want orchestration requirement", result.WriterFence)
	}
	if result.AccountEligibility != RepresentationNotRepresented || result.DeviceOwnerAuthority != RepresentationNotRepresented {
		t.Fatalf("legacy authority representation was fabricated: auth=%q owner=%q", result.AccountEligibility, result.DeviceOwnerAuthority)
	}
	if got := preflightCounts(t, db); got != before {
		t.Fatalf("preflight changed application/schema counts: before=%+v after=%+v", before, got)
	}
}

func TestSecuritySchemaPreflightRejectsDirtyAndMultipleMigrationMetadata(t *testing.T) {
	db, dsn := preflightDatabase(t)
	defer db.Close()

	execPreflight(t, db, `UPDATE schema_migrations SET dirty = true WHERE version = 4`)
	result, err := RunSecuritySchemaPreflight(context.Background(), dsn)
	if !errors.Is(err, ErrDirtyMigrationState) || result.Migration.MetadataRowCount != 1 || !result.Migration.Dirty {
		t.Fatalf("dirty migration result=%+v err=%v", result.Migration, err)
	}
	execPreflight(t, db, `UPDATE schema_migrations SET dirty = false WHERE version = 4`)

	execPreflight(t, db, `INSERT INTO schema_migrations (version, dirty) VALUES (5, true)`)
	defer execPreflight(t, db, `DELETE FROM schema_migrations WHERE version = 5`)
	result, err = RunSecuritySchemaPreflight(context.Background(), dsn)
	if !errors.Is(err, ErrMigrationMetadataCardinality) || result.Migration.MetadataRowCount != 2 {
		t.Fatalf("multiple migration metadata result=%+v err=%v", result.Migration, err)
	}
	if result.Disposition() != PreflightBlockingIntegrity {
		t.Fatalf("multiple migration metadata disposition=%q", result.Disposition())
	}
}

func TestSecuritySchemaPreflightClassifiesLegacyIntegrityAndProvenance(t *testing.T) {
	db, dsn := preflightDatabase(t)
	defer db.Close()
	fixture := createPreflightFixture(t, db)
	defer fixture.cleanup(t, db)

	result, err := RunSecuritySchemaPreflight(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition() != PreflightBlockingIntegrity {
		t.Fatalf("Disposition()=%q, want blocking integrity: %+v", result.Disposition(), result)
	}

	shopFacts := indexShopFacts(result.ShopClient.Facts)
	if shopFacts[fixture.validShop].Disposition != PreflightReadyFacts {
		t.Fatalf("valid Shop fact=%+v", shopFacts[fixture.validShop])
	}
	if shopFacts[fixture.nullClientShop].Disposition != PreflightBlockingIntegrity || shopFacts[fixture.nullClientShop].Reason != "null_client_id" {
		t.Fatalf("NULL client Shop fact=%+v", shopFacts[fixture.nullClientShop])
	}
	if shopFacts[fixture.orphanClientShop].Disposition != PreflightBlockingIntegrity || shopFacts[fixture.orphanClientShop].Reason != "orphan_client_id" {
		t.Fatalf("orphan client Shop fact=%+v", shopFacts[fixture.orphanClientShop])
	}

	membershipFacts := indexMembershipFacts(result.Membership.Facts)
	for _, relationID := range []int64{fixture.orphanUserRelation, fixture.orphanShopRelation, fixture.bothOrphanRelation, fixture.invalidClientRelation} {
		if membershipFacts[relationID].Disposition == PreflightReadyFacts {
			t.Fatalf("invalid membership was classified ready: %+v", membershipFacts[relationID])
		}
	}
	if membershipFacts[fixture.validRelation].Disposition != PreflightReadyFacts || membershipFacts[fixture.validRelation].ShopRole != "staff" {
		t.Fatalf("valid membership fact=%+v", membershipFacts[fixture.validRelation])
	}
	if result.Membership.DuplicateLogicalMembershipCount != 0 {
		t.Fatalf("duplicate memberships=%d despite current unique constraint", result.Membership.DuplicateLogicalMembershipCount)
	}

	currentShopFacts := indexCurrentShopFacts(result.CurrentShop.Facts)
	if currentShopFacts[fixture.staleCurrentShopUser].Disposition != PreflightBlockingIntegrity {
		t.Fatalf("stale current_shop_id fact=%+v", currentShopFacts[fixture.staleCurrentShopUser])
	}
	if currentShopFacts[fixture.nullCurrentShopUser].Disposition != PreflightReadyFacts || currentShopFacts[fixture.nullCurrentShopUser].MembershipExists {
		t.Fatalf("NULL current_shop_id fact=%+v", currentShopFacts[fixture.nullCurrentShopUser])
	}

	deviceFacts := indexDeviceFacts(result.Devices.Facts)
	candidate := deviceFacts[fixture.candidateDevice]
	if candidate.Classification != DeviceOwnerCandidate || candidate.CandidateClientID == nil || *candidate.CandidateClientID != fixture.validClient {
		t.Fatalf("valid assignment chain was not classified as owner candidate: %+v", candidate)
	}
	if candidate.CompatibilityShopReferenceUsed {
		t.Fatal("Device.ShopID was used for owner inference")
	}
	if deviceFacts[fixture.unassignedDevice].Classification != DeviceManualMappingRequired || deviceFacts[fixture.unassignedDevice].Reason != "no_assignment_history" {
		t.Fatalf("unassigned device was not classified as no-history manual mapping: %+v", deviceFacts[fixture.unassignedDevice])
	}
	if deviceFacts[fixture.historicalOnlyDevice].Classification != DeviceManualMappingRequired || deviceFacts[fixture.historicalOnlyDevice].Reason != "historical_only_assignment" {
		t.Fatalf("historical-only device was not classified distinctly: %+v", deviceFacts[fixture.historicalOnlyDevice])
	}
	if deviceFacts[fixture.unresolvedDevice].Classification != DeviceManualMappingRequired {
		t.Fatalf("incomplete assignment chain was not manual mapping required: %+v", deviceFacts[fixture.unresolvedDevice])
	}
	if deviceFacts[fixture.orphanShopDevice].Disposition != PreflightBlockingIntegrity {
		t.Fatalf("orphan compatibility Shop reference was not blocking: %+v", deviceFacts[fixture.orphanShopDevice])
	}
	if deviceFacts[fixture.futureDevice].Disposition != PreflightBlockingIntegrity || deviceFacts[fixture.futureDevice].Reason != "future_open_assignment" {
		t.Fatalf("future assignment was not blocked: %+v", deviceFacts[fixture.futureDevice])
	}

	assignmentFacts := indexAssignmentFacts(result.Assignments.Facts)
	if assignmentFacts[fixture.candidateAssignment].Disposition != PreflightReadyFacts {
		t.Fatalf("candidate assignment fact=%+v", assignmentFacts[fixture.candidateAssignment])
	}
	if assignmentFacts[fixture.unresolvedAssignment].Disposition != PreflightBlockingIntegrity {
		t.Fatalf("incomplete assignment fact=%+v", assignmentFacts[fixture.unresolvedAssignment])
	}
	if assignmentFacts[fixture.historicalAssignment].Active {
		t.Fatal("historical-only assignment was classified active")
	}
	if !assignmentFacts[fixture.futureAssignment].FutureStart || assignmentFacts[fixture.futureAssignment].Disposition != PreflightBlockingIntegrity {
		t.Fatalf("future assignment fact=%+v", assignmentFacts[fixture.futureAssignment])
	}

	userFacts := indexUserFacts(result.Users.Facts)
	for _, userID := range []int64{fixture.authReviewUser, fixture.staleCurrentShopUser} {
		fact := userFacts[userID]
		if !fact.ReviewRequired || fact.AutoApproved {
			t.Fatalf("user %d eligibility was not explicit-review-only: %+v", userID, fact)
		}
	}
	if result.Users.AuthEnabledColumnPresent {
		t.Fatal("preflight invented users.auth_enabled in migration 000004")
	}

	auditFacts := indexProvenanceFacts(result.AuditProvenance.Facts)
	if auditFacts[fixture.validAuditID].Classification != ProvenanceIndependentlyDerived {
		t.Fatalf("valid audit provenance=%+v", auditFacts[fixture.validAuditID])
	}
	if auditFacts[fixture.ambiguousAuditID].Classification != ProvenanceLegacyUnresolved {
		t.Fatalf("ambiguous audit provenance=%+v", auditFacts[fixture.ambiguousAuditID])
	}
	operationFacts := indexProvenanceFacts(result.OperationProvenance.Facts)
	if operationFacts[fixture.validOperationID].Classification != ProvenanceIndependentlyDerived {
		t.Fatalf("valid operation provenance=%+v", operationFacts[fixture.validOperationID])
	}
	if operationFacts[fixture.ambiguousOperationID].Classification != ProvenanceLegacyUnresolved {
		t.Fatalf("ambiguous operation provenance=%+v", operationFacts[fixture.ambiguousOperationID])
	}
	if operationFacts[fixture.noAuditOperationID].Classification != ProvenanceLegacyUnresolved {
		t.Fatalf("operation without audit provenance=%+v", operationFacts[fixture.noAuditOperationID])
	}
}

func TestSecuritySchemaPreflightSnapshotDoesNotClaimWriterFence(t *testing.T) {
	db, dsn := preflightDatabase(t)
	defer db.Close()
	clientID := insertPreflightInt64(t, db, `INSERT INTO clients (code, name) VALUES ($1, $2) RETURNING id`, "preflight-fence-client-"+uniquePreflightSuffix(), "Fence Client")
	defer execPreflight(t, db, `DELETE FROM clients WHERE id = $1`, clientID)

	writer, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	shopCode := "preflight-uncommitted-" + uniquePreflightSuffix()
	var uncommittedShopID int64
	if err := writer.QueryRowContext(context.Background(), `INSERT INTO shops (client_id, code, name) VALUES ($1, $2, $3) RETURNING id`, clientID, shopCode, "Uncommitted Shop").Scan(&uncommittedShopID); err != nil {
		writer.Rollback()
		t.Fatal(err)
	}

	before := preflightCounts(t, db)
	first, err := RunSecuritySchemaPreflight(context.Background(), dsn)
	if err != nil {
		writer.Rollback()
		t.Fatal(err)
	}
	if first.WriterFence != WriterFenceRequiresMigrationOrchestration {
		writer.Rollback()
		t.Fatalf("first writer fence=%q", first.WriterFence)
	}
	if containsShopFact(first.ShopClient.Facts, uncommittedShopID) {
		writer.Rollback()
		t.Fatal("preflight observed an uncommitted writer row")
	}
	if got := preflightCounts(t, db); got != before {
		writer.Rollback()
		t.Fatalf("preflight changed counts: before=%+v after=%+v", before, got)
	}
	if err := writer.Commit(); err != nil {
		t.Fatal(err)
	}
	defer execPreflight(t, db, `DELETE FROM shops WHERE id = $1`, uncommittedShopID)

	second, err := RunSecuritySchemaPreflight(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	if second.WriterFence != WriterFenceRequiresMigrationOrchestration {
		t.Fatalf("second writer fence=%q", second.WriterFence)
	}
	if !containsShopFact(second.ShopClient.Facts, uncommittedShopID) {
		t.Fatal("preflight did not observe committed writer row on rerun")
	}
}

type preflightFixture struct {
	validClient, otherClient, validShop, otherShop, nullClientShop, orphanClientShop                          int64
	validRelation, orphanUserRelation, orphanShopRelation, bothOrphanRelation, invalidClientRelation          int64
	authReviewUser, staleCurrentShopUser, nullCurrentShopUser                                                 int64
	candidateDevice, unassignedDevice, historicalOnlyDevice, unresolvedDevice, orphanShopDevice, futureDevice int64
	candidateMP, unresolvedMP, futureMP                                                                       string
	candidateAssignment, historicalAssignment, unresolvedAssignment, futureAssignment                         string
	validAuditID, ambiguousAuditID                                                                            string
	validOperationID, ambiguousOperationID, noAuditOperationID                                                string
}

func createPreflightFixture(t *testing.T, db *sql.DB) preflightFixture {
	t.Helper()
	f := preflightFixture{}
	f.validClient = insertPreflightInt64(t, db, `INSERT INTO clients (code, name) VALUES ($1, $2) RETURNING id`, "preflight-client-"+uniquePreflightSuffix(), "Preflight Client")
	f.otherClient = insertPreflightInt64(t, db, `INSERT INTO clients (code, name) VALUES ($1, $2) RETURNING id`, "preflight-other-client-"+uniquePreflightSuffix(), "Other Client")
	f.validShop = insertPreflightInt64(t, db, `INSERT INTO shops (client_id, code, name) VALUES ($1, $2, $3) RETURNING id`, f.validClient, "preflight-valid-shop-"+uniquePreflightSuffix(), "Valid Shop")
	f.otherShop = insertPreflightInt64(t, db, `INSERT INTO shops (client_id, code, name) VALUES ($1, $2, $3) RETURNING id`, f.otherClient, "preflight-other-shop-"+uniquePreflightSuffix(), "Other Shop")
	f.nullClientShop = insertPreflightInt64(t, db, `INSERT INTO shops (client_id, code, name) VALUES (NULL, $1, $2) RETURNING id`, "preflight-null-shop-"+uniquePreflightSuffix(), "NULL Client Shop")
	f.orphanClientShop = insertPreflightInt64(t, db, `INSERT INTO shops (client_id, code, name) VALUES ($1, $2, $3) RETURNING id`, int64(987654321), "preflight-orphan-shop-"+uniquePreflightSuffix(), "Orphan Client Shop")

	f.authReviewUser = insertPreflightInt64(t, db, `INSERT INTO users (account, password_hash, name, is_admin) VALUES ($1, $2, $3, true) RETURNING id`, "preflight-auth-"+uniquePreflightSuffix(), "hash-not-returned", "Auth Review")
	f.staleCurrentShopUser = insertPreflightInt64(t, db, `INSERT INTO users (account, password_hash, name, current_shop_id) VALUES ($1, $2, $3, $4) RETURNING id`, "preflight-stale-"+uniquePreflightSuffix(), "hash-not-returned", "Stale Current Shop", int64(987654322))
	f.nullCurrentShopUser = insertPreflightInt64(t, db, `INSERT INTO users (account, password_hash, name) VALUES ($1, $2, $3) RETURNING id`, "preflight-null-current-"+uniquePreflightSuffix(), "hash-not-returned", "Null Current Shop")
	f.validRelation = insertPreflightInt64(t, db, `INSERT INTO user_shop_relations (user_id, shop_id) VALUES ($1, $2) RETURNING id`, f.authReviewUser, f.validShop)
	f.orphanUserRelation = insertPreflightInt64(t, db, `INSERT INTO user_shop_relations (user_id, shop_id) VALUES ($1, $2) RETURNING id`, int64(987654323), f.validShop)
	f.orphanShopRelation = insertPreflightInt64(t, db, `INSERT INTO user_shop_relations (user_id, shop_id) VALUES ($1, $2) RETURNING id`, f.authReviewUser, int64(987654324))
	f.bothOrphanRelation = insertPreflightInt64(t, db, `INSERT INTO user_shop_relations (user_id, shop_id) VALUES ($1, $2) RETURNING id`, int64(987654325), int64(987654326))
	f.invalidClientRelation = insertPreflightInt64(t, db, `INSERT INTO user_shop_relations (user_id, shop_id) VALUES ($1, $2) RETURNING id`, f.nullCurrentShopUser, f.nullClientShop)

	f.candidateMP = insertPreflightUUID(t, db, `INSERT INTO measurement_points (shop_id, name) VALUES ($1, $2) RETURNING id`, f.validShop, "Candidate MP")
	f.unresolvedMP = insertPreflightUUID(t, db, `INSERT INTO measurement_points (shop_id, name) VALUES ($1, $2) RETURNING id`, f.nullClientShop, "Unresolved MP")
	f.futureMP = insertPreflightUUID(t, db, `INSERT INTO measurement_points (shop_id, name) VALUES ($1, $2) RETURNING id`, f.validShop, "Future MP")
	f.candidateDevice = insertPreflightInt64(t, db, `INSERT INTO devices (shop_id, mac_address, name) VALUES ($1, $2, $3) RETURNING id`, f.otherShop, uniquePreflightMAC(), "Candidate Device")
	f.unassignedDevice = insertPreflightInt64(t, db, `INSERT INTO devices (shop_id, mac_address, name) VALUES ($1, $2, $3) RETURNING id`, f.validShop, uniquePreflightMAC(), "Unassigned Device")
	f.historicalOnlyDevice = insertPreflightInt64(t, db, `INSERT INTO devices (shop_id, mac_address, name) VALUES ($1, $2, $3) RETURNING id`, f.validShop, uniquePreflightMAC(), "Historical Device")
	f.unresolvedDevice = insertPreflightInt64(t, db, `INSERT INTO devices (shop_id, mac_address, name) VALUES ($1, $2, $3) RETURNING id`, f.validShop, uniquePreflightMAC(), "Unresolved Device")
	f.orphanShopDevice = insertPreflightInt64(t, db, `INSERT INTO devices (shop_id, mac_address, name) VALUES ($1, $2, $3) RETURNING id`, int64(987654327), uniquePreflightMAC(), "Orphan Shop Device")
	f.futureDevice = insertPreflightInt64(t, db, `INSERT INTO devices (shop_id, mac_address, name) VALUES ($1, $2, $3) RETURNING id`, f.validShop, uniquePreflightMAC(), "Future Device")
	now := time.Now().UTC()
	f.candidateAssignment = insertPreflightUUID(t, db, `INSERT INTO device_assignments (device_id, measurement_point_id, valid_from, valid_to) VALUES ($1, $2, $3, $4) RETURNING id`, f.candidateDevice, f.candidateMP, now.Add(-time.Hour), now.Add(time.Hour))
	f.historicalAssignment = insertPreflightUUID(t, db, `INSERT INTO device_assignments (device_id, measurement_point_id, valid_from, valid_to) VALUES ($1, $2, $3, $4) RETURNING id`, f.historicalOnlyDevice, f.candidateMP, time.Now().UTC().Add(-3*time.Hour), time.Now().UTC().Add(-2*time.Hour))
	f.unresolvedAssignment = insertPreflightUUID(t, db, `INSERT INTO device_assignments (device_id, measurement_point_id, valid_from) VALUES ($1, $2, $3) RETURNING id`, f.unresolvedDevice, f.unresolvedMP, time.Now().UTC().Add(-time.Hour))
	f.futureAssignment = insertPreflightUUID(t, db, `INSERT INTO device_assignments (device_id, measurement_point_id, valid_from) VALUES ($1, $2, $3) RETURNING id`, f.futureDevice, f.futureMP, time.Now().UTC().Add(time.Hour))

	f.validOperationID = insertPreflightOperation(t, db, f.authReviewUser, "bind", "valid-operation")
	f.validAuditID = insertPreflightAudit(t, db, f.validOperationID, f.authReviewUser, f.validShop, "valid-audit")
	f.ambiguousOperationID = insertPreflightOperation(t, db, f.authReviewUser, "bind", "ambiguous-operation")
	f.ambiguousAuditID = insertPreflightAudit(t, db, f.ambiguousOperationID, f.authReviewUser, 0, "ambiguous-audit")
	f.noAuditOperationID = insertPreflightOperation(t, db, f.authReviewUser, "bind", "no-audit-operation")
	return f
}

func (f preflightFixture) cleanup(t *testing.T, db *sql.DB) {
	t.Helper()
	execPreflight(t, db, `TRUNCATE admin_binding_audits, admin_binding_operations`)
	execPreflight(t, db, `DELETE FROM device_assignments WHERE id IN ($1, $2, $3, $4)`, f.candidateAssignment, f.historicalAssignment, f.unresolvedAssignment, f.futureAssignment)
	// Measurement points are removed only after their assignments are gone.
	execPreflight(t, db, `DELETE FROM measurement_points WHERE id IN ($1, $2, $3)`, f.candidateMP, f.unresolvedMP, f.futureMP)
	execPreflight(t, db, `DELETE FROM devices WHERE id IN ($1, $2, $3, $4, $5, $6)`, f.candidateDevice, f.unassignedDevice, f.historicalOnlyDevice, f.unresolvedDevice, f.orphanShopDevice, f.futureDevice)
	execPreflight(t, db, `DELETE FROM user_shop_relations WHERE id IN ($1, $2, $3, $4, $5)`, f.validRelation, f.orphanUserRelation, f.orphanShopRelation, f.bothOrphanRelation, f.invalidClientRelation)
	execPreflight(t, db, `DELETE FROM users WHERE id IN ($1, $2, $3)`, f.authReviewUser, f.staleCurrentShopUser, f.nullCurrentShopUser)
	execPreflight(t, db, `DELETE FROM shops WHERE id IN ($1, $2, $3, $4)`, f.validShop, f.otherShop, f.nullClientShop, f.orphanClientShop)
	execPreflight(t, db, `DELETE FROM clients WHERE id IN ($1, $2)`, f.validClient, f.otherClient)
}

func insertPreflightOperation(t *testing.T, db *sql.DB, actorID int64, operation, key string) string {
	t.Helper()
	operationID := uniquePreflightUUID()
	_, err := db.Exec(`INSERT INTO admin_binding_operations (operation_id, idempotency_key, operation, scope_key, actor_id, scope_snapshot, canonical_request_hash) VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7)`, operationID, key+uniquePreflightSuffix(), operation, `misleading-scope`, actorID, `{"shop_id":987654321,"device_id":987654327}`, make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	return operationID
}

func insertPreflightAudit(t *testing.T, db *sql.DB, operationID string, actorID, shopID int64, key string) string {
	t.Helper()
	auditID := uniquePreflightUUID()
	var shop any
	if shopID != 0 {
		shop = shopID
	}
	_, err := db.Exec(`INSERT INTO admin_binding_audits (id, operation_id, request_identity, actor_id, scope_key, scope_snapshot, action, shop_id, metadata) VALUES ($1, $2, $3, $4, $5, $6::jsonb, 'bind', $7, '{}'::jsonb)`, auditID, operationID, key, actorID, `misleading-scope`, `{"shop_id":987654321}`, shop)
	if err != nil {
		t.Fatal(err)
	}
	return auditID
}

func insertPreflightInt64(t *testing.T, db *sql.DB, query string, args ...any) int64 {
	t.Helper()
	var value int64
	if err := db.QueryRow(query, args...).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func insertPreflightUUID(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	var value string
	if err := db.QueryRow(query, args...).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func execPreflight(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func uniquePreflightUUID() string   { return uuid.NewString() }
func uniquePreflightSuffix() string { return strings.ReplaceAll(uuid.NewString(), "-", "")[:12] }
func uniquePreflightMAC() string {
	return strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", "")[:12])
}

func preflightCounts(t *testing.T, db *sql.DB) preflightDatabaseCounts {
	t.Helper()
	var counts preflightDatabaseCounts
	row := db.QueryRow(`SELECT (SELECT count(*) FROM users), (SELECT count(*) FROM shops), (SELECT count(*) FROM user_shop_relations), (SELECT count(*) FROM devices), (SELECT count(*) FROM measurement_points), (SELECT count(*) FROM device_assignments), (SELECT count(*) FROM admin_binding_audits), (SELECT count(*) FROM admin_binding_operations)`)
	if err := row.Scan(&counts.users, &counts.shops, &counts.memberships, &counts.devices, &counts.points, &counts.assignments, &counts.audits, &counts.operations); err != nil {
		t.Fatal(err)
	}
	return counts
}

type preflightDatabaseCounts struct{ users, shops, memberships, devices, points, assignments, audits, operations int64 }

func indexShopFacts(facts []ShopClientFact) map[int64]ShopClientFact {
	result := make(map[int64]ShopClientFact)
	for _, fact := range facts {
		result[fact.ShopID] = fact
	}
	return result
}
func indexMembershipFacts(facts []MembershipFact) map[int64]MembershipFact {
	result := make(map[int64]MembershipFact)
	for _, fact := range facts {
		result[fact.RelationID] = fact
	}
	return result
}
func indexCurrentShopFacts(facts []CurrentShopFact) map[int64]CurrentShopFact {
	result := make(map[int64]CurrentShopFact)
	for _, fact := range facts {
		result[fact.UserID] = fact
	}
	return result
}
func indexDeviceFacts(facts []DeviceOwnershipFact) map[int64]DeviceOwnershipFact {
	result := make(map[int64]DeviceOwnershipFact)
	for _, fact := range facts {
		result[fact.DeviceID] = fact
	}
	return result
}
func indexAssignmentFacts(facts []AssignmentIntegrityFact) map[string]AssignmentIntegrityFact {
	result := make(map[string]AssignmentIntegrityFact)
	for _, fact := range facts {
		result[fact.AssignmentID] = fact
	}
	return result
}
func indexUserFacts(facts []UserEligibilityFact) map[int64]UserEligibilityFact {
	result := make(map[int64]UserEligibilityFact)
	for _, fact := range facts {
		result[fact.ID] = fact
	}
	return result
}
func indexProvenanceFacts(facts []ProvenanceFact) map[string]ProvenanceFact {
	result := make(map[string]ProvenanceFact)
	for _, fact := range facts {
		result[fact.ID] = fact
	}
	return result
}
func containsShopFact(facts []ShopClientFact, id int64) bool {
	for _, fact := range facts {
		if fact.ShopID == id {
			return true
		}
	}
	return false
}
