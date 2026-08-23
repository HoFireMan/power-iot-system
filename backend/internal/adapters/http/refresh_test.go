package httpadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	applicationauth "power-iot-backend/internal/application/auth"
	"power-iot-backend/internal/security"

	"github.com/gin-gonic/gin"
)

type refreshStub struct {
	calls         int
	raw, sourceIP string
	err           error
	result        applicationauth.RefreshResult
	detail        *applicationauth.RefreshResultWithTimestamps
}

func (s *refreshStub) RefreshWithSourceIP(_ context.Context, raw, sourceIP string) (applicationauth.RefreshResult, error) {
	s.calls++
	s.raw, s.sourceIP = raw, sourceIP
	return s.result, s.err
}

func (s *refreshStub) RefreshWithSourceIPWithTimestamps(_ context.Context, raw, sourceIP string) (applicationauth.RefreshResultWithTimestamps, error) {
	s.calls++
	s.raw, s.sourceIP = raw, sourceIP
	if s.detail == nil {
		return applicationauth.RefreshResultWithTimestamps{}, s.err
	}
	return *s.detail, s.err
}

func refreshRouter(stub *refreshStub) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestIDMiddleware(), ClientIPMiddleware(security.TrustedProxyConfig{}))
	RegisterRefreshRoute(router, RefreshHandlerConfig{Runner: stub, Now: func() time.Time { return time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC) }})
	return router
}

func refreshRequest(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", bytes.NewBufferString(body))
	req.RemoteAddr = "198.51.100.8:1234"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(RequestIDHeader, "0f8fad5b-d9cb-469f-a165-70867728950e")
	return req
}

func TestRefreshValidationDoesNotCallRunner(t *testing.T) {
	stub := &refreshStub{result: applicationauth.RefreshResult{AccessToken: "a", RefreshToken: "r"}}
	router := refreshRouter(stub)
	for _, body := range []string{
		`{`, `{}`, `{"refreshToken":null}`, `{"refreshToken":123}`,
		`{"refreshToken":"a","refreshToken":"b"}`, `{"refreshToken":"a"} {`,
		`{"other":"a"}`, `{"refreshToken":"a",}`, `null`,
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, refreshRequest(body))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body %q status=%d, want validation", body, response.Code)
		}
		var envelope security.PublicError
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || envelope.Code != "VALIDATION_ERROR" {
			t.Fatalf("validation envelope=%s err=%v", response.Body.String(), err)
		}
	}
	if stub.calls != 0 {
		t.Fatalf("runner calls=%d for validation requests", stub.calls)
	}
}

func TestRefreshSuccessUsesAuthoritativeIPAndExactTokenPairShape(t *testing.T) {
	at := time.Date(2026, 2, 3, 4, 15, 6, 0, time.UTC)
	rt := at.Add(applicationauth.RefreshFamilyTTL)
	stub := &refreshStub{detail: &applicationauth.RefreshResultWithTimestamps{
		RefreshResult:        applicationauth.RefreshResult{AccessToken: "access", RefreshToken: "refresh"},
		AccessTokenExpiresAt: at, RefreshTokenExpiresAt: rt,
	}}
	router := refreshRouter(stub)
	req := refreshRequest(`{"refreshToken":"opaque"}`)
	req.Header.Set("X-Forwarded-For", "203.0.113.99")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusOK || response.Header().Get(RequestIDHeader) != req.Header.Get(RequestIDHeader) {
		t.Fatalf("status/request ID=%d/%q", response.Code, response.Header().Get(RequestIDHeader))
	}
	var body map[string]interface{}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 5 || body["tokenType"] != "Bearer" || body["accessToken"] != "access" || body["refreshToken"] != "refresh" || body["accessTokenExpiresAt"] != at.Format(time.RFC3339) || body["refreshTokenExpiresAt"] != rt.Format(time.RFC3339) {
		t.Fatalf("unexpected refresh body: %#v", body)
	}
	if stub.raw != "opaque" || stub.sourceIP != "198.51.100.8" {
		t.Fatalf("runner received raw/source=%q/%q", stub.raw, stub.sourceIP)
	}
}

func TestRefreshInvalidIsGenericUnauthorizedWithRequestID(t *testing.T) {
	stub := &refreshStub{err: applicationauth.ErrUnauthorized}
	response := httptest.NewRecorder()
	refreshRouter(stub).ServeHTTP(response, refreshRequest(`{"refreshToken":"unknown"}`))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", response.Code)
	}
	var envelope security.PublicError
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || envelope.Code != "UNAUTHORIZED" || envelope.RequestID == "" {
		t.Fatalf("envelope=%s err=%v", response.Body.String(), err)
	}
	if bytes.Contains(response.Body.Bytes(), []byte("unknown")) {
		t.Fatal("raw token leaked")
	}
}

func TestRefreshInfrastructureIsNotMappedToUnauthorized(t *testing.T) {
	stub := &refreshStub{err: errors.New("database detail")}
	response := httptest.NewRecorder()
	refreshRouter(stub).ServeHTTP(response, refreshRequest(`{"refreshToken":"opaque"}`))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d", response.Code)
	}
	if bytes.Contains(response.Body.Bytes(), []byte("database detail")) {
		t.Fatal("infrastructure detail leaked")
	}
}
