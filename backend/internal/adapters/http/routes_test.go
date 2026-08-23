package httpadapter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	applicationauth "power-iot-backend/internal/application/auth"
	"power-iot-backend/internal/deployment"

	"github.com/gin-gonic/gin"
)

type routeLoginStub struct{}

func (routeLoginStub) Login(context.Context, string, string) (applicationauth.LoginResult, error) {
	return applicationauth.LoginResult{}, applicationauth.ErrInvalidCredentials
}

type routeLogoutStub struct{}

func (routeLogoutStub) AuthenticateAccessToken(context.Context, string) (applicationauth.AuthenticatedIdentity, error) {
	return applicationauth.AuthenticatedIdentity{}, applicationauth.ErrUnauthorized
}
func (routeLogoutStub) Logout(context.Context, applicationauth.AuthenticatedIdentity) error {
	return applicationauth.ErrUnauthorized
}

func TestAuthRouteInventoryIsExactlyThreeVersionedPosts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	login := &loginStub{}
	refresh := &refreshStub{}
	logout := routeLogoutStub{}
	RegisterLoginRoute(router, LoginHandlerConfig{Runner: login})
	RegisterRefreshRoute(router, RefreshHandlerConfig{Runner: refresh})
	RegisterLogoutRoute(router, logout, LogoutHandlerConfig{Runner: logout})
	want := map[string]bool{
		"POST /api/v1/auth/login": false, "POST /api/v1/auth/refresh": false, "POST /api/v1/auth/logout": false,
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
			t.Errorf("missing route %s", route)
		}
	}
}

func TestDashboardRouteInventoryContainsExactlyOneVersionedAlias(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterDashboardRoute(router, routeLogoutStub{}, &dashboardQueryStub{})
	want := map[string]bool{"GET /api/v1/shops/:shopId/dashboard": false}
	for _, route := range router.Routes() {
		key := route.Method + " " + route.Path
		if _, ok := want[key]; !ok {
			t.Fatalf("unexpected route %s", key)
		}
		want[key] = true
	}
	for route, present := range want {
		if !present {
			t.Errorf("missing route %s", route)
		}
	}
	if len(router.Routes()) != len(want) {
		t.Fatalf("routes=%v", router.Routes())
	}
}

func TestReadRouteInventoryContainsOnlyVersionedGets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterMeRoute(router, routeLogoutStub{}, &meQueryStub{})
	RegisterShopsRoute(router, routeLogoutStub{}, &shopsQueryStub{})
	want := map[string]bool{"GET /api/v1/me": false, "GET /api/v1/shops": false}
	for _, route := range router.Routes() {
		key := route.Method + " " + route.Path
		if _, ok := want[key]; !ok {
			t.Fatalf("unexpected route %s", key)
		}
		want[key] = true
	}
	for route, present := range want {
		if !present {
			t.Errorf("missing route %s", route)
		}
	}
	if len(router.Routes()) != len(want) {
		t.Fatalf("routes=%v", router.Routes())
	}
}

func TestD6PreCutoverAllowsReadsToReachAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestIDMiddleware())
	RegisterMeRoute(router, routeLogoutStub{}, &meQueryStub{})
	RegisterShopsRoute(router, routeLogoutStub{}, &shopsQueryStub{})
	RegisterDashboardRoute(router, routeLogoutStub{}, &dashboardQueryStub{})
	handler := RequestIDHTTPMiddleware(deployment.NewWriteGate(true).Middleware(router))
	for _, path := range []string{"/api/v1/me", "/api/v1/shops", "/api/v1/shops/1/dashboard"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("pre-cutover %s status=%d want=%d", path, response.Code, http.StatusUnauthorized)
		}
	}
}

func TestD6PreCutoverBlocksAllAuthPostsBeforeHandlersAndPostCutoverReachesHandlers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	login := &loginStub{}
	refresh := &refreshStub{}
	logout := routeLogoutStub{}
	build := func() http.Handler {
		router := gin.New()
		router.Use(RequestIDMiddleware())
		RegisterLoginRoute(router, LoginHandlerConfig{Runner: login})
		RegisterRefreshRoute(router, RefreshHandlerConfig{Runner: refresh})
		RegisterLogoutRoute(router, logout, LogoutHandlerConfig{Runner: logout})
		return RequestIDHTTPMiddleware(deployment.NewWriteGate(true).Middleware(router))
	}
	handler := build()
	for _, path := range []string{"/api/v1/auth/login", "/api/v1/auth/refresh", "/api/v1/auth/logout"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("pre-cutover %s status=%d", path, response.Code)
		}
	}
	if login.calls != 0 || refresh.calls != 0 {
		t.Fatalf("pre-cutover reached auth handlers login=%d refresh=%d", login.calls, refresh.calls)
	}

	gate := deployment.NewWriteGate(false)
	router := gin.New()
	router.Use(RequestIDMiddleware())
	RegisterLoginRoute(router, LoginHandlerConfig{Runner: login})
	RegisterRefreshRoute(router, RefreshHandlerConfig{Runner: refresh, Now: func() time.Time { return time.Unix(0, 0).UTC() }})
	RegisterLogoutRoute(router, logout, LogoutHandlerConfig{Runner: logout})
	post := RequestIDHTTPMiddleware(gate.Middleware(router))
	for _, test := range []struct {
		path string
		want int
	}{
		{path: "/api/v1/auth/login", want: http.StatusBadRequest},
		{path: "/api/v1/auth/refresh", want: http.StatusBadRequest},
		{path: "/api/v1/auth/logout", want: http.StatusUnauthorized},
	} {
		response := httptest.NewRecorder()
		post.ServeHTTP(response, httptest.NewRequest(http.MethodPost, test.path, nil))
		if response.Code != test.want {
			t.Fatalf("post-cutover %s status=%d want=%d", test.path, response.Code, test.want)
		}
	}
}
