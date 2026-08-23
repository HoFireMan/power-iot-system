// Package auth provides the transport-independent authentication application
// service. It owns authentication policy while persistence, HTTP, and token
// cryptography remain behind narrow seams.
package auth

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"strconv"
	"time"

	"github.com/google/uuid"

	"power-iot-backend/internal/adapters/persistence"
	"power-iot-backend/internal/security"
)

const (
	RefreshFamilyTTL = 30 * 24 * time.Hour
	refreshBytes     = 32
)

var (
	// These errors intentionally contain no account, password, PHC, token, or
	// database data. Callers can map them without inspecting implementation
	// details.
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrUnauthorized       = errors.New("unauthorized")
	ErrInfrastructure     = errors.New("authentication infrastructure failure")
)

// AuthenticatedIdentity is the complete identity made available to later
// application layers. Authorization and shop data are deliberately absent.
type AuthenticatedIdentity struct {
	UserID    uint
	SessionID uuid.UUID
}

// TokenPair is an application result, not a wire representation. The raw
// refresh token exists only in this transient result and is never handed to
// persistence.
type TokenPair struct {
	AccessToken  string
	RefreshToken string
}

// LoginResultWithTimestamps is a transport composition seam. The timestamps
// are captured from the same application clock used for signing and session
// persistence; it does not alter TokenPair or token claims.
type LoginResultWithTimestamps struct {
	LoginResult
	AccessTokenExpiresAt  time.Time
	RefreshTokenExpiresAt time.Time
}

// RefreshResultWithTimestamps is the transport-only metadata seam for refresh.
// It preserves the TokenPair result and token claims while exposing expiry
// instants captured by the same application clock used for rotation and
// signing.
type RefreshResultWithTimestamps struct {
	RefreshResult
	AccessTokenExpiresAt  time.Time
	RefreshTokenExpiresAt time.Time
}

// These names make operation-specific APIs readable while retaining one
// minimal result shape.
type LoginResult = TokenPair
type RefreshResult = TokenPair

// AccessTokenSigner is implemented by security.Keyring. Keeping this seam
// injectable allows signing failures to be tested without replacing crypto.
type AccessTokenSigner interface {
	IssueAccessTokenAt(subject, sessionID string, now time.Time) (string, error)
	VerifyAccessTokenAt(raw string, now time.Time) (security.AccessClaims, error)
}

// RefreshAbuseLimiter is the narrow B1 capability needed by the refresh
// authority. The limiter receives only the authoritative source IP and the
// persisted family UUID string; it never receives a raw token or digest.
type RefreshAbuseLimiter interface {
	RefreshAttemptAccepted(sourceIP, sessionFamily string) bool
}

var _ RefreshAbuseLimiter = (*security.AbuseLimiter)(nil)

// Config contains only deterministic infrastructure seams needed by this
// service. The repository is bound to the caller-owned transaction.
type Config struct {
	Repository     persistence.AuthPersistence
	Signer         AccessTokenSigner
	Now            func() time.Time
	Random         io.Reader
	RefreshLimiter RefreshAbuseLimiter

	// PersistenceClock is a deterministic B3 test seam. Production leaves it
	// nil so B2 samples PostgreSQL clock_timestamp() after its row locks.
	PersistenceClock func() time.Time
}

// Application is safe to use with one transaction-bound repository at a time.
// Mutation callers must admit the transaction through the B2 auth writer-fence
// seam before invoking Login, Refresh, or Logout.
type Application struct {
	repository     persistence.AuthPersistence
	signer         AccessTokenSigner
	now            func() time.Time
	random         io.Reader
	refreshLimiter RefreshAbuseLimiter
}

// Service is an alternate name for callers that prefer the application-service
// terminology used at transport boundaries.
type Service = Application

func New(repository persistence.AuthPersistence, signer AccessTokenSigner) *Application {
	return NewWithConfig(Config{Repository: repository, Signer: signer})
}

func NewService(repository persistence.AuthPersistence, signer AccessTokenSigner) *Application {
	return New(repository, signer)
}

func NewWithConfig(config Config) *Application {
	now := config.Now
	if now == nil {
		now = time.Now
	}
	random := config.Random
	if random == nil {
		random = cryptorand.Reader
	}
	return &Application{repository: config.Repository, signer: config.Signer, now: now, random: random, refreshLimiter: config.RefreshLimiter}
}

// Login performs exact account lookup and creates one refresh-token family in
// the caller-owned transaction. The access token is signed before persistence
// so an injected signing failure cannot leave a usable family behind.
func (a *Application) Login(ctx context.Context, account, password string) (LoginResult, error) {
	result, _, _, err := a.login(ctx, account, password)
	return result, err
}

// LoginWithTimestamps is the transport-only metadata seam used by B5-A. It
// delegates to the same login authority and returns no additional identity or
// claims metadata.
func (a *Application) LoginWithTimestamps(ctx context.Context, account, password string) (LoginResultWithTimestamps, error) {
	result, accessExpiresAt, refreshExpiresAt, err := a.login(ctx, account, password)
	if err != nil {
		return LoginResultWithTimestamps{}, err
	}
	return LoginResultWithTimestamps{LoginResult: result, AccessTokenExpiresAt: accessExpiresAt, RefreshTokenExpiresAt: refreshExpiresAt}, nil
}

func (a *Application) login(ctx context.Context, account, password string) (LoginResult, time.Time, time.Time, error) {
	var out LoginResult
	if a == nil || a.repository == nil || a.signer == nil {
		return out, time.Time{}, time.Time{}, ErrInfrastructure
	}
	user, err := a.repository.FindUserByAccount(ctx, account)
	if err != nil {
		if errors.Is(err, persistence.ErrAuthRecordNotFound) {
			return out, time.Time{}, time.Time{}, ErrInvalidCredentials
		}
		return out, time.Time{}, time.Time{}, ErrInfrastructure
	}
	if user == nil || !user.AuthEnabled {
		return out, time.Time{}, time.Time{}, ErrInvalidCredentials
	}
	valid, verifyErr := security.VerifyPassword([]byte(password), user.PasswordHash)
	if verifyErr != nil || !valid {
		return out, time.Time{}, time.Time{}, ErrInvalidCredentials
	}

	now := a.clock()
	sessionID := uuid.New()
	access, err := a.issueAccess(user.ID, sessionID, now)
	if err != nil {
		return out, time.Time{}, time.Time{}, ErrInfrastructure
	}
	rawRefresh, digest, err := a.newRefreshToken()
	if err != nil {
		return out, time.Time{}, time.Time{}, ErrInfrastructure
	}
	session := persistence.RefreshSession{ID: sessionID, UserID: user.ID, CreatedAt: now, ExpiresAt: now.Add(RefreshFamilyTTL)}
	if _, err := a.repository.CreateRefreshSession(ctx, session); err != nil {
		return out, time.Time{}, time.Time{}, ErrInfrastructure
	}
	if _, err := a.repository.InsertRefreshTokenDigest(ctx, persistence.RefreshTokenInsertCommand{
		SessionID: sessionID, Digest: digest, IssuedAt: now, ExpiresAt: session.ExpiresAt,
	}); err != nil {
		return out, time.Time{}, time.Time{}, ErrInfrastructure
	}
	return LoginResult{AccessToken: access, RefreshToken: rawRefresh}, now.Add(security.AccessTokenTTL), session.ExpiresAt, nil
}

// Refresh preserves the transport-independent legacy call shape. A future
// caller that has resolved the authoritative peer should use
// RefreshWithSourceIP so the B1 policy runs inside the B2 authority flow.
func (a *Application) Refresh(ctx context.Context, rawRefresh string) (RefreshResult, error) {
	return a.RefreshWithSourceIP(ctx, rawRefresh, "")
}

// RefreshWithSourceIP consumes the presented token and obtains a replacement
// in one B2 lineage transition. The source IP is supplied by the trusted
// transport boundary, while family identity is discovered only after B2 locks
// the owning session and token. Since the replacement session's user is only
// revealed by RotateRefreshToken, access signing follows rotation.
func (a *Application) RefreshWithSourceIP(ctx context.Context, rawRefresh, sourceIP string) (RefreshResult, error) {
	result, _, _, err := a.refreshWithSourceIP(ctx, rawRefresh, sourceIP)
	return result, err
}

// RefreshWithSourceIPWithTimestamps is the transport-only metadata seam used
// by B5-B. It delegates to the same rotation authority and exposes no token
// classification, persistence, or family metadata.
func (a *Application) RefreshWithSourceIPWithTimestamps(ctx context.Context, rawRefresh, sourceIP string) (RefreshResultWithTimestamps, error) {
	result, accessExpiresAt, refreshExpiresAt, err := a.refreshWithSourceIP(ctx, rawRefresh, sourceIP)
	if err != nil {
		return RefreshResultWithTimestamps{}, err
	}
	return RefreshResultWithTimestamps{RefreshResult: result, AccessTokenExpiresAt: accessExpiresAt, RefreshTokenExpiresAt: refreshExpiresAt}, nil
}

func (a *Application) refreshWithSourceIP(ctx context.Context, rawRefresh, sourceIP string) (RefreshResult, time.Time, time.Time, error) {
	var out RefreshResult
	var zero time.Time
	if a == nil || a.repository == nil || a.signer == nil {
		return out, zero, zero, ErrInfrastructure
	}
	if rawRefresh == "" {
		return out, zero, zero, ErrUnauthorized
	}
	presented := sha256.Sum256([]byte(rawRefresh))
	replacementRaw, replacementDigest, err := a.newRefreshToken()
	if err != nil {
		return out, zero, zero, ErrInfrastructure
	}
	now := a.clock()
	var abuseCheck persistence.RefreshAbuseCheck
	if a.refreshLimiter != nil {
		abuseCheck = func(_ context.Context, decision persistence.RefreshAbuseDecision) bool {
			return a.refreshLimiter.RefreshAttemptAccepted(decision.SourceIP, decision.SessionID.String())
		}
	}
	rotation, err := a.repository.RotateRefreshToken(ctx, persistence.RotateRefreshTokenCommand{
		PresentedDigest: presented[:], ReplacementDigest: replacementDigest, SourceIP: sourceIP, AbuseCheck: abuseCheck, Now: now,
	})
	if err != nil {
		return out, zero, zero, ErrInfrastructure
	}
	switch rotation.Outcome {
	case persistence.RotateRefreshTokenUnknown, persistence.RotateRefreshTokenRejected:
		return out, zero, zero, ErrUnauthorized
	case persistence.RotateRefreshTokenReplay:
		// B2 has already revoked the family. This outcome must be committed by
		// the caller, rather than returned as a transaction error.
		return out, zero, zero, ErrUnauthorized
	case persistence.RotateRefreshTokenSucceeded:
		// continue
	default:
		return out, zero, zero, ErrUnauthorized
	}
	if rotation.SessionID == uuid.Nil {
		return out, zero, zero, ErrInfrastructure
	}
	session, err := a.repository.GetActiveSessionBySID(ctx, rotation.SessionID, now)
	if err != nil {
		return out, zero, zero, ErrInfrastructure
	}
	access, err := a.issueAccess(session.UserID, rotation.SessionID, now)
	if err != nil {
		return out, zero, zero, ErrInfrastructure
	}
	return RefreshResult{AccessToken: access, RefreshToken: replacementRaw}, now.Add(security.AccessTokenTTL), session.ExpiresAt, nil
}

// AuthenticateAccessToken verifies the complete B1 token policy and then
// always checks the live B2 session using the same UTC instant.
func (a *Application) AuthenticateAccessToken(ctx context.Context, rawAccess string) (AuthenticatedIdentity, error) {
	var identity AuthenticatedIdentity
	if a == nil || a.repository == nil || a.signer == nil {
		return identity, ErrInfrastructure
	}
	now := a.clock()
	claims, err := a.signer.VerifyAccessTokenAt(rawAccess, now)
	if err != nil {
		return identity, ErrUnauthorized
	}
	userID64, err := strconv.ParseUint(claims.Subject, 10, 64)
	if err != nil || userID64 == 0 || uint64(uint(userID64)) != userID64 {
		return identity, ErrUnauthorized
	}
	sessionID, err := uuid.Parse(claims.SID)
	if err != nil || sessionID == uuid.Nil {
		return identity, ErrUnauthorized
	}
	session, err := a.repository.GetActiveSessionBySID(ctx, sessionID, now)
	if err != nil {
		if errors.Is(err, persistence.ErrAuthRecordNotFound) {
			return identity, ErrUnauthorized
		}
		return identity, ErrInfrastructure
	}
	if session.UserID != uint(userID64) {
		return identity, ErrUnauthorized
	}
	return AuthenticatedIdentity{UserID: session.UserID, SessionID: sessionID}, nil
}

// Logout revokes the family identified by an already authenticated identity.
func (a *Application) Logout(ctx context.Context, identity AuthenticatedIdentity) error {
	if a == nil || a.repository == nil {
		return ErrInfrastructure
	}
	if identity.UserID == 0 || identity.SessionID == uuid.Nil {
		return ErrUnauthorized
	}
	if err := a.repository.RevokeSessionFamily(ctx, identity.SessionID, a.clock()); err != nil {
		if errors.Is(err, persistence.ErrAuthRecordNotFound) {
			return ErrUnauthorized
		}
		return ErrInfrastructure
	}
	return nil
}

func (a *Application) issueAccess(userID uint, sessionID uuid.UUID, now time.Time) (string, error) {
	if userID == 0 || sessionID == uuid.Nil {
		return "", ErrInfrastructure
	}
	return a.signer.IssueAccessTokenAt(strconv.FormatUint(uint64(userID), 10), sessionID.String(), now)
}

func (a *Application) newRefreshToken() (string, []byte, error) {
	buf := make([]byte, refreshBytes)
	if _, err := io.ReadFull(a.random, buf); err != nil {
		return "", nil, err
	}
	// URL encoding keeps the opaque application value transport-safe without
	// introducing JSON or any HTTP-specific representation.
	raw := encodeRefresh(buf)
	digest := sha256.Sum256([]byte(raw))
	return raw, digest[:], nil
}

func encodeRefresh(raw []byte) string {
	return base64.RawURLEncoding.EncodeToString(raw)
}

func (a *Application) clock() time.Time {
	now := a.now()
	if now.IsZero() {
		now = time.Now()
	}
	return now.UTC()
}
