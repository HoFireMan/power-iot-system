// Package httpadapter contains the narrow HTTP transport seams shared by
// future handlers. It deliberately does not verify credentials or implement
// application routes.
package httpadapter

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"

	applicationauth "power-iot-backend/internal/application/auth"
	applicationdashboard "power-iot-backend/internal/application/dashboard"
	applicationmeasurementpointdetail "power-iot-backend/internal/application/measurementpointdetail"
	"power-iot-backend/internal/security"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	// RequestIDHeader is the canonical HTTP request correlation header.
	RequestIDHeader = "X-Request-ID"
	requestIDHeader = RequestIDHeader
)

// RequestIDMiddleware establishes one canonical request identity for the
// lifetime of a request. The value is available through both Gin and the
// standard request context and is emitted before a handler can write a reply.
func RequestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := RequestIDFromContext(c.Request.Context())
		if !ok {
			id = security.NewRequestID(c.GetHeader(requestIDHeader))
		}
		c.Set(requestIDContextKey{}, id)
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), requestIDContextKey{}, id))
		c.Header(requestIDHeader, id)
		c.Next()
	}
}

// RequestIDHTTPMiddleware is the server-level counterpart to
// RequestIDMiddleware. It runs before net/http middleware such as the D6
// write gate, so rejected requests still carry a correlation header.
func RequestIDHTTPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := security.NewRequestID(r.Header.Get(requestIDHeader))
		w.Header().Set(requestIDHeader, id)
		r = r.WithContext(context.WithValue(r.Context(), requestIDContextKey{}, id))
		next.ServeHTTP(w, r)
	})
}

// RequestIDFromContext returns the middleware-established request ID.
func RequestIDFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	id, ok := ctx.Value(requestIDContextKey{}).(string)
	return id, ok && security.IsCanonicalUUIDv4(id)
}

// RequestIDFromGin returns the middleware-established request ID.
func RequestIDFromGin(c *gin.Context) (string, bool) {
	if c == nil {
		return "", false
	}
	if value, ok := c.Get(requestIDContextKey{}); ok {
		id, valid := value.(string)
		return id, valid && security.IsCanonicalUUIDv4(id)
	}
	return RequestIDFromContext(c.Request.Context())
}

type requestIDContextKey struct{}

// ErrValidation marks a safe client-side validation failure. The underlying
// validation details must remain server-side and are never written to JSON.
var ErrValidation = errors.New("request validation failed")

// ErrInvalidCredentials and ErrUnauthorized are transport markers. The
// authentication application markers are also recognized by MapPublicError.
var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrUnauthorized       = errors.New("unauthorized")
)

// PublicErrorMapping is the complete public response selected for an error.
// It contains no underlying error text.
type PublicErrorMapping struct {
	Status int
	Error  security.PublicError
}

// MapPublicError maps known capability outcomes to fixed public messages.
// Unknown errors always become INTERNAL_ERROR; err is never serialized.
func MapPublicError(err error, requestID string) PublicErrorMapping {
	code, message, status := "INTERNAL_ERROR", "internal server error", http.StatusInternalServerError
	switch {
	case errors.Is(err, ErrValidation):
		code, message, status = "VALIDATION_ERROR", "request validation failed", http.StatusBadRequest
	case errors.Is(err, ErrInvalidCredentials), errors.Is(err, applicationauth.ErrInvalidCredentials):
		code, message, status = "INVALID_CREDENTIALS", "invalid credentials", http.StatusUnauthorized
	case errors.Is(err, ErrUnauthorized), errors.Is(err, applicationauth.ErrUnauthorized):
		code, message, status = "UNAUTHORIZED", "unauthorized", http.StatusUnauthorized
	case errors.Is(err, applicationdashboard.ErrShopNotFound):
		code, message, status = "SHOP_NOT_FOUND", "shop not found", http.StatusNotFound
	case errors.Is(err, applicationmeasurementpointdetail.ErrMeasurementPointNotFound):
		code, message, status = "MEASUREMENT_POINT_NOT_FOUND", "measurement point not found", http.StatusNotFound
	}
	return PublicErrorMapping{Status: status, Error: security.NewPublicError(code, message, requestID)}
}

// WritePublicError writes exactly one JSON error response. Calling it after a
// response has already been written is a no-op, preventing double writes.
func WritePublicError(c *gin.Context, err error) {
	if c == nil || c.Writer.Written() {
		return
	}
	requestID, ok := RequestIDFromGin(c)
	if !ok {
		requestID = security.NewRequestID(c.GetHeader(requestIDHeader))
	}
	c.Header(requestIDHeader, requestID)
	mapped := MapPublicError(err, requestID)
	c.Abort()
	c.JSON(mapped.Status, mapped.Error)
}

// ParseBearerAuthorization parses one RFC 6750 bearer authorization value.
// It returns the token unchanged after removing only the auth scheme and
// required separating spaces. It performs no JWT verification.
func ParseBearerAuthorization(value string) (string, error) {
	const scheme = "Bearer"
	if len(value) < len(scheme) || !strings.EqualFold(value[:len(scheme)], scheme) {
		return "", ErrMalformedBearerAuthorization
	}
	index := len(scheme)
	if index == len(value) || value[index] != ' ' {
		return "", ErrMalformedBearerAuthorization
	}
	for index < len(value) && value[index] == ' ' {
		index++
	}
	if index == len(value) {
		return "", ErrMalformedBearerAuthorization
	}
	token := value[index:]
	if !validToken68(token) {
		return "", ErrMalformedBearerAuthorization
	}
	return token, nil
}

// ParseBearerHeader rejects absent, malformed, wrong-scheme, and repeated
// Authorization header values without exposing their contents.
func ParseBearerHeader(headers http.Header) (string, error) {
	values := headers.Values("Authorization")
	if len(values) != 1 {
		return "", ErrMalformedBearerAuthorization
	}
	return ParseBearerAuthorization(values[0])
}

// ParseAuthorizationBearer is an intentionally explicit alias for callers
// that prefer the header-oriented name.
func ParseAuthorizationBearer(headers http.Header) (string, error) {
	return ParseBearerHeader(headers)
}

var ErrMalformedBearerAuthorization = errors.New("malformed bearer authorization")

// AccessTokenAuthenticator is the B3 application capability used by the HTTP
// boundary. Keeping this interface here prevents transport code from verifying
// tokens or querying session storage directly.
type AccessTokenAuthenticator interface {
	AuthenticateAccessToken(context.Context, string) (applicationauth.AuthenticatedIdentity, error)
}

// B3Authenticator adapts the accepted B3 transaction boundary for a read of
// live session authority. The application service remains the only component
// that verifies the access credential or interprets session state.
type B3Authenticator struct {
	runner applicationauth.TransactionRunner
}

// NewB3Authenticator returns a transport capability backed by B3. A nil
// runner is retained as an unavailable capability and fails closed in the
// middleware.
func NewB3Authenticator(runner applicationauth.TransactionRunner) AccessTokenAuthenticator {
	return &B3Authenticator{runner: runner}
}

func (a *B3Authenticator) AuthenticateAccessToken(ctx context.Context, raw string) (applicationauth.AuthenticatedIdentity, error) {
	var identity applicationauth.AuthenticatedIdentity
	if a == nil || a.runner == nil {
		return identity, applicationauth.ErrUnauthorized
	}
	err := a.runner.WithTransaction(ctx, func(service *applicationauth.Application) error {
		var err error
		identity, err = service.AuthenticateAccessToken(ctx, raw)
		return err
	})
	if err != nil {
		return applicationauth.AuthenticatedIdentity{}, err
	}
	if identity.UserID == 0 || identity.SessionID == uuid.Nil {
		return applicationauth.AuthenticatedIdentity{}, applicationauth.ErrUnauthorized
	}
	return identity, nil
}

// AuthenticationMiddleware authenticates a protected route through B3 and
// places only the minimal identity in the request context. Every credential
// failure has the same safe public response.
func AuthenticationMiddleware(service AccessTokenAuthenticator) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw, err := ParseBearerHeader(c.Request.Header)
		if err != nil || service == nil {
			WritePublicError(c, ErrUnauthorized)
			return
		}
		identity, err := service.AuthenticateAccessToken(c.Request.Context(), raw)
		if err != nil || identity.UserID == 0 || identity.SessionID == uuid.Nil {
			WritePublicError(c, ErrUnauthorized)
			return
		}
		SetIdentity(c, Identity{
			UserID:    strconv.FormatUint(uint64(identity.UserID), 10),
			SessionID: identity.SessionID.String(),
		})
		c.Next()
	}
}

// AuthenticatedMiddleware is the descriptive route-middleware name.
func AuthenticatedMiddleware(service AccessTokenAuthenticator) gin.HandlerFunc {
	return AuthenticationMiddleware(service)
}

// AuthMiddleware is retained as a concise alias for route declarations.
func AuthMiddleware(service AccessTokenAuthenticator) gin.HandlerFunc {
	return AuthenticationMiddleware(service)
}

func validToken68(token string) bool {
	if token == "" {
		return false
	}
	padding, value := false, false
	for _, char := range token {
		switch {
		case char == '=':
			padding = true
		case padding:
			return false
		case (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || strings.ContainsRune("-._~+/", char):
			value = true
		default:
			return false
		}
	}
	return value
}

// Identity contains only the authenticated user and session identity. It is
// deliberately not a claims, token, or authorization object.
type Identity struct {
	UserID    string
	SessionID string
}

// AuthenticatedIdentity is a descriptive alias for Identity.
type AuthenticatedIdentity = Identity

type identityContextKey struct{}

// WithIdentity returns a context carrying a copy of the minimal identity.
func WithIdentity(ctx context.Context, identity Identity) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, identityContextKey{}, identity)
}

// IdentityFromContext safely reports whether an identity was established.
func IdentityFromContext(ctx context.Context) (Identity, bool) {
	if ctx == nil {
		return Identity{}, false
	}
	identity, ok := ctx.Value(identityContextKey{}).(Identity)
	if !ok || identity.UserID == "" || identity.SessionID == "" {
		return Identity{}, false
	}
	return identity, true
}

// SetIdentity establishes the identity on both Gin and request contexts.
func SetIdentity(c *gin.Context, identity Identity) {
	if c == nil {
		return
	}
	c.Set(identityContextKey{}, identity)
	c.Request = c.Request.WithContext(WithIdentity(c.Request.Context(), identity))
}

// IdentityFromGin safely reads the identity seam from Gin/request context.
func IdentityFromGin(c *gin.Context) (Identity, bool) {
	if c == nil {
		return Identity{}, false
	}
	if value, ok := c.Get(identityContextKey{}); ok {
		identity, valid := value.(Identity)
		if valid && identity.UserID != "" && identity.SessionID != "" {
			return identity, true
		}
		return Identity{}, false
	}
	return IdentityFromContext(c.Request.Context())
}

// ClientIPMiddleware resolves the authoritative client address using the
// accepted B1 proxy policy. It never delegates to Gin's ClientIP behavior.
func ClientIPMiddleware(config security.TrustedProxyConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		peer := parseRemotePeer(c.Request.RemoteAddr)
		client := security.ResolveClientIP(peer, c.Request.Header, config)
		if client != nil {
			client = append(net.IP(nil), client...)
			c.Set(clientIPContextKey{}, client)
			c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), clientIPContextKey{}, append(net.IP(nil), client...)))
		}
		c.Next()
	}
}

// ClientIPHTTPMiddleware establishes the same B4-A client-IP policy before
// net/http gates run. This keeps global context setup ahead of D6 without
// delegating trust decisions to a framework helper.
func ClientIPHTTPMiddleware(config security.TrustedProxyConfig, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer := parseRemotePeer(r.RemoteAddr)
		client := security.ResolveClientIP(peer, r.Header, config)
		if client != nil {
			r = r.WithContext(context.WithValue(r.Context(), clientIPContextKey{}, append(net.IP(nil), client...)))
		}
		next.ServeHTTP(w, r)
	})
}

type clientIPContextKey struct{}

// ClientIPFromContext returns a defensive copy of the authoritative address.
func ClientIPFromContext(ctx context.Context) (net.IP, bool) {
	if ctx == nil {
		return nil, false
	}
	ip, ok := ctx.Value(clientIPContextKey{}).(net.IP)
	if !ok || ip == nil {
		return nil, false
	}
	return append(net.IP(nil), ip...), true
}

// ClientIPFromGin returns the middleware-established authoritative address.
func ClientIPFromGin(c *gin.Context) (net.IP, bool) {
	if c == nil {
		return nil, false
	}
	if value, ok := c.Get(clientIPContextKey{}); ok {
		ip, valid := value.(net.IP)
		if !valid || ip == nil {
			return nil, false
		}
		return append(net.IP(nil), ip...), true
	}
	return ClientIPFromContext(c.Request.Context())
}

func parseRemotePeer(remoteAddr string) net.IP {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return net.ParseIP(host)
	}
	return net.ParseIP(strings.Trim(strings.TrimSpace(remoteAddr), "[]"))
}
