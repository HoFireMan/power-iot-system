package httpadapter

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	applicationauth "power-iot-backend/internal/application/auth"
	applicationbilling "power-iot-backend/internal/application/billing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type billingRouteQueryStub struct{}

func (billingRouteQueryStub) GetConfiguration(context.Context, uint, uint) (applicationbilling.Configuration, error) {
	tariff := "LIGHTING_COMMERCIAL"
	return applicationbilling.Configuration{ShopID: "7", ElectricityTariff: &tariff, Supported: true, Plans: []applicationbilling.Plan{{Code: "LIGHTING_COMMERCIAL_NON_TOU"}}}, nil
}

type billingRouteMutationStub struct {
	actorID uint
	shopID  uint
	plan    string
}

func (s *billingRouteMutationStub) SetConfiguration(_ context.Context, actorID, shopID uint, plan string) error {
	s.actorID, s.shopID, s.plan = actorID, shopID, plan
	return nil
}

type billingAuthStub struct{}

func (billingAuthStub) AuthenticateAccessToken(context.Context, string) (applicationauth.AuthenticatedIdentity, error) {
	return applicationauth.AuthenticatedIdentity{UserID: 42, SessionID: uuid.New()}, nil
}

func TestBillingConfigurationRoutesAreNarrowAndVersioned(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterBillingConfigurationRoutes(router, billingAuthStub{}, billingRouteQueryStub{}, &billingRouteMutationStub{})
	want := map[string]bool{
		"GET /api/v1/shops/:shopId/billing/configuration": false,
		"PUT /api/v1/shops/:shopId/billing/configuration": false,
	}
	for _, route := range router.Routes() {
		key := route.Method + " " + route.Path
		if _, ok := want[key]; !ok {
			t.Fatalf("unexpected route %s", key)
		}
		want[key] = true
	}
	for route, present := range want {
		if !present {
			t.Fatalf("missing route %s", route)
		}
	}
}

func TestBillingConfigurationHTTPUsesPublicIDsAndStablePlanBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mutation := &billingRouteMutationStub{}
	router := gin.New()
	RegisterBillingConfigurationRoutes(router, billingAuthStub{}, billingRouteQueryStub{}, mutation)
	get := httptest.NewRecorder()
	getRequest := httptest.NewRequest(http.MethodGet, "/api/v1/shops/7/billing/configuration", nil)
	getRequest.Header.Set("Authorization", "Bearer test-token")
	router.ServeHTTP(get, getRequest)
	if get.Code != http.StatusOK || bytes.Contains(get.Body.Bytes(), []byte(`"id":1`)) {
		t.Fatalf("GET status=%d body=%s", get.Code, get.Body.String())
	}
	put := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/api/v1/shops/7/billing/configuration", bytes.NewBufferString(`{"planCode":"LIGHTING_COMMERCIAL_NON_TOU"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer test-token")
	router.ServeHTTP(put, request)
	if put.Code != http.StatusNoContent || mutation.actorID != 42 || mutation.shopID != 7 || mutation.plan != "LIGHTING_COMMERCIAL_NON_TOU" {
		t.Fatalf("PUT status=%d mutation=%+v body=%s", put.Code, mutation, put.Body.String())
	}
}
