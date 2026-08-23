package auth

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
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

// rotateFailureRepository simulates a database failure after B2 has staged a
// replay revocation. The runner must return infrastructure and roll that state
// back rather than converting it to generic unauthorized.
type rotateFailureRepository struct {
	persistence.AuthPersistence
}

func (r rotateFailureRepository) RotateRefreshToken(ctx context.Context, command persistence.RotateRefreshTokenCommand) (persistence.RotateRefreshTokenResult, error) {
	result, err := r.AuthPersistence.RotateRefreshToken(ctx, command)
	if err != nil {
		return result, err
	}
	return result, errors.New("injected replay persistence failure")
}

func TestRefreshRunnerReplayCommitAndFailureRollbackProof(t *testing.T) {
	source := os.Getenv("TEST_DATABASE_URL")
	if source == "" {
		t.Skip("TEST_DATABASE_URL is not set; PostgreSQL replay proof not run")
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
	keyring, err := security.NewKeyring(security.SigningKey{KID: "replay-proof", Private: private, Public: public}, nil)
	if err != nil {
		t.Fatalf("ephemeral keyring setup failed (%T)", err)
	}
	hash, err := security.HashPassword([]byte("proof password"))
	if err != nil {
		t.Fatalf("password fixture setup failed (%T)", err)
	}
	accountA := "auth-replay-a-" + uuid.NewString()[:12]
	accountB := "auth-replay-b-" + uuid.NewString()[:12]
	if err := db.Exec(`INSERT INTO users (account, password_hash, name, auth_enabled) VALUES (?, ?, ?, true), (?, ?, ?, true)`, accountA, hash, "Replay A", accountB, hash, "Replay B").Error; err != nil {
		t.Fatalf("proof user setup failed (%T)", err)
	}

	runner := NewGormTransactionRunnerWithConfig(db, Config{
		Signer:           keyring,
		Now:              func() time.Time { return now },
		PersistenceClock: func() time.Time { return now },
		Random: &fixedReader{data: bytes.Join([][]byte{
			bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32),
			bytes.Repeat([]byte{3}, 32), bytes.Repeat([]byte{4}, 32),
			bytes.Repeat([]byte{5}, 32), bytes.Repeat([]byte{6}, 32),
			bytes.Repeat([]byte{7}, 32), bytes.Repeat([]byte{8}, 32),
			bytes.Repeat([]byte{10}, 32), bytes.Repeat([]byte{11}, 32),
			bytes.Repeat([]byte{12}, 32),
		}, nil)},
	})
	loginA, err := runner.Login(ctx, accountA, "proof password")
	if err != nil {
		t.Fatalf("login A failed (%T)", err)
	}
	claimsA, err := keyring.VerifyAccessTokenAt(loginA.AccessToken, now)
	if err != nil {
		t.Fatalf("access A verification failed (%T)", err)
	}
	refreshB, err := runner.Refresh(ctx, loginA.RefreshToken)
	if err != nil {
		t.Fatalf("refresh A to B failed (%T)", err)
	}
	if refreshB.AccessToken == "" || refreshB.RefreshToken == "" {
		t.Fatal("successful refresh did not return both token-presence values")
	}
	claimsB, err := keyring.VerifyAccessTokenAt(refreshB.AccessToken, now)
	if err != nil || claimsB.Subject != claimsA.Subject || claimsB.SID != claimsA.SID {
		t.Fatalf("rotation claims preserved=%t err=%v", err == nil && claimsB.Subject == claimsA.Subject && claimsB.SID == claimsA.SID, err)
	}

	if _, err := runner.Refresh(ctx, loginA.RefreshToken); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("replay err=%v, want generic unauthorized", err)
	}
	var replayState struct {
		RevokedSession int
		RevokedTokens  int
		CurrentTokens  int
	}
	if err := db.Raw(`
		SELECT
			(SELECT count(*) FROM refresh_sessions WHERE id = ? AND revoked_at IS NOT NULL) AS revoked_session,
			(SELECT count(*) FROM refresh_tokens WHERE session_id = ? AND revoked_at IS NOT NULL) AS revoked_tokens,
			(SELECT count(*) FROM refresh_tokens WHERE session_id = ? AND consumed_at IS NULL AND revoked_at IS NULL) AS current_tokens`, claimsA.SID, claimsA.SID, claimsA.SID).Scan(&replayState).Error; err != nil {
		t.Fatalf("replay state query failed (%T)", err)
	}
	if replayState.RevokedSession != 1 || replayState.RevokedTokens != 2 || replayState.CurrentTokens != 0 {
		t.Fatalf("replay state revoked_session=%d revoked_tokens=%d current=%d", replayState.RevokedSession, replayState.RevokedTokens, replayState.CurrentTokens)
	}
	if _, err := runner.Refresh(ctx, refreshB.RefreshToken); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("replacement replay err=%v, want generic unauthorized", err)
	}
	if err := runner.WithTransaction(ctx, func(app *Application) error {
		_, err := app.AuthenticateAccessToken(ctx, loginA.AccessToken)
		return err
	}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked access err=%v, want generic unauthorized", err)
	}

	_, err = runner.Login(ctx, accountB, "proof password")
	if err != nil {
		t.Fatalf("login B failed (%T)", err)
	}
	unknown := encodeRefresh(bytes.Repeat([]byte{0xf1}, refreshBytes))
	if _, err := runner.Refresh(ctx, unknown); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unknown token err=%v, want generic unauthorized", err)
	}
	var activeB int
	if err := db.Raw(`SELECT count(*) FROM refresh_sessions s JOIN users u ON u.id = s.user_id WHERE u.account = ? AND s.revoked_at IS NULL`, accountB).Scan(&activeB).Error; err != nil {
		t.Fatalf("unknown-token state query failed (%T)", err)
	}
	if activeB != 1 {
		t.Fatalf("unknown token revoked unrelated family count=%d", activeB)
	}

	loginC, err := runner.Login(ctx, accountA, "proof password")
	if err != nil {
		t.Fatalf("login C failed (%T)", err)
	}
	rotatedC, err := runner.Refresh(ctx, loginC.RefreshToken)
	if err != nil {
		t.Fatalf("refresh C failed (%T)", err)
	}
	claimsC, err := keyring.VerifyAccessTokenAt(rotatedC.AccessToken, now)
	if err != nil {
		t.Fatalf("access C verification failed (%T)", err)
	}
	failureRunner := NewGormTransactionRunnerWithConfig(db, Config{
		Signer:           keyring,
		Now:              func() time.Time { return now },
		PersistenceClock: func() time.Time { return now },
		Random:           &fixedReader{data: bytes.Repeat([]byte{9}, 32)},
	})
	failureRunner.repositoryFactory = func(ctx context.Context, tx *gorm.DB) (persistence.AuthPersistence, error) {
		if err := persistence.AcquireAuthWriterFence(ctx, tx); err != nil {
			return nil, err
		}
		return rotateFailureRepository{AuthPersistence: persistence.NewAuthRepositoryWithClock(tx, func() time.Time { return now })}, nil
	}
	if _, err := failureRunner.Refresh(ctx, loginC.RefreshToken); !errors.Is(err, ErrInfrastructure) {
		t.Fatalf("injected replay failure err=%v, want infrastructure", err)
	}
	var rollbackState struct {
		RevokedSession int
		RevokedTokens  int
		CurrentTokens  int
	}
	if err := db.Raw(`
		SELECT
			(SELECT count(*) FROM refresh_sessions WHERE id = ? AND revoked_at IS NOT NULL) AS revoked_session,
			(SELECT count(*) FROM refresh_tokens WHERE session_id = ? AND revoked_at IS NOT NULL) AS revoked_tokens,
			(SELECT count(*) FROM refresh_tokens WHERE session_id = ? AND consumed_at IS NULL AND revoked_at IS NULL) AS current_tokens`, claimsC.SID, claimsC.SID, claimsC.SID).Scan(&rollbackState).Error; err != nil {
		t.Fatalf("rollback state query failed (%T)", err)
	}
	if rollbackState.RevokedSession != 0 || rollbackState.RevokedTokens != 0 || rollbackState.CurrentTokens != 1 {
		t.Fatalf("replay failure rollback revoked_session=%d revoked_tokens=%d current=%d", rollbackState.RevokedSession, rollbackState.RevokedTokens, rollbackState.CurrentTokens)
	}
	if _, err := runner.Refresh(ctx, rotatedC.RefreshToken); err != nil {
		t.Fatalf("current C token was not preserved after rollback: %v", err)
	}
}
