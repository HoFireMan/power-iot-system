package auth

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/argon2"

	"power-iot-backend/internal/adapters/persistence"
	"power-iot-backend/internal/core/domain"
	"power-iot-backend/internal/security"
)

type fakeAuthRepository struct {
	user             *domain.User
	account          string
	findErr          error
	createErr        error
	insertErr        error
	rotateResult     persistence.RotateRefreshTokenResult
	rotateErr        error
	rotateCmd        persistence.RotateRefreshTokenCommand
	session          persistence.RefreshSession
	activeErr        error
	revokeErr        error
	created          []persistence.RefreshSession
	inserted         []persistence.RefreshTokenInsertCommand
	revoked          []uuid.UUID
	invokeAbuseCheck bool
}

func (f *fakeAuthRepository) FindUserByAccount(_ context.Context, account string) (*domain.User, error) {
	f.account = account
	if f.findErr != nil {
		return nil, f.findErr
	}
	if f.user == nil {
		return nil, persistence.ErrAuthRecordNotFound
	}
	copy := *f.user
	return &copy, nil
}
func (f *fakeAuthRepository) CreateRefreshSession(_ context.Context, value persistence.RefreshSession) (persistence.RefreshSession, error) {
	if f.createErr != nil {
		return persistence.RefreshSession{}, f.createErr
	}
	f.created = append(f.created, value)
	return value, nil
}
func (f *fakeAuthRepository) InsertRefreshTokenDigest(_ context.Context, value persistence.RefreshTokenInsertCommand) (persistence.RefreshTokenState, error) {
	if f.insertErr != nil {
		return persistence.RefreshTokenState{}, f.insertErr
	}
	f.inserted = append(f.inserted, value)
	return persistence.RefreshTokenState{ID: value.ID, SessionID: value.SessionID, IssuedAt: value.IssuedAt, ExpiresAt: value.ExpiresAt}, nil
}
func (f *fakeAuthRepository) LockRefreshSession(context.Context, uuid.UUID) (persistence.RefreshSession, error) {
	return persistence.RefreshSession{}, persistence.ErrAuthRecordNotFound
}
func (f *fakeAuthRepository) LockRefreshToken(context.Context, uuid.UUID) (persistence.RefreshTokenState, error) {
	return persistence.RefreshTokenState{}, persistence.ErrAuthRecordNotFound
}
func (f *fakeAuthRepository) GetActiveSessionBySID(_ context.Context, id uuid.UUID, _ time.Time) (persistence.RefreshSession, error) {
	if f.activeErr != nil {
		return persistence.RefreshSession{}, f.activeErr
	}
	for _, revoked := range f.revoked {
		if revoked == id {
			return persistence.RefreshSession{}, persistence.ErrAuthRecordNotFound
		}
	}
	return f.session, nil
}

func (f *fakeAuthRepository) RevokeSessionFamily(_ context.Context, id uuid.UUID, _ time.Time) error {
	f.revoked = append(f.revoked, id)
	return f.revokeErr
}
func (f *fakeAuthRepository) RotateRefreshToken(ctx context.Context, command persistence.RotateRefreshTokenCommand) (persistence.RotateRefreshTokenResult, error) {
	f.rotateCmd = command
	result := f.rotateResult
	if f.invokeAbuseCheck && command.AbuseCheck != nil && (result.Outcome == persistence.RotateRefreshTokenSucceeded || result.Outcome == persistence.RotateRefreshTokenReplay) {
		allowed := command.AbuseCheck(ctx, persistence.RefreshAbuseDecision{
			SourceIP: command.SourceIP, SessionID: result.SessionID,
			Replay: result.Outcome == persistence.RotateRefreshTokenReplay,
		})
		if result.Outcome == persistence.RotateRefreshTokenSucceeded && !allowed {
			result.Outcome = persistence.RotateRefreshTokenRejected
		}
	}
	return result, f.rotateErr
}

type failingSigner struct{ issueErr, verifyErr error }

func (s failingSigner) IssueAccessTokenAt(string, string, time.Time) (string, error) {
	return "", s.issueErr
}
func (s failingSigner) VerifyAccessTokenAt(string, time.Time) (security.AccessClaims, error) {
	return security.AccessClaims{}, s.verifyErr
}

type fixedReader struct{ data []byte }

func (r *fixedReader) Read(p []byte) (int, error) {
	if len(r.data) < len(p) {
		return 0, io.ErrUnexpectedEOF
	}
	copy(p, r.data[:len(p)])
	r.data = r.data[len(p):]
	return len(p), nil
}

func testKeyring(t *testing.T, now time.Time) *security.Keyring {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyring, err := security.NewKeyring(security.SigningKey{KID: "test", Private: private, Public: public}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return keyring.WithClock(func() time.Time { return now })
}

func testUser(t *testing.T, enabled bool) *domain.User {
	t.Helper()
	hash, err := security.HashPassword([]byte("correct password"))
	if err != nil {
		t.Fatal(err)
	}
	return &domain.User{ID: 42, Account: "exact", PasswordHash: hash, AuthEnabled: enabled}
}

func TestLoginSuccessPersistsOneFamilyAndFrozenClaims(t *testing.T) {
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	repo := &fakeAuthRepository{user: testUser(t, true)}
	app := NewWithConfig(Config{Repository: repo, Signer: testKeyring(t, now), Now: func() time.Time { return now }, Random: &fixedReader{data: make([]byte, 32)}})
	result, err := app.Login(context.Background(), "exact", "correct password")
	if err != nil {
		t.Fatal(err)
	}
	if result.AccessToken == "" || result.RefreshToken == "" || len(repo.created) != 1 || len(repo.inserted) != 1 {
		t.Fatalf("result or persistence incomplete: access=%t refresh=%t created=%d inserted=%d", result.AccessToken != "", result.RefreshToken != "", len(repo.created), len(repo.inserted))
	}
	if repo.account != "exact" {
		t.Fatalf("account lookup preserved exact predicate=%t", repo.account == "exact")
	}
	claims, err := app.signer.VerifyAccessTokenAt(result.AccessToken, now)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Subject != "42" || claims.SID != repo.created[0].ID.String() || claims.Issuer != security.JWTIssuer || claims.Audience[0] != security.JWTAudience {
		t.Fatalf("access claims identity predicate failed")
	}
	if !repo.created[0].ExpiresAt.Equal(now.Add(RefreshFamilyTTL)) || !repo.inserted[0].ExpiresAt.Equal(now.Add(RefreshFamilyTTL)) {
		t.Fatal("family expiry did not remain fixed at 30 days")
	}
}

func TestLoginAcceptsWeakerSupportedArgon2WithoutRewritingHash(t *testing.T) {
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	salt := bytesForTest(16, 0x21)
	key := argon2.IDKey([]byte("correct password"), salt, 1, 8192, 1, 32)
	weaker := fmt.Sprintf("$argon2id$v=19$m=8192,t=1,p=1$%s$%s", base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
	user := &domain.User{ID: 42, Account: "exact", PasswordHash: weaker, AuthEnabled: true}
	repo := &fakeAuthRepository{user: user}
	app := NewWithConfig(Config{Repository: repo, Signer: testKeyring(t, now), Now: func() time.Time { return now }, Random: &fixedReader{data: make([]byte, 32)}})
	result, err := app.Login(context.Background(), "exact", "correct password")
	if err != nil || result.AccessToken == "" || result.RefreshToken == "" {
		t.Fatalf("login result present=%t/%t err=%v", result.AccessToken != "", result.RefreshToken != "", err)
	}
	if user.PasswordHash != weaker || repo.user.PasswordHash != weaker {
		t.Fatal("login rewrote the weaker supported password hash")
	}
}

func bytesForTest(size int, value byte) []byte {
	out := make([]byte, size)
	for i := range out {
		out[i] = value
	}
	return out
}

func TestLoginInvalidCredentialsCollapseAndSignerFailureLeavesNoFamily(t *testing.T) {
	cases := []struct {
		name string
		user *domain.User
		pass string
	}{
		{"missing", nil, "correct password"},
		{"disabled", testUser(t, false), "correct password"},
		{"wrong password", testUser(t, true), "wrong"},
		{"malformed PHC", &domain.User{ID: 1, AuthEnabled: true, PasswordHash: "$argon2id$bad"}, "correct password"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeAuthRepository{user: tc.user}
			app := NewWithConfig(Config{Repository: repo, Signer: testKeyring(t, time.Now()), Now: time.Now, Random: &fixedReader{data: make([]byte, 32)}})
			if _, err := app.Login(context.Background(), "exact", tc.pass); !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("err=%v, want generic invalid credentials", err)
			}
			if len(repo.created) != 0 || len(repo.inserted) != 0 {
				t.Fatal("invalid credentials created persistence rows")
			}
		})
	}

	repo := &fakeAuthRepository{user: testUser(t, true)}
	app := NewWithConfig(Config{Repository: repo, Signer: failingSigner{issueErr: errors.New("injected signing failure")}, Now: time.Now, Random: &fixedReader{data: make([]byte, 32)}})
	if _, err := app.Login(context.Background(), "exact", "correct password"); !errors.Is(err, ErrInfrastructure) {
		t.Fatalf("err=%v, want infrastructure error", err)
	}
	if len(repo.created) != 0 || len(repo.inserted) != 0 {
		t.Fatal("signer failure left a usable family")
	}
}

func TestLoginPersistenceFailureReturnsNoUsableResult(t *testing.T) {
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	repo := &fakeAuthRepository{user: testUser(t, true), insertErr: errors.New("injected persistence failure")}
	app := NewWithConfig(Config{Repository: repo, Signer: testKeyring(t, now), Now: func() time.Time { return now }, Random: &fixedReader{data: make([]byte, 32)}})
	result, err := app.Login(context.Background(), "exact", "correct password")
	if !errors.Is(err, ErrInfrastructure) || result != (LoginResult{}) {
		t.Fatalf("result present=%t/%t err=%v", result.AccessToken != "", result.RefreshToken != "", err)
	}
	if len(repo.created) != 1 || len(repo.inserted) != 0 {
		t.Fatalf("partial persistence calls created=%d inserted=%d", len(repo.created), len(repo.inserted))
	}
}

func TestRefreshSuccessRotatesToReplacementAndKeepsFamilyExpiry(t *testing.T) {
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	sid := uuid.New()
	repo := &fakeAuthRepository{rotateResult: persistence.RotateRefreshTokenResult{Outcome: persistence.RotateRefreshTokenSucceeded, SessionID: sid}, session: persistence.RefreshSession{ID: sid, UserID: 42, CreatedAt: now, ExpiresAt: now.Add(RefreshFamilyTTL)}}
	keyring := testKeyring(t, now)
	app := NewWithConfig(Config{Repository: repo, Signer: keyring, Now: func() time.Time { return now }, Random: &fixedReader{data: make([]byte, 32)}})
	result, err := app.Refresh(context.Background(), "presented-A")
	if err != nil || result.AccessToken == "" || result.RefreshToken == "" {
		t.Fatalf("result present=%t/%t err=%v", result.AccessToken != "", result.RefreshToken != "", err)
	}
	presented := sha256.Sum256([]byte("presented-A"))
	if string(repo.rotateCmd.PresentedDigest) != string(presented[:]) || string(repo.rotateCmd.ReplacementDigest) == string(repo.rotateCmd.PresentedDigest) {
		t.Fatal("refresh did not hash presented and replacement tokens independently")
	}
	claims, err := keyring.VerifyAccessTokenAt(result.AccessToken, now)
	if err != nil || claims.Subject != "42" || claims.SID != sid.String() {
		t.Fatalf("access claims identity predicate failed=%t err=%v", err == nil && claims.Subject == "42" && claims.SID == sid.String(), err)
	}
	if !repo.session.ExpiresAt.Equal(now.Add(RefreshFamilyTTL)) {
		t.Fatal("refresh changed fixed family expiry")
	}
}

type refreshLimiterProbe struct {
	sourceIP string
	family   string
	calls    int
	allow    bool
}

func (p *refreshLimiterProbe) RefreshAttemptAccepted(sourceIP, family string) bool {
	p.sourceIP, p.family, p.calls = sourceIP, family, p.calls+1
	return p.allow
}

func TestRefreshPolicyReceivesOnlyAuthoritativeIPAndStableFamily(t *testing.T) {
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	sid := uuid.New()
	probe := &refreshLimiterProbe{allow: false}
	repo := &fakeAuthRepository{
		invokeAbuseCheck: true,
		rotateResult:     persistence.RotateRefreshTokenResult{Outcome: persistence.RotateRefreshTokenSucceeded, SessionID: sid},
		session:          persistence.RefreshSession{ID: sid, UserID: 42, CreatedAt: now, ExpiresAt: now.Add(RefreshFamilyTTL)},
	}
	app := NewWithConfig(Config{Repository: repo, Signer: testKeyring(t, now), RefreshLimiter: probe, Now: func() time.Time { return now }, Random: &fixedReader{data: make([]byte, 32)}})
	if _, err := app.RefreshWithSourceIP(context.Background(), "presented", "203.0.113.9"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("denied current refresh err=%v, want unauthorized", err)
	}
	if probe.calls != 1 || probe.sourceIP != "203.0.113.9" || probe.family != sid.String() {
		t.Fatalf("policy input calls=%d ip=%q family=%q", probe.calls, probe.sourceIP, probe.family)
	}
	if len(repo.rotateCmd.PresentedDigest) == 0 || repo.rotateCmd.AbuseCheck == nil {
		t.Fatal("refresh policy seam did not retain digest-free callback")
	}
}

func TestRefreshPolicyNeverSeesUnknownTokenFamily(t *testing.T) {
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	probe := &refreshLimiterProbe{allow: false}
	repo := &fakeAuthRepository{invokeAbuseCheck: true, rotateResult: persistence.RotateRefreshTokenResult{Outcome: persistence.RotateRefreshTokenUnknown}}
	app := NewWithConfig(Config{Repository: repo, Signer: testKeyring(t, now), RefreshLimiter: probe, Now: func() time.Time { return now }, Random: &fixedReader{data: make([]byte, 32)}})
	if _, err := app.RefreshWithSourceIP(context.Background(), "unknown", "203.0.113.10"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unknown refresh err=%v, want unauthorized", err)
	}
	if probe.calls != 0 {
		t.Fatalf("unknown token fabricated family policy call count=%d", probe.calls)
	}
}

func TestRefreshPolicyDenialCannotSuppressReplayOutcome(t *testing.T) {
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	sid := uuid.New()
	probe := &refreshLimiterProbe{allow: false}
	repo := &fakeAuthRepository{
		invokeAbuseCheck: true,
		rotateResult:     persistence.RotateRefreshTokenResult{Outcome: persistence.RotateRefreshTokenReplay, SessionID: sid},
	}
	app := NewWithConfig(Config{Repository: repo, Signer: testKeyring(t, now), RefreshLimiter: probe, Now: func() time.Time { return now }, Random: &fixedReader{data: make([]byte, 32)}})
	if _, err := app.RefreshWithSourceIP(context.Background(), "historical", "198.51.100.4"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("replay err=%v, want unauthorized", err)
	}
	if probe.calls != 1 || probe.family != sid.String() {
		t.Fatalf("replay policy input calls=%d family=%q", probe.calls, probe.family)
	}
}

func TestRefreshSignerFailureBeforeCommitDoesNotRevokeFamilyAndNoResult(t *testing.T) {
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	sid := uuid.New()
	repo := &fakeAuthRepository{rotateResult: persistence.RotateRefreshTokenResult{Outcome: persistence.RotateRefreshTokenSucceeded, SessionID: sid}, session: persistence.RefreshSession{ID: sid, UserID: 42, CreatedAt: now, ExpiresAt: now.Add(RefreshFamilyTTL)}}
	app := NewWithConfig(Config{Repository: repo, Signer: failingSigner{issueErr: errors.New("injected signing failure")}, Now: func() time.Time { return now }, Random: &fixedReader{data: make([]byte, 64)}})
	result, err := app.Refresh(context.Background(), "presented")
	if !errors.Is(err, ErrInfrastructure) || result != (RefreshResult{}) {
		t.Fatalf("result present=%t/%t err=%v", result.AccessToken != "", result.RefreshToken != "", err)
	}
	if len(repo.revoked) != 0 {
		t.Fatalf("revocations=%v, want no compensation before caller commit", repo.revoked)
	}
}

func TestAuthenticateRequiresLiveMatchingSessionAndLogoutRevokes(t *testing.T) {
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	sid := uuid.New()
	repo := &fakeAuthRepository{session: persistence.RefreshSession{ID: sid, UserID: 42, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}}
	keyring := testKeyring(t, now)
	raw, err := keyring.IssueAccessTokenAt("42", sid.String(), now)
	if err != nil {
		t.Fatal(err)
	}
	app := NewWithConfig(Config{Repository: repo, Signer: keyring, Now: func() time.Time { return now }})
	identity, err := app.AuthenticateAccessToken(context.Background(), raw)
	if err != nil || identity.UserID != 42 || identity.SessionID != sid {
		t.Fatalf("authenticated identity predicate failed=%t err=%v", err == nil && identity.UserID == 42 && identity.SessionID == sid, err)
	}
	if err := app.Logout(context.Background(), identity); err != nil {
		t.Fatal(err)
	}
	if len(repo.revoked) != 1 || repo.revoked[0] != sid {
		t.Fatalf("revocations=%v", repo.revoked)
	}
	if _, err := app.AuthenticateAccessToken(context.Background(), raw); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("post-logout err=%v, want unauthorized", err)
	}
	// Verify the claim/session user binding independently after restoring a
	// live session in the fake repository.
	repo.revoked = nil
	repo.session.UserID = 43
	if _, err := app.AuthenticateAccessToken(context.Background(), raw); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("mismatch err=%v, want unauthorized", err)
	}
}
