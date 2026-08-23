package httpadapter

import (
	"bytes"
	"context"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	applicationauth "power-iot-backend/internal/application/auth"
	"power-iot-backend/internal/data/migrations"
	"power-iot-backend/internal/security"
	"power-iot-backend/internal/testsupport"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// TestAuthRoutesAgainstIsolatedPostgres proves the HTTP composition over the
// accepted B3 transaction runner: rotation lineage, committed replay family
// revocation, unknown-token isolation, and immediate logout revocation.
func TestAuthRoutesAgainstIsolatedPostgres(t *testing.T) {
	source := os.Getenv("TEST_DATABASE_URL")
	if source == "" {
		t.Skip("TEST_DATABASE_URL is not set; isolated auth HTTP proof not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
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
	keyring, err := security.NewKeyring(security.SigningKey{KID: "http-auth-proof", Private: private, Public: public}, nil)
	if err != nil {
		t.Fatalf("keyring fixture failed (%T)", err)
	}
	passwordHash, err := security.HashPassword([]byte("http-auth-proof-secret"))
	if err != nil {
		t.Fatalf("password fixture failed (%T)", err)
	}
	account := "http-auth-proof-" + uuid.NewString()[:12]
	if err := db.Exec(`INSERT INTO users (account, password_hash, name, auth_enabled) VALUES (?, ?, ?, true)`, account, passwordHash, "HTTP Auth Proof").Error; err != nil {
		t.Fatalf("user fixture failed (%T)", err)
	}
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	runner := applicationauth.NewGormTransactionRunnerWithConfig(db, applicationauth.Config{
		Signer: keyring, Now: func() time.Time { return now }, PersistenceClock: func() time.Time { return now }, RefreshLimiter: security.NewAbuseLimiter(),
	})
	loginA, err := runner.Login(ctx, account, "http-auth-proof-secret")
	if err != nil {
		t.Fatalf("login A failed (%T)", err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestIDMiddleware(), ClientIPMiddleware(security.TrustedProxyConfig{}))
	RegisterRefreshRoute(router, RefreshHandlerConfig{Runner: runner})
	RegisterLogoutRoute(router, NewB3Authenticator(runner), LogoutHandlerConfig{Runner: runner})
	refresh := func(token string) (applicationauth.RefreshResult, int) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", bytes.NewBufferString(`{"refreshToken":"`+token+`"}`))
		req.RemoteAddr = "198.51.100.8:1234"
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != http.StatusOK {
			return applicationauth.RefreshResult{}, response.Code
		}
		var body LoginResponse
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			return applicationauth.RefreshResult{}, http.StatusInternalServerError
		}
		return applicationauth.RefreshResult{AccessToken: body.AccessToken, RefreshToken: body.RefreshToken}, response.Code
	}
	loginB, status := refresh(loginA.RefreshToken)
	if status != http.StatusOK {
		t.Fatalf("refresh A->B status=%d", status)
	}
	loginC, status := refresh(loginB.RefreshToken)
	if status != http.StatusOK {
		t.Fatalf("refresh B->C status=%d", status)
	}
	if _, status := refresh(loginA.RefreshToken); status != http.StatusUnauthorized {
		t.Fatalf("historical replay status=%d", status)
	}
	if _, status := refresh(loginC.RefreshToken); status != http.StatusUnauthorized {
		t.Fatalf("latest token after replay status=%d", status)
	}

	loginD, err := runner.Login(ctx, account, "http-auth-proof-secret")
	if err != nil {
		t.Fatalf("independent family login failed (%T)", err)
	}
	if _, status := refresh("unknown-refresh-token"); status != http.StatusUnauthorized {
		t.Fatalf("unknown token status=%d", status)
	}
	if _, status := refresh(loginD.RefreshToken); status != http.StatusOK {
		t.Fatalf("unknown token revoked unrelated family, status=%d", status)
	}

	logoutReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	logoutReq.Header.Set("Authorization", "Bearer "+loginD.AccessToken)
	logoutReq.RemoteAddr = "198.51.100.8:1234"
	logoutResponse := httptest.NewRecorder()
	router.ServeHTTP(logoutResponse, logoutReq)
	if logoutResponse.Code != http.StatusNoContent || logoutResponse.Body.Len() != 0 {
		t.Fatalf("logout status/body=%d/%q", logoutResponse.Code, logoutResponse.Body.String())
	}
	secondLogout := httptest.NewRecorder()
	router.ServeHTTP(secondLogout, logoutReq)
	if secondLogout.Code != http.StatusUnauthorized {
		t.Fatalf("second logout status=%d", secondLogout.Code)
	}
}
