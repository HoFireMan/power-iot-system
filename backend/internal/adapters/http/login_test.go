package httpadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	applicationauth "power-iot-backend/internal/application/auth"
	"power-iot-backend/internal/security"

	"github.com/gin-gonic/gin"
)

type loginStub struct {
	calls   int
	account string
	pass    string
	err     error
	result  applicationauth.LoginResult
	detail  *applicationauth.LoginResultWithTimestamps
}

func (s *loginStub) Login(_ context.Context, account, password string) (applicationauth.LoginResult, error) {
	s.calls++
	s.account, s.pass = account, password
	return s.result, s.err
}
func (s *loginStub) LoginWithTimestamps(_ context.Context, account, password string) (applicationauth.LoginResultWithTimestamps, error) {
	s.calls++
	s.account, s.pass = account, password
	if s.detail == nil {
		return applicationauth.LoginResultWithTimestamps{}, s.err
	}
	return *s.detail, s.err
}

func loginRouter(stub *loginStub, limiter *security.AbuseLimiter) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestIDMiddleware(), ClientIPMiddleware(security.TrustedProxyConfig{}))
	RegisterLoginRoute(router, LoginHandlerConfig{Runner: stub, Limiter: limiter, Now: func() time.Time { return time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC) }})
	return router
}

func loginRequest(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(body))
	req.RemoteAddr = "198.51.100.8:1234"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(RequestIDHeader, "0f8fad5b-d9cb-469f-a165-70867728950e")
	return req
}

func TestLoginSuccessShapeAndExactInputs(t *testing.T) {
	at := time.Date(2026, 2, 3, 4, 15, 6, 0, time.UTC)
	rt := at.Add(applicationauth.RefreshFamilyTTL)
	stub := &loginStub{detail: &applicationauth.LoginResultWithTimestamps{
		LoginResult:          applicationauth.LoginResult{AccessToken: "access", RefreshToken: "refresh"},
		AccessTokenExpiresAt: at, RefreshTokenExpiresAt: rt,
	}}
	response := httptest.NewRecorder()
	loginRouter(stub, security.NewAbuseLimiter()).ServeHTTP(response, loginRequest(`{"account":"  Exact ","password":"p ass"}`))
	if response.Code != http.StatusOK || response.Header().Get(RequestIDHeader) != "0f8fad5b-d9cb-469f-a165-70867728950e" {
		t.Fatalf("status/request ID = %d/%q", response.Code, response.Header().Get(RequestIDHeader))
	}
	var body map[string]interface{}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 5 || body["tokenType"] != "Bearer" || body["accessToken"] != "access" || body["refreshToken"] != "refresh" || body["accessTokenExpiresAt"] != at.Format(time.RFC3339) || body["refreshTokenExpiresAt"] != rt.Format(time.RFC3339) {
		t.Fatalf("unexpected login body: %#v", body)
	}
	if stub.account != "  Exact " || stub.pass != "p ass" {
		t.Fatalf("handler changed credentials: account=%q password=%q", stub.account, stub.pass)
	}
}

func TestLoginValidationDoesNotCallApplication(t *testing.T) {
	stub := &loginStub{result: applicationauth.LoginResult{AccessToken: "a", RefreshToken: "r"}}
	router := loginRouter(stub, security.NewAbuseLimiter())
	for _, body := range []string{`{`, `{ "account": "a" }`, `{ "password": "p" }`, `{ "account": "a", "password": "` + string(bytes.Repeat([]byte{'x'}, security.MaxPasswordBytes+1)) + `" }`, `{"account":"a","password":"p"} {}`} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, loginRequest(body))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body %q status=%d, want validation", body[:min(len(body), 20)], response.Code)
		}
		var envelope security.PublicError
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || envelope.Code != "VALIDATION_ERROR" {
			t.Fatalf("validation envelope=%s err=%v", response.Body.String(), err)
		}
	}
	if stub.calls != 0 {
		t.Fatalf("application calls=%d for validation requests", stub.calls)
	}
}

func TestLoginCredentialFailuresAreGenericAndSafe(t *testing.T) {
	for _, name := range []string{"unknown", "wrong", "disabled", "rate limited"} {
		t.Run(name, func(t *testing.T) {
			stub := &loginStub{err: applicationauth.ErrInvalidCredentials}
			limiter := security.NewAbuseLimiter()
			router := loginRouter(stub, limiter)
			if name == "rate limited" {
				for i := 0; i < 5; i++ {
					response := httptest.NewRecorder()
					router.ServeHTTP(response, loginRequest(`{"account":"a","password":"secret"}`))
				}
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, loginRequest(`{"account":"a","password":"secret"}`))
			if response.Code != http.StatusUnauthorized || !bytes.Contains(response.Body.Bytes(), []byte(`"code":"INVALID_CREDENTIALS"`)) || bytes.Contains(response.Body.Bytes(), []byte("secret")) {
				t.Fatalf("unsafe credential response: status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestLoginLimiterSuccessfulLoginIsNotCountedAndUntrustedXFFCannotBypassIP(t *testing.T) {
	limiter := security.NewAbuseLimiter()
	stub := &loginStub{detail: &applicationauth.LoginResultWithTimestamps{LoginResult: applicationauth.LoginResult{AccessToken: "a", RefreshToken: "r"}, AccessTokenExpiresAt: time.Now(), RefreshTokenExpiresAt: time.Now().Add(time.Hour)}}
	router := loginRouter(stub, limiter)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, loginRequest(`{"account":"a","password":"p"}`))
	if response.Code != http.StatusOK {
		t.Fatalf("successful login status=%d", response.Code)
	}
	stub.detail = nil
	stub.err = applicationauth.ErrInvalidCredentials
	for i := 0; i < 5; i++ {
		response = httptest.NewRecorder()
		router.ServeHTTP(response, loginRequest(`{"account":"a","password":"p"}`))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d status=%d", i, response.Code)
		}
	}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, loginRequest(`{"account":"a","password":"p"}`))
	if response.Code != http.StatusUnauthorized || stub.calls != 6 {
		t.Fatalf("successful login counted as failure: status=%d calls=%d", response.Code, stub.calls)
	}

	ipLimiter := security.NewAbuseLimiter()
	ipStub := &loginStub{err: applicationauth.ErrInvalidCredentials}
	ipRouter := loginRouter(ipStub, ipLimiter)
	for i := 0; i < 21; i++ {
		req := loginRequest(`{"account":"account-` + string(rune('a'+i)) + `","password":"p"}`)
		req.Header.Set("X-Forwarded-For", "203.0.113."+string(rune('1'+i)))
		response = httptest.NewRecorder()
		ipRouter.ServeHTTP(response, req)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("untrusted XFF request %d status=%d", i, response.Code)
		}
	}
	if ipStub.calls != 20 {
		t.Fatalf("untrusted XFF bypassed source IP limit: calls=%d", ipStub.calls)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
