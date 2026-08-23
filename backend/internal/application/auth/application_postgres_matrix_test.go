package auth

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/argon2"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"power-iot-backend/internal/data/migrations"
	"power-iot-backend/internal/security"
	"power-iot-backend/internal/testsupport"
)

func TestAuthRunnerPostgresRotationAuthenticationMatrix(t *testing.T) {
	source := os.Getenv("TEST_DATABASE_URL")
	if source == "" {
		t.Skip("TEST_DATABASE_URL is not set; PostgreSQL auth matrix not run")
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
	keyring, err := security.NewKeyring(security.SigningKey{KID: "matrix", Private: private, Public: public}, nil)
	if err != nil {
		t.Fatalf("ephemeral keyring setup failed (%T)", err)
	}
	weakSalt := bytes.Repeat([]byte{0x33}, 16)
	weakKey := argon2.IDKey([]byte("matrix password"), weakSalt, 1, 8192, 1, 32)
	weaker := "$argon2id$v=19$m=8192,t=1,p=1$" + base64.RawStdEncoding.EncodeToString(weakSalt) + "$" + base64.RawStdEncoding.EncodeToString(weakKey)
	account := "auth-matrix-" + uuid.NewString()[:12]
	if err := db.Exec(`INSERT INTO users (account, password_hash, name, auth_enabled) VALUES (?, ?, ?, true)`, account, weaker, "Matrix User").Error; err != nil {
		t.Fatalf("matrix user setup failed (%T)", err)
	}

	runner := NewGormTransactionRunnerWithConfig(db, Config{
		Signer:           keyring,
		Now:              func() time.Time { return now },
		PersistenceClock: func() time.Time { return now },
		Random:           &fixedReader{data: append(bytes.Repeat([]byte{4}, 32), bytes.Repeat([]byte{5}, 96)...)},
	})
	login, err := runner.Login(ctx, account, "matrix password")
	if err != nil {
		t.Fatalf("weaker PHC login failed (%T)", err)
	}
	var storedHash string
	if err := db.Raw(`SELECT password_hash FROM users WHERE account = ?`, account).Scan(&storedHash).Error; err != nil {
		t.Fatalf("password hash query failed (%T)", err)
	}
	if storedHash != weaker {
		t.Fatal("weaker supported PHC was rewritten")
	}
	claimsA, err := keyring.VerifyAccessTokenAt(login.AccessToken, now)
	if err != nil {
		t.Fatalf("access A verification failed (%T)", err)
	}
	identity, err := authenticateWithRunner(ctx, runner, login.AccessToken)
	if err != nil || identity.UserID == 0 || identity.SessionID.String() != claimsA.SID {
		t.Fatalf("active authentication predicate failed=%t err=%v", err == nil && identity.UserID != 0 && identity.SessionID.String() == claimsA.SID, err)
	}

	rotated, err := runner.Refresh(ctx, login.RefreshToken)
	if err != nil {
		t.Fatalf("refresh A to B failed (%T)", err)
	}
	claimsB, err := keyring.VerifyAccessTokenAt(rotated.AccessToken, now)
	if err != nil || claimsB.Subject != claimsA.Subject || claimsB.SID != claimsA.SID || claimsB.ExpiresAt == nil || claimsB.ExpiresAt.Time.Sub(now) != security.AccessTokenTTL {
		t.Fatalf("rotation identity/TTL predicate failed=%t err=%v", err == nil && claimsB.Subject == claimsA.Subject && claimsB.SID == claimsA.SID && claimsB.ExpiresAt != nil && claimsB.ExpiresAt.Time.Sub(now) == security.AccessTokenTTL, err)
	}
	var familyState struct {
		Current      int
		Consumed     int
		FamilyExpiry time.Time
	}
	if err := db.Raw(`
		SELECT
			(SELECT count(*) FROM refresh_tokens WHERE session_id = ? AND consumed_at IS NULL AND revoked_at IS NULL) AS current,
			(SELECT count(*) FROM refresh_tokens WHERE session_id = ? AND consumed_at IS NOT NULL) AS consumed,
			(SELECT expires_at FROM refresh_sessions WHERE id = ?) AS family_expiry`, claimsA.SID, claimsA.SID, claimsA.SID).Scan(&familyState).Error; err != nil {
		t.Fatalf("rotation state query failed (%T)", err)
	}
	if familyState.Current != 1 || familyState.Consumed != 1 || !familyState.FamilyExpiry.Equal(now.Add(RefreshFamilyTTL)) {
		t.Fatalf("rotation state current=%d consumed=%d fixed_expiry=%t", familyState.Current, familyState.Consumed, familyState.FamilyExpiry.Equal(now.Add(RefreshFamilyTTL)))
	}

	wrongSubject, err := keyring.IssueAccessTokenAt("999999", claimsA.SID, now)
	if err != nil {
		t.Fatalf("wrong-subject token setup failed (%T)", err)
	}
	if _, err := authenticateWithRunner(ctx, runner, wrongSubject); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("sub mismatch err=%v, want unauthorized", err)
	}
	expiredJWT, err := keyring.IssueAccessTokenAt(claimsA.Subject, claimsA.SID, now.Add(-security.AccessTokenTTL-security.JWTClockSkew-time.Second))
	if err != nil {
		t.Fatalf("expired JWT setup failed (%T)", err)
	}
	if _, err := authenticateWithRunner(ctx, runner, expiredJWT); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expired JWT err=%v, want unauthorized", err)
	}
	if err := db.Exec(`UPDATE refresh_sessions SET expires_at = ? WHERE id = ?`, now.Add(time.Second), claimsA.SID).Error; err != nil {
		t.Fatalf("expired session setup failed (%T)", err)
	}
	expiredSessionRunner := NewGormTransactionRunnerWithConfig(db, Config{
		Signer:           keyring,
		Now:              func() time.Time { return now.Add(2 * time.Second) },
		PersistenceClock: func() time.Time { return now.Add(2 * time.Second) },
		Random:           &fixedReader{data: bytes.Repeat([]byte{6}, 64)},
	})
	if _, err := authenticateWithRunner(ctx, expiredSessionRunner, rotated.AccessToken); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expired session access err=%v, want unauthorized", err)
	}
	if err := db.Exec(`UPDATE refresh_sessions SET expires_at = ? WHERE id = ?`, now.Add(RefreshFamilyTTL), claimsA.SID).Error; err != nil {
		t.Fatalf("session restore failed (%T)", err)
	}
	if err := runner.Logout(ctx, identity); err != nil {
		t.Fatalf("logout failed (%T)", err)
	}
	if _, err := authenticateWithRunner(ctx, runner, rotated.AccessToken); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked session access err=%v, want unauthorized", err)
	}
}

func authenticateWithRunner(ctx context.Context, runner *GormTransactionRunner, raw string) (AuthenticatedIdentity, error) {
	var identity AuthenticatedIdentity
	err := runner.WithTransaction(ctx, func(app *Application) error {
		var err error
		identity, err = app.AuthenticateAccessToken(ctx, raw)
		return err
	})
	if err != nil {
		return AuthenticatedIdentity{}, err
	}
	return identity, nil
}
