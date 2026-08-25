package persistence

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func authDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set; auth persistence integration test not run")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func authUser(t *testing.T, db *gorm.DB, account string, enabled bool) uint {
	t.Helper()
	var id uint
	if err := db.Raw(`INSERT INTO users (account, password_hash, name, auth_enabled) VALUES (?, ?, ?, ?) RETURNING id`, account, "test-hash", "Auth Test User", enabled).Scan(&id).Error; err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAuthPersistenceUserMappingAndExactAccountLookup(t *testing.T) {
	db := authDB(t)
	account := "auth-exact-" + uuid.NewString()[:12]
	userID := authUser(t, db, account, true)
	disabledAccount := "auth-disabled-" + uuid.NewString()[:12]
	authUser(t, db, disabledAccount, false)

	repo := NewAuthRepository(db)
	user, err := repo.FindUserByAccount(context.Background(), account)
	if err != nil {
		t.Fatal(err)
	}
	if user.ID != userID || !user.AuthEnabled {
		t.Fatalf("mapped user=%+v, want id=%d enabled", user, userID)
	}
	disabled, err := repo.FindUserByAccount(context.Background(), disabledAccount)
	if err != nil {
		t.Fatal(err)
	}
	if disabled.AuthEnabled {
		t.Fatal("disabled auth user mapped as enabled")
	}
	if _, err := repo.FindUserByAccount(context.Background(), " "+account); !errors.Is(err, ErrAuthRecordNotFound) {
		t.Fatalf("trimmed account lookup error=%v, want exact missing error", err)
	}
	if _, err := repo.FindUserByAccount(context.Background(), strings.ToUpper(account)); !errors.Is(err, ErrAuthRecordNotFound) {
		t.Fatalf("case-folded account lookup error=%v, want exact missing error", err)
	}
}

func TestAuthPersistenceRefreshLifecycle(t *testing.T) {
	db := authDB(t)
	userID := authUser(t, db, "auth-lifecycle-"+uuid.NewString()[:12], false)
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	expires := created.Add(time.Hour)
	sessionID := uuid.New()
	digest := make([]byte, refreshTokenDigestSize)
	digest[0] = 7

	if err := db.Transaction(func(tx *gorm.DB) error {
		repo := NewAuthRepository(tx)
		if _, err := repo.CreateRefreshSession(context.Background(), RefreshSession{ID: sessionID, UserID: userID, CreatedAt: created, ExpiresAt: expires}); err != nil {
			return err
		}
		_, err := repo.InsertRefreshTokenDigest(context.Background(), RefreshTokenInsertCommand{SessionID: sessionID, Digest: digest, IssuedAt: created, ExpiresAt: expires})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	repo := NewAuthRepository(db)
	active, err := repo.GetActiveSessionBySID(context.Background(), sessionID, created.Add(time.Minute))
	if err != nil || active.ID != sessionID {
		t.Fatalf("active session=%+v err=%v", active, err)
	}
	if _, err := repo.GetActiveSessionBySID(context.Background(), sessionID, expires); !errors.Is(err, ErrAuthRecordNotFound) {
		t.Fatalf("expired session error=%v, want missing", err)
	}

	if err := db.Transaction(func(tx *gorm.DB) error {
		repo := NewAuthRepository(tx)
		return repo.RevokeSessionFamily(context.Background(), sessionID, created.Add(10*time.Minute))
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetActiveSessionBySID(context.Background(), sessionID, created.Add(20*time.Minute)); !errors.Is(err, ErrAuthRecordNotFound) {
		t.Fatalf("revoked session error=%v, want missing", err)
	}
	var revoked int
	if err := db.Raw(`SELECT count(*) FROM refresh_tokens WHERE session_id = ? AND revoked_at IS NOT NULL`, sessionID).Scan(&revoked).Error; err != nil {
		t.Fatal(err)
	}
	if revoked != 1 {
		t.Fatalf("revoked token count=%d, want 1", revoked)
	}
}

func TestAuthPersistenceUsesMigrationDigestAndLineageConstraints(t *testing.T) {
	db := authDB(t)
	userID := authUser(t, db, "auth-constraints-"+uuid.NewString()[:12], false)
	now := time.Now().UTC().Truncate(time.Microsecond)
	digest := make([]byte, refreshTokenDigestSize)
	digest[0] = 9
	sessionA := RefreshSession{ID: uuid.New(), UserID: userID, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	sessionB := RefreshSession{ID: uuid.New(), UserID: userID, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	var first RefreshTokenState
	if err := db.Transaction(func(tx *gorm.DB) error {
		repo := NewAuthRepository(tx)
		if _, err := repo.CreateRefreshSession(context.Background(), sessionA); err != nil {
			return err
		}
		var err error
		first, err = repo.InsertRefreshTokenDigest(context.Background(), RefreshTokenInsertCommand{SessionID: sessionA.ID, Digest: digest, IssuedAt: now, ExpiresAt: now.Add(time.Hour)})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		repo := NewAuthRepository(tx)
		_, err := repo.CreateRefreshSession(context.Background(), sessionB)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		repo := NewAuthRepository(tx)
		_, err := repo.InsertRefreshTokenDigest(context.Background(), RefreshTokenInsertCommand{SessionID: sessionB.ID, Digest: digest, IssuedAt: now, ExpiresAt: now.Add(time.Hour)})
		return err
	}); err == nil {
		t.Fatal("duplicate digest unexpectedly inserted")
	}
	secondDigest := make([]byte, refreshTokenDigestSize)
	secondDigest[0] = 10
	if err := db.Transaction(func(tx *gorm.DB) error {
		repo := NewAuthRepository(tx)
		_, err := repo.InsertRefreshTokenDigest(context.Background(), RefreshTokenInsertCommand{SessionID: sessionA.ID, Digest: secondDigest, IssuedAt: now, ExpiresAt: now.Add(time.Hour)})
		return err
	}); err == nil {
		t.Fatal("second current token unexpectedly inserted")
	}
	if err := db.Exec(`UPDATE refresh_tokens SET replaced_by_token_id = id WHERE id = ?`, first.ID).Error; err == nil {
		t.Fatal("self-replacement unexpectedly accepted")
	}
	if err := db.Exec(`INSERT INTO refresh_tokens (session_id, token_hash, issued_at, expires_at, replaced_by_token_id) VALUES (?, ?, ?, ?, ?)`, sessionB.ID, secondDigest, now, now.Add(time.Hour), first.ID).Error; err == nil {
		t.Fatal("cross-session lineage unexpectedly accepted")
	}
}

func newAuthFamily(t *testing.T, db *gorm.DB) (RefreshSession, []byte) {
	t.Helper()
	userID := authUser(t, db, "auth-rotate-"+uuid.NewString()[:12], false)
	created := time.Now().UTC().Truncate(time.Microsecond)
	session := RefreshSession{ID: uuid.New(), UserID: userID, CreatedAt: created, ExpiresAt: created.Add(time.Hour)}
	digest := make([]byte, refreshTokenDigestSize)
	copy(digest, uuid.NewString())
	if err := db.Transaction(func(tx *gorm.DB) error {
		repo := NewAuthRepository(tx)
		if _, err := repo.CreateRefreshSession(context.Background(), session); err != nil {
			return err
		}
		_, err := repo.InsertRefreshTokenDigest(context.Background(), RefreshTokenInsertCommand{SessionID: session.ID, Digest: digest, IssuedAt: created, ExpiresAt: session.ExpiresAt})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return session, digest
}

func TestAuthPersistenceRotateFullLineageAndInvariants(t *testing.T) {
	db := authDB(t)
	userID := authUser(t, db, "auth-lineage-"+uuid.NewString()[:12], false)
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	expires := created.Add(24 * time.Hour)
	session := RefreshSession{ID: uuid.New(), UserID: userID, CreatedAt: created, ExpiresAt: expires}
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New()}
	digests := [][]byte{makeDigest(0x41), makeDigest(0x42), makeDigest(0x43), makeDigest(0x44)}
	if err := db.Transaction(func(tx *gorm.DB) error {
		repo := NewAuthRepository(tx)
		if _, err := repo.CreateRefreshSession(context.Background(), session); err != nil {
			return err
		}
		_, err := repo.InsertRefreshTokenDigest(context.Background(), RefreshTokenInsertCommand{
			ID: ids[0], SessionID: session.ID, Digest: digests[0], IssuedAt: created, ExpiresAt: expires,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < len(ids)-1; i++ {
		rotationNow := created.Add(time.Duration(i+1) * time.Minute)
		var result RotateRefreshTokenResult
		if err := db.Transaction(func(tx *gorm.DB) error {
			var err error
			result, err = NewAuthRepositoryWithClock(tx, func() time.Time { return rotationNow }).RotateRefreshToken(context.Background(), RotateRefreshTokenCommand{
				PresentedDigest: digests[i], ReplacementDigest: digests[i+1],
				ReplacementTokenID: ids[i+1], ReplacementExpiresAt: expires, Now: rotationNow,
			})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if result.Outcome != RotateRefreshTokenSucceeded {
			t.Fatalf("rotation step %d outcome=%s, want success", i, result.Outcome)
		}

		var currentCount int64
		if err := db.Raw(`SELECT count(*) FROM refresh_tokens WHERE session_id = ? AND consumed_at IS NULL AND revoked_at IS NULL`, session.ID).Scan(&currentCount).Error; err != nil {
			t.Fatal(err)
		}
		if currentCount != 1 {
			t.Fatalf("rotation step %d current token count=%d, want 1", i, currentCount)
		}
		var expiryCount int64
		if err := db.Raw(`SELECT count(*) FROM refresh_tokens WHERE session_id = ? AND expires_at = ?`, session.ID, expires).Scan(&expiryCount).Error; err != nil {
			t.Fatal(err)
		}
		if expiryCount != int64(i+2) {
			t.Fatalf("rotation step %d fixed-expiry count=%d, want %d", i, expiryCount, i+2)
		}
	}

	for i := 0; i < len(ids)-1; i++ {
		var row struct {
			ReplacedByTokenID *uuid.UUID `gorm:"column:replaced_by_token_id"`
		}
		if err := db.Raw(`SELECT replaced_by_token_id FROM refresh_tokens WHERE id = ? AND session_id = ?`, ids[i], session.ID).Scan(&row).Error; err != nil {
			t.Fatal(err)
		}
		if row.ReplacedByTokenID == nil || *row.ReplacedByTokenID != ids[i+1] {
			t.Fatalf("lineage link %d=%v, want %s", i, row.ReplacedByTokenID, ids[i+1])
		}
	}
	var terminalRow struct {
		ReplacedByTokenID *uuid.UUID `gorm:"column:replaced_by_token_id"`
	}
	if err := db.Raw(`SELECT replaced_by_token_id FROM refresh_tokens WHERE id = ? AND session_id = ?`, ids[len(ids)-1], session.ID).Scan(&terminalRow).Error; err != nil {
		t.Fatal(err)
	}
	if terminalRow.ReplacedByTokenID != nil {
		t.Fatalf("terminal token has outgoing lineage link %s", *terminalRow.ReplacedByTokenID)
	}

	var selfLinks, crossSessionLinks, cycles int64
	if err := db.Raw(`SELECT count(*) FROM refresh_tokens WHERE id = replaced_by_token_id`).Scan(&selfLinks).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw(`SELECT count(*) FROM refresh_tokens child JOIN refresh_tokens parent ON parent.id = child.replaced_by_token_id WHERE child.session_id <> parent.session_id`).Scan(&crossSessionLinks).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw(`WITH RECURSIVE walk AS (
		SELECT id, replaced_by_token_id, ARRAY[id] AS path FROM refresh_tokens WHERE session_id = ?
		UNION ALL
		SELECT child.id, child.replaced_by_token_id, walk.path || child.id
		FROM refresh_tokens child JOIN walk ON child.id = walk.replaced_by_token_id
		WHERE NOT child.id = ANY(walk.path)
	) SELECT count(*) FROM walk WHERE replaced_by_token_id IS NOT NULL AND replaced_by_token_id = ANY(path)`, session.ID).Scan(&cycles).Error; err != nil {
		t.Fatal(err)
	}
	if selfLinks != 0 || crossSessionLinks != 0 || cycles != 0 {
		t.Fatalf("lineage integrity self=%d cross_session=%d cycles=%d", selfLinks, crossSessionLinks, cycles)
	}
}

func TestAuthPersistenceRotateReplayUnknownAndRollback(t *testing.T) {
	db := authDB(t)
	session, oldDigest := newAuthFamily(t, db)
	replacementDigest := make([]byte, refreshTokenDigestSize)
	replacementDigest[0] = 0xB
	now := session.CreatedAt.Add(time.Minute)
	var rotated RotateRefreshTokenResult
	if err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		rotated, err = NewAuthRepositoryWithClock(tx, func() time.Time { return now }).RotateRefreshToken(context.Background(), RotateRefreshTokenCommand{PresentedDigest: oldDigest, ReplacementDigest: replacementDigest, Now: now})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if rotated.Outcome != RotateRefreshTokenSucceeded || rotated.ReplacementTokenID == uuid.Nil {
		t.Fatalf("rotation outcome=%s replacement_id=%s", rotated.Outcome, rotated.ReplacementTokenID)
	}
	var counts struct {
		Consumed int
		Linked   int
		Current  int
	}
	if err := db.Raw(`SELECT count(*) FILTER (WHERE consumed_at IS NOT NULL) AS consumed, count(*) FILTER (WHERE replaced_by_token_id IS NOT NULL) AS linked, count(*) FILTER (WHERE consumed_at IS NULL AND revoked_at IS NULL) AS current FROM refresh_tokens WHERE session_id = ?`, session.ID).Scan(&counts).Error; err != nil {
		t.Fatal(err)
	}
	if counts.Consumed != 1 || counts.Linked != 1 || counts.Current != 1 {
		t.Fatalf("lineage consumed=%d linked=%d current=%d", counts.Consumed, counts.Linked, counts.Current)
	}

	if err := db.Transaction(func(tx *gorm.DB) error {
		result, err := NewAuthRepositoryWithClock(tx, func() time.Time { return now.Add(time.Minute) }).RotateRefreshToken(context.Background(), RotateRefreshTokenCommand{PresentedDigest: oldDigest, ReplacementDigest: makeDigest(0xC), Now: now.Add(time.Minute)})
		if err != nil {
			return err
		}
		if result.Outcome != RotateRefreshTokenReplay {
			t.Fatalf("replay outcome=%s session_id=%s", result.Outcome, result.SessionID)
		}
		return nil // replay revocation must be committed, not returned as an error
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewAuthRepository(db).GetActiveSessionBySID(context.Background(), session.ID, now.Add(2*time.Minute)); !errors.Is(err, ErrAuthRecordNotFound) {
		t.Fatalf("replayed session err=%v, want inactive", err)
	}
	var revokedCount int
	if err := db.Raw(`SELECT count(*) FROM refresh_tokens WHERE session_id = ? AND revoked_at IS NOT NULL`, session.ID).Scan(&revokedCount).Error; err != nil {
		t.Fatal(err)
	}
	if revokedCount != 2 {
		t.Fatalf("replay revoked token count=%d, want 2", revokedCount)
	}
	unknownSession, _ := newAuthFamily(t, db)
	if err := db.Transaction(func(tx *gorm.DB) error {
		result, err := NewAuthRepositoryWithClock(tx, func() time.Time { return unknownSession.CreatedAt.Add(time.Minute) }).RotateRefreshToken(context.Background(), RotateRefreshTokenCommand{PresentedDigest: makeDigest(0xD), ReplacementDigest: makeDigest(0xE), Now: unknownSession.CreatedAt.Add(time.Minute)})
		if err != nil {
			return err
		}
		if result.Outcome != RotateRefreshTokenUnknown {
			t.Fatalf("unknown outcome=%s", result.Outcome)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewAuthRepository(db).GetActiveSessionBySID(context.Background(), unknownSession.ID, unknownSession.CreatedAt.Add(time.Minute)); err != nil {
		t.Fatalf("unknown digest changed unrelated family: %v", err)
	}

	session2, oldDigest2 := newAuthFamily(t, db)
	if err := db.Transaction(func(tx *gorm.DB) error {
		result, err := NewAuthRepositoryWithClock(tx, func() time.Time { return session2.CreatedAt.Add(time.Minute) }).RotateRefreshToken(context.Background(), RotateRefreshTokenCommand{PresentedDigest: oldDigest2, ReplacementDigest: makeDigest(0xF), Now: session2.CreatedAt.Add(time.Minute)})
		if err != nil {
			return err
		}
		if result.Outcome != RotateRefreshTokenSucceeded {
			t.Fatalf("rollback rotation outcome=%s", result.Outcome)
		}
		return errors.New("forced caller transaction failure")
	}); err == nil {
		t.Fatal("forced rollback unexpectedly committed")
	}
	var rollbackCounts struct {
		Consumed    int
		Replacement int
	}
	if err := db.Raw(`SELECT count(*) FILTER (WHERE consumed_at IS NOT NULL) AS consumed, count(*) FILTER (WHERE issued_at > ?) AS replacement FROM refresh_tokens WHERE session_id = ?`, session2.CreatedAt, session2.ID).Scan(&rollbackCounts).Error; err != nil {
		t.Fatal(err)
	}
	if rollbackCounts.Consumed != 0 || rollbackCounts.Replacement != 0 {
		t.Fatalf("rollback left half-state consumed=%d replacement=%d", rollbackCounts.Consumed, rollbackCounts.Replacement)
	}
}

func TestAuthPersistenceDelayedLoserUsesFreshDecisionTime(t *testing.T) {
	db := authDB(t)
	session, digest := newAuthFamily(t, db)
	// Capture request metadata before either transaction can issue B. The
	// database clock, not this stale value, is authoritative after R2 waits.
	staleNow := time.Now().UTC()

	t1 := db.Begin()
	if t1.Error != nil {
		t.Fatal(t1.Error)
	}
	defer t1.Rollback()
	if _, err := NewAuthRepository(t1).LockRefreshSession(context.Background(), session.ID); err != nil {
		t.Fatal(err)
	}
	// T1 owns the authoritative session row while T2 discovers A and waits.
	t2Started := make(chan int, 1)
	t2Done := make(chan struct {
		result RotateRefreshTokenResult
		err    error
	}, 1)
	go func() {
		tx := db.Begin()
		if tx.Error != nil {
			t2Done <- struct {
				result RotateRefreshTokenResult
				err    error
			}{err: tx.Error}
			return
		}
		if err := AcquireAuthWriterFence(context.Background(), tx); err != nil {
			_ = tx.Rollback()
			t2Done <- struct {
				result RotateRefreshTokenResult
				err    error
			}{err: err}
			return
		}
		var pid int
		if err := tx.Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
			_ = tx.Rollback()
			t2Done <- struct {
				result RotateRefreshTokenResult
				err    error
			}{err: err}
			return
		}
		t2Started <- pid
		result, err := NewAuthRepository(tx).RotateRefreshToken(context.Background(), RotateRefreshTokenCommand{
			PresentedDigest: digest, ReplacementDigest: makeDigest(0xC), Now: staleNow,
		})
		if err != nil {
			_ = tx.Rollback()
		} else {
			err = tx.Commit().Error
		}
		t2Done <- struct {
			result RotateRefreshTokenResult
			err    error
		}{result: result, err: err}
	}()

	pid := <-t2Started
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting int
		if err := db.Raw("SELECT count(*) FROM pg_stat_activity WHERE pid = ? AND wait_event_type = 'Lock'", pid).Scan(&waiting).Error; err != nil {
			t.Fatal(err)
		}
		if waiting == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("T2 did not wait on the authoritative session lock")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// T1 performs A->B with a database issuance time later than T2's
	// captured Now. R2 must classify the now-consumed A as replay after its
	// lock wait, rather than reject it because request metadata is stale.
	result1, err := NewAuthRepository(t1).RotateRefreshToken(context.Background(), RotateRefreshTokenCommand{
		PresentedDigest: digest, ReplacementDigest: makeDigest(0xB), Now: staleNow,
	})
	if err != nil || result1.Outcome != RotateRefreshTokenSucceeded {
		t.Fatalf("T1 rotation result=%s err=%v", result1.Outcome, err)
	}
	var issuedAt time.Time
	if err := t1.Raw(`SELECT issued_at FROM refresh_tokens WHERE id = ?`, result1.ReplacementTokenID).Scan(&issuedAt).Error; err != nil {
		t.Fatal(err)
	}
	if !issuedAt.After(staleNow) {
		t.Fatalf("T1 replacement issued_at=%s is not after stale R2 Now=%s", issuedAt, staleNow)
	}
	if err := t1.Commit().Error; err != nil {
		t.Fatal(err)
	}

	t2Result := <-t2Done
	if t2Result.err != nil || t2Result.result.Outcome != RotateRefreshTokenReplay {
		t.Fatalf("T2 result=%s err=%v, want committed replay revocation", t2Result.result.Outcome, t2Result.err)
	}
	var state struct {
		RevokedSession int
		RevokedTokens  int
		CurrentTokens  int
	}
	if err := db.Raw(`SELECT
		(SELECT count(*) FROM refresh_sessions WHERE id = ? AND revoked_at IS NOT NULL) AS revoked_session,
		(SELECT count(*) FROM refresh_tokens WHERE session_id = ? AND revoked_at IS NOT NULL) AS revoked_tokens,
		(SELECT count(*) FROM refresh_tokens WHERE session_id = ? AND consumed_at IS NULL AND revoked_at IS NULL) AS current_tokens`, session.ID, session.ID, session.ID).Scan(&state).Error; err != nil {
		t.Fatal(err)
	}
	if state.RevokedSession != 1 || state.RevokedTokens != 2 || state.CurrentTokens != 0 {
		t.Fatalf("delayed-loser state revoked_session=%d revoked_tokens=%d current=%d", state.RevokedSession, state.RevokedTokens, state.CurrentTokens)
	}
	var finalSession struct {
		ID        uuid.UUID  `gorm:"column:id"`
		RevokedAt *time.Time `gorm:"column:revoked_at"`
	}
	if err := db.Raw(`SELECT id, revoked_at FROM refresh_sessions WHERE id = ?`, session.ID).Scan(&finalSession).Error; err != nil {
		t.Fatal(err)
	}
	if finalSession.RevokedAt == nil {
		t.Fatal("delayed-loser final session is not revoked")
	}
	var finalTokens []struct {
		ID                uuid.UUID  `gorm:"column:id"`
		IssuedAt          time.Time  `gorm:"column:issued_at"`
		ConsumedAt        *time.Time `gorm:"column:consumed_at"`
		RevokedAt         *time.Time `gorm:"column:revoked_at"`
		ReplacedByTokenID *uuid.UUID `gorm:"column:replaced_by_token_id"`
	}
	if err := db.Raw(`SELECT id, issued_at, consumed_at, revoked_at, replaced_by_token_id FROM refresh_tokens WHERE session_id = ? ORDER BY issued_at, id`, session.ID).Scan(&finalTokens).Error; err != nil {
		t.Fatal(err)
	}
	if len(finalTokens) != 2 {
		t.Fatalf("delayed-loser final token count=%d, want 2", len(finalTokens))
	}
	t.Logf("delayed-loser final session=%+v tokens=%+v", finalSession, finalTokens)
}

func TestAuthPersistenceExpiryRevalidatedAfterWaiting(t *testing.T) {
	for _, tc := range []struct {
		name          string
		sessionExpiry bool
		tokenExpiry   bool
	}{
		{name: "session", sessionExpiry: true},
		{name: "token", tokenExpiry: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := authDB(t)
			session, digest := newAuthFamily(t, db)
			expiresAt := time.Now().UTC().Add(250 * time.Millisecond)
			if tc.sessionExpiry {
				if err := db.Exec("UPDATE refresh_sessions SET expires_at = ? WHERE id = ?", expiresAt, session.ID).Error; err != nil {
					t.Fatal(err)
				}
			} else {
				if err := db.Exec("UPDATE refresh_sessions SET expires_at = ? WHERE id = ?", time.Now().UTC().Add(time.Hour), session.ID).Error; err != nil {
					t.Fatal(err)
				}
			}
			if tc.tokenExpiry {
				if err := db.Exec("UPDATE refresh_tokens SET expires_at = ? WHERE session_id = ?", expiresAt, session.ID).Error; err != nil {
					t.Fatal(err)
				}
			} else {
				if err := db.Exec("UPDATE refresh_tokens SET expires_at = ? WHERE session_id = ?", time.Now().UTC().Add(time.Hour), session.ID).Error; err != nil {
					t.Fatal(err)
				}
			}
			// This stale value is intentionally still before expiry. The
			// authoritative PostgreSQL clock must be sampled after waiting.
			staleNow := time.Now().UTC().Add(-time.Hour)

			t1 := db.Begin()
			if t1.Error != nil {
				t.Fatal(t1.Error)
			}
			defer t1.Rollback()
			if _, err := NewAuthRepository(t1).LockRefreshSession(context.Background(), session.ID); err != nil {
				t.Fatal(err)
			}
			t2Started := make(chan int, 1)
			t2Done := make(chan struct {
				result RotateRefreshTokenResult
				err    error
			}, 1)
			go func() {
				tx := db.Begin()
				if tx.Error != nil {
					t2Done <- struct {
						result RotateRefreshTokenResult
						err    error
					}{err: tx.Error}
					return
				}
				if err := AcquireAuthWriterFence(context.Background(), tx); err != nil {
					_ = tx.Rollback()
					t2Done <- struct {
						result RotateRefreshTokenResult
						err    error
					}{err: err}
					return
				}
				var pid int
				if err := tx.Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
					_ = tx.Rollback()
					t2Done <- struct {
						result RotateRefreshTokenResult
						err    error
					}{err: err}
					return
				}
				t2Started <- pid
				result, err := NewAuthRepository(tx).RotateRefreshToken(context.Background(), RotateRefreshTokenCommand{
					PresentedDigest: digest, ReplacementDigest: makeDigest(0xD), Now: staleNow,
				})
				if err != nil {
					_ = tx.Rollback()
				} else {
					err = tx.Commit().Error
				}
				t2Done <- struct {
					result RotateRefreshTokenResult
					err    error
				}{result: result, err: err}
			}()
			pid := <-t2Started
			deadline := time.Now().Add(5 * time.Second)
			for {
				var waiting int
				if err := db.Raw("SELECT count(*) FROM pg_stat_activity WHERE pid = ? AND wait_event_type = 'Lock'", pid).Scan(&waiting).Error; err != nil {
					t.Fatal(err)
				}
				if waiting == 1 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("T2 did not wait on the authoritative session lock")
				}
				time.Sleep(5 * time.Millisecond)
			}
			// Keep the session lock held beyond the short expiries above, so
			// R2 can only succeed if it uses post-lock authoritative time.
			time.Sleep(600 * time.Millisecond)
			if err := t1.Commit().Error; err != nil {
				t.Fatal(err)
			}
			t2Result := <-t2Done
			if t2Result.err != nil || t2Result.result.Outcome != RotateRefreshTokenRejected {
				t.Fatalf("expired %s result=%s err=%v", tc.name, t2Result.result.Outcome, t2Result.err)
			}
		})
	}
}

func makeDigest(first byte) []byte {
	digest := make([]byte, refreshTokenDigestSize)
	copy(digest, []byte(uuid.NewString()+uuid.NewString()))
	digest[0] = first
	return digest
}

func TestAuthPersistenceConcurrentRotationAndIndependentFamilies(t *testing.T) {
	db := authDB(t)
	session, digest := newAuthFamily(t, db)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	results := make(chan RotateRefreshTokenResult, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tx := db.WithContext(ctx).Begin()
			if tx.Error != nil {
				errs <- tx.Error
				return
			}
			result, err := NewAuthRepositoryWithClock(tx, func() time.Time { return session.CreatedAt.Add(time.Minute) }).RotateRefreshToken(ctx, RotateRefreshTokenCommand{PresentedDigest: digest, ReplacementDigest: makeDigest(byte(0x20 + i)), Now: session.CreatedAt.Add(time.Minute)})
			if err != nil {
				_ = tx.Rollback()
				errs <- err
				return
			}
			if err := tx.Commit().Error; err != nil {
				errs <- err
				return
			}
			results <- result
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var succeeded, replayed int
	for result := range results {
		switch result.Outcome {
		case RotateRefreshTokenSucceeded:
			succeeded++
		case RotateRefreshTokenReplay:
			replayed++
		default:
			t.Fatalf("concurrent outcome=%s session_id=%s", result.Outcome, result.SessionID)
		}
	}
	if succeeded != 1 || replayed != 1 {
		t.Fatalf("concurrent outcomes succeeded=%d replayed=%d", succeeded, replayed)
	}
	if _, err := NewAuthRepository(db).GetActiveSessionBySID(context.Background(), session.ID, session.CreatedAt.Add(2*time.Minute)); !errors.Is(err, ErrAuthRecordNotFound) {
		t.Fatalf("replay did not revoke family: %v", err)
	}

	sessionA, digestA := newAuthFamily(t, db)
	sessionB, digestB := newAuthFamily(t, db)
	independentErrs := make(chan error, 2)
	wg = sync.WaitGroup{}
	for _, input := range []struct {
		session RefreshSession
		digest  []byte
		value   byte
	}{{sessionA, digestA, 0x31}, {sessionB, digestB, 0x32}} {
		wg.Add(1)
		go func(input struct {
			session RefreshSession
			digest  []byte
			value   byte
		}) {
			defer wg.Done()
			err := db.Transaction(func(tx *gorm.DB) error {
				result, err := NewAuthRepositoryWithClock(tx, func() time.Time { return input.session.CreatedAt.Add(time.Minute) }).RotateRefreshToken(ctx, RotateRefreshTokenCommand{PresentedDigest: input.digest, ReplacementDigest: makeDigest(input.value), Now: input.session.CreatedAt.Add(time.Minute)})
				if err != nil {
					return err
				}
				if result.Outcome != RotateRefreshTokenSucceeded {
					return fmt.Errorf("independent rotation outcome=%s", result.Outcome)
				}
				return nil
			})
			independentErrs <- err
		}(input)
	}
	wg.Wait()
	close(independentErrs)
	for err := range independentErrs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, independent := range []RefreshSession{sessionA, sessionB} {
		var currentCount int
		if err := db.Raw(`SELECT count(*) FROM refresh_tokens WHERE session_id = ? AND consumed_at IS NULL AND revoked_at IS NULL`, independent.ID).Scan(&currentCount).Error; err != nil {
			t.Fatal(err)
		}
		if currentCount != 1 {
			t.Fatalf("independent family session=%s current=%d, want 1", independent.ID, currentCount)
		}
		t.Logf("different-family concurrency final session=%s current=%d", independent.ID, currentCount)
	}
}

func rotateAuthForTest(t *testing.T, db *gorm.DB, command RotateRefreshTokenCommand) RotateRefreshTokenResult {
	t.Helper()
	var result RotateRefreshTokenResult
	if err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		result, err = NewAuthRepositoryWithClock(tx, func() time.Time { return command.Now }).RotateRefreshToken(context.Background(), command)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return result
}

func newAuthLineageFamily(t *testing.T, db *gorm.DB) (RefreshSession, []uuid.UUID, [][]byte) {
	t.Helper()
	userID := authUser(t, db, "auth-lineage-proof-"+uuid.NewString()[:12], false)
	created := time.Now().UTC().Truncate(time.Microsecond)
	session := RefreshSession{ID: uuid.New(), UserID: userID, CreatedAt: created, ExpiresAt: created.Add(time.Hour)}
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New()}
	digests := [][]byte{makeDigest(0x51), makeDigest(0x52), makeDigest(0x53), makeDigest(0x54)}
	if err := db.Transaction(func(tx *gorm.DB) error {
		repo := NewAuthRepository(tx)
		if _, err := repo.CreateRefreshSession(context.Background(), session); err != nil {
			return err
		}
		_, err := repo.InsertRefreshTokenDigest(context.Background(), RefreshTokenInsertCommand{
			ID: ids[0], SessionID: session.ID, Digest: digests[0], IssuedAt: created, ExpiresAt: session.ExpiresAt,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return session, ids, digests
}

func TestAuthPersistenceReplayAtEachHistoricalDepthRevokesDescendants(t *testing.T) {
	db := authDB(t)

	// A->B->C, then replay A: no replacement is created and the whole family,
	// including the current C descendant, becomes unusable.
	sessionABC, idsABC, digestsABC := newAuthLineageFamily(t, db)
	for i := 0; i < 2; i++ {
		result := rotateAuthForTest(t, db, RotateRefreshTokenCommand{
			PresentedDigest: digestsABC[i], ReplacementDigest: digestsABC[i+1], ReplacementTokenID: idsABC[i+1],
			Now: sessionABC.CreatedAt.Add(time.Duration(i+1) * time.Minute),
		})
		if result.Outcome != RotateRefreshTokenSucceeded {
			t.Fatalf("A->B->C step %d outcome=%s", i, result.Outcome)
		}
	}
	replayA := rotateAuthForTest(t, db, RotateRefreshTokenCommand{
		PresentedDigest: digestsABC[0], ReplacementDigest: makeDigest(0x61),
		Now: sessionABC.CreatedAt.Add(3 * time.Minute),
	})
	if replayA.Outcome != RotateRefreshTokenReplay || replayA.ReplacementTokenID != uuid.Nil {
		t.Fatalf("replay A outcome=%s replacement=%s", replayA.Outcome, replayA.ReplacementTokenID)
	}
	var abcState struct {
		Total   int
		Revoked int
		Current int
		Links   int
	}
	if err := db.Raw(`SELECT count(*) AS total,
		count(*) FILTER (WHERE revoked_at IS NOT NULL) AS revoked,
		count(*) FILTER (WHERE consumed_at IS NULL AND revoked_at IS NULL) AS current,
		count(*) FILTER (WHERE replaced_by_token_id IS NOT NULL) AS links
		FROM refresh_tokens WHERE session_id = ?`, sessionABC.ID).Scan(&abcState).Error; err != nil {
		t.Fatal(err)
	}
	if abcState.Total != 3 || abcState.Revoked != 3 || abcState.Current != 0 || abcState.Links != 2 {
		t.Fatalf("replay A state total=%d revoked=%d current=%d links=%d", abcState.Total, abcState.Revoked, abcState.Current, abcState.Links)
	}
	replayC := rotateAuthForTest(t, db, RotateRefreshTokenCommand{
		PresentedDigest: digestsABC[2], ReplacementDigest: makeDigest(0x62),
		Now: sessionABC.CreatedAt.Add(4 * time.Minute),
	})
	if replayC.Outcome != RotateRefreshTokenReplay {
		t.Fatalf("revoked descendant C outcome=%s, want replay", replayC.Outcome)
	}
	var abcTotal int
	if err := db.Raw(`SELECT count(*) FROM refresh_tokens WHERE session_id = ?`, sessionABC.ID).Scan(&abcTotal).Error; err != nil {
		t.Fatal(err)
	}
	if abcTotal != 3 {
		t.Fatalf("revoked descendant created a token: total=%d", abcTotal)
	}

	// A->B->C->D, then replay B: both C and D descendants are unusable.
	sessionABCD, idsABCD, digestsABCD := newAuthLineageFamily(t, db)
	for i := 0; i < 3; i++ {
		result := rotateAuthForTest(t, db, RotateRefreshTokenCommand{
			PresentedDigest: digestsABCD[i], ReplacementDigest: digestsABCD[i+1], ReplacementTokenID: idsABCD[i+1],
			Now: sessionABCD.CreatedAt.Add(time.Duration(i+1) * time.Minute),
		})
		if result.Outcome != RotateRefreshTokenSucceeded {
			t.Fatalf("A->B->C->D step %d outcome=%s", i, result.Outcome)
		}
	}
	replayB := rotateAuthForTest(t, db, RotateRefreshTokenCommand{
		PresentedDigest: digestsABCD[1], ReplacementDigest: makeDigest(0x63),
		Now: sessionABCD.CreatedAt.Add(4 * time.Minute),
	})
	if replayB.Outcome != RotateRefreshTokenReplay || replayB.ReplacementTokenID != uuid.Nil {
		t.Fatalf("replay B outcome=%s replacement=%s", replayB.Outcome, replayB.ReplacementTokenID)
	}
	var abcdState struct {
		Total   int
		Revoked int
		Current int
		Links   int
	}
	if err := db.Raw(`SELECT count(*) AS total,
		count(*) FILTER (WHERE revoked_at IS NOT NULL) AS revoked,
		count(*) FILTER (WHERE consumed_at IS NULL AND revoked_at IS NULL) AS current,
		count(*) FILTER (WHERE replaced_by_token_id IS NOT NULL) AS links
		FROM refresh_tokens WHERE session_id = ?`, sessionABCD.ID).Scan(&abcdState).Error; err != nil {
		t.Fatal(err)
	}
	if abcdState.Total != 4 || abcdState.Revoked != 4 || abcdState.Current != 0 || abcdState.Links != 3 {
		t.Fatalf("replay B state total=%d revoked=%d current=%d links=%d", abcdState.Total, abcdState.Revoked, abcdState.Current, abcdState.Links)
	}
	replayD := rotateAuthForTest(t, db, RotateRefreshTokenCommand{
		PresentedDigest: digestsABCD[3], ReplacementDigest: makeDigest(0x64),
		Now: sessionABCD.CreatedAt.Add(5 * time.Minute),
	})
	if replayD.Outcome != RotateRefreshTokenReplay {
		t.Fatalf("revoked descendant D outcome=%s, want replay", replayD.Outcome)
	}
}

func TestAuthPersistenceRotationInsertFailureLeavesOriginalCurrent(t *testing.T) {
	db := authDB(t)
	session, oldDigest := newAuthFamily(t, db)
	blocker, _ := newAuthFamily(t, db)
	var blockerTokenIDText, originalIDText string
	if err := db.Raw(`SELECT id FROM refresh_tokens WHERE session_id = ?`, blocker.ID).Scan(&blockerTokenIDText).Error; err != nil {
		t.Fatal(err)
	}
	blockerTokenID, err := uuid.Parse(blockerTokenIDText)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Raw(`SELECT id FROM refresh_tokens WHERE session_id = ?`, session.ID).Scan(&originalIDText).Error; err != nil {
		t.Fatal(err)
	}
	originalID, err := uuid.Parse(originalIDText)
	if err != nil {
		t.Fatal(err)
	}
	// The duplicate replacement primary key fails after consume, so the
	// caller transaction must roll back both consume and lineage insertion.
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, err := NewAuthRepositoryWithClock(tx, func() time.Time { return session.CreatedAt.Add(time.Minute) }).RotateRefreshToken(context.Background(), RotateRefreshTokenCommand{
			PresentedDigest: oldDigest, ReplacementDigest: makeDigest(0x71), ReplacementTokenID: blockerTokenID,
			Now: session.CreatedAt.Add(time.Minute),
		})
		return err
	}); err == nil {
		t.Fatal("duplicate replacement ID unexpectedly committed")
	}
	var originalState struct {
		Consumed bool
		Linked   bool
		Revoked  bool
	}
	if err := db.Raw(`SELECT consumed_at IS NOT NULL, replaced_by_token_id IS NOT NULL, revoked_at IS NOT NULL
		FROM refresh_tokens WHERE id = ?`, originalID).Scan(&originalState).Error; err != nil {
		t.Fatal(err)
	}
	if originalState.Consumed || originalState.Linked || originalState.Revoked {
		t.Fatalf("failed rotation left original non-current consumed=%t linked=%t revoked=%t", originalState.Consumed, originalState.Linked, originalState.Revoked)
	}
	var tokenCount int
	if err := db.Raw(`SELECT count(*) FROM refresh_tokens WHERE session_id = ?`, session.ID).Scan(&tokenCount).Error; err != nil {
		t.Fatal(err)
	}
	if tokenCount != 1 {
		t.Fatalf("failed rotation created half-state token count=%d", tokenCount)
	}
	// The original remains authoritative and can be rotated successfully.
	result := rotateAuthForTest(t, db, RotateRefreshTokenCommand{
		PresentedDigest: oldDigest, ReplacementDigest: makeDigest(0x72),
		Now: session.CreatedAt.Add(2 * time.Minute),
	})
	if result.Outcome != RotateRefreshTokenSucceeded {
		t.Fatalf("retry with original current outcome=%s", result.Outcome)
	}
}

func TestAuthPersistenceConcurrentRotationOfCurrentCHasNoSiblings(t *testing.T) {
	db := authDB(t)
	session, ids, digests := newAuthLineageFamily(t, db)
	for i := 0; i < 2; i++ {
		result := rotateAuthForTest(t, db, RotateRefreshTokenCommand{
			PresentedDigest: digests[i], ReplacementDigest: digests[i+1], ReplacementTokenID: ids[i+1],
			Now: session.CreatedAt.Add(time.Duration(i+1) * time.Minute),
		})
		if result.Outcome != RotateRefreshTokenSucceeded {
			t.Fatalf("setup C step %d outcome=%s", i, result.Outcome)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	results := make(chan RotateRefreshTokenResult, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tx := db.WithContext(ctx).Begin()
			if tx.Error != nil {
				errs <- tx.Error
				return
			}
			result, err := NewAuthRepositoryWithClock(tx, func() time.Time { return session.CreatedAt.Add(3 * time.Minute) }).RotateRefreshToken(ctx, RotateRefreshTokenCommand{
				PresentedDigest: digests[2], ReplacementDigest: makeDigest(byte(0x80 + i)),
				Now: session.CreatedAt.Add(3 * time.Minute),
			})
			if err != nil {
				_ = tx.Rollback()
				errs <- err
				return
			}
			if err := tx.Commit().Error; err != nil {
				errs <- err
				return
			}
			results <- result
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var succeeded, replayed int
	var winnerID uuid.UUID
	for result := range results {
		switch result.Outcome {
		case RotateRefreshTokenSucceeded:
			succeeded++
			winnerID = result.ReplacementTokenID
			if winnerID == uuid.Nil {
				t.Fatal("successful concurrent rotation returned no successor")
			}
		case RotateRefreshTokenReplay:
			replayed++
			if result.ReplacementTokenID != uuid.Nil {
				t.Fatalf("replay loser returned successor=%s", result.ReplacementTokenID)
			}
		default:
			t.Fatalf("current C concurrent outcome=%s", result.Outcome)
		}
	}
	if succeeded != 1 || replayed != 1 {
		t.Fatalf("current C concurrent outcomes succeeded=%d replayed=%d", succeeded, replayed)
	}
	var state struct {
		Total   int
		Current int
		Links   int
		Revoked int
	}
	if err := db.Raw(`SELECT count(*) AS total,
		count(*) FILTER (WHERE consumed_at IS NULL AND revoked_at IS NULL) AS current,
		count(*) FILTER (WHERE replaced_by_token_id IS NOT NULL) AS links,
		count(*) FILTER (WHERE revoked_at IS NOT NULL) AS revoked
		FROM refresh_tokens WHERE session_id = ?`, session.ID).Scan(&state).Error; err != nil {
		t.Fatal(err)
	}
	if state.Total != 4 || state.Current != 0 || state.Links != 3 || state.Revoked != 4 {
		t.Fatalf("current C concurrent state total=%d current=%d links=%d revoked=%d", state.Total, state.Current, state.Links, state.Revoked)
	}
	var successorCount int
	if err := db.Raw(`SELECT count(*) FROM refresh_tokens WHERE id = ? AND session_id = ?`, winnerID, session.ID).Scan(&successorCount).Error; err != nil {
		t.Fatal(err)
	}
	if successorCount != 1 {
		t.Fatalf("concurrent loser/winner successor rows=%d, want exactly one successor", successorCount)
	}
	t.Logf("same-token concurrency final session=%s total=%d current=%d revoked=%d winner=%s", session.ID, state.Total, state.Current, state.Revoked, winnerID)
}

func TestAuthPersistenceDigestCapabilityBoundary(t *testing.T) {
	for _, typ := range []reflect.Type{
		reflect.TypeOf(RefreshTokenState{}),
		reflect.TypeOf(RotateRefreshTokenResult{}),
		reflect.TypeOf(RefreshTokenInsertCommand{}),
	} {
		if _, ok := typ.FieldByName("TokenHash"); ok {
			t.Fatalf("exported persistence type %s exposes TokenHash", typ.Name())
		}
	}
	if _, ok := reflect.TypeOf((*AuthRepository)(nil)).MethodByName("FindRefreshTokenByDigestForDiscovery"); ok {
		t.Fatal("digest discovery remains an exported repository seam")
	}
	if _, ok := reflect.TypeOf((*AuthPersistence)(nil)).Elem().MethodByName("FindRefreshTokenByDigestForDiscovery"); ok {
		t.Fatal("digest discovery remains in the exported persistence interface")
	}
}

func TestAuthPersistenceRequiresCallerTransactionAndFence(t *testing.T) {
	db := authDB(t)
	repo := NewAuthRepository(db)
	if _, err := repo.CreateRefreshSession(context.Background(), RefreshSession{UserID: 1, ExpiresAt: time.Now().UTC().Add(time.Hour)}); !errors.Is(err, ErrAuthTransactionRequired) {
		t.Fatalf("pooled mutation error=%v, want transaction required", err)
	}
	if err := AcquireAuthWriterFence(context.Background(), db); !errors.Is(err, ErrAuthTransactionRequired) {
		t.Fatalf("pooled fence error=%v, want transaction required", err)
	}
	if _, err := findRefreshTokenByDigest(db, [refreshTokenDigestSize]byte{}); !errors.Is(err, ErrAuthTransactionRequired) {
		t.Fatalf("pooled discovery error=%v, want transaction required", err)
	}
}
