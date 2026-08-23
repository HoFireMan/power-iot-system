package httpadapter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	applicationauth "power-iot-backend/internal/application/auth"
	"power-iot-backend/internal/security"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type logoutAuthStub struct {
	identity applicationauth.AuthenticatedIdentity
	err      error
}

func (s logoutAuthStub) AuthenticateAccessToken(context.Context, string) (applicationauth.AuthenticatedIdentity, error) {
	return s.identity, s.err
}

type logoutStub struct {
	calls    int
	identity applicationauth.AuthenticatedIdentity
	err      error
}

func (s *logoutStub) Logout(_ context.Context, identity applicationauth.AuthenticatedIdentity) error {
	s.calls++
	s.identity = identity
	return s.err
}

func logoutRouter(auth AccessTokenAuthenticator, runner LogoutRunner) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestIDMiddleware())
	RegisterLogoutRoute(router, auth, LogoutHandlerConfig{Runner: runner})
	return router
}

func logoutRequest() *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	req.Header.Set("Authorization", "Bearer eyJhbGciOiJub25lIn0.token")
	req.Header.Set(RequestIDHeader, "0f8fad5b-d9cb-469f-a165-70867728950e")
	return req
}

func TestLogoutSuccessIs204WithEmptyBodyAndMinimalIdentityConversion(t *testing.T) {
	sessionID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	runner := &logoutStub{}
	auth := logoutAuthStub{identity: applicationauth.AuthenticatedIdentity{UserID: 42, SessionID: sessionID}}
	response := httptest.NewRecorder()
	logoutRouter(auth, runner).ServeHTTP(response, logoutRequest())
	if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
		t.Fatalf("status/body=%d/%q", response.Code, response.Body.String())
	}
	if runner.calls != 1 || runner.identity.UserID != 42 || runner.identity.SessionID != sessionID {
		t.Fatalf("runner identity/calls=%d/%#v", runner.calls, runner.identity)
	}
}

func TestLogoutCredentialFailuresAreUniform401WithRequestID(t *testing.T) {
	tests := []struct {
		name   string
		auth   AccessTokenAuthenticator
		runner LogoutRunner
	}{
		{name: "missing", auth: logoutAuthStub{err: applicationauth.ErrUnauthorized}, runner: &logoutStub{}},
		{name: "malformed", auth: logoutAuthStub{err: applicationauth.ErrUnauthorized}, runner: &logoutStub{}},
		{name: "expired", auth: logoutAuthStub{err: applicationauth.ErrUnauthorized}, runner: &logoutStub{}},
		{name: "revoked", auth: logoutAuthStub{err: applicationauth.ErrUnauthorized}, runner: &logoutStub{}},
		{name: "session subject mismatch", auth: logoutAuthStub{identity: applicationauth.AuthenticatedIdentity{UserID: 0, SessionID: uuid.New()}}, runner: &logoutStub{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := logoutRequest()
			if test.name == "missing" {
				req.Header.Del("Authorization")
			}
			if test.name == "malformed" {
				req.Header.Set("Authorization", "Basic abc")
			}
			response := httptest.NewRecorder()
			logoutRouter(test.auth, test.runner).ServeHTTP(response, req)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			var envelope security.PublicError
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || envelope.Code != "UNAUTHORIZED" || envelope.RequestID == "" {
				t.Fatalf("envelope=%s err=%v", response.Body.String(), err)
			}
		})
	}
}

func TestLogoutRunnerUnauthorizedIsMappedAndSecondLogoutRejected(t *testing.T) {
	sessionID := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	runner := &logoutStub{err: applicationauth.ErrUnauthorized}
	auth := logoutAuthStub{identity: applicationauth.AuthenticatedIdentity{UserID: 7, SessionID: sessionID}}
	response := httptest.NewRecorder()
	logoutRouter(auth, runner).ServeHTTP(response, logoutRequest())
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", response.Code)
	}
}
