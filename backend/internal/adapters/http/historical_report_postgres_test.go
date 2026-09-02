package httpadapter

import (
	"context"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"power-iot-backend/internal/adapters/persistence"
	applicationauth "power-iot-backend/internal/application/auth"
	apphistoricalreport "power-iot-backend/internal/application/historicalreport"
	"power-iot-backend/internal/data/migrations"
	"power-iot-backend/internal/security"
	"power-iot-backend/internal/testsupport"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type historicalReportHTTPFixture struct {
	db      *gorm.DB
	handler http.Handler
	token   string
	userID  uint
	shopID  uint
	otherID uint
	points  map[string]uuid.UUID
}

func TestHistoricalReportRouteAgainstIsolatedPostgres(t *testing.T) {
	fixture := newHistoricalReportHTTPFixture(t)
	get := func(token, path string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		fixture.handler.ServeHTTP(response, request)
		return response
	}

	response := get(fixture.token, fmt.Sprintf("/api/v1/shops/%d/reports/energy?month=2026-08", fixture.shopID))
	assertStatus(t, response, http.StatusOK)
	var body struct {
		Month             string                                        `json:"month"`
		Timezone          string                                        `json:"timezone"`
		Period            struct{ Start, End, Cutoff, Snapshot string } `json:"period"`
		Summary           historicalReportJSONFacts                     `json:"summary"`
		MeasurementPoints []historicalReportJSONPoint                   `json:"measurementPoints"`
		Warnings          []string                                      `json:"warnings"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Month != "2026-08" || body.Timezone != "Asia/Taipei" || body.Period.Start == "" || body.Period.End == "" || body.Period.Cutoff == "" || body.Period.Snapshot == "" {
		t.Fatalf("period response=%s", response.Body.String())
	}
	if body.Summary.Status != string(apphistoricalreport.StatusPartial) || body.Summary.UsageKwh == nil || *body.Summary.UsageKwh != "10" || body.Summary.Coverage == nil {
		t.Fatalf("summary=%+v", body.Summary)
	}
	points := make(map[string]historicalReportJSONPoint, len(body.MeasurementPoints))
	for _, point := range body.MeasurementPoints {
		points[point.MeasurementPointID] = point
	}
	for _, key := range []string{"replacement", "zero", "no-data", "relocation-source", "relocation-target"} {
		point := points[fixture.points[key].String()]
		if point.MeasurementPointID == "" {
			t.Fatalf("missing point %s in response=%s", key, response.Body.String())
		}
	}
	if points[fixture.points["replacement"].String()].Status != string(apphistoricalreport.StatusComplete) || points[fixture.points["replacement"].String()].UsageKwh == nil || *points[fixture.points["replacement"].String()].UsageKwh != "3" {
		t.Fatalf("replacement point=%+v", points[fixture.points["replacement"].String()])
	}
	if points[fixture.points["zero"].String()].UsageKwh == nil || *points[fixture.points["zero"].String()].UsageKwh != "0" {
		t.Fatalf("zero point=%+v", points[fixture.points["zero"].String()])
	}
	noData := points[fixture.points["no-data"].String()]
	if noData.Status != string(apphistoricalreport.StatusNoData) || noData.UsageKwh != nil || !containsString(noData.Warnings, "CONFLICTING_TELEMETRY_EXCLUDED") {
		t.Fatalf("no-data point=%+v", noData)
	}
	if points[fixture.points["relocation-source"].String()].UsageKwh == nil || *points[fixture.points["relocation-source"].String()].UsageKwh != "3" || points[fixture.points["relocation-target"].String()].UsageKwh == nil || *points[fixture.points["relocation-target"].String()].UsageKwh != "4" {
		t.Fatalf("relocation points source=%+v target=%+v", points[fixture.points["relocation-source"].String()], points[fixture.points["relocation-target"].String()])
	}

	assertPublicError(t, get("", fmt.Sprintf("/api/v1/shops/%d/reports/energy?month=2026-08", fixture.shopID)), http.StatusUnauthorized, "UNAUTHORIZED")
	assertPublicError(t, get(fixture.token, fmt.Sprintf("/api/v1/shops/%d/reports/energy?month=2026-8", fixture.shopID)), http.StatusBadRequest, "VALIDATION_ERROR")
	assertPublicError(t, get(fixture.token, fmt.Sprintf("/api/v1/shops/%d/reports/energy?month=2026-10", fixture.shopID)), http.StatusBadRequest, "VALIDATION_ERROR")
	assertPublicError(t, get(fixture.token, fmt.Sprintf("/api/v1/shops/%d/reports/energy?month=2026-08", fixture.otherID)), http.StatusForbidden, "FORBIDDEN")
}

type historicalReportJSONFacts struct {
	Status                  string  `json:"status"`
	UsageKwh                *string `json:"usageKwh"`
	ExpectedDurationSeconds int64   `json:"expectedDurationSeconds"`
	ObservedDurationSeconds int64   `json:"observedDurationSeconds"`
	Coverage                *string `json:"coverage"`
}

type historicalReportJSONPoint struct {
	MeasurementPointID string   `json:"measurementPointId"`
	Status             string   `json:"status"`
	UsageKwh           *string  `json:"usageKwh"`
	Warnings           []string `json:"warnings"`
}

func newHistoricalReportHTTPFixture(t *testing.T) *historicalReportHTTPFixture {
	t.Helper()
	source := os.Getenv("TEST_DATABASE_URL")
	if source == "" {
		source = os.Getenv("TEST_MIGRATION_DATABASE_URL")
	}
	if source == "" {
		t.Skip("TEST_DATABASE_URL or TEST_MIGRATION_DATABASE_URL is not set; historical report integration test not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	isolated, err := testsupport.New(ctx, source, migrateHistoricalReportSchema)
	if err != nil {
		t.Fatalf("isolated database setup failed: %v", err)
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
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("database handle failed (%T)", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	suffix := uuid.NewString()[:12]
	var clientID, shopID, otherID, userID uint
	if err := db.Raw(`INSERT INTO clients (code, name) VALUES (?, ?) RETURNING id`, "report-client-"+suffix, "Report Client").Scan(&clientID).Error; err != nil {
		t.Fatalf("client fixture failed (%T)", err)
	}
	if err := db.Raw(`INSERT INTO shops (client_id, code, name) VALUES (?, ?, ?) RETURNING id`, clientID, "report-shop-"+suffix, "Report Shop").Scan(&shopID).Error; err != nil {
		t.Fatalf("shop fixture failed (%T)", err)
	}
	if err := db.Raw(`INSERT INTO shops (client_id, code, name) VALUES (?, ?, ?) RETURNING id`, clientID, "report-other-"+suffix, "Other Report Shop").Scan(&otherID).Error; err != nil {
		t.Fatalf("other Shop fixture failed (%T)", err)
	}
	passwordHash, err := security.HashPassword([]byte("report-test-password"))
	if err != nil {
		t.Fatalf("password fixture failed (%T)", err)
	}
	if err := db.Raw(`INSERT INTO users (account, password_hash, name, is_admin, auth_enabled) VALUES (?, ?, ?, false, true) RETURNING id`, "report-user-"+suffix, passwordHash, "Report User").Scan(&userID).Error; err != nil {
		t.Fatalf("user fixture failed (%T)", err)
	}
	if err := db.Exec(`INSERT INTO user_shop_relations (user_id, shop_id, shop_role) VALUES (?, ?, 'staff')`, userID, shopID).Error; err != nil {
		t.Fatalf("relation fixture failed (%T)", err)
	}

	points := make(map[string]uuid.UUID)
	for _, item := range []struct {
		key, name string
		shop      uint
	}{
		{"replacement", "Replacement MP", shopID}, {"zero", "Zero MP", shopID}, {"no-data", "No Data MP", shopID},
		{"relocation-source", "Relocation Source MP", shopID}, {"relocation-target", "Relocation Target MP", shopID},
	} {
		points[item.key] = uuid.New()
		if err := db.Exec(`INSERT INTO measurement_points (id, shop_id, name) VALUES (?, ?, ?)`, points[item.key], item.shop, item.name).Error; err != nil {
			t.Fatalf("point fixture failed (%T)", err)
		}
	}
	loc, err := time.LoadLocation("Asia/Taipei")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, loc).UTC()
	end := time.Date(2026, 9, 1, 0, 0, 0, 0, loc).UTC()
	boundary := start.Add(24 * time.Hour)
	boundary2 := start.AddDate(0, 0, 15)
	var devices []uint
	for index := 0; index < 5; index++ {
		var device uint
		if err := db.Raw(`INSERT INTO devices (shop_id, inventory_owner_client_id, mac_address, serial_number, name) VALUES (?, ?, ?, ?, ?) RETURNING id`, shopID, clientID, fmt.Sprintf("AABBCCDDEE%02d", index+1), fmt.Sprintf("REPORT-SERIAL-%d", index+1), fmt.Sprintf("Report Device %d", index+1)).Scan(&device).Error; err != nil {
			t.Fatalf("device fixture failed (%T)", err)
		}
		devices = append(devices, device)
	}
	assign := func(device uint, point uuid.UUID, from time.Time, to *time.Time) {
		t.Helper()
		if err := db.Exec(`INSERT INTO device_assignments (device_id, measurement_point_id, valid_from, valid_to) VALUES (?, ?, ?, ?)`, device, point, from, to).Error; err != nil {
			t.Fatalf("assignment fixture failed (%T)", err)
		}
	}
	assign(devices[0], points["replacement"], start, &boundary)
	assign(devices[1], points["replacement"], boundary, nil)
	assign(devices[2], points["zero"], start, nil)
	assign(devices[3], points["no-data"], start, nil)
	assign(devices[4], points["relocation-source"], start, &boundary2)
	assign(devices[4], points["relocation-target"], boundary2, nil)
	reading := func(point uuid.UUID, device uint, from, to time.Time, sequence int64, energy string, conflict bool) {
		t.Helper()
		for _, value := range []string{energy} {
			if conflict {
				value = "5"
			}
			if err := db.Exec(`INSERT INTO power_readings (time, recorded_at, received_at, measurement_point_id, device_id, energy_delta_kwh, protocol_version, coverage_version, interval_start, interval_end, boot_counter, sequence) VALUES (?, ?, ?, ?, ?, ?::numeric, 1, 1, ?, ?, ?, ?)`, from, from, from.Add(time.Hour), point, device, value, from, to, 1, sequence).Error; err != nil {
				t.Fatalf("reading fixture failed (%T)", err)
			}
		}
		if conflict {
			if err := db.Exec(`INSERT INTO power_readings (time, recorded_at, received_at, measurement_point_id, device_id, energy_delta_kwh, protocol_version, coverage_version, interval_start, interval_end, boot_counter, sequence) VALUES (?, ?, ?, ?, ?, ?::numeric, 1, 1, ?, ?, ?, ?)`, from, from, from.Add(time.Hour), point, device, "6", from, to, 1, sequence).Error; err != nil {
				t.Fatalf("conflicting reading fixture failed (%T)", err)
			}
		}
		if err := db.Exec(`INSERT INTO telemetry_ingest_keys (device_id, boot_counter, sequence, canonical_coverage_digest, conflict_detected) VALUES (?, ?, ?, ?, ?)`, device, 1, sequence, make([]byte, 32), conflict).Error; err != nil {
			t.Fatalf("ingest key fixture failed (%T)", err)
		}
	}
	reading(points["replacement"], devices[0], start, boundary, 1, "1", false)
	reading(points["replacement"], devices[1], boundary, end, 2, "2", false)
	reading(points["zero"], devices[2], start, end, 3, "0", false)
	reading(points["no-data"], devices[3], start, start.Add(time.Hour), 4, "5", true)
	reading(points["relocation-source"], devices[4], start, start.Add(time.Hour), 5, "3", false)
	reading(points["relocation-target"], devices[4], boundary2, boundary2.Add(time.Hour), 6, "4", false)

	public, private, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		t.Fatalf("signing fixture failed (%T)", err)
	}
	keyring, err := security.NewKeyring(security.SigningKey{KID: "report-test", Private: private, Public: public}, nil)
	if err != nil {
		t.Fatalf("keyring fixture failed (%T)", err)
	}
	now := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	runner := applicationauth.NewGormTransactionRunnerWithConfig(db, applicationauth.Config{Signer: keyring, Now: func() time.Time { return now }, PersistenceClock: func() time.Time { return now }})
	login, err := runner.Login(ctx, "report-user-"+suffix, "report-test-password")
	if err != nil {
		t.Fatalf("login fixture failed (%T)", err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestIDMiddleware())
	service := apphistoricalreport.New(persistence.NewBillingEnergyQueryRepository(db), func() time.Time { return now })
	RegisterHistoricalReportRoute(router, NewB3Authenticator(runner), service)
	routerHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := gin.CreateTestContextOnly(w, router)
		c.Request = r
		router.HandleContext(c)
	})
	return &historicalReportHTTPFixture{db: db, handler: RequestIDHTTPMiddleware(routerHandler), token: login.AccessToken, userID: userID, shopID: shopID, otherID: otherID, points: points}
}

func migrateHistoricalReportSchema(databaseURL string) error {
	if err := migrations.Up(databaseURL); err != nil {
		return err
	}
	body, err := fs.ReadFile(migrations.Files, "sql/000007_b02_coverage_foundation.up.sql")
	if err != nil {
		return err
	}
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(string(body))
	return err
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
