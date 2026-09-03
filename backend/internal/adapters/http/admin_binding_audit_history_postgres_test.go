package httpadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"power-iot-backend/internal/core/domain"
	"power-iot-backend/internal/security"

	"github.com/google/uuid"
)

func TestAdminBindingAuditHistoryThroughRealHTTPAgainstIsolatedPostgres(t *testing.T) {
	fixture := newAdminBindingHTTPFixture(t)
	var clientID uint
	if err := fixture.db.Raw("SELECT client_id FROM shops WHERE id = ?", fixture.shopID).Scan(&clientID).Error; err != nil {
		t.Fatal(err)
	}

	// This test-only relation enables a cross-Shop target reader without
	// broadening the normal mutation fixture's authorization expectations.
	if err := fixture.db.Exec("INSERT INTO user_shop_relations (user_id, shop_id, shop_role) VALUES (?, ?, 'admin')", fixture.adminID, fixture.otherShopID).Error; err != nil {
		t.Fatal(err)
	}

	insertAudit := func(action string, occurred time.Time, shopID uint, primary, oldPoint, newPoint *uuid.UUID, deviceID *uint) {
		t.Helper()
		opID := uuid.New()
		op := domain.AdminBindingOperation{
			ID: uuid.New(), OperationID: opID, IdempotencyKey: "http-audit-read-" + uuid.NewString(),
			Operation: action, ScopeKey: "http-audit-read-scope", ActorID: fixture.adminID,
			ScopeSnapshot: json.RawMessage(`{"shop_ids":[]}`), CanonicalRequestHash: bytes.Repeat([]byte{1}, 32),
			ClientID: &clientID, CommittedResponse: json.RawMessage(`{}`), CommittedAt: &occurred,
		}
		if err := fixture.db.Create(&op).Error; err != nil {
			t.Fatal(err)
		}
		audit := domain.AdminBindingAudit{
			ID: uuid.New(), OperationID: opID, RequestIdentity: op.IdempotencyKey,
			ActorID: fixture.adminID, ScopeKey: op.ScopeKey, ScopeSnapshot: op.ScopeSnapshot,
			ClientID: &clientID, Action: action, OccurredAt: occurred, EffectiveAt: &occurred,
			ShopID: &shopID, MeasurementPointID: primary, OldMeasurementPointID: oldPoint,
			NewMeasurementPointID: newPoint, DeviceID: deviceID, Reason: "HTTP audit test",
			Metadata: json.RawMessage(`{}`),
		}
		if deviceID != nil {
			serial, mac := "HTTP-SERIAL-A", "AABBCCDDEE11"
			if *deviceID == fixture.deviceB {
				serial, mac = "HTTP-SERIAL-B", "AABBCCDDEE22"
			}
			audit.DeviceSerialNumber, audit.DeviceMAC = &serial, &mac
		}
		if err := fixture.db.Create(&audit).Error; err != nil {
			t.Fatal(err)
		}
	}

	now := time.Date(2026, 8, 10, 2, 0, 0, 0, time.UTC)
	insertAudit("create_measurement_point", now, fixture.shopID, &fixture.pointA, nil, nil, nil)
	insertAudit("bind", now.Add(time.Second), fixture.shopID, nil, nil, &fixture.pointA, &fixture.deviceA)
	insertAudit("replace", now.Add(2*time.Second), fixture.shopID, nil, &fixture.pointA, &fixture.pointA, &fixture.deviceB)
	insertAudit("relocate", now.Add(3*time.Second), fixture.shopID, nil, &fixture.pointA, &fixture.pointB, &fixture.deviceA)
	insertAudit("unbind", now.Add(4*time.Second), fixture.shopID, nil, &fixture.pointA, nil, &fixture.deviceA)
	insertAudit("relocate", now.Add(5*time.Second), fixture.otherShopID, nil, &fixture.pointA, &fixture.otherPoint, &fixture.deviceA)

	passwordHash, err := security.HashPassword([]byte("target-only-secret"))
	if err != nil {
		t.Fatal(err)
	}
	var targetOnlyID uint
	targetAccount := "http-target-only-" + uuid.NewString()[:12]
	if err := fixture.db.Raw(`INSERT INTO users (account, password_hash, name, is_admin, auth_enabled) VALUES (?, ?, ?, true, true) RETURNING id`, targetAccount, passwordHash, "Target Only").Scan(&targetOnlyID).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Exec("INSERT INTO user_shop_relations (user_id, shop_id, shop_role) VALUES (?, ?, 'admin')", targetOnlyID, fixture.otherShopID).Error; err != nil {
		t.Fatal(err)
	}
	// The target-only login is created through the same configured auth runner;
	// using a real session is required by the read repository.
	targetLogin, err := fixture.runner.Login(context.Background(), targetAccount, "target-only-secret")
	if err != nil {
		t.Fatal(err)
	}

	get := func(token, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		fixture.handler.ServeHTTP(response, req)
		return response
	}
	base := fmt.Sprintf("/api/v1/shops/%d/admin/binding-audits", fixture.shopID)
	response := get(fixture.accessToken, base+"?limit=2")
	assertStatus(t, response, http.StatusOK)
	var first struct {
		Items      []map[string]interface{} `json:"items"`
		NextCursor string                   `json:"nextCursor"`
	}
	decodeJSON(t, response, &first)
	if len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatalf("first page=%+v", first)
	}
	seen := map[string]bool{}
	for _, item := range first.Items {
		seen[item["id"].(string)] = true
		if _, forbidden := item["requestIdentity"]; forbidden {
			t.Fatal("request identity leaked")
		}
		if _, forbidden := item["clientId"]; forbidden {
			t.Fatal("client identity leaked")
		}
	}
	second := get(fixture.accessToken, base+"?cursor="+first.NextCursor+"&limit=100")
	assertStatus(t, second, http.StatusOK)
	var secondPage struct {
		Items []map[string]interface{} `json:"items"`
	}
	decodeJSON(t, second, &secondPage)
	for _, item := range secondPage.Items {
		if seen[item["id"].(string)] {
			t.Fatalf("cursor duplicated audit %v", item["id"])
		}
	}
	if len(secondPage.Items) != 3 {
		t.Fatalf("second page=%+v", secondPage)
	}

	filtered := get(fixture.accessToken, base+"?action=relocate&measurementPointId="+fixture.pointB.String())
	assertStatus(t, filtered, http.StatusOK)
	var filteredPage struct {
		Items []map[string]interface{} `json:"items"`
	}
	decodeJSON(t, filtered, &filteredPage)
	if len(filteredPage.Items) != 1 {
		t.Fatalf("filtered=%+v", filteredPage)
	}
	if filteredPage.Items[0]["oldMeasurementPoint"] == nil || filteredPage.Items[0]["newMeasurementPoint"] == nil {
		t.Fatal("relocation did not return both MP references")
	}

	crossBase := fmt.Sprintf("/api/v1/shops/%d/admin/binding-audits?action=relocate", fixture.otherShopID)
	cross := get(fixture.accessToken, crossBase)
	assertStatus(t, cross, http.StatusOK)
	var crossPage struct {
		Items []map[string]interface{} `json:"items"`
	}
	decodeJSON(t, cross, &crossPage)
	if len(crossPage.Items) != 1 || crossPage.Items[0]["oldMeasurementPoint"] == nil {
		t.Fatalf("authorized cross-Shop page=%+v", crossPage)
	}
	if source := get(fixture.accessToken, fmt.Sprintf("/api/v1/shops/%d/admin/binding-audits?action=relocate", fixture.shopID)); source.Code != http.StatusOK {
		t.Fatalf("source status=%d", source.Code)
	} else {
		var sourcePage struct {
			Items []map[string]interface{} `json:"items"`
		}
		decodeJSON(t, source, &sourcePage)
		if len(sourcePage.Items) != 1 {
			t.Fatalf("source should not own cross-Shop relocation: %+v", sourcePage)
		}
	}
	if target := get(targetLogin.AccessToken, crossBase); target.Code != http.StatusOK {
		t.Fatalf("target-only status=%d body=%s", target.Code, target.Body.String())
	} else {
		var targetPage struct {
			Items []map[string]interface{} `json:"items"`
		}
		decodeJSON(t, target, &targetPage)
		if len(targetPage.Items) != 0 {
			t.Fatalf("target-only reader saw cross-Shop relocation: %+v", targetPage)
		}
	}

	assertPublicError(t, get("", base), http.StatusUnauthorized, "UNAUTHORIZED")
	assertPublicError(t, get(fixture.nonAdminToken, base), http.StatusForbidden, "FORBIDDEN")
	assertPublicError(t, get(fixture.unscopedAdminToken, base), http.StatusNotFound, "SHOP_NOT_FOUND")
	assertPublicError(t, get(fixture.accessToken, base+"?action=delete"), http.StatusBadRequest, "VALIDATION_ERROR")
}
