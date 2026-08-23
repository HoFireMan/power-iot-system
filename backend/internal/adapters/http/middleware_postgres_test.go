package httpadapter

import (
	"context"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"power-iot-backend/internal/application/auth"
	"power-iot-backend/internal/data/migrations"
	"power-iot-backend/internal/deployment"
	"power-iot-backend/internal/security"
	"power-iot-backend/internal/testsupport"
)

// TestAuthenticationMiddlewareWithIsolatedSession proves the transport seam
// against a real B3 application and isolated PostgreSQL session. The router is
// assembled only in this test; production intentionally has no auth routes.
func TestAuthenticationMiddlewareWithIsolatedSession(t *testing.T) {
	source := os.Getenv("TEST_DATABASE_URL")
	if source == "" {
		t.Skip("TEST_DATABASE_URL is not set; isolated HTTP/B3 proof not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	isolated, err := testsupport.New(ctx, source, migrations.Up)
	if err != nil {
		t.Fatalf("isolated database setup failed (%T)", err)
	}
	defer func() { _ = isolated.Close() }()
	db, err := gorm.Open(postgres.Open(isolated.DSN()), &gorm.Config{})
	if err != nil {
		t.Fatalf("database open failed (%T)", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("database handle failed (%T)", err)
	}
	defer sqlDB.Close()

	public, private, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		t.Fatalf("signing fixture failed (%T)", err)
	}
	keyring, err := security.NewKeyring(security.SigningKey{KID: "http-proof", Private: private, Public: public}, nil)
	if err != nil {
		t.Fatalf("keyring fixture failed (%T)", err)
	}
	hash, err := security.HashPassword([]byte("http-proof-secret"))
	if err != nil {
		t.Fatalf("credential fixture failed (%T)", err)
	}
	account := "http-proof-" + uuid.NewString()[:12]
	if err := db.Exec(`INSERT INTO users (account, password_hash, name, auth_enabled) VALUES (?, ?, ?, true)`, account, hash, "HTTP Proof").Error; err != nil {
		t.Fatalf("user fixture failed (%T)", err)
	}
	testNow := time.Now().UTC()
	runnerNow := testNow
	runner := auth.NewGormTransactionRunnerWithConfig(db, auth.Config{
		Signer:           keyring,
		Now:              func() time.Time { return runnerNow },
		PersistenceClock: func() time.Time { return runnerNow },
	})
	login, err := runner.Login(ctx, account, "http-proof-secret")
	if err != nil {
		t.Fatalf("B3 login fixture failed (%T)", err)
	}
	b3 := NewB3Authenticator(runner)

	gin.SetMode(gin.TestMode)
	handlerCalls := 0
	postCutover := gin.New()
	postCutover.Use(RequestIDMiddleware())
	postCutover.GET("/protected", AuthenticationMiddleware(b3), func(c *gin.Context) {
		handlerCalls++
		c.Status(http.StatusNoContent)
	})
	postCutover.POST("/protected", AuthenticationMiddleware(b3), func(c *gin.Context) {
		handlerCalls++
		c.Status(http.StatusNoContent)
	})
	server := RequestIDHTTPMiddleware(deployment.NewWriteGate(false).Middleware(postCutover))

	valid := httptest.NewRecorder()
	validRequest := httptest.NewRequest(http.MethodGet, "/protected", nil)
	validRequest.Header.Set("Authorization", "Bearer "+login.AccessToken)
	server.ServeHTTP(valid, validRequest)
	if valid.Code != http.StatusNoContent || handlerCalls != 1 {
		t.Fatalf("valid request status=%d handler-calls=%d", valid.Code, handlerCalls)
	}
	validMutation := httptest.NewRecorder()
	validMutationRequest := httptest.NewRequest(http.MethodPost, "/protected", nil)
	validMutationRequest.Header.Set("Authorization", "Bearer "+login.AccessToken)
	server.ServeHTTP(validMutation, validMutationRequest)
	if validMutation.Code != http.StatusNoContent || handlerCalls != 2 {
		t.Fatalf("post-cutover mutation status=%d handler-calls=%d", validMutation.Code, handlerCalls)
	}
	missingMutation := httptest.NewRecorder()
	server.ServeHTTP(missingMutation, httptest.NewRequest(http.MethodPost, "/protected", nil))
	assertUnauthorizedEnvelope(t, missingMutation)
	if handlerCalls != 2 {
		t.Fatal("unauthenticated post-cutover mutation reached downstream handler")
	}

	var identity auth.AuthenticatedIdentity
	if err := runner.WithTransaction(ctx, func(service *auth.Application) error {
		var err error
		identity, err = service.AuthenticateAccessToken(ctx, login.AccessToken)
		return err
	}); err != nil {
		t.Fatalf("B3 identity lookup failed (%T)", err)
	}
	wrongSubject, err := keyring.IssueAccessTokenAt("999999999", identity.SessionID.String(), testNow)
	if err != nil {
		t.Fatalf("wrong-subject fixture failed (%T)", err)
	}
	otherPublic, otherPrivate, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		t.Fatalf("alternate signing fixture failed (%T)", err)
	}
	invalidKeyring, err := security.NewKeyring(security.SigningKey{KID: "http-proof", Private: otherPrivate, Public: otherPublic}, nil)
	if err != nil {
		t.Fatalf("invalid-signature keyring fixture failed (%T)", err)
	}
	invalidSignature, err := invalidKeyring.IssueAccessTokenAt(strconv.FormatUint(uint64(identity.UserID), 10), identity.SessionID.String(), testNow)
	if err != nil {
		t.Fatalf("invalid-signature fixture failed (%T)", err)
	}
	unknownKeyring, err := security.NewKeyring(security.SigningKey{KID: "unknown-http", Private: otherPrivate, Public: otherPublic}, nil)
	if err != nil {
		t.Fatalf("unknown-kid keyring fixture failed (%T)", err)
	}
	unknownKid, err := unknownKeyring.IssueAccessTokenAt(strconv.FormatUint(uint64(identity.UserID), 10), identity.SessionID.String(), testNow)
	if err != nil {
		t.Fatalf("unknown-kid fixture failed (%T)", err)
	}
	expiredJWT, err := keyring.IssueAccessTokenAt(strconv.FormatUint(uint64(identity.UserID), 10), identity.SessionID.String(), testNow.Add(-security.AccessTokenTTL-security.JWTClockSkew-time.Second))
	if err != nil {
		t.Fatalf("expired-token fixture failed (%T)", err)
	}
	matrix := map[string]string{
		"user mismatch": wrongSubject, "expired JWT": expiredJWT,
		"invalid signature": invalidSignature, "unknown kid": unknownKid,
	}
	for name, raw := range matrix {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/protected", nil)
			request.Header.Set("Authorization", "Bearer "+raw)
			server.ServeHTTP(response, request)
			assertUnauthorizedEnvelope(t, response)
		})
	}
	if err := db.Exec(`UPDATE refresh_sessions SET expires_at = ? WHERE id = ?`, testNow.Add(30*time.Minute), identity.SessionID).Error; err != nil {
		t.Fatalf("session-expiry fixture failed (%T)", err)
	}
	runnerNow = testNow.Add(31 * time.Minute)
	expiredSession := httptest.NewRecorder()
	expiredSessionRequest := httptest.NewRequest(http.MethodGet, "/protected", nil)
	expiredSessionRequest.Header.Set("Authorization", "Bearer "+login.AccessToken)
	server.ServeHTTP(expiredSession, expiredSessionRequest)
	assertUnauthorizedEnvelope(t, expiredSession)
	if err := db.Exec(`UPDATE refresh_sessions SET expires_at = ? WHERE id = ?`, testNow.Add(auth.RefreshFamilyTTL), identity.SessionID).Error; err != nil {
		t.Fatalf("session-expiry restore failed (%T)", err)
	}

	if err := runner.Logout(ctx, identity); err != nil {
		t.Fatalf("B3 logout failed (%T)", err)
	}
	revoked := httptest.NewRecorder()
	revokedRequest := httptest.NewRequest(http.MethodGet, "/protected", nil)
	revokedRequest.Header.Set("Authorization", "Bearer "+login.AccessToken)
	server.ServeHTTP(revoked, revokedRequest)
	assertUnauthorizedEnvelope(t, revoked)
	if handlerCalls != 2 {
		t.Fatal("revoked request reached downstream handler")
	}

	missing := httptest.NewRecorder()
	server.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/protected", nil))
	assertUnauthorizedEnvelope(t, missing)

	// D6 remains the first decision for mutations, with no auth exception.
	preCutoverCalls := 0
	preCutoverRouter := gin.New()
	preCutoverRouter.Use(RequestIDMiddleware())
	preCutoverRouter.POST("/protected", AuthenticationMiddleware(b3), func(c *gin.Context) {
		preCutoverCalls++
		c.Status(http.StatusNoContent)
	})
	preCutover := RequestIDHTTPMiddleware(deployment.NewWriteGate(true).Middleware(preCutoverRouter))
	blocked := httptest.NewRecorder()
	blockedRequest := httptest.NewRequest(http.MethodPost, "/protected", nil)
	blockedRequest.Header.Set("Authorization", "Bearer "+login.AccessToken)
	blockedRequest.Header.Set(RequestIDHeader, "not-canonical")
	preCutover.ServeHTTP(blocked, blockedRequest)
	if blocked.Code != http.StatusServiceUnavailable || preCutoverCalls != 0 {
		t.Fatalf("pre-cutover mutation status=%d downstream=%d", blocked.Code, preCutoverCalls)
	}
	if !security.IsCanonicalUUIDv4(blocked.Header().Get(RequestIDHeader)) {
		t.Fatalf("blocked request ID was not canonical: %q", blocked.Header().Get(RequestIDHeader))
	}
}

func assertUnauthorizedEnvelope(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", response.Code)
	}
	var envelope security.PublicError
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("error envelope decode failed: %v", err)
	}
	if envelope.Code != "UNAUTHORIZED" || envelope.Message != "unauthorized" || !security.IsCanonicalUUIDv4(envelope.RequestID) || envelope.RequestID != response.Header().Get(RequestIDHeader) {
		t.Fatalf("unsafe unauthorized envelope: %+v", envelope)
	}
}
