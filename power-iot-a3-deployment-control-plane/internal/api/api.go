package api

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"power-iot-a3-deployment-control-plane/internal/auth"
	"power-iot-a3-deployment-control-plane/internal/ledger"
	"power-iot-a3-deployment-control-plane/internal/provider"
	"power-iot-a3-deployment-control-plane/internal/store"
)

type Handler struct {
	store     *store.Store
	authority *provider.Authority
}

func NewHandler(s *store.Store, a *provider.Authority) *Handler {
	return &Handler{store: s, authority: a}
}
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", h.health)
	mux.HandleFunc("/readyz", h.ready)
	mux.HandleFunc("/v1/authorizations", h.issue)
	mux.HandleFunc("/v1/authorizations/", h.authorization)
	mux.HandleFunc("/v1/issuer-requests/", h.issuerRequest)
	return h.identityMiddleware(mux)
}
func (h *Handler) identityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			writeError(w, http.StatusUnauthorized, "client certificate required")
			return
		}
		id, e := auth.ParseURIIdentity(r.TLS.PeerCertificates[0])
		if e != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), id)))
	})
}
func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]string{"status": "ok"})
}
func (h *Handler) ready(w http.ResponseWriter, r *http.Request) {
	if h.authority == nil || !h.authority.Ready() {
		writeJSON(w, 503, map[string]string{"status": "not_ready"})
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ready"})
}

type issueBody struct {
	IssuerRequestID string            `json:"issuer_request_id"`
	AttemptID       string            `json:"attempt_id"`
	Scope           string            `json:"scope"`
	Bindings        map[string]string `json:"bindings"`
	TTLSeconds      int               `json:"ttl_seconds"`
}

func (h *Handler) issue(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeError(w, 405, "method not allowed")
		return
	}
	if _, e := auth.Require(r.Context(), "issue"); e != nil {
		writeError(w, 403, "forbidden")
		return
	}
	if h.authority == nil || h.authority.RequireMutation() != nil {
		writeError(w, 503, "authority unavailable")
		return
	}
	var in issueBody
	if !decode(r, &in) {
		writeError(w, 400, "invalid request")
		return
	}
	ttl := time.Duration(in.TTLSeconds) * time.Second
	if in.TTLSeconds == 0 {
		ttl = 10 * time.Minute
	}
	out, e := h.store.Issue(r.Context(), store.RequestData{ID: in.IssuerRequestID, AttemptID: in.AttemptID, Role: string(auth.RoleRunbook), Scope: in.Scope, Bindings: in.Bindings}, ttl)
	if e != nil {
		writeError(w, 409, safeError(e))
		return
	}
	writeJSON(w, 200, out)
}
func (h *Handler) authorization(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/authorizations/")
	parts := strings.Split(path, ":")
	if len(parts) != 2 {
		writeError(w, 404, "not found")
		return
	}
	id, action := parts[0], parts[1]
	switch action {
	case "inspect":
		if r.Method != "POST" {
			writeError(w, 405, "method not allowed")
			return
		}
		idn, e := auth.Require(r.Context(), "inspect")
		if e != nil {
			writeError(w, 403, "forbidden")
			return
		}
		if h.authority == nil || h.authority.RequireMutation() != nil {
			writeError(w, 503, "authority unavailable")
			return
		}
		out, e := h.store.Inspect(r.Context(), id, idn.URI)
		if e != nil {
			writeError(w, 404, "not found")
			return
		}
		writeJSON(w, 200, out)
	case "consume":
		if r.Method != "POST" {
			writeError(w, 405, "method not allowed")
			return
		}
		idn, e := auth.Require(r.Context(), "consume")
		if e != nil {
			writeError(w, 403, "forbidden")
			return
		}
		if h.authority == nil || h.authority.RequireMutation() != nil {
			writeError(w, 503, "authority unavailable")
			return
		}
		var in store.ConsumeRequest
		if !decode(r, &in) {
			writeError(w, 400, "invalid request")
			return
		}
		in.AuthorizationID = id
		in.ConsumerIdentity = idn.URI
		out, e := h.store.Consume(r.Context(), in)
		if e != nil {
			if errors.Is(e, ledger.ErrUnknownCommit) {
				writeJSON(w, 200, out)
				return
			}
			writeError(w, 409, safeError(e))
			return
		}
		writeJSON(w, 200, out)
	case "expire", "revoke":
		if r.Method != "POST" {
			writeError(w, 405, "method not allowed")
			return
		}
		if _, e := auth.Require(r.Context(), action); e != nil {
			writeError(w, 403, "forbidden")
			return
		}
		if h.authority == nil || h.authority.RequireMutation() != nil {
			writeError(w, 503, "authority unavailable")
			return
		}
		var in struct {
			IssuerRequestID string `json:"issuer_request_id"`
		}
		if !decode(r, &in) || in.IssuerRequestID == "" {
			writeError(w, 400, "invalid request")
			return
		}
		var out store.ResolveResult
		var e error
		if action == "expire" {
			out, e = h.store.Expire(r.Context(), id, in.IssuerRequestID)
		} else {
			out, e = h.store.Revoke(r.Context(), id, in.IssuerRequestID)
		}
		if e != nil {
			writeError(w, 409, safeError(e))
			return
		}
		writeJSON(w, 200, out)
	case "resolve":
		if r.Method != "POST" {
			writeError(w, 405, "method not allowed")
			return
		}
		idn, e := auth.Require(r.Context(), "resolve-owner")
		recovery := false
		if e != nil {
			idn, e = auth.Require(r.Context(), "resolve-recovery")
			recovery = e == nil
		}
		if e != nil {
			writeError(w, 403, "forbidden")
			return
		}
		if h.authority == nil || h.authority.RequireMutation() != nil {
			writeError(w, 503, "authority unavailable")
			return
		}
		var in store.ResolveConsumeRequest
		if !decode(r, &in) {
			writeError(w, 400, "invalid request")
			return
		}
		in.AuthorizationID = id
		in.ConsumerIdentity = idn.URI
		out, e := h.store.ResolveConsume(r.Context(), in, recovery)
		if e != nil {
			writeError(w, 409, safeError(e))
			return
		}
		writeJSON(w, 200, out)
	default:
		writeError(w, 404, "not found")
	}
}
func (h *Handler) issuerRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" || !strings.HasSuffix(r.URL.Path, ":resolve") {
		writeError(w, 404, "not found")
		return
	}
	if _, e := auth.Require(r.Context(), "resolve-issue"); e != nil {
		writeError(w, 403, "forbidden")
		return
	}
	if h.authority == nil || h.authority.RequireMutation() != nil {
		writeError(w, 503, "authority unavailable")
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/issuer-requests/"), ":resolve")
	out, e := h.store.ResolveIssue(r.Context(), id)
	if e != nil {
		writeError(w, 409, safeError(e))
		return
	}
	writeJSON(w, 200, out)
}
func decode(r *http.Request, v any) bool {
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v) == nil
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
func safeError(e error) string {
	if e == nil {
		return ""
	}
	return "request rejected"
}

// TLSConfig is a construction seam: callers provide a trust-rooted config
// with ClientAuth=RequireAndVerifyClientCert. The provider does not load keys.
func TLSConfig(base *tls.Config) *tls.Config {
	if base == nil {
		base = &tls.Config{}
	}
	c := base.Clone()
	c.MinVersion = tls.VersionTLS13
	c.ClientAuth = tls.RequireAndVerifyClientCert
	return c
}
