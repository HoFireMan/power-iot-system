package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"power-iot-backend/internal/adapters/persistence"
	apphistoricalreport "power-iot-backend/internal/application/historicalreport"
	corebillingenergy "power-iot-backend/internal/core/billingenergy"

	"github.com/gin-gonic/gin"
)

type historicalReportQueryStub struct {
	result apphistoricalreport.Report
	err    error
	userID uint
	shopID uint
	month  string
}

func (s *historicalReportQueryStub) Find(_ context.Context, userID, shopID uint, month string) (apphistoricalreport.Report, error) {
	s.userID, s.shopID, s.month = userID, shopID, month
	return s.result, s.err
}

func TestHistoricalReportRouteIsVersionedAndAuthenticated(t *testing.T) {
	gin.SetMode(gin.TestMode)
	query := &historicalReportQueryStub{result: apphistoricalreport.Report{Month: "2026-08"}}
	router := gin.New()
	RegisterHistoricalReportRoute(router, billingEstimateAuthStub(), query)
	if len(router.Routes()) != 1 || router.Routes()[0].Path != "/api/v1/shops/:shopId/reports/energy" || router.Routes()[0].Method != http.MethodGet {
		t.Fatalf("routes=%v", router.Routes())
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/shops/7/reports/energy?month=2026-08", nil)
	request.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || query.userID != 42 || query.shopID != 7 || query.month != "2026-08" {
		t.Fatalf("status=%d body=%s query=%+v", response.Code, response.Body.String(), query)
	}
}

func TestHistoricalReportResponsePreservesNullZeroStatusAndWarnings(t *testing.T) {
	zero := "0"
	query := &historicalReportQueryStub{result: apphistoricalreport.Report{
		Month: "2026-08", Timezone: corebillingenergy.BusinessTimezone,
		Period: apphistoricalreport.Period{
			Start: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
			Cutoff: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Snapshot: time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC),
		},
		Summary: apphistoricalreport.Facts{Status: apphistoricalreport.StatusComplete, UsageKwh: &zero, Coverage: ptrStringReport("1")},
		MeasurementPoints: []apphistoricalreport.MeasurementPoint{
			{MeasurementPointID: "mp-zero", Status: apphistoricalreport.StatusComplete, UsageKwh: &zero, Coverage: ptrStringReport("1")},
			{MeasurementPointID: "mp-empty", Status: apphistoricalreport.StatusNoData, UsageKwh: nil, Coverage: ptrStringReport("0")},
		},
		Warnings: []string{"PARTIAL_MONITORING_DATA"},
	}}
	router := gin.New()
	RegisterHistoricalReportRoute(router, billingEstimateAuthStub(), query)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/shops/7/reports/energy?month=2026-08", nil)
	request.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["month"] != "2026-08" || body["timezone"] != corebillingenergy.BusinessTimezone {
		t.Fatalf("body=%s", response.Body.String())
	}
	summary := body["summary"].(map[string]any)
	if summary["status"] != string(apphistoricalreport.StatusComplete) || summary["usageKwh"] != "0" || summary["coverage"] != "1" {
		t.Fatalf("summary=%v", summary)
	}
	points := body["measurementPoints"].([]any)
	if len(points) != 2 || points[0].(map[string]any)["usageKwh"] != "0" || points[1].(map[string]any)["usageKwh"] != nil {
		t.Fatalf("points=%v", points)
	}
}

func TestHistoricalReportHandlerMapsValidationForbiddenAndInternalErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name string
		path string
		err  error
		want int
	}{
		{name: "invalid shop", path: "/api/v1/shops/no/reports/energy?month=2026-08", want: http.StatusBadRequest},
		{name: "invalid month", path: "/api/v1/shops/7/reports/energy?month=2026-8", err: corebillingenergy.ErrInvalidBillingMonth, want: http.StatusBadRequest},
		{name: "future month", path: "/api/v1/shops/7/reports/energy?month=2999-01", err: corebillingenergy.ErrFutureBillingMonth, want: http.StatusBadRequest},
		{name: "forbidden", path: "/api/v1/shops/7/reports/energy?month=2026-08", err: persistence.ErrBillingEnergyAccess, want: http.StatusForbidden},
		{name: "internal", path: "/api/v1/shops/7/reports/energy?month=2026-08", err: errors.New("database secret details"), want: http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			query := &historicalReportQueryStub{err: test.err}
			router := gin.New()
			RegisterHistoricalReportRoute(router, billingEstimateAuthStub(), query)
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			request.Header.Set("Authorization", "Bearer test-token")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if test.name == "internal" && string(response.Body.Bytes()) == "database secret details" {
				t.Fatal("internal error leaked")
			}
		})
	}
}

func TestHistoricalReportHandlerRejectsUnauthenticatedRequest(t *testing.T) {
	router := gin.New()
	RegisterHistoricalReportRoute(router, billingEstimateAuthStub(), &historicalReportQueryStub{})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/shops/7/reports/energy?month=2026-08", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func ptrStringReport(value string) *string { return &value }
