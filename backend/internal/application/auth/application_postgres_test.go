package auth

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"power-iot-backend/internal/adapters/persistence"
	"power-iot-backend/internal/data/migrations"
	"power-iot-backend/internal/security"
	"power-iot-backend/internal/testsupport"
)

// TestRefreshSignerFailureTransactionRollbackProof proves that a signer
// failure before caller commit rolls back rotation without compensation. The
// presented token therefore remains current and retryable; this is distinct
// from a committed replay, which B2 revokes as a security response.
type failAfterSessionRepository struct {
	persistence.AuthPersistence
}

func (r failAfterSessionRepository) CreateRefreshSession(ctx context.Context, session persistence.RefreshSession) (persistence.RefreshSession, error) {
	_, err := r.AuthPersistence.CreateRefreshSession(ctx, session)
	if err != nil {
		return persistence.RefreshSession{}, err
	}
	return persistence.RefreshSession{}, errors.New("injected persistence failure after session insert")
}

// TestLoginPersistenceFailureTransactionRollbackProof verifies that a failure
// between session and token persistence cannot commit a half-family.
func TestLoginPersistenceFailureTransactionRollbackProof(t *testing.T) {
	source := os.Getenv("TEST_DATABASE_URL")
	if source == "" {
		t.Skip("TEST_DATABASE_URL is not set; PostgreSQL atomicity proof not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	isolated, err := testsupport.New(ctx, source, migrations.Up)
	if err != nil {
		t.Fatalf("isolated database setup failed (%T)", err)
	}
	defer func() {
		if err := isolated.Close(); err != nil {
			t.Fatalf("isolated database cleanup failed (%T)", err)
		}
	}()
	db, err := gorm.Open(postgres.Open(isolated.DSN()), &gorm.Config{})
	if err != nil {
		t.Fatalf("database open failed (%T)", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("database handle failed (%T)", err)
	}
	defer sqlDB.Close()
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ephemeral key generation failed (%T)", err)
	}
	keyring, err := security.NewKeyring(security.SigningKey{KID: "login-proof", Private: private, Public: public}, nil)
	if err != nil {
		t.Fatalf("ephemeral keyring setup failed (%T)", err)
	}
	hash, err := security.HashPassword([]byte("proof password"))
	if err != nil {
		t.Fatalf("password fixture setup failed (%T)", err)
	}
	account := "auth-login-proof-" + uuid.NewString()[:12]
	if err := db.Exec(`INSERT INTO users (account, password_hash, name, auth_enabled) VALUES (?, ?, ?, true)`, account, hash, "Proof User").Error; err != nil {
		t.Fatalf("proof user setup failed (%T)", err)
	}
	runner := NewGormTransactionRunnerWithConfig(db, Config{
		Signer:           keyring,
		Now:              func() time.Time { return now },
		PersistenceClock: func() time.Time { return now },
		Random:           &fixedReader{data: bytes.Repeat([]byte{3}, 32)},
	})
	runner.repositoryFactory = func(ctx context.Context, tx *gorm.DB) (persistence.AuthPersistence, error) {
		repository, err := persistence.NewAuthMutationRepository(ctx, tx)
		if err != nil {
			return nil, err
		}
		return failAfterSessionRepository{AuthPersistence: repository}, nil
	}
	result, transactionErr := runner.Login(ctx, account, "proof password")
	if !errors.Is(transactionErr, ErrInfrastructure) || result != (LoginResult{}) {
		t.Fatalf("login failure result present=%t/%t err=%v", result.AccessToken != "", result.RefreshToken != "", transactionErr)
	}
	var counts struct {
		Sessions int
		Tokens   int
	}
	if err := db.Raw(`SELECT (SELECT count(*) FROM refresh_sessions) AS sessions, (SELECT count(*) FROM refresh_tokens) AS tokens`).Scan(&counts).Error; err != nil {
		t.Fatalf("post-failure state query failed (%T)", err)
	}
	if counts.Sessions != 0 || counts.Tokens != 0 {
		t.Fatalf("login failure left rows: sessions=%d tokens=%d", counts.Sessions, counts.Tokens)
	}
}

func TestRefreshSignerFailureTransactionRollbackProof(t *testing.T) {
	source := os.Getenv("TEST_DATABASE_URL")
	if source == "" {
		t.Skip("TEST_DATABASE_URL is not set; PostgreSQL compatibility proof not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	isolated, err := testsupport.New(ctx, source, migrations.Up)
	if err != nil {
		t.Fatalf("isolated database setup failed (%T)", err)
	}
	defer func() {
		if err := isolated.Close(); err != nil {
			t.Fatalf("isolated database cleanup failed (%T)", err)
		}
	}()

	db, err := gorm.Open(postgres.Open(isolated.DSN()), &gorm.Config{})
	if err != nil {
		t.Fatalf("database open failed (%T)", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("database handle failed (%T)", err)
	}
	defer sqlDB.Close()

	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ephemeral key generation failed (%T)", err)
	}
	keyring, err := security.NewKeyring(security.SigningKey{KID: "proof", Private: private, Public: public}, nil)
	if err != nil {
		t.Fatalf("ephemeral keyring setup failed (%T)", err)
	}
	keyring = keyring.WithClock(func() time.Time { return now })
	hash, err := security.HashPassword([]byte("proof password"))
	if err != nil {
		t.Fatalf("password fixture setup failed (%T)", err)
	}
	account := "auth-proof-" + uuid.NewString()[:12]
	if err := db.Exec(`INSERT INTO users (account, password_hash, name, auth_enabled) VALUES (?, ?, ?, true)`, account, hash, "Proof User").Error; err != nil {
		t.Fatalf("proof user setup failed (%T)", err)
	}
	var userID uint
	if err := db.Raw(`SELECT id FROM users WHERE account = ?`, account).Scan(&userID).Error; err != nil || userID == 0 {
		t.Fatalf("proof user lookup failed (%T)", err)
	}

	runner := NewGormTransactionRunnerWithConfig(db, Config{
		Signer:           keyring,
		Now:              func() time.Time { return now },
		PersistenceClock: func() time.Time { return now },
		Random:           &fixedReader{data: make([]byte, 32)},
	})
	login, err := runner.Login(ctx, account, "proof password")
	if err != nil {
		t.Fatalf("login transaction failed (%T)", err)
	}
	if login.RefreshToken == "" {
		t.Fatal("login returned no refresh token")
	}
	var loginState struct {
		Sessions int
		Current  int
	}
	if err := db.Raw(`SELECT (SELECT count(*) FROM refresh_sessions) AS sessions, (SELECT count(*) FROM refresh_tokens WHERE consumed_at IS NULL AND revoked_at IS NULL) AS current`).Scan(&loginState).Error; err != nil {
		t.Fatalf("committed login state query failed (%T)", err)
	}
	if loginState.Sessions != 1 || loginState.Current != 1 {
		t.Fatalf("committed login did not create one family/current token: sessions=%d current=%d", loginState.Sessions, loginState.Current)
	}

	failureRunner := NewGormTransactionRunnerWithConfig(db, Config{
		Signer:           failingSigner{issueErr: errors.New("injected signer failure")},
		Now:              func() time.Time { return now },
		PersistenceClock: func() time.Time { return now },
		Random:           &fixedReader{data: bytes.Repeat([]byte{1}, 32)},
	})
	_, transactionErr := failureRunner.Refresh(ctx, login.RefreshToken)
	if !errors.Is(transactionErr, ErrInfrastructure) {
		t.Fatalf("signer failure transaction result was not preserved (%T)", transactionErr)
	}

	var state struct {
		Sessions       int
		ActiveTokens   int
		ConsumedTokens int
		RevokedTokens  int
		LineageLinks   int
		RevokedSession int
	}
	if err := db.Raw(`
		SELECT
			(SELECT count(*) FROM refresh_sessions) AS sessions,
			(SELECT count(*) FROM refresh_tokens WHERE consumed_at IS NULL AND revoked_at IS NULL) AS active_tokens,
			(SELECT count(*) FROM refresh_tokens WHERE consumed_at IS NOT NULL) AS consumed_tokens,
			(SELECT count(*) FROM refresh_tokens WHERE revoked_at IS NOT NULL) AS revoked_tokens,
			(SELECT count(*) FROM refresh_tokens WHERE replaced_by_token_id IS NOT NULL) AS lineage_links,
			(SELECT count(*) FROM refresh_sessions WHERE revoked_at IS NOT NULL) AS revoked_session`).Scan(&state).Error; err != nil {
		t.Fatalf("final state query failed (%T)", err)
	}
	if state.Sessions != 1 || state.ActiveTokens != 1 || state.ConsumedTokens != 0 || state.RevokedTokens != 0 || state.LineageLinks != 0 || state.RevokedSession != 0 {
		t.Fatalf("unexpected rollback state: sessions=%d active=%d consumed=%d revoked_tokens=%d lineage_links=%d revoked_sessions=%d", state.Sessions, state.ActiveTokens, state.ConsumedTokens, state.RevokedTokens, state.LineageLinks, state.RevokedSession)
	}

	successRunner := NewGormTransactionRunnerWithConfig(db, Config{
		Signer:           keyring,
		Now:              func() time.Time { return now },
		PersistenceClock: func() time.Time { return now },
		Random:           &fixedReader{data: bytes.Repeat([]byte{2}, 32)},
	})
	recovered, err := successRunner.Refresh(ctx, login.RefreshToken)
	if err != nil {
		t.Fatalf("old token could not be consumed after rollback (%T)", err)
	}
	if recovered.AccessToken == "" || recovered.RefreshToken == "" {
		t.Fatal("old token did not produce a replacement after rollback")
	}
	claims, err := keyring.VerifyAccessTokenAt(recovered.AccessToken, now)
	if err != nil {
		t.Fatalf("replacement access token verification failed (%T)", err)
	}
	if claims.Subject != strconv.FormatUint(uint64(userID), 10) || claims.SID == "" || claims.ExpiresAt == nil || claims.ExpiresAt.Time.Sub(now) != security.AccessTokenTTL {
		t.Fatalf("replacement access claims did not preserve identity or 10-minute TTL")
	}
	var postRetry struct {
		Sessions     int
		Current      int
		Consumed     int
		LineageLinks int
		FamilyExpiry time.Time
	}
	if err := db.Raw(`
		SELECT
			(SELECT count(*) FROM refresh_sessions) AS sessions,
			(SELECT count(*) FROM refresh_tokens WHERE consumed_at IS NULL AND revoked_at IS NULL) AS current,
			(SELECT count(*) FROM refresh_tokens WHERE consumed_at IS NOT NULL) AS consumed,
			(SELECT count(*) FROM refresh_tokens WHERE replaced_by_token_id IS NOT NULL) AS lineage_links,
			(SELECT expires_at FROM refresh_sessions LIMIT 1) AS family_expiry`).Scan(&postRetry).Error; err != nil {
		t.Fatalf("post-retry state query failed (%T)", err)
	}
	if postRetry.Sessions != 1 || postRetry.Current != 1 || postRetry.Consumed != 1 || postRetry.LineageLinks != 1 || !postRetry.FamilyExpiry.Equal(now.Add(RefreshFamilyTTL)) {
		t.Fatalf("unexpected post-retry state: sessions=%d current=%d consumed=%d lineage_links=%d", postRetry.Sessions, postRetry.Current, postRetry.Consumed, postRetry.LineageLinks)
	}
}
