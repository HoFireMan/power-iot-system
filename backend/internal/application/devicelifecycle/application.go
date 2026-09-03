// Package devicelifecycle owns the bounded administrative Device lifecycle
// transition. It reuses the existing Admin Binding operation ledger for
// idempotency, but deliberately does not append to the five-action binding
// audit history.
package devicelifecycle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"power-iot-backend/internal/adapters/persistence"
	"power-iot-backend/internal/core/domain"
	"power-iot-backend/internal/data/migrations"
)

type Command struct {
	DeviceID        uint
	Reason          string
	RequestIdentity string
	Actor           domain.ActorContext
}

type Result struct {
	OperationID     uuid.UUID              `json:"operationId"`
	Action          domain.BindingAction   `json:"action"`
	DeviceID        uint                   `json:"deviceId"`
	LifecycleStatus domain.DeviceLifecycle `json:"lifecycleStatus"`
}

type Application struct{ db *gorm.DB }

func New(db *gorm.DB) *Application { return &Application{db: db} }

func (a *Application) Disable(ctx context.Context, cmd Command) (Result, error) {
	return a.transition(ctx, domain.ActionDisableDevice, domain.DeviceLifecycleDisabled, cmd)
}
func (a *Application) Enable(ctx context.Context, cmd Command) (Result, error) {
	return a.transition(ctx, domain.ActionEnableDevice, domain.DeviceLifecycleActive, cmd)
}
func (a *Application) Retire(ctx context.Context, cmd Command) (Result, error) {
	return a.transition(ctx, domain.ActionRetireDevice, domain.DeviceLifecycleRetired, cmd)
}

func canonicalHash(action domain.BindingAction, cmd Command) []byte {
	body, _ := json.Marshal(struct {
		Action domain.BindingAction `json:"action"`
		Device uint                 `json:"deviceId"`
		Reason string               `json:"reason"`
	}{action, cmd.DeviceID, strings.TrimSpace(cmd.Reason)})
	hash := sha256.Sum256(body)
	return hash[:]
}

func (a *Application) transition(ctx context.Context, action domain.BindingAction, target domain.DeviceLifecycle, cmd Command) (result Result, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if a == nil || a.db == nil {
		return result, domain.NewDomainError(domain.ErrPersistenceFailure, "database is not configured")
	}
	if cmd.DeviceID == 0 || strings.TrimSpace(cmd.RequestIdentity) == "" {
		return result, domain.NewDomainError(domain.ErrInvalidRequest, "device and request identity are required")
	}
	if err := authorizeActor(ctx, a.db, cmd.Actor, cmd.DeviceID, action); err != nil {
		return result, err
	}
	hash := canonicalHash(action, cmd)
	err = a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := migrations.AcquireSharedWriterFenceOnGORM(ctx, tx); err != nil {
			return domain.NewDomainError(domain.ErrPersistenceFailure, "shared writer admission failed")
		}
		// Recheck authorization after admission, in the same transaction as the
		// lock and mutation. The preflight above only gives the HTTP layer a
		// useful early failure and is never authority for the write.
		if err := authorizeActorTx(tx, cmd.Actor, cmd.DeviceID, action); err != nil {
			return err
		}
		existing, lookupErr := persistence.LoadAdminBindingOperation(tx, cmd.Actor.ActorID, cmd.Actor.ScopeKey, string(action), cmd.RequestIdentity)
		if lookupErr == nil {
			if !bytes.Equal(existing.CanonicalRequestHash, hash) {
				return domain.NewDomainError(domain.ErrIdempotencyKeyReused, "idempotency key was reused")
			}
			if existing.CommittedAt == nil || len(existing.CommittedResponse) == 0 {
				return domain.NewDomainError(domain.ErrConcurrentTransition, "idempotency operation is not committed")
			}
			if err := json.Unmarshal(existing.CommittedResponse, &result); err != nil {
				return domain.NewDomainError(domain.ErrPersistenceFailure, "committed operation result is invalid")
			}
			if result.OperationID != existing.OperationID {
				return domain.NewDomainError(domain.ErrPersistenceFailure, "committed operation identity is invalid")
			}
			return nil
		}
		if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
			return lookupErr
		}
		clientID, err := actorClientID(cmd.Actor)
		if err != nil {
			return err
		}
		op := domain.AdminBindingOperation{ID: uuid.New(), OperationID: uuid.New(), IdempotencyKey: cmd.RequestIdentity, Operation: string(action), ScopeKey: cmd.Actor.ScopeKey, ActorID: cmd.Actor.ActorID, CanonicalRequestHash: hash, ScopeSnapshot: mustJSON(cmd.Actor.Scope), ClientID: &clientID}
		existing, claimed, err := persistence.ClaimAdminBindingOperation(tx, &op)
		if err != nil {
			return err
		}
		if !claimed {
			if existing.CommittedAt == nil || len(existing.CommittedResponse) == 0 {
				return domain.NewDomainError(domain.ErrConcurrentTransition, "idempotency operation is not committed")
			}
			if !bytes.Equal(existing.CanonicalRequestHash, hash) {
				return domain.NewDomainError(domain.ErrIdempotencyKeyReused, "idempotency key was reused")
			}
			if err := json.Unmarshal(existing.CommittedResponse, &result); err != nil {
				return domain.NewDomainError(domain.ErrPersistenceFailure, "committed operation result is invalid")
			}
			return nil
		}
		var device domain.Device
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&device, cmd.DeviceID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.NewDomainError(domain.ErrDeviceNotFound, "device was not found")
			}
			return err
		}
		state := device.LifecycleStatus
		if state == "" {
			state = domain.DeviceLifecycleActive
		}
		if err := domain.ValidateDeviceLifecycleTransition(state, target); err != nil {
			if state == domain.DeviceLifecycleRetired {
				return domain.NewDomainError(domain.ErrDeviceRetired, "device is retired")
			}
			return domain.NewDomainError(domain.ErrInvalidStateTransition, "device lifecycle transition is not allowed")
		}
		if target == domain.DeviceLifecycleDisabled || target == domain.DeviceLifecycleRetired {
			active, err := hasActiveAssignment(tx, cmd.DeviceID)
			if err != nil {
				return err
			}
			if active {
				return domain.NewDomainError(domain.ErrDeviceAlreadyAssigned, "device has an active assignment")
			}
		}
		updated := tx.Model(&domain.Device{}).Where("id = ? AND lifecycle_status = ?", cmd.DeviceID, string(state)).Update("lifecycle_status", string(target))
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return domain.NewDomainError(domain.ErrConcurrentTransition, "device lifecycle changed concurrently")
		}
		result = Result{OperationID: op.OperationID, Action: action, DeviceID: device.ID, LifecycleStatus: target}
		body, err := json.Marshal(result)
		if err != nil {
			return err
		}
		return persistence.CommitAdminBindingOperation(tx, op.OperationID, body, timeNow())
	})
	if err != nil {
		return Result{}, mapError(err)
	}
	return result, nil
}

// timeNow is a variable-shaped helper kept local so lifecycle commits have the
// same UTC representation as the existing operation ledger without introducing
// a second clock dependency into the public API.
func timeNow() (t time.Time) { return time.Now().UTC() }

func mustJSON(value interface{}) json.RawMessage { body, _ := json.Marshal(value); return body }

func hasActiveAssignment(tx *gorm.DB, deviceID uint) (bool, error) {
	var count int64
	if err := tx.Model(&domain.DeviceAssignment{}).Where("device_id = ? AND valid_to IS NULL", deviceID).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func actorClientID(actor domain.ActorContext) (uint, error) {
	value := strings.TrimPrefix(actor.Scope.TenantKey, "client:")
	id, err := strconv.ParseUint(value, 10, 64)
	if err != nil || id == 0 || uint64(uint(id)) != id {
		return 0, domain.NewDomainError(domain.ErrTenantScopeDenied, "authoritative Client scope is required")
	}
	return uint(id), nil
}

func authorizeActor(ctx context.Context, db *gorm.DB, actor domain.ActorContext, deviceID uint, action domain.BindingAction) error {
	if db == nil {
		return domain.NewDomainError(domain.ErrPersistenceFailure, "authorization unavailable")
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return authorizeActorTx(tx, actor, deviceID, action) })
}

func authorizeActorTx(tx *gorm.DB, actor domain.ActorContext, deviceID uint, action domain.BindingAction) error {
	if actor.ActorID == 0 || strings.TrimSpace(actor.ScopeKey) == "" || !actor.HasAction(action) {
		return domain.NewDomainError(domain.ErrOperationForbidden, "admin access required")
	}
	clientID, err := actorClientID(actor)
	if err != nil {
		return err
	}
	var user struct{ IsAdmin bool }
	if q := tx.Raw("SELECT is_admin FROM users WHERE id = ? AND auth_enabled = TRUE FOR SHARE", actor.ActorID).Scan(&user); q.Error != nil {
		return domain.NewDomainError(domain.ErrPersistenceFailure, "authorization unavailable")
	} else if q.RowsAffected != 1 || !user.IsAdmin {
		return domain.NewDomainError(domain.ErrOperationForbidden, "admin access required")
	}
	if actor.SessionID != uuid.Nil {
		var session struct{ UserID uint }
		q := tx.Raw(`WITH locked_session AS MATERIALIZED (SELECT user_id, expires_at FROM refresh_sessions WHERE id = ? AND user_id = ? AND revoked_at IS NULL FOR UPDATE) SELECT user_id FROM locked_session WHERE expires_at > clock_timestamp()`, actor.SessionID, actor.ActorID).Scan(&session)
		if q.Error != nil {
			return domain.NewDomainError(domain.ErrPersistenceFailure, "session revalidation failed")
		}
		if q.RowsAffected != 1 {
			return domain.NewDomainError(domain.ErrAuthenticationRequired, "authenticated session is no longer valid")
		}
	}
	if len(actor.Scope.ShopIDs) == 0 {
		return domain.NewDomainError(domain.ErrSiteScopeDenied, "authoritative Shop scope is required")
	}
	for _, shopID := range actor.Scope.ShopIDs {
		var relation struct{ ClientID uint }
		q := tx.Raw(`SELECT s.client_id FROM user_shop_relations r JOIN shops s ON s.id = r.shop_id WHERE r.user_id = ? AND r.shop_id = ? AND s.client_id = ? AND s.is_active = TRUE FOR SHARE OF r, s`, actor.ActorID, shopID, clientID).Scan(&relation)
		if q.Error != nil {
			return domain.NewDomainError(domain.ErrPersistenceFailure, "scope revalidation failed")
		}
		if q.RowsAffected != 1 {
			return domain.NewDomainError(domain.ErrSiteScopeDenied, "Shop is outside the authorized scope")
		}
	}
	var owned bool
	if q := tx.Raw("SELECT EXISTS (SELECT 1 FROM devices WHERE id = ? AND inventory_owner_client_id = ? AND inventory_owner_client_id IS NOT NULL)", deviceID, clientID).Scan(&owned); q.Error != nil {
		return domain.NewDomainError(domain.ErrPersistenceFailure, "inventory authority unavailable")
	} else if !owned {
		return domain.NewDomainError(domain.ErrDeviceScopeDenied, "Device inventory authority is unavailable")
	}
	if len(actor.Scope.DeviceIDs) > 0 {
		found := false
		for _, allowed := range actor.Scope.DeviceIDs {
			if allowed == deviceID {
				found = true
				break
			}
		}
		if !found {
			return domain.NewDomainError(domain.ErrDeviceScopeDenied, "Device is outside the authorized scope")
		}
	}
	return nil
}

func mapError(err error) error {
	if _, ok := err.(*domain.DomainError); ok {
		return err
	}
	return domain.NewDomainError(domain.ErrPersistenceFailure, fmt.Sprintf("device lifecycle operation failed: %v", err))
}
