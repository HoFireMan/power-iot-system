package httpadapter

import (
	"bytes"
	"context"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"power-iot-backend/internal/application/adminbinding"
	applicationauth "power-iot-backend/internal/application/auth"
	"power-iot-backend/internal/data/migrations"
	"power-iot-backend/internal/security"
	"power-iot-backend/internal/testsupport"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type adminBindingHTTPFixture struct {
	db                 *gorm.DB
	handler            http.Handler
	accessToken        string
	nonAdminToken      string
	unscopedAdminToken string
	adminID            uint
	shopID             uint
	otherShopID        uint
	pointA             uuid.UUID
	pointB             uuid.UUID
	otherPoint         uuid.UUID
	deviceA            uint
	deviceB            uint
	assignment         uuid.UUID
}

// TestAdminBindingRoutesAgainstIsolatedPostgres proves all five mutation
// routes through authentication, admin capability, scoped HTTP lookup,
// application execution, and PostgreSQL persistence. The helper owns and
// drops the generated security_test_* database even when this test fails.
func TestAdminBindingRoutesAgainstIsolatedPostgres(t *testing.T) {
	fixture := newAdminBindingHTTPFixture(t)
	postAs := func(token, path, key, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Idempotency-Key", key)
		response := httptest.NewRecorder()
		fixture.handler.ServeHTTP(response, req)
		return response
	}
	post := func(path, key, body string) *httptest.ResponseRecorder {
		t.Helper()
		return postAs(fixture.accessToken, path, key, body)
	}

	missingAuth := httptest.NewRecorder()
	fixture.handler.ServeHTTP(missingAuth, httptest.NewRequest(http.MethodPost, "/api/v1/admin/measurement-points", bytes.NewBufferString(`{"shopId":1,"name":"No Auth"}`)))
	assertPublicError(t, missingAuth, http.StatusUnauthorized, "UNAUTHORIZED")

	invalid := post("/api/v1/admin/measurement-points", "", `{"shopId":1,"name":"Invalid"}`)
	assertPublicError(t, invalid, http.StatusBadRequest, "VALIDATION_ERROR")

	nonAdminRequest := httptest.NewRequest(http.MethodPost, "/api/v1/admin/measurement-points", bytes.NewBufferString(fmt.Sprintf(`{"shopId":%d,"name":"Non-admin"}`, fixture.shopID)))
	nonAdminRequest.Header.Set("Authorization", "Bearer "+fixture.nonAdminToken)
	nonAdminRequest.Header.Set("Idempotency-Key", "http-non-admin")
	nonAdminResponse := httptest.NewRecorder()
	fixture.handler.ServeHTTP(nonAdminResponse, nonAdminRequest)
	assertPublicError(t, nonAdminResponse, http.StatusForbidden, "FORBIDDEN")
	if got := countOperations(t, fixture.db, "http-non-admin"); got != 0 {
		t.Fatalf("non-admin request mutated idempotency ledger: %d", got)
	}

	unscopedAdminKey := "http-admin-without-shop-relation"
	beforeUnscopedAdmin := countOperations(t, fixture.db, unscopedAdminKey)
	unscopedAdmin := postAs(fixture.unscopedAdminToken, "/api/v1/admin/measurement-points", unscopedAdminKey, fmt.Sprintf(`{"shopId":%d,"name":"Unscoped Admin"}`, fixture.shopID))
	assertPublicError(t, unscopedAdmin, http.StatusNotFound, "RESOURCE_NOT_FOUND")
	if got := countOperations(t, fixture.db, unscopedAdminKey); got != beforeUnscopedAdmin {
		t.Fatalf("admin without Shop relation mutated idempotency ledger: before=%d after=%d", beforeUnscopedAdmin, got)
	}

	createBody := fmt.Sprintf(`{"shopId":%d,"name":"HTTP MP"}`, fixture.shopID)
	created := post("/api/v1/admin/measurement-points", "http-create-replay", createBody)
	assertStatus(t, created, http.StatusOK)
	createdReplay := post("/api/v1/admin/measurement-points", "http-create-replay", createBody)
	assertStatus(t, createdReplay, http.StatusOK)
	var firstResult, replayResult map[string]interface{}
	decodeJSON(t, created, &firstResult)
	decodeJSON(t, createdReplay, &replayResult)
	if firstResult["operationId"] != replayResult["operationId"] {
		t.Fatalf("same-key Create MP changed operation identity: %v vs %v", firstResult["operationId"], replayResult["operationId"])
	}
	conflicting := post("/api/v1/admin/measurement-points", "http-create-replay", fmt.Sprintf(`{"shopId":%d,"name":"Different"}`, fixture.shopID))
	assertPublicError(t, conflicting, http.StatusConflict, "CONFLICT")

	bind := post("/api/v1/admin/device-bindings", "http-bind", fmt.Sprintf(`{"deviceRef":{"serialNumber":"HTTP-SERIAL-A"},"measurementPointId":%q}`, fixture.pointA.String()))
	assertStatus(t, bind, http.StatusOK)
	var bindResult struct {
		NewAssignmentID string `json:"newAssignmentId"`
	}
	decodeJSON(t, bind, &bindResult)
	fixture.assignment, _ = uuid.Parse(bindResult.NewAssignmentID)
	if fixture.assignment == uuid.Nil {
		t.Fatal("Bind response omitted new assignment identity")
	}

	replace := post(fmt.Sprintf("/api/v1/admin/device-bindings/%s/replace", fixture.assignment), "http-replace", `{"replacementDeviceRef":{"serialNumber":"HTTP-SERIAL-B"}}`)
	assertStatus(t, replace, http.StatusOK)
	var replaceResult struct {
		NewAssignmentID string `json:"newAssignmentId"`
	}
	decodeJSON(t, replace, &replaceResult)
	fixture.assignment, _ = uuid.Parse(replaceResult.NewAssignmentID)

	relocate := post(fmt.Sprintf("/api/v1/admin/device-bindings/%s/relocate", fixture.assignment), "http-relocate", fmt.Sprintf(`{"targetMeasurementPointId":%q}`, fixture.pointB.String()))
	assertStatus(t, relocate, http.StatusOK)
	var relocateResult struct {
		NewAssignmentID string `json:"newAssignmentId"`
	}
	decodeJSON(t, relocate, &relocateResult)
	fixture.assignment, _ = uuid.Parse(relocateResult.NewAssignmentID)

	unbind := post(fmt.Sprintf("/api/v1/admin/device-bindings/%s/unbind", fixture.assignment), "http-unbind", `{"reason":"HTTP integration cleanup"}`)
	assertStatus(t, unbind, http.StatusOK)
	var active int64
	if err := fixture.db.Table("device_assignments").Where("valid_to IS NULL AND device_id IN (?, ?)", fixture.deviceA, fixture.deviceB).Count(&active).Error; err != nil {
		t.Fatalf("active assignment query failed: %v", err)
	}
	if active != 0 {
		t.Fatalf("HTTP mutation sequence left %d active assignments", active)
	}

	beforeCrossShop := countOperations(t, fixture.db, "http-cross-shop")
	crossShop := post("/api/v1/admin/device-bindings", "http-cross-shop", fmt.Sprintf(`{"deviceRef":{"serialNumber":"HTTP-SERIAL-A"},"measurementPointId":%q}`, fixture.otherPoint.String()))
	assertPublicError(t, crossShop, http.StatusNotFound, "RESOURCE_NOT_FOUND")
	if got := countOperations(t, fixture.db, "http-cross-shop"); got != beforeCrossShop {
		t.Fatalf("cross-Shop request mutated idempotency ledger: before=%d after=%d", beforeCrossShop, got)
	}
}

func newAdminBindingHTTPFixture(t *testing.T) *adminBindingHTTPFixture {
	t.Helper()
	source := os.Getenv("TEST_DATABASE_URL")
	if source == "" {
		source = os.Getenv("TEST_MIGRATION_DATABASE_URL")
	}
	if source == "" {
		t.Skip("TEST_DATABASE_URL or TEST_MIGRATION_DATABASE_URL is not set; admin HTTP integration test not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	isolated, err := testsupport.New(ctx, source, migrations.Up)
	if err != nil {
		t.Fatalf("isolated database setup failed (%T)", err)
	}
	t.Cleanup(func() {
		if err := isolated.Close(); err != nil {
			t.Errorf("isolated database cleanup failed (%T)", err)
		}
	})
	db, err := gorm.Open(postgres.Open(isolated.DSN()), &gorm.Config{})
	if err != nil {
		t.Fatalf("database open failed (%T)", err)
	}
	for _, name := range []string{
		"sql/000007_b02_coverage_foundation.up.sql",
		"sql/000010_measurement_point_identity.up.sql",
		"sql/000011_measurement_point_alerts.up.sql",
	} {
		body, err := fs.ReadFile(migrations.Files, name)
		if err != nil {
			t.Fatalf("alerts schema read failed (%T)", err)
		}
		if err := db.Exec(string(body)).Error; err != nil {
			t.Fatalf("alerts schema apply failed (%T)", err)
		}
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("database handle failed (%T)", err)
	}
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("database connection cleanup failed (%T)", err)
		}
	})

	suffix := uuid.NewString()[:12]
	var clientID, shopID, otherShopID, adminID, deviceA, deviceB uint
	if err := db.Raw(`INSERT INTO clients (code, name) VALUES (?, ?) RETURNING id`, "http-client-"+suffix, "HTTP Client").Scan(&clientID).Error; err != nil {
		t.Fatalf("client fixture failed (%T)", err)
	}
	if err := db.Raw(`INSERT INTO shops (client_id, code, name) VALUES (?, ?, ?) RETURNING id`, clientID, "http-shop-"+suffix, "HTTP Shop").Scan(&shopID).Error; err != nil {
		t.Fatalf("shop fixture failed (%T)", err)
	}
	if err := db.Raw(`INSERT INTO shops (client_id, code, name) VALUES (?, ?, ?) RETURNING id`, clientID, "http-other-shop-"+suffix, "Other HTTP Shop").Scan(&otherShopID).Error; err != nil {
		t.Fatalf("other Shop fixture failed (%T)", err)
	}
	passwordHash, err := security.HashPassword([]byte("http-admin-secret"))
	if err != nil {
		t.Fatalf("password fixture failed (%T)", err)
	}
	if err := db.Raw(`INSERT INTO users (account, password_hash, name, is_admin, auth_enabled) VALUES (?, ?, ?, true, true) RETURNING id`, "http-admin-"+suffix, passwordHash, "HTTP Admin").Scan(&adminID).Error; err != nil {
		t.Fatalf("admin fixture failed (%T)", err)
	}
	if err := db.Exec(`INSERT INTO user_shop_relations (user_id, shop_id, shop_role) VALUES (?, ?, 'admin')`, adminID, shopID).Error; err != nil {
		t.Fatalf("admin Shop relation fixture failed (%T)", err)
	}
	var nonAdminID uint
	if err := db.Raw(`INSERT INTO users (account, password_hash, name, is_admin, auth_enabled) VALUES (?, ?, ?, false, true) RETURNING id`, "http-user-"+suffix, passwordHash, "HTTP User").Scan(&nonAdminID).Error; err != nil {
		t.Fatalf("non-admin fixture failed (%T)", err)
	}
	if err := db.Exec(`INSERT INTO user_shop_relations (user_id, shop_id, shop_role) VALUES (?, ?, 'staff')`, nonAdminID, shopID).Error; err != nil {
		t.Fatalf("non-admin Shop relation fixture failed (%T)", err)
	}
	if err := db.Exec(`INSERT INTO users (account, password_hash, name, is_admin, auth_enabled) VALUES (?, ?, ?, true, true)`, "http-unscoped-admin-"+suffix, passwordHash, "HTTP Unscoped Admin").Error; err != nil {
		t.Fatalf("unscoped admin fixture failed (%T)", err)
	}
	if err := db.Raw(`INSERT INTO devices (shop_id, inventory_owner_client_id, mac_address, serial_number, name) VALUES (?, ?, ?, ?, ?) RETURNING id`, shopID, clientID, "AABBCCDDEE11", "HTTP-SERIAL-A", "HTTP Device A").Scan(&deviceA).Error; err != nil {
		t.Fatalf("device A fixture failed (%T)", err)
	}
	if err := db.Raw(`INSERT INTO devices (shop_id, inventory_owner_client_id, mac_address, serial_number, name) VALUES (?, ?, ?, ?, ?) RETURNING id`, shopID, clientID, "AABBCCDDEE22", "HTTP-SERIAL-B", "HTTP Device B").Scan(&deviceB).Error; err != nil {
		t.Fatalf("device B fixture failed (%T)", err)
	}
	pointA := uuid.New()
	pointB := uuid.New()
	otherPoint := uuid.New()
	for _, point := range []struct {
		id     uuid.UUID
		shopID uint
		name   string
	}{{pointA, shopID, "HTTP MP A"}, {pointB, shopID, "HTTP MP B"}, {otherPoint, otherShopID, "Other Shop MP"}} {
		if err := db.Exec(`INSERT INTO measurement_points (id, shop_id, name) VALUES (?, ?, ?)`, point.id, point.shopID, point.name).Error; err != nil {
			t.Fatalf("measurement point fixture failed (%T)", err)
		}
	}

	public, private, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		t.Fatalf("signing fixture failed (%T)", err)
	}
	keyring, err := security.NewKeyring(security.SigningKey{KID: "http-admin-proof", Private: private, Public: public}, nil)
	if err != nil {
		t.Fatalf("keyring fixture failed (%T)", err)
	}
	now := time.Date(2026, 8, 10, 1, 0, 0, 0, time.UTC)
	runner := applicationauth.NewGormTransactionRunnerWithConfig(db, applicationauth.Config{Signer: keyring, Now: func() time.Time { return now }, PersistenceClock: func() time.Time { return now }})
	login, err := runner.Login(ctx, "http-admin-"+suffix, "http-admin-secret")
	if err != nil {
		t.Fatalf("admin login fixture failed (%T)", err)
	}
	nonAdminLogin, err := runner.Login(ctx, "http-user-"+suffix, "http-admin-secret")
	if err != nil {
		t.Fatalf("non-admin login fixture failed (%T)", err)
	}
	unscopedAdminLogin, err := runner.Login(ctx, "http-unscoped-admin-"+suffix, "http-admin-secret")
	if err != nil {
		t.Fatalf("unscoped admin login fixture failed (%T)", err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestIDMiddleware())
	RegisterAdminBindingRoutes(router, NewB3Authenticator(runner), AdminBindingHandlerConfig{Executor: adminbinding.NewExecutor(db), DB: db})
	// Gin's production ServeHTTP returns its context to a sync.Pool as soon as
	// the handler returns. database/sql may still have a Rows cancellation
	// watcher reading the context supplied to GORM, and this adapter supplies
	// the Gin context itself. Reusing that context for the next sequential
	// request produces a race in Gin's mutable Request field, rather than in
	// the feature under test. Allocate a test-only context per request and
	// dispatch through the real router so the integration still exercises the
	// complete HTTP stack without recycling a context before its DB watcher is
	// done.
	routerHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := gin.CreateTestContextOnly(w, router)
		c.Request = r
		router.HandleContext(c)
	})
	return &adminBindingHTTPFixture{db: db, handler: RequestIDHTTPMiddleware(routerHandler), accessToken: login.AccessToken, nonAdminToken: nonAdminLogin.AccessToken, unscopedAdminToken: unscopedAdminLogin.AccessToken, adminID: adminID, shopID: shopID, otherShopID: otherShopID, pointA: pointA, pointB: pointB, otherPoint: otherPoint, deviceA: deviceA, deviceB: deviceB}
}

func countOperations(t *testing.T, db *gorm.DB, key string) int64 {
	t.Helper()
	var count int64
	if err := db.Table("admin_binding_operations").Where("idempotency_key = ?", key).Count(&count).Error; err != nil {
		t.Fatalf("operation count failed: %v", err)
	}
	return count
}

func assertStatus(t *testing.T, response *httptest.ResponseRecorder, want int) {
	t.Helper()
	if response.Code != want {
		t.Fatalf("status=%d want=%d body=%s", response.Code, want, response.Body.String())
	}
}

func assertPublicError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	assertStatus(t, response, status)
	var envelope security.PublicError
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("public error decode failed: %v", err)
	}
	if envelope.Code != code || envelope.Message == "" || envelope.RequestID == "" {
		t.Fatalf("unexpected public error: %+v", envelope)
	}
}

func decodeJSON(t *testing.T, response *httptest.ResponseRecorder, target interface{}) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
		t.Fatalf("response decode failed: %v", err)
	}
}
