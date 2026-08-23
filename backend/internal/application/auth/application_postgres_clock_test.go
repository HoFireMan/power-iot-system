package auth

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"power-iot-backend/internal/data/migrations"
	"power-iot-backend/internal/security"
	"power-iot-backend/internal/testsupport"
)

type sharedAuthClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *sharedAuthClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *sharedAuthClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func TestAuthRunnerPostgresUsesSharedClockForRotationExpiry(t *testing.T) {
	runner, keyring, clock, account, cleanup := newClockAuthFixture(t)
	defer cleanup()
	ctx := context.Background()

	login, err := runner.Login(ctx, account, "clock password")
	if err != nil {
		t.Fatalf("login failed (%T)", err)
	}
	clock.Advance(RefreshFamilyTTL - time.Second)
	rotated, err := runner.Refresh(ctx, login.RefreshToken)
	if err != nil || rotated.RefreshToken == "" {
		t.Fatalf("before-expiry rotation failed=%t err=%v", err == nil && rotated.RefreshToken != "", err)
	}
	if _, err := keyring.VerifyAccessTokenAt(rotated.AccessToken, clock.Now()); err != nil {
		t.Fatalf("before-expiry access token verification failed (%T)", err)
	}

	clock.Advance(2 * time.Second)
	if _, err := runner.Refresh(ctx, rotated.RefreshToken); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("after-expiry rotation err=%v, want unauthorized", err)
	}
}

// TestAuthRunnerPostgresPersistenceClockIsSampledAfterLocking exercises the
// runner's injected B2 clock while a competing transaction holds the session
// lock. The clock callback must not run until the lock is released.
func TestAuthRunnerPostgresPersistenceClockIsSampledAfterLocking(t *testing.T) {
	runner, _, clock, account, cleanup := newClockAuthFixture(t)
	defer cleanup()
	ctx := context.Background()

	login, err := runner.Login(ctx, account, "clock password")
	if err != nil {
		t.Fatalf("login failed (%T)", err)
	}
	claims, err := runner.signer.VerifyAccessTokenAt(login.AccessToken, clock.Now())
	if err != nil {
		t.Fatalf("login access token verification failed (%T)", err)
	}

	// Replace the persistence seam only for this proof. Application time stays
	// independent, while the callback records the post-lock decision sample.
	var sampledMu sync.Mutex
	sampled := 0
	runner.persistenceClock = func() time.Time {
		sampledMu.Lock()
		sampled++
		sampledMu.Unlock()
		return clock.Now()
	}

	lockTx := runner.db.Begin()
	if lockTx.Error != nil {
		t.Fatalf("lock transaction failed (%T)", lockTx.Error)
	}
	if err := lockTx.Exec(`SELECT id FROM refresh_sessions WHERE id = ? FOR UPDATE`, claims.SID).Error; err != nil {
		_ = lockTx.Rollback()
		t.Fatalf("session lock failed (%T)", err)
	}

	resultCh := make(chan error, 1)
	go func() {
		_, refreshErr := runner.Refresh(ctx, login.RefreshToken)
		resultCh <- refreshErr
	}()
	time.Sleep(150 * time.Millisecond)
	sampledMu.Lock()
	beforeRelease := sampled
	sampledMu.Unlock()
	if beforeRelease != 0 {
		_ = lockTx.Rollback()
		t.Fatalf("persistence clock sampled before session lock release: %d", beforeRelease)
	}
	if err := lockTx.Commit().Error; err != nil {
		t.Fatalf("session lock release failed (%T)", err)
	}
	select {
	case refreshErr := <-resultCh:
		if refreshErr != nil {
			t.Fatalf("rotation after lock release failed (%T)", refreshErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("rotation did not complete after session lock release")
	}
	sampledMu.Lock()
	afterRelease := sampled
	sampledMu.Unlock()
	if afterRelease != 1 {
		t.Fatalf("persistence clock samples=%d, want one post-lock sample", afterRelease)
	}
}

func newClockAuthFixture(t *testing.T) (*GormTransactionRunner, *security.Keyring, *sharedAuthClock, string, func()) {
	t.Helper()
	source := os.Getenv("TEST_DATABASE_URL")
	if source == "" {
		t.Skip("TEST_DATABASE_URL is not set; PostgreSQL clock integration proof not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	isolated, err := testsupport.New(ctx, source, migrations.Up)
	if err != nil {
		cancel()
		t.Fatalf("isolated database setup failed (%T)", err)
	}
	db, err := gorm.Open(postgres.Open(isolated.DSN()), &gorm.Config{})
	if err != nil {
		_ = isolated.Close()
		cancel()
		t.Fatalf("database open failed (%T)", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		_ = isolated.Close()
		cancel()
		t.Fatalf("database handle failed (%T)", err)
	}

	now := &sharedAuthClock{now: time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ephemeral key generation failed (%T)", err)
	}
	keyring, err := security.NewKeyring(security.SigningKey{KID: "clock-proof", Private: private, Public: public}, nil)
	if err != nil {
		t.Fatalf("ephemeral keyring setup failed (%T)", err)
	}
	hash, err := security.HashPassword([]byte("clock password"))
	if err != nil {
		t.Fatalf("password fixture setup failed (%T)", err)
	}
	account := "auth-clock-" + uuid.NewString()[:12]
	if err := db.Exec(`INSERT INTO users (account, password_hash, name, auth_enabled) VALUES (?, ?, ?, true)`, account, hash, "Clock User").Error; err != nil {
		t.Fatalf("clock user setup failed (%T)", err)
	}
	runner := NewGormTransactionRunnerWithConfig(db, Config{
		Signer:           keyring,
		Now:              now.Now,
		PersistenceClock: now.Now,
		Random: &fixedReader{data: bytes.Join([][]byte{
			bytes.Repeat([]byte{7}, 32),
			bytes.Repeat([]byte{8}, 32),
			bytes.Repeat([]byte{9}, 32),
		}, nil)},
	})
	cleanup := func() {
		cancel()
		_ = sqlDB.Close()
		_ = isolated.Close()
	}
	return runner, keyring, now, account, cleanup
}
