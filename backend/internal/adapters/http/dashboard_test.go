package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	applicationauth "power-iot-backend/internal/application/auth"
	applicationdashboard "power-iot-backend/internal/application/dashboard"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type dashboardAuthStub struct {
	identity applicationauth.AuthenticatedIdentity
	err      error
}

func (s dashboardAuthStub) AuthenticateAccessToken(context.Context, string) (applicationauth.AuthenticatedIdentity, error) {
	return s.identity, s.err
}

type dashboardQueryStub struct {
	result applicationdashboard.Dashboard
	err    error
	calls  int
	userID uint
	shopID uint
}

func (s *dashboardQueryStub) GetDashboard(_ context.Context, userID, shopID uint) (applicationdashboard.Dashboard, error) {
	s.calls++
	s.userID, s.shopID = userID, shopID
	return s.result, s.err
}

func dashboardRouter(auth AccessTokenAuthenticator, query DashboardQuery) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestIDMiddleware())
	RegisterDashboardRoute(router, auth, query)
	return router
}

func TestDashboardResponseHasExactB7AShapeEnergyAndNullCarbon(t *testing.T) {
	snapshot := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	lastSeen := snapshot.Add(-time.Minute)
	daily, monthly := 0.0, 12.5
	query := &dashboardQueryStub{result: applicationdashboard.Dashboard{
		Shop:    applicationdashboard.Shop{ID: "7", Code: "S7", Name: "Shop 7"},
		Devices: []applicationdashboard.Device{{ID: "9", Name: "Meter", IsOnline: true, LastSeen: &lastSeen}}, GeneratedAt: snapshot,
		DailyKwh: &daily, MonthlyKwh: &monthly,
	}}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/shops/7/dashboard", nil)
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set(RequestIDHeader, "0f8fad5b-d9cb-469f-a165-70867728950e")
	response := httptest.NewRecorder()
	dashboardRouter(dashboardAuthStub{identity: applicationauth.AuthenticatedIdentity{UserID: 42, SessionID: uuid.New()}}, query).ServeHTTP(response, req)
	if response.Code != http.StatusOK || response.Header().Get(RequestIDHeader) != req.Header.Get(RequestIDHeader) {
		t.Fatalf("status/request-id=%d/%q", response.Code, response.Header().Get(RequestIDHeader))
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload) != 8 {
		t.Fatalf("top-level fields=%v", payload)
	}
	if payload["currentPowerW"] != nil || payload["dailyKwh"] != float64(0) || payload["monthlyKwh"] != float64(12.5) {
		t.Fatalf("power/energy payload=%v", payload)
	}
	for _, field := range []string{"dailyKg", "monthlyKg"} {
		if value, exists := payload[field]; !exists || value != nil {
			t.Fatalf("%s=%v exists=%t", field, value, exists)
		}
	}
	if payload["generatedAt"] != snapshot.Format(time.RFC3339Nano) {
		t.Fatalf("generatedAt=%v", payload["generatedAt"])
	}
	if query.calls != 1 || query.userID != 42 || query.shopID != 7 {
		t.Fatalf("query calls/user/shop=%d/%d/%d", query.calls, query.userID, query.shopID)
	}
}

func TestDashboardShopNotFoundIsUniformPublic404(t *testing.T) {
	for _, name := range []string{"missing", "inactive", "unrelated", "admin-unrelated"} {
		t.Run(name, func(t *testing.T) {
			query := &dashboardQueryStub{err: applicationdashboard.ErrShopNotFound}
			req := httptest.NewRequest(http.MethodGet, "/api/v1/shops/99/dashboard", nil)
			req.Header.Set("Authorization", "Bearer token")
			req.Header.Set(RequestIDHeader, "0f8fad5b-d9cb-469f-a165-70867728950e")
			response := httptest.NewRecorder()
			dashboardRouter(dashboardAuthStub{identity: applicationauth.AuthenticatedIdentity{UserID: 42, SessionID: uuid.New()}}, query).ServeHTTP(response, req)
			if response.Code != http.StatusNotFound || response.Header().Get(RequestIDHeader) != req.Header.Get(RequestIDHeader) {
				t.Fatalf("status/request-id=%d/%q", response.Code, response.Header().Get(RequestIDHeader))
			}
			if query.calls != 1 {
				t.Fatalf("query calls=%d", query.calls)
			}
		})
	}
}

func TestDashboardAuthAndMalformedIDDoNotReachQuery(t *testing.T) {
	for _, test := range []struct {
		name string
		path string
		auth dashboardAuthStub
		want int
	}{
		{name: "missing auth", path: "/api/v1/shops/7/dashboard", auth: dashboardAuthStub{err: applicationauth.ErrUnauthorized}, want: http.StatusUnauthorized},
		{name: "revoked auth", path: "/api/v1/shops/7/dashboard", auth: dashboardAuthStub{err: applicationauth.ErrUnauthorized}, want: http.StatusUnauthorized},
		{name: "malformed shop id", path: "/api/v1/shops/not-a-number/dashboard", auth: dashboardAuthStub{identity: applicationauth.AuthenticatedIdentity{UserID: 42, SessionID: uuid.New()}}, want: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			query := &dashboardQueryStub{err: errors.New("must not reach query")}
			req := httptest.NewRequest(http.MethodGet, test.path, nil)
			req.Header.Set(RequestIDHeader, "0f8fad5b-d9cb-469f-a165-70867728950e")
			if test.name != "missing auth" {
				req.Header.Set("Authorization", "Bearer token")
			}
			response := httptest.NewRecorder()
			dashboardRouter(test.auth, query).ServeHTTP(response, req)
			if response.Code != test.want || query.calls != 0 {
				t.Fatalf("status/query=%d/%d", response.Code, query.calls)
			}
		})
	}
}
