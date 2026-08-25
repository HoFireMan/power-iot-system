package httpadapter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	appbillingestimate "power-iot-backend/internal/application/billingestimate"
	corebillingenergy "power-iot-backend/internal/core/billingenergy"
	coreestimate "power-iot-backend/internal/core/billingestimate"
)

type billingEstimateQueryStub struct {
	result appbillingestimate.Estimate
	err    error
	userID uint
	shopID uint
	month  string
}

func (s *billingEstimateQueryStub) Find(_ context.Context, userID, shopID uint, month string) (appbillingestimate.Estimate, error) {
	s.userID, s.shopID, s.month = userID, shopID, month
	return s.result, s.err
}

func billingEstimateAuthStub() billingAuthStub { return billingAuthStub{} }

func TestBillingEstimateRouteIsVersionedAndAuthenticated(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterBillingEstimateRoute(router, billingEstimateAuthStub(), &billingEstimateQueryStub{result: appbillingestimate.Estimate{Status: coreestimate.StatusNoData, Month: "2026-08"}})
	if len(router.Routes()) != 1 || router.Routes()[0].Path != "/api/v1/shops/:shopId/billing/estimate" || router.Routes()[0].Method != http.MethodGet {
		t.Fatalf("routes=%v", router.Routes())
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/shops/7/billing/estimate?month=2026-08", nil)
	request.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestBillingEstimateResponseUsesDecimalStringsAndNullCharges(t *testing.T) {
	gin.SetMode(gin.TestMode)
	query := &billingEstimateQueryStub{result: appbillingestimate.Estimate{
		Status: coreestimate.StatusNoData, Month: "2026-08", Period: appbillingestimate.Period{Timezone: "Asia/Taipei"},
		Energy: appbillingestimate.Energy{UsageKwh: nil, Coverage: ptrStringEstimate("0")},
	}}
	router := gin.New()
	RegisterBillingEstimateRoute(router, billingEstimateAuthStub(), query)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/shops/7/billing/estimate?month=2026-08", nil)
	request.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != string(coreestimate.StatusNoData) || body["month"] != "2026-08" || body["charges"] != nil {
		t.Fatalf("body=%s", response.Body.String())
	}
	energy := body["energy"].(map[string]any)
	if energy["usageKwh"] != nil || energy["coverage"] != "0" {
		t.Fatalf("energy=%v", energy)
	}
}

func TestBillingEstimateResponseExposesOnlyPublicFinancialStrings(t *testing.T) {
	query := &billingEstimateQueryStub{result: appbillingestimate.Estimate{
		Status: coreestimate.StatusComplete, Month: "2026-08",
		Charges: &appbillingestimate.Charges{EnergyCharge: "1533.5", MinimumMonthlyCharge: "100.0", MinimumChargeAdjustment: "0.0", EstimatedTotal: "1534"},
		Tiers:   []appbillingestimate.Tier{{FromKwh: "0", ToKwh: ptrStringEstimate("330"), UsageKwh: "330", RatePerKwh: "2.71", Subtotal: "894.3"}},
	}}
	router := gin.New()
	RegisterBillingEstimateRoute(router, billingEstimateAuthStub(), query)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/shops/7/billing/estimate?month=2026-08", nil)
	request.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	charges := body["charges"].(map[string]any)
	if charges["estimatedTotal"] != "1534" || charges["energyCharge"] != "1533.5" || charges["minimumMonthlyCharge"] != "100.0" {
		t.Fatalf("charges=%v", charges)
	}
	if _, ok := charges["ratePlanId"]; ok || string(response.Body.Bytes()) == "" {
		t.Fatalf("internal identifiers exposed: %s", response.Body.String())
	}
}

func TestBillingEstimateHandlerMapsValidationAndForbidden(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name string
		path string
		err  error
		want int
	}{
		{name: "missing month", path: "/api/v1/shops/7/billing/estimate", want: http.StatusBadRequest},
		{name: "invalid shop", path: "/api/v1/shops/no/billing/estimate?month=2026-08", want: http.StatusBadRequest},
		{name: "forbidden", path: "/api/v1/shops/7/billing/estimate?month=2026-08", err: coreestimate.ErrEstimateAccess, want: http.StatusForbidden},
		{name: "invalid month", path: "/api/v1/shops/7/billing/estimate?month=2026-8", err: corebillingenergy.ErrInvalidBillingMonth, want: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			query := &billingEstimateQueryStub{err: test.err}
			router := gin.New()
			RegisterBillingEstimateRoute(router, billingEstimateAuthStub(), query)
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			request.Header.Set("Authorization", "Bearer test-token")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestBillingEstimateHandlerPassesPublicIDsAndMonth(t *testing.T) {
	query := &billingEstimateQueryStub{result: appbillingestimate.Estimate{Status: coreestimate.StatusComplete}}
	router := gin.New()
	RegisterBillingEstimateRoute(router, billingEstimateAuthStub(), query)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/shops/19/billing/estimate?month=2026-08", nil)
	request.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || query.userID != 42 || query.shopID != 19 || query.month != "2026-08" {
		t.Fatalf("status=%d query=%+v", response.Code, query)
	}
}

func ptrStringEstimate(value string) *string { return &value }
