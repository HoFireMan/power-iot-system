package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"power-iot-backend/internal/application/auth"
	"power-iot-backend/internal/deployment"
	"power-iot-backend/internal/security"

	"github.com/gin-gonic/gin"
)

func TestRequestIDMiddlewarePreservesAndReplaces(t *testing.T) {
	gin.SetMode(gin.TestMode)
	canonical := "0f8fad5b-d9cb-469f-a165-70867728950e"
	cases := []struct {
		name   string
		header string
		keep   bool
	}{
		{name: "canonical v4", header: canonical, keep: true},
		{name: "absent", header: "", keep: false},
		{name: "malformed", header: "not-a-uuid", keep: false},
		{name: "wrong version", header: "0f8fad5b-d9cb-169f-a165-70867728950e", keep: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router := gin.New()
			var seen string
			router.Use(RequestIDMiddleware())
			router.GET("/", func(c *gin.Context) {
				seen, _ = RequestIDFromGin(c)
				c.Status(http.StatusNoContent)
			})
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.header != "" {
				req.Header.Set(requestIDHeader, tc.header)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			if !security.IsCanonicalUUIDv4(seen) || response.Header().Get(requestIDHeader) != seen {
				t.Fatalf("request ID was not canonical and consistent: context=%q header=%q", seen, response.Header().Get(requestIDHeader))
			}
			if tc.keep && seen != tc.header {
				t.Fatalf("canonical ID changed: got %q", seen)
			}
			if !tc.keep && seen == tc.header {
				t.Fatal("invalid input was echoed")
			}
			if got, ok := RequestIDFromContext(req.Context()); ok || got != "" {
				t.Fatal("original request context should not be mutated outside the served request")
			}
		})
	}
}

func TestRequestIDMiddlewareConcurrent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestIDMiddleware())
	router.GET("/", func(c *gin.Context) {
		id, ok := RequestIDFromGin(c)
		if !ok {
			c.Status(http.StatusInternalServerError)
			return
		}
		c.Header("X-Seen", id)
		c.Status(http.StatusNoContent)
	})
	const count = 40
	ids := make(chan string, count)
	var wait sync.WaitGroup
	for i := 0; i < count; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
			ids <- response.Header().Get(requestIDHeader)
		}()
	}
	wait.Wait()
	close(ids)
	seen := map[string]bool{}
	for id := range ids {
		if !security.IsCanonicalUUIDv4(id) || seen[id] {
			t.Fatalf("non-unique concurrent request ID")
		}
		seen[id] = true
	}
}

func TestWritePublicErrorUsesSafeEnvelopeAndOneWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestIDMiddleware())
	router.GET("/", func(c *gin.Context) {
		WritePublicError(c, errors.New("database PHC and bearer token must never be public"))
		WritePublicError(c, auth.ErrInvalidCredentials)
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(requestIDHeader, "0f8fad5b-d9cb-469f-a165-70867728950e")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusInternalServerError || response.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("unexpected response metadata: status=%d content-type=%q", response.Code, response.Header().Get("Content-Type"))
	}
	var envelope security.PublicError
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Code != "INTERNAL_ERROR" || envelope.Message != "internal server error" || envelope.RequestID != req.Header.Get(requestIDHeader) || response.Header().Get(requestIDHeader) != envelope.RequestID {
		t.Fatalf("unsafe or inconsistent envelope: code=%q message=%q request-id-ok=%t", envelope.Code, envelope.Message, envelope.RequestID == response.Header().Get(requestIDHeader))
	}
	if strings.Contains(response.Body.String(), "database") || strings.Contains(response.Body.String(), "PHC") || strings.Contains(response.Body.String(), "bearer") {
		t.Fatal("internal error details were serialized")
	}
}

func TestMapPublicErrorCapabilities(t *testing.T) {
	requestID := "0f8fad5b-d9cb-469f-a165-70867728950e"
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"validation", ErrValidation, http.StatusBadRequest, "VALIDATION_ERROR"},
		{"invalid credentials", auth.ErrInvalidCredentials, http.StatusUnauthorized, "INVALID_CREDENTIALS"},
		{"unauthorized", auth.ErrUnauthorized, http.StatusUnauthorized, "UNAUTHORIZED"},
		{"forbidden", ErrForbidden, http.StatusForbidden, "FORBIDDEN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mapped := MapPublicError(tc.err, requestID)
			if mapped.Status != tc.status || mapped.Error.Code != tc.code || mapped.Error.RequestID != requestID {
				t.Fatalf("unexpected mapping: status=%d code=%q request-id=%q", mapped.Status, mapped.Error.Code, mapped.Error.RequestID)
			}
		})
	}
}

func TestBearerParserRejectsUnsafeFormsAndPreservesToken(t *testing.T) {
	valid := "AbC-._~+/0123=="
	for _, value := range []string{"Bearer " + valid, "bearer   " + valid} {
		got, err := ParseBearerAuthorization(value)
		if err != nil || got != valid {
			t.Fatalf("valid bearer changed or rejected: present=%t err=%t", got != "", err != nil)
		}
	}
	for _, value := range []string{"", "Basic " + valid, "Bearer", "Bearer ", "Bearer =", "Bearer ==", "Bearer " + valid + " ", "Bearer " + valid + "x=y", "Bearer\t" + valid, "Bearer one,two"} {
		if got, err := ParseBearerAuthorization(value); err == nil || got != "" {
			t.Fatalf("unsafe bearer accepted: returned=%t error=%t", got != "", err != nil)
		}
	}
	for _, headers := range []http.Header{
		{},
		{"Authorization": []string{"Bearer " + valid, "Bearer " + valid}},
	} {
		if got, err := ParseBearerHeader(headers); err == nil || got != "" {
			t.Fatal("missing or repeated authorization accepted")
		}
	}
}

func TestIdentityContextIsMinimalAndSafeAbsent(t *testing.T) {
	if _, ok := IdentityFromContext(context.Background()); ok {
		t.Fatal("background context unexpectedly authenticated")
	}
	identity := Identity{UserID: "user", SessionID: "session"}
	ctx := WithIdentity(context.Background(), identity)
	got, ok := IdentityFromContext(ctx)
	if !ok || got != identity {
		t.Fatalf("identity seam mismatch: got=%+v present=%t", got, ok)
	}
	if _, ok := IdentityFromContext(WithIdentity(context.Background(), Identity{UserID: "user"})); ok {
		t.Fatal("incomplete identity was present")
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/", func(c *gin.Context) {
		SetIdentity(c, identity)
		got, ok := IdentityFromGin(c)
		if !ok || got != identity {
			t.Fatal("Gin identity seam mismatch")
		}
		c.Status(http.StatusNoContent)
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("status=%d", response.Code)
	}
}

func TestClientIPMiddlewareUsesTrustedProxyPolicy(t *testing.T) {
	config, err := security.NewTrustedProxyConfig([]string{"203.0.113.0/24", "192.0.2.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		peer    string
		header  http.Header
		want    string
		present bool
	}{
		{"direct untrusted", "198.18.0.2:1234", http.Header{"X-Forwarded-For": []string{"198.51.100.5"}}, "198.18.0.2", true},
		{"trusted one hop", "203.0.113.9:1234", http.Header{"X-Forwarded-For": []string{"198.51.100.5"}}, "198.51.100.5", true},
		{"trusted multihop", "203.0.113.9:1234", http.Header{"Forwarded": []string{"for=198.51.100.5, for=192.0.2.9, for=203.0.113.8"}}, "198.51.100.5", true},
		{"malformed fail closed", "203.0.113.9:1234", http.Header{"X-Forwarded-For": []string{"198.51.100.5, not-an-ip"}}, "203.0.113.9", true},
		{"malformed peer", "not-an-address", http.Header{"X-Forwarded-For": []string{"198.51.100.5"}}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router := gin.New()
			router.Use(ClientIPMiddleware(config))
			router.GET("/", func(c *gin.Context) {
				ip, ok := ClientIPFromGin(c)
				if ok {
					c.Header("X-Client-IP", ip.String())
				}
				c.Status(http.StatusNoContent)
			})
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tc.peer
			for name, values := range tc.header {
				for _, value := range values {
					req.Header.Add(name, value)
				}
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			if got := response.Header().Get("X-Client-IP"); (got != tc.want) || (got == "" && tc.present) {
				t.Fatalf("client IP=%q want=%q", got, tc.want)
			}
		})
	}
}

func TestClientIPGetterReturnsDefensiveCopy(t *testing.T) {
	ctx := context.WithValue(context.Background(), clientIPContextKey{}, net.ParseIP("198.51.100.5"))
	ip, ok := ClientIPFromContext(ctx)
	if !ok {
		t.Fatal("missing IP")
	}
	ip[0] = 1
	again, _ := ClientIPFromContext(ctx)
	if again.String() != "198.51.100.5" {
		t.Fatal("client IP context was mutable")
	}
}

type authenticationStub struct {
	identity auth.AuthenticatedIdentity
	err      error
	calls    int
	seen     string
}

func (s *authenticationStub) AuthenticateAccessToken(_ context.Context, raw string) (auth.AuthenticatedIdentity, error) {
	s.calls++
	s.seen = raw
	return s.identity, s.err
}

func TestAuthenticationMiddlewareDelegatesToB3AndSetsMinimalIdentity(t *testing.T) {
	stub := &authenticationStub{identity: auth.AuthenticatedIdentity{UserID: 42, SessionID: uuid.New()}}
	router := gin.New()
	router.Use(RequestIDMiddleware())
	router.GET("/protected", AuthenticationMiddleware(stub), func(c *gin.Context) {
		identity, ok := IdentityFromGin(c)
		if !ok || identity.UserID != "42" || identity.SessionID != stub.identity.SessionID.String() {
			t.Fatalf("minimal identity missing: %+v present=%t", identity, ok)
		}
		c.Status(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer opaque-access-value")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusNoContent || stub.calls != 1 || stub.seen != "opaque-access-value" {
		t.Fatalf("delegation status=%d calls=%d token-preserved=%t", response.Code, stub.calls, stub.seen != "")
	}
}

func TestAuthenticationMiddlewareUniformlyRejectsCredentialFailures(t *testing.T) {
	cases := []struct {
		name    string
		header  string
		service *authenticationStub
	}{
		{name: "absent", service: &authenticationStub{}},
		{name: "malformed", header: "Basic value", service: &authenticationStub{}},
		{name: "service rejection", header: "Bearer value", service: &authenticationStub{err: errors.New("implementation detail")}},
		{name: "incomplete identity", header: "Bearer value", service: &authenticationStub{identity: auth.AuthenticatedIdentity{UserID: 42}}},
	}
	var requestID = "0f8fad5b-d9cb-469f-a165-70867728950e"
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router := gin.New()
			router.Use(RequestIDMiddleware())
			handlerCalls := 0
			router.GET("/protected", AuthenticationMiddleware(tc.service), func(c *gin.Context) {
				handlerCalls++
				c.Status(http.StatusNoContent)
			})
			req := httptest.NewRequest(http.MethodGet, "/protected", nil)
			req.Header.Set(requestIDHeader, requestID)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			if response.Code != http.StatusUnauthorized || handlerCalls != 0 || response.Header().Get(requestIDHeader) != requestID {
				t.Fatalf("status=%d handler-calls=%d request-id=%q", response.Code, handlerCalls, response.Header().Get(requestIDHeader))
			}
			var envelope security.PublicError
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Code != "UNAUTHORIZED" || envelope.Message != "unauthorized" || envelope.RequestID != requestID {
				t.Fatalf("unsafe auth envelope: %+v", envelope)
			}
		})
	}
}

func TestClientIPHTTPMiddlewareEstablishesPolicyBeforeGate(t *testing.T) {
	config, err := security.NewTrustedProxyConfig([]string{"203.0.113.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	seen := ""
	server := ClientIPHTTPMiddleware(config, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ip, ok := ClientIPFromContext(r.Context()); ok {
			seen = ip.String()
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.8:1234"
	req.Header.Set("X-Forwarded-For", "198.51.100.5")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, req)
	if response.Code != http.StatusNoContent || seen != "198.51.100.5" {
		t.Fatalf("status=%d client=%q", response.Code, seen)
	}
}

func TestRequestIDHTTPMiddlewarePrecedesWriteGate(t *testing.T) {
	gate := deployment.NewWriteGate(true)
	service := &authenticationStub{}
	router := gin.New()
	router.Use(RequestIDMiddleware())
	handlerCalls := 0
	router.POST("/mutation", AuthenticationMiddleware(service), func(c *gin.Context) {
		handlerCalls++
		c.Status(http.StatusNoContent)
	})
	router.GET("/mutation", AuthenticationMiddleware(service), func(c *gin.Context) {
		handlerCalls++
		c.Status(http.StatusNoContent)
	})
	server := RequestIDHTTPMiddleware(gate.Middleware(router))
	req := httptest.NewRequest(http.MethodPost, "/mutation", nil)
	req.Header.Set(requestIDHeader, "not-canonical")
	req.Header.Set("Authorization", "Bearer value")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, req)
	if response.Code != http.StatusServiceUnavailable || handlerCalls != 0 || service.calls != 0 {
		t.Fatalf("gate status=%d handler-calls=%d auth-calls=%d", response.Code, handlerCalls, service.calls)
	}
	if id := response.Header().Get(requestIDHeader); !security.IsCanonicalUUIDv4(id) || id == "not-canonical" {
		t.Fatalf("request ID was not safely established: %q", id)
	}
	getResponse := httptest.NewRecorder()
	getRequest := httptest.NewRequest(http.MethodGet, "/mutation", nil)
	server.ServeHTTP(getResponse, getRequest)
	if getResponse.Code != http.StatusUnauthorized || service.calls != 0 || handlerCalls != 0 {
		t.Fatalf("pre-cutover GET status=%d handler-calls=%d auth-calls=%d", getResponse.Code, handlerCalls, service.calls)
	}
}
