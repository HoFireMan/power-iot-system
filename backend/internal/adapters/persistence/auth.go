package persistence

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"power-iot-backend/internal/core/domain"
	"power-iot-backend/internal/data/migrations"
)

const refreshTokenDigestSize = 32

var (
	// ErrAuthTransactionRequired is returned when an auth persistence seam is
	// used without a caller-owned transaction. Auth writes must never start or
	// commit a transaction themselves.
	ErrAuthTransactionRequired = errors.New("auth persistence transaction is required")
	ErrAuthInput               = errors.New("invalid auth persistence input")
	ErrAuthRecordNotFound      = errors.New("auth persistence record not found")
	ErrRefreshTokenDigestSize  = errors.New("refresh token digest must be SHA-256")
)

// RotateRefreshTokenOutcome is deliberately an internal classification rather
// than an error. In particular, callers must be able to commit a replay
// revocation; returning a Go error from a GORM Transaction closure would roll
// that revocation back.
type RotateRefreshTokenOutcome string

const (
	RotateRefreshTokenSucceeded RotateRefreshTokenOutcome = "rotated"
	RotateRefreshTokenReplay    RotateRefreshTokenOutcome = "replay_revoked"
	RotateRefreshTokenUnknown   RotateRefreshTokenOutcome = "unknown"
	RotateRefreshTokenRejected  RotateRefreshTokenOutcome = "rejected"
)

// RotateRefreshTokenCommand contains token digests plus non-secret refresh
// policy metadata. Replacement token material is supplied by the caller and is
// never generated or returned here. RefreshAbuseDecision is the post-lock,
// digest-free input to the refresh
// abuse policy. SessionID is the authoritative family identity; it is never
// resolved from an unknown token and is not part of any HTTP-facing seam.
type RefreshAbuseDecision struct {
	SourceIP  string
	SessionID uuid.UUID
	Replay    bool
}

// RefreshAbuseCheck is supplied by the application authority. Persistence
// invokes it only after the writer fence and session-then-token locks, and
// never supplies raw token material or a digest.
type RefreshAbuseCheck func(context.Context, RefreshAbuseDecision) bool

type RotateRefreshTokenCommand struct {
	PresentedDigest   []byte
	ReplacementDigest []byte
	SourceIP          string
	AbuseCheck        RefreshAbuseCheck
	// ReplacementTokenID is optional; persistence generates an identifier when
	// the caller does not need to choose one.
	ReplacementTokenID uuid.UUID
	// ReplacementExpiresAt defaults to the owning session expiry.
	ReplacementExpiresAt time.Time
	// Now is request metadata retained for API compatibility. It is never used
	// for an authoritative post-lock decision.
	Now time.Time
}

// RotateRefreshTokenResult contains identifiers and timestamps only, never a
// raw refresh token or a caller-supplied digest.
type RotateRefreshTokenResult struct {
	Outcome             RotateRefreshTokenOutcome
	SessionID           uuid.UUID
	ConsumedTokenID     uuid.UUID
	ReplacementTokenID  uuid.UUID
	ConsumedAt          time.Time
	ReplacementIssuedAt time.Time
}

// RefreshSession is the persisted server-side refresh-token family. It does
// not contain the presented opaque refresh token.
type RefreshSession struct {
	ID        uuid.UUID  `gorm:"column:id;type:uuid;primaryKey"`
	UserID    uint       `gorm:"column:user_id;not null"`
	CreatedAt time.Time  `gorm:"column:created_at;not null"`
	ExpiresAt time.Time  `gorm:"column:expires_at;not null"`
	RevokedAt *time.Time `gorm:"column:revoked_at"`
}

func (RefreshSession) TableName() string { return "refresh_sessions" }

// RefreshTokenState is the safe projection of a persisted token lineage row.
// The token digest is intentionally absent: callers can observe lifecycle state
// but cannot receive database rows containing token material.
type RefreshTokenState struct {
	ID                uuid.UUID
	SessionID         uuid.UUID
	IssuedAt          time.Time
	ExpiresAt         time.Time
	ConsumedAt        *time.Time
	RevokedAt         *time.Time
	ReplacedByTokenID *uuid.UUID
}

// RefreshTokenInsertCommand is the only exported command that accepts a
// caller-supplied token digest. It is an input, never a returned persistence
// state or database row.
type RefreshTokenInsertCommand struct {
	ID        uuid.UUID
	SessionID uuid.UUID
	Digest    []byte
	IssuedAt  time.Time
	ExpiresAt time.Time
}

// refreshTokenRow is private so token_hash cannot cross the persistence
// capability boundary. Every query and mutation involving the digest uses
// this row type internally.
type refreshTokenRow struct {
	ID                uuid.UUID  `gorm:"column:id;type:uuid;primaryKey"`
	SessionID         uuid.UUID  `gorm:"column:session_id;type:uuid;not null"`
	TokenHash         []byte     `gorm:"column:token_hash;type:bytea;not null"`
	IssuedAt          time.Time  `gorm:"column:issued_at;not null"`
	ExpiresAt         time.Time  `gorm:"column:expires_at;not null"`
	ConsumedAt        *time.Time `gorm:"column:consumed_at"`
	RevokedAt         *time.Time `gorm:"column:revoked_at"`
	ReplacedByTokenID *uuid.UUID `gorm:"column:replaced_by_token_id;type:uuid"`
}

func (refreshTokenRow) TableName() string { return "refresh_tokens" }

func refreshTokenState(row refreshTokenRow) RefreshTokenState {
	return RefreshTokenState{
		ID: row.ID, SessionID: row.SessionID, IssuedAt: row.IssuedAt,
		ExpiresAt: row.ExpiresAt, ConsumedAt: row.ConsumedAt,
		RevokedAt: row.RevokedAt, ReplacedByTokenID: row.ReplacedByTokenID,
	}
}

// AuthPersistence is the narrow seam consumed by future authentication
// application services. Implementations operate on a caller-owned GORM
// transaction and do not expose raw refresh tokens.
type AuthPersistence interface {
	FindUserByAccount(context.Context, string) (*domain.User, error)
	CreateRefreshSession(context.Context, RefreshSession) (RefreshSession, error)
	InsertRefreshTokenDigest(context.Context, RefreshTokenInsertCommand) (RefreshTokenState, error)
	LockRefreshSession(context.Context, uuid.UUID) (RefreshSession, error)
	LockRefreshToken(context.Context, uuid.UUID) (RefreshTokenState, error)
	GetActiveSessionBySID(context.Context, uuid.UUID, time.Time) (RefreshSession, error)
	RevokeSessionFamily(context.Context, uuid.UUID, time.Time) error
	RotateRefreshToken(context.Context, RotateRefreshTokenCommand) (RotateRefreshTokenResult, error)
}

// AuthRepository is a PostgreSQL/GORM adapter bound to a caller-owned
// transaction. The constructor performs no database operation.
type AuthRepository struct {
	tx    *gorm.DB
	clock func() time.Time
}

var _ AuthPersistence = (*AuthRepository)(nil)

func NewAuthRepository(tx *gorm.DB) *AuthRepository { return &AuthRepository{tx: tx} }

// NewAuthRepositoryWithClock is a deterministic server-clock seam for B2
// persistence tests. Production repositories use PostgreSQL clock_timestamp().
func NewAuthRepositoryWithClock(tx *gorm.DB, clock func() time.Time) *AuthRepository {
	return &AuthRepository{tx: tx, clock: clock}
}

// NewAuthMutationRepository acquires the shared writer fence before returning
// an auth adapter. Call this immediately after beginning a transaction when a
// mutation path may perform reads before its first write.
func NewAuthMutationRepository(ctx context.Context, tx *gorm.DB) (*AuthRepository, error) {
	if err := AcquireAuthWriterFence(ctx, tx); err != nil {
		return nil, err
	}
	return NewAuthRepository(tx), nil
}

// AcquireAuthWriterFence is the explicit transaction admission hook for auth
// mutation paths. Call it as the first database operation in the transaction.
// It intentionally rejects pooled GORM handles.
func AcquireAuthWriterFence(ctx context.Context, tx *gorm.DB) error {
	if tx == nil {
		return ErrAuthTransactionRequired
	}
	if err := migrations.AcquireSharedWriterFenceOnGORM(ctx, tx); err != nil {
		if errors.Is(err, migrations.ErrSharedWriterTransactionRequired) {
			return ErrAuthTransactionRequired
		}
		return fmt.Errorf("auth writer fence: %w", err)
	}
	return nil
}

func (r *AuthRepository) transaction() (*gorm.DB, error) {
	if r == nil || r.tx == nil || r.tx.Statement == nil {
		return nil, ErrAuthTransactionRequired
	}
	return r.tx, nil
}

func authContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// FindUserByAccount preserves the legacy account lookup exactly: the supplied
// account is compared as-is, without trimming, case-folding, or limiter-key
// normalization. A missing account is represented by ErrAuthRecordNotFound.
func (r *AuthRepository) FindUserByAccount(ctx context.Context, account string) (*domain.User, error) {
	tx, err := r.transaction()
	if err != nil {
		return nil, err
	}
	var user domain.User
	result := tx.WithContext(authContext(ctx)).Where("account = ?", account).First(&user)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, ErrAuthRecordNotFound
	}
	if result.Error != nil {
		return nil, fmt.Errorf("find user by account: %w", result.Error)
	}
	// A missing/NULL value must never become an enabled runtime user. The
	// migration is NOT NULL; this assignment also protects non-PostgreSQL test
	// doubles and future legacy rows.
	return &user, nil
}

// CreateRefreshSession inserts one new refresh-token family.
func (r *AuthRepository) CreateRefreshSession(ctx context.Context, session RefreshSession) (RefreshSession, error) {
	if err := r.requireFence(ctx); err != nil {
		return RefreshSession{}, err
	}
	if session.ID == uuid.Nil {
		session.ID = uuid.New()
	}
	if session.UserID == 0 {
		return RefreshSession{}, ErrAuthInput
	}
	if session.CreatedAt.IsZero() {
		session.CreatedAt = time.Now().UTC()
	} else {
		session.CreatedAt = session.CreatedAt.UTC()
	}
	if session.ExpiresAt.IsZero() || !session.ExpiresAt.After(session.CreatedAt) {
		return RefreshSession{}, ErrAuthInput
	}
	session.ExpiresAt = session.ExpiresAt.UTC()
	tx, err := r.transaction()
	if err != nil {
		return RefreshSession{}, err
	}
	if err := tx.WithContext(authContext(ctx)).Create(&session).Error; err != nil {
		return RefreshSession{}, fmt.Errorf("create refresh session: %w", err)
	}
	return session, nil
}

// InsertRefreshTokenDigest inserts one lineage row. Raw refresh-token values
// cannot be represented by this API.
func (r *AuthRepository) InsertRefreshTokenDigest(ctx context.Context, command RefreshTokenInsertCommand) (RefreshTokenState, error) {
	if err := r.requireFence(ctx); err != nil {
		return RefreshTokenState{}, err
	}
	if len(command.Digest) != refreshTokenDigestSize {
		return RefreshTokenState{}, ErrRefreshTokenDigestSize
	}
	if command.SessionID == uuid.Nil {
		return RefreshTokenState{}, ErrAuthInput
	}
	row := refreshTokenRow{ID: command.ID, SessionID: command.SessionID, TokenHash: append([]byte(nil), command.Digest...), IssuedAt: command.IssuedAt, ExpiresAt: command.ExpiresAt}
	if row.ID == uuid.Nil {
		row.ID = uuid.New()
	}
	if row.IssuedAt.IsZero() {
		row.IssuedAt = time.Now().UTC()
	} else {
		row.IssuedAt = row.IssuedAt.UTC()
	}
	if row.ExpiresAt.IsZero() || !row.ExpiresAt.After(row.IssuedAt) {
		return RefreshTokenState{}, ErrAuthInput
	}
	row.ExpiresAt = row.ExpiresAt.UTC()
	tx, err := r.transaction()
	if err != nil {
		return RefreshTokenState{}, err
	}
	if err := tx.WithContext(authContext(ctx)).Create(&row).Error; err != nil {
		return RefreshTokenState{}, fmt.Errorf("insert refresh token digest: %w", err)
	}
	return refreshTokenState(row), nil
}

// refreshTokenDiscovery contains only identifiers needed to establish the
// session-then-token lock order. It deliberately cannot carry token material.
type refreshTokenDiscovery struct {
	ID        uuid.UUID
	SessionID uuid.UUID
}

// findRefreshTokenByDigest is discovery only. It requires the caller-owned
// transaction already admitted by AcquireAuthWriterFence, never acquires
// authority, and does not return the discovered digest.
func findRefreshTokenByDigest(tx *gorm.DB, digest [refreshTokenDigestSize]byte) (refreshTokenDiscovery, error) {
	if tx == nil || tx.Statement == nil {
		return refreshTokenDiscovery{}, ErrAuthTransactionRequired
	}
	if _, ok := tx.Statement.ConnPool.(*sql.Tx); !ok {
		return refreshTokenDiscovery{}, ErrAuthTransactionRequired
	}
	var row refreshTokenRow
	result := tx.Where("token_hash = ?", digest[:]).First(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return refreshTokenDiscovery{}, ErrAuthRecordNotFound
	}
	if result.Error != nil {
		return refreshTokenDiscovery{}, fmt.Errorf("find refresh token digest: %w", result.Error)
	}
	return refreshTokenDiscovery{ID: row.ID, SessionID: row.SessionID}, nil
}

// LockRefreshSession obtains the session row lock. Mutation callers must use
// this before locking a token row (session then token order).
func (r *AuthRepository) LockRefreshSession(ctx context.Context, sid uuid.UUID) (RefreshSession, error) {
	if err := r.requireFence(ctx); err != nil {
		return RefreshSession{}, err
	}
	if sid == uuid.Nil {
		return RefreshSession{}, ErrAuthInput
	}
	tx, err := r.transaction()
	if err != nil {
		return RefreshSession{}, err
	}
	var session RefreshSession
	result := tx.WithContext(authContext(ctx)).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", sid).First(&session)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return RefreshSession{}, ErrAuthRecordNotFound
	}
	if result.Error != nil {
		return RefreshSession{}, fmt.Errorf("lock refresh session: %w", result.Error)
	}
	return session, nil
}

// LockRefreshToken obtains a token row lock after its owning session has been
// locked. The adapter does not perform a token-to-session lock inversion.
func (r *AuthRepository) LockRefreshToken(ctx context.Context, tokenID uuid.UUID) (RefreshTokenState, error) {
	if err := r.requireFence(ctx); err != nil {
		return RefreshTokenState{}, err
	}
	if tokenID == uuid.Nil {
		return RefreshTokenState{}, ErrAuthInput
	}
	tx, err := r.transaction()
	if err != nil {
		return RefreshTokenState{}, err
	}
	var row refreshTokenRow
	result := tx.WithContext(authContext(ctx)).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", tokenID).First(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return RefreshTokenState{}, ErrAuthRecordNotFound
	}
	if result.Error != nil {
		return RefreshTokenState{}, fmt.Errorf("lock refresh token: %w", result.Error)
	}
	return refreshTokenState(row), nil
}

// RotateRefreshToken performs one refresh-token lineage transition. The
// caller owns the transaction and must commit it even for RotateRefreshTokenReplay:
// that outcome includes the family revocation and is intentionally not returned
// as a Go error.
//
// The order below is security-critical: the shared writer fence is the first
// database operation, digest lookup is discovery only, and all authoritative
// state is checked after session-then-token FOR UPDATE locks.
func (r *AuthRepository) RotateRefreshToken(ctx context.Context, command RotateRefreshTokenCommand) (RotateRefreshTokenResult, error) {
	var result RotateRefreshTokenResult
	tx, err := r.transaction()
	if err != nil {
		return result, err
	}
	if len(command.PresentedDigest) != refreshTokenDigestSize || len(command.ReplacementDigest) != refreshTokenDigestSize {
		return result, ErrRefreshTokenDigestSize
	}
	// A digest is copied before it is passed to the driver so a caller cannot
	// mutate the value while the discovery query is in flight.
	presented := append([]byte(nil), command.PresentedDigest...)
	replacementDigest := append([]byte(nil), command.ReplacementDigest...)
	if subtle.ConstantTimeCompare(presented, replacementDigest) == 1 {
		return result, ErrAuthInput
	}
	if err := AcquireAuthWriterFence(ctx, tx); err != nil {
		return result, err
	}

	// This query is deliberately non-authoritative. Its only purpose is to
	// discover the identifiers needed for the lock sequence.
	var presentedDigest [refreshTokenDigestSize]byte
	copy(presentedDigest[:], presented)
	discovered, discoveryErr := findRefreshTokenByDigest(tx.WithContext(authContext(ctx)), presentedDigest)
	if errors.Is(discoveryErr, ErrAuthRecordNotFound) {
		return RotateRefreshTokenResult{Outcome: RotateRefreshTokenUnknown}, nil
	}
	if discoveryErr != nil {
		return result, discoveryErr
	}

	// Lock the owning session before any token row, including replay paths.
	var session RefreshSession
	sessionQuery := tx.WithContext(authContext(ctx)).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", discovered.SessionID).First(&session)
	if errors.Is(sessionQuery.Error, gorm.ErrRecordNotFound) {
		return RotateRefreshTokenResult{Outcome: RotateRefreshTokenRejected}, nil
	}
	if sessionQuery.Error != nil {
		return result, fmt.Errorf("lock refresh session for rotation: %w", sessionQuery.Error)
	}
	var token refreshTokenRow
	tokenQuery := tx.WithContext(authContext(ctx)).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", discovered.ID).First(&token)
	if errors.Is(tokenQuery.Error, gorm.ErrRecordNotFound) {
		return RotateRefreshTokenResult{Outcome: RotateRefreshTokenRejected}, nil
	}
	if tokenQuery.Error != nil {
		return result, fmt.Errorf("lock refresh token for rotation: %w", tokenQuery.Error)
	}

	// Sample exactly one fresh decision time after both authoritative rows are
	// locked. command.Now is request metadata and may be stale while waiting.
	now, err := r.freshDecisionTime(ctx, tx)
	if err != nil {
		return result, err
	}
	// Revalidate every authoritative field after both locks. A historical row
	// is a replay even when its owning session is still active; revoking the
	// family is part of the successful, commit-safe replay outcome.
	if token.SessionID != session.ID || discovered.SessionID != session.ID ||
		subtle.ConstantTimeCompare(token.TokenHash, presented) != 1 {
		return RotateRefreshTokenResult{Outcome: RotateRefreshTokenRejected}, nil
	}
	if token.ConsumedAt != nil || token.RevokedAt != nil || token.ReplacedByTokenID != nil {
		// A replay always revokes the family. The policy is advisory for this
		// branch: denial must never suppress the committed historical replay
		// mutation.
		if command.AbuseCheck != nil {
			_ = command.AbuseCheck(authContext(ctx), RefreshAbuseDecision{SourceIP: command.SourceIP, SessionID: session.ID, Replay: true})
		}
		if err := r.revokeSessionFamilyLocked(ctx, tx, session, now); err != nil {
			return result, err
		}
		return RotateRefreshTokenResult{Outcome: RotateRefreshTokenReplay, SessionID: session.ID}, nil
	}
	if session.RevokedAt != nil || !session.ExpiresAt.After(now) ||
		!token.ExpiresAt.After(now) || token.IssuedAt.Before(session.CreatedAt) ||
		token.ExpiresAt.After(session.ExpiresAt) {
		return RotateRefreshTokenResult{Outcome: RotateRefreshTokenRejected, SessionID: session.ID}, nil
	}
	// The policy runs while both authoritative rows remain locked. A denial
	// returns a rejected outcome without consuming or inserting any token.
	if command.AbuseCheck != nil && !command.AbuseCheck(authContext(ctx), RefreshAbuseDecision{SourceIP: command.SourceIP, SessionID: session.ID}) {
		return RotateRefreshTokenResult{Outcome: RotateRefreshTokenRejected, SessionID: session.ID}, nil
	}
	// The partial unique index protects this invariant at the database level;
	// retain an explicit check so malformed legacy lineage cannot rotate.
	var siblingCount int64
	if err := tx.WithContext(authContext(ctx)).Model(&refreshTokenRow{}).
		Where("session_id = ? AND id <> ? AND consumed_at IS NULL AND revoked_at IS NULL", session.ID, token.ID).
		Count(&siblingCount).Error; err != nil {
		return result, fmt.Errorf("check current refresh token lineage: %w", err)
	}
	if siblingCount != 0 {
		return RotateRefreshTokenResult{Outcome: RotateRefreshTokenRejected, SessionID: session.ID}, nil
	}
	// A valid candidate has no outgoing replacement of its own. Do not query
	// replaced_by_token_id = token.ID here: that is the expected incoming
	// parent reference for every token after the first lineage row.
	var outgoingReplacementCount int64
	if err := tx.WithContext(authContext(ctx)).Model(&refreshTokenRow{}).
		Where("id = ? AND replaced_by_token_id IS NOT NULL", token.ID).Count(&outgoingReplacementCount).Error; err != nil {
		return result, fmt.Errorf("check refresh token lineage: %w", err)
	}
	if outgoingReplacementCount != 0 {
		return RotateRefreshTokenResult{Outcome: RotateRefreshTokenRejected, SessionID: session.ID}, nil
	}

	expiresAt := command.ReplacementExpiresAt
	if expiresAt.IsZero() {
		expiresAt = session.ExpiresAt
	} else {
		expiresAt = expiresAt.UTC()
	}
	if !expiresAt.After(now) || expiresAt.After(session.ExpiresAt) {
		return RotateRefreshTokenResult{Outcome: RotateRefreshTokenRejected, SessionID: session.ID}, nil
	}
	replacementID := command.ReplacementTokenID
	if replacementID == uuid.Nil {
		replacementID = uuid.New()
	}
	// Consuming first removes the old row from the one-current-token index;
	// insertion and lineage linking remain in this caller-owned transaction.
	consume := tx.WithContext(authContext(ctx)).Model(&refreshTokenRow{}).
		Where("id = ? AND consumed_at IS NULL AND revoked_at IS NULL AND replaced_by_token_id IS NULL", token.ID).
		Updates(map[string]interface{}{"consumed_at": now})
	if consume.Error != nil {
		return result, fmt.Errorf("consume refresh token: %w", consume.Error)
	}
	if consume.RowsAffected != 1 {
		return result, errors.New("refresh token rotation lost current row")
	}
	replacement := refreshTokenRow{
		ID:        replacementID,
		SessionID: session.ID,
		TokenHash: replacementDigest,
		IssuedAt:  now,
		ExpiresAt: expiresAt,
	}
	if err := tx.WithContext(authContext(ctx)).Create(&replacement).Error; err != nil {
		return result, fmt.Errorf("insert refresh token replacement: %w", err)
	}
	link := tx.WithContext(authContext(ctx)).Model(&refreshTokenRow{}).
		Where("id = ? AND session_id = ? AND consumed_at IS NOT NULL", token.ID, session.ID).
		Update("replaced_by_token_id", replacementID)
	if link.Error != nil {
		return result, fmt.Errorf("link refresh token replacement: %w", link.Error)
	}
	if link.RowsAffected != 1 {
		return result, errors.New("refresh token rotation lineage link failed")
	}
	return RotateRefreshTokenResult{
		Outcome:             RotateRefreshTokenSucceeded,
		SessionID:           session.ID,
		ConsumedTokenID:     token.ID,
		ReplacementTokenID:  replacementID,
		ConsumedAt:          now,
		ReplacementIssuedAt: now,
	}, nil
}

// freshDecisionTime samples the authoritative server clock after all required
// row locks. A clock seam is available only for deterministic B2 tests.
func (r *AuthRepository) freshDecisionTime(ctx context.Context, tx *gorm.DB) (time.Time, error) {
	if r.clock != nil {
		now := r.clock()
		if !now.IsZero() {
			return now.UTC(), nil
		}
	}
	var now time.Time
	if err := tx.WithContext(authContext(ctx)).Raw("SELECT clock_timestamp()").Scan(&now).Error; err != nil {
		return time.Time{}, fmt.Errorf("read refresh rotation time: %w", err)
	}
	return now.UTC(), nil
}

// GetActiveSessionBySID returns a session active at now. It intentionally does
// not lock and does not acquire the writer fence because it is a read seam.
func (r *AuthRepository) GetActiveSessionBySID(ctx context.Context, sid uuid.UUID, now time.Time) (RefreshSession, error) {
	if sid == uuid.Nil {
		return RefreshSession{}, ErrAuthInput
	}
	tx, err := r.transaction()
	if err != nil {
		return RefreshSession{}, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	var session RefreshSession
	result := tx.WithContext(authContext(ctx)).Where("id = ? AND revoked_at IS NULL AND expires_at > ?", sid, now).First(&session)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return RefreshSession{}, ErrAuthRecordNotFound
	}
	if result.Error != nil {
		return RefreshSession{}, fmt.Errorf("get active refresh session: %w", result.Error)
	}
	return session, nil
}

// RevokeSessionFamily revokes a session and every token in it. It locks the
// session first and token rows second, matching the future rotation order.
func (r *AuthRepository) RevokeSessionFamily(ctx context.Context, sid uuid.UUID, revokedAt time.Time) error {
	if err := r.requireFence(ctx); err != nil {
		return err
	}
	if sid == uuid.Nil || revokedAt.IsZero() {
		return ErrAuthInput
	}
	revokedAt = revokedAt.UTC()
	tx, err := r.transaction()
	if err != nil {
		return err
	}
	var session RefreshSession
	result := tx.WithContext(authContext(ctx)).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", sid).First(&session)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return ErrAuthRecordNotFound
	}
	if result.Error != nil {
		return fmt.Errorf("lock refresh session for revocation: %w", result.Error)
	}
	return r.revokeSessionFamilyLocked(ctx, tx, session, revokedAt)
}

// revokeSessionFamilyLocked requires that the caller already holds the
// session FOR UPDATE lock. It is shared by the explicit revocation seam and
// replay handling, and always acquires token locks second.
func (r *AuthRepository) revokeSessionFamilyLocked(ctx context.Context, tx *gorm.DB, session RefreshSession, revokedAt time.Time) error {
	if session.ID == uuid.Nil || revokedAt.IsZero() {
		return ErrAuthInput
	}
	revokedAt = revokedAt.UTC()
	var tokens []refreshTokenRow
	if err := tx.WithContext(authContext(ctx)).Clauses(clause.Locking{Strength: "UPDATE"}).Where("session_id = ?", session.ID).Find(&tokens).Error; err != nil {
		return fmt.Errorf("lock refresh token family: %w", err)
	}
	if revokedAt.Before(session.CreatedAt) {
		return ErrAuthInput
	}
	for _, token := range tokens {
		if revokedAt.Before(token.IssuedAt) {
			return ErrAuthInput
		}
	}
	if err := tx.WithContext(authContext(ctx)).Model(&refreshTokenRow{}).
		Where("session_id = ? AND revoked_at IS NULL", session.ID).
		Update("revoked_at", revokedAt).Error; err != nil {
		return fmt.Errorf("revoke refresh token family: %w", err)
	}
	if err := tx.WithContext(authContext(ctx)).Model(&RefreshSession{}).
		Where("id = ? AND revoked_at IS NULL", session.ID).
		Update("revoked_at", revokedAt).Error; err != nil {
		return fmt.Errorf("revoke refresh session family: %w", err)
	}
	return nil
}

// requireFence is intentionally called at the beginning of every mutation or
// lock seam. The caller-owned transaction path must invoke the first method on
// a fresh transaction; callers that combine reads and writes should call
// AcquireAuthWriterFence explicitly before any read.
func (r *AuthRepository) requireFence(ctx context.Context) error {
	if r == nil || r.tx == nil || r.tx.Statement == nil {
		return ErrAuthTransactionRequired
	}
	return AcquireAuthWriterFence(ctx, r.tx)
}
