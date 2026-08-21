package deployment

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteGateBlocksMutationMethodsOnly(t *testing.T) {
	gate := NewWriteGate(true)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	handler := gate.Middleware(next)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		req := httptest.NewRequest(method, "/write", nil)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != http.StatusServiceUnavailable {
			t.Fatalf("method=%s status=%d, want blocked", method, res.Code)
		}
	}
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if get.Code != http.StatusOK {
		t.Fatalf("GET status=%d, want pass through", get.Code)
	}
	gate.Reopen()
	post := httptest.NewRecorder()
	handler.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/write", nil))
	if post.Code != http.StatusOK {
		t.Fatalf("reopened POST status=%d, want pass through", post.Code)
	}
}
