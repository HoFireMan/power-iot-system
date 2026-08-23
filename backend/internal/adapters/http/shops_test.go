package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	applicationauth "power-iot-backend/internal/application/auth"
	applicationshops "power-iot-backend/internal/application/shops"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type shopsAuthStub struct {
	identity applicationauth.AuthenticatedIdentity
	err      error
}

func (s shopsAuthStub) AuthenticateAccessToken(context.Context, string) (applicationauth.AuthenticatedIdentity, error) {
	return s.identity, s.err
}

type shopsQueryStub struct {
	result applicationshops.Shops
	err    error
	calls  int
	userID uint
}

func (s *shopsQueryStub) GetShops(_ context.Context, userID uint) (applicationshops.Shops, error) {
	s.calls++
	s.userID = userID
	return s.result, s.err
}

func shopsRouter(auth AccessTokenAuthenticator, query ShopsQuery) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestIDMiddleware())
	RegisterShopsRoute(router, auth, query)
	return router
}

func TestShopsResponseHasExactSafeShapeAndStringIDs(t *testing.T) {
	address := "Address"
	query := &shopsQueryStub{result: applicationshops.Shops{Shops: []applicationshops.Shop{{
		ID: "2", Code: "b", Name: "B", Address: &address, IsHead: true,
	}, {ID: "8", Code: "a", Name: "A"}}, CurrentShopID: stringPtr("8")}}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/shops", nil)
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set(RequestIDHeader, "0f8fad5b-d9cb-469f-a165-70867728950e")
	response := httptest.NewRecorder()
	shopsRouter(shopsAuthStub{identity: applicationauth.AuthenticatedIdentity{UserID: 42, SessionID: uuid.New()}}, query).ServeHTTP(response, req)
	if response.Code != http.StatusOK || response.Header().Get(RequestIDHeader) != req.Header.Get(RequestIDHeader) {
		t.Fatalf("status/request-id=%d/%q", response.Code, response.Header().Get(RequestIDHeader))
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload) != 2 {
		t.Fatalf("top-level fields=%v", payload)
	}
	shops, ok := payload["shops"].([]any)
	if !ok || len(shops) != 2 || payload["currentShopId"] != "8" {
		t.Fatalf("payload=%v", payload)
	}
	first := shops[0].(map[string]any)
	if len(first) != 6 || first["id"] != "2" || first["address"] != address || first["phone"] != nil || first["isHead"] != true {
		t.Fatalf("shop fields=%v", first)
	}
	if query.calls != 1 || query.userID != 42 {
		t.Fatalf("query calls/user=%d/%d", query.calls, query.userID)
	}
}

func TestShopsAuthFailuresDoNotReachQuery(t *testing.T) {
	for _, test := range []struct {
		name   string
		bearer string
		auth   shopsAuthStub
	}{
		{name: "missing", auth: shopsAuthStub{err: applicationauth.ErrUnauthorized}},
		{name: "revoked", bearer: "Bearer revoked", auth: shopsAuthStub{err: applicationauth.ErrUnauthorized}},
	} {
		t.Run(test.name, func(t *testing.T) {
			query := &shopsQueryStub{}
			req := httptest.NewRequest(http.MethodGet, "/api/v1/shops", nil)
			req.Header.Set(RequestIDHeader, "0f8fad5b-d9cb-469f-a165-70867728950e")
			if test.bearer != "" {
				req.Header.Set("Authorization", test.bearer)
			}
			response := httptest.NewRecorder()
			shopsRouter(test.auth, query).ServeHTTP(response, req)
			if response.Code != http.StatusUnauthorized || response.Header().Get(RequestIDHeader) != req.Header.Get(RequestIDHeader) {
				t.Fatalf("status/request-id=%d/%q", response.Code, response.Header().Get(RequestIDHeader))
			}
			if query.calls != 0 {
				t.Fatalf("query calls=%d", query.calls)
			}
		})
	}
}

func TestShopsPersistenceFailureIsInternalWithMatchingRequestID(t *testing.T) {
	query := &shopsQueryStub{err: errors.New("secret database details")}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/shops", nil)
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set(RequestIDHeader, "0f8fad5b-d9cb-469f-a165-70867728950e")
	response := httptest.NewRecorder()
	shopsRouter(shopsAuthStub{identity: applicationauth.AuthenticatedIdentity{UserID: 9, SessionID: uuid.New()}}, query).ServeHTTP(response, req)
	if response.Code != http.StatusInternalServerError || response.Header().Get(RequestIDHeader) != req.Header.Get(RequestIDHeader) {
		t.Fatalf("status/request-id=%d/%q", response.Code, response.Header().Get(RequestIDHeader))
	}
	if string(response.Body.Bytes()) == "" || string(response.Body.Bytes()) == "secret database details" {
		t.Fatalf("body=%s", response.Body.String())
	}
}

func stringPtr(value string) *string { return &value }
