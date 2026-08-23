package auth

import (
	"context"
	cryptorand "crypto/rand"
	"errors"
	"io"
	"time"

	"gorm.io/gorm"

	"power-iot-backend/internal/adapters/persistence"
)

// TransactionRunner admits one complete auth mutation operation to a database
// transaction. The callback receives an application bound to a fenced,
// transaction-scoped B2 repository.
type TransactionRunner interface {
	WithTransaction(context.Context, func(*Application) error) error
}

// GormTransactionRunner is the production B3 transaction boundary. It keeps
// transaction ownership out of Application so a replay can be committed as a
// security mutation while still returning a generic unauthorized outcome.
type GormTransactionRunner struct {
	db               *gorm.DB
	signer           AccessTokenSigner
	now              func() time.Time
	random           io.Reader
	refreshLimiter   RefreshAbuseLimiter
	persistenceClock func() time.Time

	// repositoryFactory is a private test seam. Production construction always
	// uses NewAuthMutationRepository, preserving the first-operation fence.
	repositoryFactory func(context.Context, *gorm.DB) (persistence.AuthPersistence, error)
}

var _ TransactionRunner = (*GormTransactionRunner)(nil)

// NewGormTransactionRunner creates a runner using the supplied signer and
// default clock/randomness seams. The database handle is never used outside a
// transaction by this adapter.
func NewGormTransactionRunner(db *gorm.DB, signer AccessTokenSigner) *GormTransactionRunner {
	return NewGormTransactionRunnerWithConfig(db, Config{Signer: signer})
}

// NewGormTransactionRunnerWithConfig creates a runner with deterministic test
// seams. Config.Repository is intentionally ignored: each operation receives
// a fresh repository bound to its own transaction.
func NewGormTransactionRunnerWithConfig(db *gorm.DB, config Config) *GormTransactionRunner {
	now := config.Now
	if now == nil {
		now = time.Now
	}
	random := config.Random
	if random == nil {
		random = cryptorand.Reader
	}
	runner := &GormTransactionRunner{
		db: db, signer: config.Signer, now: now, random: random,
		refreshLimiter: config.RefreshLimiter, persistenceClock: config.PersistenceClock,
	}
	runner.repositoryFactory = func(ctx context.Context, tx *gorm.DB) (persistence.AuthPersistence, error) {
		if runner.persistenceClock != nil {
			// Preserve B2's fence-first admission even when a deterministic
			// clock is injected for PostgreSQL integration proofs.
			if err := persistence.AcquireAuthWriterFence(ctx, tx); err != nil {
				return nil, err
			}
			return persistence.NewAuthRepositoryWithClock(tx, runner.persistenceClock), nil
		}
		return persistence.NewAuthMutationRepository(ctx, tx)
	}
	return runner
}

// WithTransaction runs one mutation callback after admitting the transaction
// through persistence.NewAuthMutationRepository. Authentication denials are
// deliberately converted to a nil closure result: failed logins are read-only,
// while replay outcomes include B2 family revocation that must commit. Any
// infrastructure error remains non-nil and therefore rolls back.
func (r *GormTransactionRunner) WithTransaction(ctx context.Context, fn func(*Application) error) error {
	if r == nil || r.db == nil || r.signer == nil || fn == nil || r.repositoryFactory == nil {
		return ErrInfrastructure
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var outward error
	txErr := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		repository, err := r.repositoryFactory(ctx, tx)
		if err != nil {
			return err
		}
		app := NewWithConfig(Config{Repository: repository, Signer: r.signer, Now: r.now, Random: r.random, RefreshLimiter: r.refreshLimiter, PersistenceClock: r.persistenceClock})
		err = fn(app)
		if errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrInvalidCredentials) {
			outward = err
			return nil
		}
		return err
	})
	if txErr != nil {
		return ErrInfrastructure
	}
	return outward
}

// Login executes Login in an explicit GORM transaction.
func (r *GormTransactionRunner) Login(ctx context.Context, account, password string) (LoginResult, error) {
	result, err := r.LoginWithTimestamps(ctx, account, password)
	return result.LoginResult, err
}

// LoginWithTimestamps preserves the application clock used for signing and
// persistence for the B5-A response-expiry seam.
func (r *GormTransactionRunner) LoginWithTimestamps(ctx context.Context, account, password string) (LoginResultWithTimestamps, error) {
	var result LoginResultWithTimestamps
	err := r.WithTransaction(ctx, func(app *Application) error {
		var err error
		result, err = app.LoginWithTimestamps(ctx, account, password)
		return err
	})
	if err != nil {
		return LoginResultWithTimestamps{}, err
	}
	return result, nil
}

// Refresh executes Refresh in an explicit GORM transaction. A replay commits
// its revocation and is returned as generic ErrUnauthorized only afterwards.
func (r *GormTransactionRunner) Refresh(ctx context.Context, rawRefresh string) (RefreshResult, error) {
	return r.RefreshWithSourceIP(ctx, rawRefresh, "")
}

// RefreshWithSourceIP admits the source-IP-aware refresh policy without
// exposing family resolution or token material to the transport boundary.
func (r *GormTransactionRunner) RefreshWithSourceIP(ctx context.Context, rawRefresh, sourceIP string) (RefreshResult, error) {
	result, err := r.RefreshWithSourceIPWithTimestamps(ctx, rawRefresh, sourceIP)
	return result.RefreshResult, err
}

// RefreshWithSourceIPWithTimestamps preserves the B3 rotation authority while
// supplying expiry metadata captured by the same application clock.
func (r *GormTransactionRunner) RefreshWithSourceIPWithTimestamps(ctx context.Context, rawRefresh, sourceIP string) (RefreshResultWithTimestamps, error) {
	var result RefreshResultWithTimestamps
	err := r.WithTransaction(ctx, func(app *Application) error {
		var err error
		result, err = app.RefreshWithSourceIPWithTimestamps(ctx, rawRefresh, sourceIP)
		return err
	})
	if err != nil {
		return RefreshResultWithTimestamps{}, err
	}
	return result, nil
}

// Logout executes Logout in an explicit GORM transaction.
func (r *GormTransactionRunner) Logout(ctx context.Context, identity AuthenticatedIdentity) error {
	return r.WithTransaction(ctx, func(app *Application) error {
		return app.Logout(ctx, identity)
	})
}
