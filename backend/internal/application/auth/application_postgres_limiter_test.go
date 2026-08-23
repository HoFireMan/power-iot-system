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

type postgresRefreshPolicyProbe struct {
	mu         sync.Mutex
	denyIPs    map[string]bool
	denyFamily map[string]bool
	calls      int
	families   []string
}

func (p *postgresRefreshPolicyProbe) RefreshAttemptAccepted(sourceIP, family string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.families = append(p.families, family)
	return !p.denyIPs[sourceIP] && !p.denyFamily[family]
}

func (p *postgresRefreshPolicyProbe) snapshot() (int, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls, append([]string(nil), p.families...)
}

func TestAuthRunnerPostgresRefreshLimiterAuthorityMatrix(t *testing.T) {
	fixture := newPostgresLimiterFixture(t)
	defer fixture.close()

	ctx := context.Background()
	const sourceIP = "198.51.100.21"

	loginAllow := fixture.login(t, "allow")
	if _, err := fixture.runner.RefreshWithSourceIP(ctx, loginAllow.RefreshToken, sourceIP); err != nil {
		t.Fatalf("current token with IP/family allow failed: %v", err)
	}
	if calls, _ := fixture.probe.snapshot(); calls != 1 {
		t.Fatalf("allow policy calls=%d, want 1", calls)
	}

	loginIPDenied := fixture.login(t, "ip-deny")
	fixture.probe.mu.Lock()
	fixture.probe.denyIPs[sourceIP] = true
	fixture.probe.mu.Unlock()
	if _, err := fixture.runner.RefreshWithSourceIP(ctx, loginIPDenied.RefreshToken, sourceIP); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("IP-denied current token error=%v, want unauthorized", err)
	}
	fixture.assertCurrent(t, loginIPDenied, 1, 0, 0)

	fixture.probe.mu.Lock()
	delete(fixture.probe.denyIPs, sourceIP)
	fixture.probe.mu.Unlock()
	loginFamilyDenied := fixture.login(t, "family-deny")
	family := fixture.sessionID(t, loginFamilyDenied)
	fixture.probe.mu.Lock()
	fixture.probe.denyFamily[family.String()] = true
	fixture.probe.mu.Unlock()
	if _, err := fixture.runner.RefreshWithSourceIP(ctx, loginFamilyDenied.RefreshToken, sourceIP); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("family-denied current token error=%v, want unauthorized", err)
	}
	fixture.assertCurrent(t, loginFamilyDenied, 1, 0, 0)

	fixture.probe.mu.Lock()
	delete(fixture.probe.denyFamily, family.String())
	fixture.probe.mu.Unlock()
	loginReplayIP := fixture.login(t, "replay-ip")
	rotatedIP, err := fixture.runner.RefreshWithSourceIP(ctx, loginReplayIP.RefreshToken, sourceIP)
	if err != nil {
		t.Fatalf("replay IP setup rotation failed: %v", err)
	}
	fixture.probe.mu.Lock()
	fixture.probe.denyIPs[sourceIP] = true
	fixture.probe.mu.Unlock()
	if _, err := fixture.runner.RefreshWithSourceIP(ctx, loginReplayIP.RefreshToken, sourceIP); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("IP-denied historical replay error=%v, want unauthorized", err)
	}
	fixture.assertRevoked(t, loginReplayIP, 2)
	if rotatedIP.RefreshToken == "" {
		t.Fatal("successful setup rotation returned no replacement")
	}

	fixture.probe.mu.Lock()
	delete(fixture.probe.denyIPs, sourceIP)
	fixture.probe.mu.Unlock()
	loginReplayFamily := fixture.login(t, "replay-family")
	rotatedFamily, err := fixture.runner.RefreshWithSourceIP(ctx, loginReplayFamily.RefreshToken, sourceIP)
	if err != nil {
		t.Fatalf("replay family setup rotation failed: %v", err)
	}
	replayFamily := fixture.sessionID(t, loginReplayFamily)
	fixture.probe.mu.Lock()
	fixture.probe.denyFamily[replayFamily.String()] = true
	fixture.probe.mu.Unlock()
	if _, err := fixture.runner.RefreshWithSourceIP(ctx, loginReplayFamily.RefreshToken, sourceIP); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("family-denied historical replay error=%v, want unauthorized", err)
	}
	fixture.assertRevoked(t, loginReplayFamily, 2)
	if rotatedFamily.RefreshToken == "" {
		t.Fatal("successful setup rotation returned no replacement")
	}

	beforeUnknown, _ := fixture.probe.snapshot()
	unknownFamily := fixture.login(t, "unknown")
	unknown := encodeRefresh(bytes.Repeat([]byte{0xa7}, refreshBytes))
	if _, err := fixture.runner.RefreshWithSourceIP(ctx, unknown, sourceIP); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unknown token error=%v, want unauthorized", err)
	}
	afterUnknown, _ := fixture.probe.snapshot()
	if afterUnknown != beforeUnknown {
		t.Fatalf("unknown token policy calls changed from %d to %d", beforeUnknown, afterUnknown)
	}
	fixture.assertCurrent(t, unknownFamily, 1, 0, 0)
}

func TestAuthRunnerPostgresRefreshLimiterFamilyIdentityAndIsolation(t *testing.T) {
	fixture := newPostgresLimiterFixture(t)
	defer fixture.close()

	ctx := context.Background()
	const sourceIP = "198.51.100.22"
	login := fixture.login(t, "lineage")
	firstFamily := fixture.sessionID(t, login)
	rotatedB, err := fixture.runner.RefreshWithSourceIP(ctx, login.RefreshToken, sourceIP)
	if err != nil {
		t.Fatalf("A to B rotation failed: %v", err)
	}
	if _, err := fixture.runner.RefreshWithSourceIP(ctx, rotatedB.RefreshToken, sourceIP); err != nil {
		t.Fatalf("B to C rotation failed: %v", err)
	}
	calls, families := fixture.probe.snapshot()
	if calls != 2 || len(families) != 2 || families[0] != firstFamily.String() || families[1] != firstFamily.String() {
		t.Fatalf("A-to-B-to-C limiter identity calls=%d entries=%d stable=%t", calls, len(families), len(families) == 2 && families[0] == families[1] && families[0] == firstFamily.String())
	}

	independent := fixture.login(t, "independent")
	secondFamily := fixture.sessionID(t, independent)
	if secondFamily == firstFamily {
		t.Fatal("independent refresh sessions reused family identity")
	}
	if _, err := fixture.runner.RefreshWithSourceIP(ctx, independent.RefreshToken, sourceIP); err != nil {
		t.Fatalf("independent family rotation failed: %v", err)
	}
	_, families = fixture.probe.snapshot()
	if families[len(families)-1] != secondFamily.String() {
		t.Fatal("independent family did not use its own limiter bucket")
	}
}

func TestAuthRunnerPostgresRefreshLimiterSameTokenConcurrency(t *testing.T) {
	fixture := newPostgresLimiterFixture(t)
	defer fixture.close()

	login := fixture.login(t, "concurrent")
	const sourceIP = "198.51.100.23"
	results := make(chan error, 2)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			_, err := fixture.runner.RefreshWithSourceIP(context.Background(), login.RefreshToken, sourceIP)
			results <- err
		}()
	}
	close(start)
	var successes, unauthorized int
	for i := 0; i < 2; i++ {
		switch err := <-results; {
		case err == nil:
			successes++
		case errors.Is(err, ErrUnauthorized):
			unauthorized++
		default:
			t.Fatalf("same-token concurrency error=%v", err)
		}
	}
	if successes != 1 || unauthorized != 1 {
		t.Fatalf("same-token outcomes success=%d unauthorized=%d", successes, unauthorized)
	}
	fixture.assertRevoked(t, login, 2)
}

type postgresLimiterFixture struct {
	db       *gorm.DB
	sqlDB    interface{ Close() error }
	isolated *testsupport.Database
	runner   *GormTransactionRunner
	keyring  *security.Keyring
	probe    *postgresRefreshPolicyProbe
	now      time.Time
	cancel   context.CancelFunc
}

func newPostgresLimiterFixture(t *testing.T) *postgresLimiterFixture {
	t.Helper()
	source := os.Getenv("TEST_DATABASE_URL")
	if source == "" {
		t.Skip("TEST_DATABASE_URL is not set; PostgreSQL refresh limiter proof not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
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
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		_ = sqlDB.Close()
		_ = isolated.Close()
		cancel()
		t.Fatalf("ephemeral key generation failed (%T)", err)
	}
	keyring, err := security.NewKeyring(security.SigningKey{KID: "limiter-proof", Private: private, Public: public}, nil)
	if err != nil {
		_ = sqlDB.Close()
		_ = isolated.Close()
		cancel()
		t.Fatalf("ephemeral keyring setup failed (%T)", err)
	}
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	probe := &postgresRefreshPolicyProbe{denyIPs: make(map[string]bool), denyFamily: make(map[string]bool)}
	return &postgresLimiterFixture{
		db: db, sqlDB: sqlDB, isolated: isolated, runner: NewGormTransactionRunnerWithConfig(db, Config{
			Signer:           keyring,
			Now:              func() time.Time { return now },
			PersistenceClock: func() time.Time { return now },
			RefreshLimiter:   probe,
		}), keyring: keyring, probe: probe, now: now, cancel: cancel,
	}
}

func (f *postgresLimiterFixture) close() {
	f.cancel()
	if closer, ok := f.sqlDB.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
	_ = f.isolated.Close()
}

func (f *postgresLimiterFixture) login(t *testing.T, label string) LoginResult {
	t.Helper()
	hash, err := security.HashPassword([]byte("limiter proof password"))
	if err != nil {
		t.Fatalf("password fixture setup failed (%T)", err)
	}
	account := "auth-limiter-" + label + "-" + uuid.NewString()[:12]
	if err := f.db.Exec(`INSERT INTO users (account, password_hash, name, auth_enabled) VALUES (?, ?, ?, true)`, account, hash, label).Error; err != nil {
		t.Fatalf("user fixture setup failed (%T)", err)
	}
	result, err := f.runner.Login(context.Background(), account, "limiter proof password")
	if err != nil {
		t.Fatalf("login fixture failed (%T)", err)
	}
	return result
}

func (f *postgresLimiterFixture) sessionID(t *testing.T, pair LoginResult) uuid.UUID {
	t.Helper()
	claims, err := f.keyring.VerifyAccessTokenAt(pair.AccessToken, f.now)
	if err != nil {
		t.Fatalf("access fixture verification failed (%T)", err)
	}
	id, err := uuid.Parse(claims.SID)
	if err != nil {
		t.Fatalf("session fixture identity failed (%T)", err)
	}
	return id
}

func (f *postgresLimiterFixture) assertCurrent(t *testing.T, pair LoginResult, wantCurrent, wantConsumed, wantRevoked int) {
	t.Helper()
	f.assertState(t, f.sessionID(t, pair), wantCurrent, wantConsumed, wantRevoked)
}

func (f *postgresLimiterFixture) assertRevoked(t *testing.T, pair LoginResult, wantRevokedTokens int) {
	t.Helper()
	f.assertState(t, f.sessionID(t, pair), 0, 1, wantRevokedTokens)
}

func (f *postgresLimiterFixture) assertState(t *testing.T, family uuid.UUID, wantCurrent, wantConsumed, wantRevoked int) {
	t.Helper()
	var state struct {
		Current  int
		Consumed int
		Revoked  int
		Session  int
	}
	if err := f.db.Raw(`
		SELECT
			(SELECT count(*) FROM refresh_tokens WHERE session_id = ? AND consumed_at IS NULL AND revoked_at IS NULL) AS current,
			(SELECT count(*) FROM refresh_tokens WHERE session_id = ? AND consumed_at IS NOT NULL) AS consumed,
			(SELECT count(*) FROM refresh_tokens WHERE session_id = ? AND revoked_at IS NOT NULL) AS revoked,
			(SELECT count(*) FROM refresh_sessions WHERE id = ? AND revoked_at IS NOT NULL) AS session`, family, family, family, family).Scan(&state).Error; err != nil {
		t.Fatalf("refresh state query failed (%T)", err)
	}
	if state.Current != wantCurrent || state.Consumed != wantConsumed || state.Revoked != wantRevoked || (wantRevoked > 0 && state.Session != 1) {
		t.Fatalf("refresh state current=%d consumed=%d revoked=%d session=%d", state.Current, state.Consumed, state.Revoked, state.Session)
	}
}

var _ RefreshAbuseLimiter = (*postgresRefreshPolicyProbe)(nil)
