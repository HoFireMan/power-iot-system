package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	applicationauth "power-iot-backend/internal/application/auth"
	applicationme "power-iot-backend/internal/application/me"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type meAuthStub struct {
	identity applicationauth.AuthenticatedIdentity
	err      error
}

func (s meAuthStub) AuthenticateAccessToken(context.Context, string) (applicationauth.AuthenticatedIdentity, error) {
	return s.identity, s.err
}

type meQueryStub struct {
	profile applicationme.Profile
	err     error
	userID  uint
	calls   int
}

func (s *meQueryStub) GetMe(_ context.Context, userID uint) (applicationme.Profile, error) {
	s.calls++
	s.userID = userID
	return s.profile, s.err
}

func meRouter(auth AccessTokenAuthenticator, query MeQuery) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestIDMiddleware())
	RegisterMeRoute(router, auth, query)
	return router
}

func TestMeReturnsOnlySafeNullableProjection(t *testing.T) {
	session := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	query := &meQueryStub{profile: applicationme.Profile{ID: "42", Account: "alice", Name: "Alice", IsAdmin: true}}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer test.token")
	req.Header.Set(RequestIDHeader, "0f8fad5b-d9cb-469f-a165-70867728950e")
	response := httptest.NewRecorder()
	meRouter(meAuthStub{identity: applicationauth.AuthenticatedIdentity{UserID: 42, SessionID: session}}, query).ServeHTTP(response, req)
	if response.Code != http.StatusOK || response.Header().Get(RequestIDHeader) != req.Header.Get(RequestIDHeader) {
		t.Fatalf("status=%d request-id=%q body=%s", response.Code, response.Header().Get(RequestIDHeader), response.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"id": true, "account": true, "name": true, "email": true, "phone": true, "isAdmin": true, "currentShopId": true}
	if len(payload) != len(want) {
		t.Fatalf("response fields=%v", payload)
	}
	for key := range want {
		if _, ok := payload[key]; !ok {
			t.Errorf("missing field %q", key)
		}
	}
	if payload["email"] != nil || payload["phone"] != nil || payload["currentShopId"] != nil {
		t.Fatalf("nullable fields=%v", payload)
	}
	if query.calls != 1 || query.userID != 42 {
		t.Fatalf("query calls/user=%d/%d", query.calls, query.userID)
	}
}

func TestMeMissingAndRevokedBearerAreUniformUnauthorized(t *testing.T) {
	tests := []struct {
		name   string
		auth   AccessTokenAuthenticator
		bearer bool
	}{
		{name: "missing", auth: meAuthStub{err: applicationauth.ErrUnauthorized}},
		{name: "revoked", auth: meAuthStub{err: applicationauth.ErrUnauthorized}, bearer: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			query := &meQueryStub{}
			req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
			req.Header.Set(RequestIDHeader, "0f8fad5b-d9cb-469f-a165-70867728950e")
			if test.bearer {
				req.Header.Set("Authorization", "Bearer revoked")
			}
			response := httptest.NewRecorder()
			meRouter(test.auth, query).ServeHTTP(response, req)
			if response.Code != http.StatusUnauthorized || response.Header().Get(RequestIDHeader) != req.Header.Get(RequestIDHeader) {
				t.Fatalf("status/request-id=%d/%q", response.Code, response.Header().Get(RequestIDHeader))
			}
			if query.calls != 0 {
				t.Fatalf("query called=%d", query.calls)
			}
		})
	}
}

func TestMeUnexpectedQueryErrorIsInternalWithRequestID(t *testing.T) {
	query := &meQueryStub{err: errors.New("database details must not escape")}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer valid")
	req.Header.Set(RequestIDHeader, "0f8fad5b-d9cb-469f-a165-70867728950e")
	response := httptest.NewRecorder()
	meRouter(meAuthStub{identity: applicationauth.AuthenticatedIdentity{UserID: 9, SessionID: uuid.New()}}, query).ServeHTTP(response, req)
	if response.Code != http.StatusInternalServerError || response.Header().Get(RequestIDHeader) != req.Header.Get(RequestIDHeader) {
		t.Fatalf("status/request-id=%d/%q", response.Code, response.Header().Get(RequestIDHeader))
	}
	if string(response.Body.Bytes()) == "" || string(response.Body.Bytes()) == "database details must not escape" {
		t.Fatalf("unexpected body=%s", response.Body.String())
	}
}
