package adminbinding

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"

	"power-iot-backend/internal/adapters/persistence"
	"power-iot-backend/internal/core/domain"
	"power-iot-backend/internal/data/migrations"
)

// ExecutionHooks are deliberately narrow test seams for proving rollback after
// a partial mutation. Production callers leave them nil.
type ExecutionHooks struct {
	AfterOperationClaim func() error
	BeforeDeviceLock    func(*gorm.DB) error
	AfterLocks          func() error
	AfterMutation       func() error
	AfterAudit          func() error
	AfterResult         func() error
}

// Executor is the cohesive transaction-capable Admin Binding module. Its
// public command methods own a bounded whole-transaction retry, while the
// InTransaction methods consume a caller-owned transaction and never commit it.
type Executor struct {
	db          *gorm.DB
	maxAttempts int
	hooks       ExecutionHooks
}

func NewExecutor(db *gorm.DB) *Executor {
	return &Executor{db: db, maxAttempts: 3}
}

func NewExecutorWithHooks(db *gorm.DB, hooks ExecutionHooks) *Executor {
	return &Executor{db: db, maxAttempts: 3, hooks: hooks}
}

func normalizeExecutionContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func (e *Executor) CreateMeasurementPoint(ctx context.Context, cmd domain.CreateMeasurementPointCommand) (domain.AdminBindingResult, error) {
	ctx = normalizeExecutionContext(ctx)
	hash, err := canonicalCreateHash(cmd)
	if err != nil {
		return domain.AdminBindingResult{}, err
	}
	return e.run(ctx, domain.ActionCreateMeasurementPoint, cmd.RequestIdentity, cmd.Actor, hash,
		func(tx *gorm.DB) (uint, error) {
			plan, err := New(persistence.NewTransactionLookup(tx)).CreateMeasurementPoint(ctx, cmd)
			if err != nil {
				return 0, err
			}
			if plan.Audit.ClientID == nil {
				return 0, errors.New("authoritative operation Client provenance is required")
			}
			return *plan.Audit.ClientID, nil
		},
		func(tx *gorm.DB, operation domain.AdminBindingOperation) (domain.AdminBindingResult, error) {
			return e.createMeasurementPointInTransaction(ctx, tx, cmd, operation)
		})
}

func (e *Executor) BindDevice(ctx context.Context, cmd domain.BindDeviceCommand) (domain.AdminBindingResult, error) {
	ctx = normalizeExecutionContext(ctx)
	hash, err := canonicalBindHash(cmd)
	if err != nil {
		return domain.AdminBindingResult{}, err
	}
	return e.run(ctx, domain.ActionBind, cmd.RequestIdentity, cmd.Actor, hash,
		func(tx *gorm.DB) (uint, error) {
			plan, err := New(persistence.NewTransactionLookup(tx)).BindDevice(ctx, cmd)
			if err != nil {
				return 0, err
			}
			if plan.Audit.ClientID == nil {
				return 0, errors.New("authoritative operation Client provenance is required")
			}
			return *plan.Audit.ClientID, nil
		},
		func(tx *gorm.DB, operation domain.AdminBindingOperation) (domain.AdminBindingResult, error) {
			return e.bindInTransaction(ctx, tx, cmd, operation)
		})
}

func (e *Executor) ReplaceDevice(ctx context.Context, cmd domain.ReplaceDeviceCommand) (domain.AdminBindingResult, error) {
	ctx = normalizeExecutionContext(ctx)
	hash, err := canonicalReplaceHash(cmd)
	if err != nil {
		return domain.AdminBindingResult{}, err
	}
	return e.run(ctx, domain.ActionReplace, cmd.RequestIdentity, cmd.Actor, hash,
		func(tx *gorm.DB) (uint, error) {
			plan, err := New(persistence.NewTransactionLookup(tx)).ReplaceDevice(ctx, cmd)
			if err != nil {
				return 0, err
			}
			if plan.Audit.ClientID == nil {
				return 0, errors.New("authoritative operation Client provenance is required")
			}
			return *plan.Audit.ClientID, nil
		},
		func(tx *gorm.DB, operation domain.AdminBindingOperation) (domain.AdminBindingResult, error) {
			return e.replaceInTransaction(ctx, tx, cmd, operation)
		})
}

func (e *Executor) RelocateDevice(ctx context.Context, cmd domain.RelocateDeviceCommand) (domain.AdminBindingResult, error) {
	ctx = normalizeExecutionContext(ctx)
	hash, err := canonicalRelocateHash(cmd)
	if err != nil {
		return domain.AdminBindingResult{}, err
	}
	return e.run(ctx, domain.ActionRelocate, cmd.RequestIdentity, cmd.Actor, hash,
		func(tx *gorm.DB) (uint, error) {
			plan, err := New(persistence.NewTransactionLookup(tx)).RelocateDevice(ctx, cmd)
			if err != nil {
				return 0, err
			}
			if plan.Audit.ClientID == nil {
				return 0, errors.New("authoritative operation Client provenance is required")
			}
			return *plan.Audit.ClientID, nil
		},
		func(tx *gorm.DB, operation domain.AdminBindingOperation) (domain.AdminBindingResult, error) {
			return e.relocateInTransaction(ctx, tx, cmd, operation)
		})
}

func (e *Executor) UnbindDevice(ctx context.Context, cmd domain.UnbindDeviceCommand) (domain.AdminBindingResult, error) {
	ctx = normalizeExecutionContext(ctx)
	hash, err := canonicalUnbindHash(cmd)
	if err != nil {
		return domain.AdminBindingResult{}, err
	}
	return e.run(ctx, domain.ActionUnbind, cmd.RequestIdentity, cmd.Actor, hash,
		func(tx *gorm.DB) (uint, error) {
			plan, err := New(persistence.NewTransactionLookup(tx)).UnbindDevice(ctx, cmd)
			if err != nil {
				return 0, err
			}
			if plan.Audit.ClientID == nil {
				return 0, errors.New("authoritative operation Client provenance is required")
			}
			return *plan.Audit.ClientID, nil
		},
		func(tx *gorm.DB, operation domain.AdminBindingOperation) (domain.AdminBindingResult, error) {
			return e.unbindInTransaction(ctx, tx, cmd, operation)
		})
}

// The InTransaction methods are the caller-owned transaction seam. They claim
// and persist the operation in tx but deliberately do not call Commit/Rollback.
func (e *Executor) CreateMeasurementPointInTransaction(ctx context.Context, tx *gorm.DB, cmd domain.CreateMeasurementPointCommand) (domain.AdminBindingResult, error) {
	ctx = normalizeExecutionContext(ctx)
	if tx != nil {
		tx = tx.WithContext(ctx)
	}
	hash, err := canonicalCreateHash(cmd)
	if err != nil {
		return domain.AdminBindingResult{}, err
	}
	return e.executeClaimed(ctx, tx, domain.ActionCreateMeasurementPoint, cmd.RequestIdentity, cmd.Actor, hash,
		func(tx *gorm.DB) (uint, error) {
			plan, err := New(persistence.NewTransactionLookup(tx)).CreateMeasurementPoint(ctx, cmd)
			if err != nil {
				return 0, err
			}
			if plan.Audit.ClientID == nil {
				return 0, errors.New("authoritative operation Client provenance is required")
			}
			return *plan.Audit.ClientID, nil
		},
		func(tx *gorm.DB, operation domain.AdminBindingOperation) (domain.AdminBindingResult, error) {
			return e.createMeasurementPointInTransaction(ctx, tx, cmd, operation)
		})
}

func (e *Executor) BindDeviceInTransaction(ctx context.Context, tx *gorm.DB, cmd domain.BindDeviceCommand) (domain.AdminBindingResult, error) {
	ctx = normalizeExecutionContext(ctx)
	if tx != nil {
		tx = tx.WithContext(ctx)
	}
	hash, err := canonicalBindHash(cmd)
	if err != nil {
		return domain.AdminBindingResult{}, err
	}
	return e.executeClaimed(ctx, tx, domain.ActionBind, cmd.RequestIdentity, cmd.Actor, hash,
		func(tx *gorm.DB) (uint, error) {
			plan, err := New(persistence.NewTransactionLookup(tx)).BindDevice(ctx, cmd)
			if err != nil {
				return 0, err
			}
			if plan.Audit.ClientID == nil {
				return 0, errors.New("authoritative operation Client provenance is required")
			}
			return *plan.Audit.ClientID, nil
		},
		func(tx *gorm.DB, operation domain.AdminBindingOperation) (domain.AdminBindingResult, error) {
			return e.bindInTransaction(ctx, tx, cmd, operation)
		})
}

func (e *Executor) ReplaceDeviceInTransaction(ctx context.Context, tx *gorm.DB, cmd domain.ReplaceDeviceCommand) (domain.AdminBindingResult, error) {
	ctx = normalizeExecutionContext(ctx)
	if tx != nil {
		tx = tx.WithContext(ctx)
	}
	hash, err := canonicalReplaceHash(cmd)
	if err != nil {
		return domain.AdminBindingResult{}, err
	}
	return e.executeClaimed(ctx, tx, domain.ActionReplace, cmd.RequestIdentity, cmd.Actor, hash,
		func(tx *gorm.DB) (uint, error) {
			plan, err := New(persistence.NewTransactionLookup(tx)).ReplaceDevice(ctx, cmd)
			if err != nil {
				return 0, err
			}
			if plan.Audit.ClientID == nil {
				return 0, errors.New("authoritative operation Client provenance is required")
			}
			return *plan.Audit.ClientID, nil
		},
		func(tx *gorm.DB, operation domain.AdminBindingOperation) (domain.AdminBindingResult, error) {
			return e.replaceInTransaction(ctx, tx, cmd, operation)
		})
}

func (e *Executor) RelocateDeviceInTransaction(ctx context.Context, tx *gorm.DB, cmd domain.RelocateDeviceCommand) (domain.AdminBindingResult, error) {
	ctx = normalizeExecutionContext(ctx)
	if tx != nil {
		tx = tx.WithContext(ctx)
	}
	hash, err := canonicalRelocateHash(cmd)
	if err != nil {
		return domain.AdminBindingResult{}, err
	}
	return e.executeClaimed(ctx, tx, domain.ActionRelocate, cmd.RequestIdentity, cmd.Actor, hash,
		func(tx *gorm.DB) (uint, error) {
			plan, err := New(persistence.NewTransactionLookup(tx)).RelocateDevice(ctx, cmd)
			if err != nil {
				return 0, err
			}
			if plan.Audit.ClientID == nil {
				return 0, errors.New("authoritative operation Client provenance is required")
			}
			return *plan.Audit.ClientID, nil
		},
		func(tx *gorm.DB, operation domain.AdminBindingOperation) (domain.AdminBindingResult, error) {
			return e.relocateInTransaction(ctx, tx, cmd, operation)
		})
}

func (e *Executor) UnbindDeviceInTransaction(ctx context.Context, tx *gorm.DB, cmd domain.UnbindDeviceCommand) (domain.AdminBindingResult, error) {
	ctx = normalizeExecutionContext(ctx)
	if tx != nil {
		tx = tx.WithContext(ctx)
	}
	hash, err := canonicalUnbindHash(cmd)
	if err != nil {
		return domain.AdminBindingResult{}, err
	}
	return e.executeClaimed(ctx, tx, domain.ActionUnbind, cmd.RequestIdentity, cmd.Actor, hash,
		func(tx *gorm.DB) (uint, error) {
			plan, err := New(persistence.NewTransactionLookup(tx)).UnbindDevice(ctx, cmd)
			if err != nil {
				return 0, err
			}
			if plan.Audit.ClientID == nil {
				return 0, errors.New("authoritative operation Client provenance is required")
			}
			return *plan.Audit.ClientID, nil
		},
		func(tx *gorm.DB, operation domain.AdminBindingOperation) (domain.AdminBindingResult, error) {
			return e.unbindInTransaction(ctx, tx, cmd, operation)
		})
}

type operationClientResolver func(*gorm.DB) (uint, error)

func (e *Executor) run(ctx context.Context, action domain.BindingAction, key string, actor domain.ActorContext, hash []byte, resolveClient operationClientResolver, work func(*gorm.DB, domain.AdminBindingOperation) (domain.AdminBindingResult, error)) (domain.AdminBindingResult, error) {
	ctx = normalizeExecutionContext(ctx)
	if e == nil || e.db == nil {
		return domain.AdminBindingResult{}, domain.NewDomainError(domain.ErrPersistenceFailure, "database is not configured")
	}
	attempts := e.maxAttempts
	if attempts <= 0 {
		attempts = 3
	}
	var result domain.AdminBindingResult
	for attempt := 0; attempt < attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return domain.AdminBindingResult{}, err
		}
		result = domain.AdminBindingResult{}
		err := e.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var callbackErr error
			result, callbackErr = e.executeClaimed(ctx, tx, action, key, actor, hash, resolveClient, work)
			return callbackErr
		})
		if err == nil {
			return result, nil
		}
		if isTransientPostgresError(err) && attempt+1 < attempts {
			backoff := []time.Duration{10 * time.Millisecond, 50 * time.Millisecond}[attempt]
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return domain.AdminBindingResult{}, ctx.Err()
			case <-timer.C:
			}
			continue
		}
		if isTransientPostgresError(err) {
			return domain.AdminBindingResult{}, domain.NewDomainError(domain.ErrConcurrentTransition, "transaction could not be serialized after bounded retries")
		}
		return domain.AdminBindingResult{}, domainError(err)
	}
	return result, domain.NewDomainError(domain.ErrConcurrentTransition, "transaction could not be serialized")
}

func sortedUniqueUintIDs(ids []uint) []uint {
	sorted := append([]uint(nil), ids...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	if len(sorted) < 2 {
		return sorted
	}
	unique := sorted[:1]
	for _, id := range sorted[1:] {
		if id != unique[len(unique)-1] {
			unique = append(unique, id)
		}
	}
	return unique
}

func revalidateAdminActor(tx *gorm.DB, actor domain.ActorContext) error {
	// These are authorization facts, not ordinary reads. FOR SHARE keeps a
	// revocation, deactivation, or ownership change from committing between
	// revalidation and the mutation (or an idempotent replay) in this tx.
	var auth struct {
		IsAdmin bool
	}
	user := tx.Raw("SELECT is_admin FROM users WHERE id = ? AND auth_enabled = TRUE FOR SHARE", actor.ActorID).Scan(&auth)
	if user.Error != nil {
		return domain.NewDomainError(domain.ErrPersistenceFailure, "admin authorization revalidation failed")
	}
	if user.RowsAffected != 1 {
		return domain.NewDomainError(domain.ErrOperationForbidden, "admin access required")
	}
	if !auth.IsAdmin {
		return domain.NewDomainError(domain.ErrOperationForbidden, "admin access required")
	}
	clientText := strings.TrimPrefix(actor.Scope.TenantKey, "client:")
	clientID, parseErr := strconv.ParseUint(clientText, 10, 64)
	if parseErr != nil || clientID == 0 || uint64(uint(clientID)) != clientID {
		return domain.NewDomainError(domain.ErrTenantScopeDenied, "authoritative Client scope is required")
	}
	// Relation and Shop locks must be acquired in deterministic order. This is
	// especially important when concurrent cross-Shop Relocate requests overlap.
	for _, shopID := range sortedUniqueUintIDs(actor.Scope.ShopIDs) {
		var relation struct {
			ClientID uint
		}
		query := tx.Raw(`SELECT s.client_id
			FROM user_shop_relations r
			JOIN shops s ON s.id = r.shop_id
			WHERE r.user_id = ? AND r.shop_id = ? AND s.client_id = ? AND s.is_active = TRUE
			FOR SHARE OF r, s`, actor.ActorID, shopID, uint(clientID)).Scan(&relation)
		if query.Error != nil {
			return domain.NewDomainError(domain.ErrPersistenceFailure, "admin scope revalidation failed")
		}
		if query.RowsAffected != 1 {
			return domain.NewDomainError(domain.ErrSiteScopeDenied, "actor has no authoritative UserShopRelation for Shop")
		}
	}
	deviceIDs := append([]uint(nil), actor.Scope.DeviceIDs...)
	sort.Slice(deviceIDs, func(i, j int) bool { return deviceIDs[i] < deviceIDs[j] })
	for i := 1; i < len(deviceIDs); i++ {
		if deviceIDs[i] == deviceIDs[i-1] {
			deviceIDs = append(deviceIDs[:i], deviceIDs[i+1:]...)
			i--
		}
	}
	for _, deviceID := range deviceIDs {
		var device struct {
			InventoryOwnerClientID sql.NullInt64
		}
		query := tx.Raw("SELECT inventory_owner_client_id FROM devices WHERE id = ? FOR SHARE", deviceID).Scan(&device)
		if query.Error != nil {
			return domain.NewDomainError(domain.ErrPersistenceFailure, "inventory authority revalidation failed")
		}
		if query.RowsAffected != 1 {
			return domain.NewDomainError(domain.ErrDeviceNotFound, "device was not found")
		}
		// HTTP authorization requires a resolved, positive inventory owner. A
		// NULL owner is not an implicit legacy owner and must fail closed.
		if !device.InventoryOwnerClientID.Valid || device.InventoryOwnerClientID.Int64 <= 0 || uint64(device.InventoryOwnerClientID.Int64) != clientID {
			return domain.NewDomainError(domain.ErrDeviceScopeDenied, "Device inventory owner is unavailable")
		}
	}
	return nil
}

func revalidateAdminSession(tx *gorm.DB, actor domain.ActorContext) error {
	// HTTP actors carry the authenticated session UUID. Locking this row in
	// the same transaction as replay/mutation closes the logout race and makes
	// a revoked or expired session unable to replay an old operation.
	if actor.SessionID == uuid.Nil {
		return nil
	}
	var session struct {
		UserID uint
	}
	// Materialize the locked row before evaluating expiration. This makes the
	// volatile clock_timestamp() check run after any wait to acquire FOR UPDATE,
	// rather than using a timestamp evaluated against a stale pre-wait row.
	query := tx.Raw(`WITH locked_session AS MATERIALIZED (
		SELECT user_id, expires_at
		FROM refresh_sessions
		WHERE id = ? AND user_id = ? AND revoked_at IS NULL
		FOR UPDATE
	)
	SELECT user_id FROM locked_session
	WHERE expires_at > clock_timestamp()`, actor.SessionID, actor.ActorID).Scan(&session)
	if query.Error != nil {
		return domain.NewDomainError(domain.ErrPersistenceFailure, "session revalidation failed")
	}
	if query.RowsAffected != 1 {
		return domain.NewDomainError(domain.ErrAuthenticationRequired, "authenticated session is no longer valid")
	}
	return nil
}

func (e *Executor) executeClaimed(ctx context.Context, tx *gorm.DB, action domain.BindingAction, key string, actor domain.ActorContext, hash []byte, resolveClient operationClientResolver, work func(*gorm.DB, domain.AdminBindingOperation) (domain.AdminBindingResult, error)) (domain.AdminBindingResult, error) {
	ctx = normalizeExecutionContext(ctx)
	if tx == nil {
		return domain.AdminBindingResult{}, domain.NewDomainError(domain.ErrPersistenceFailure, "caller-owned transaction is required")
	}
	tx = tx.WithContext(ctx)
	// Admission is the first application/database operation in the caller-owned
	// business transaction. Idempotency claim/replay and all planning queries
	// must occur only after the shared fence is held.
	if err := migrations.AcquireSharedWriterFenceOnGORM(ctx, tx); err != nil {
		return domain.AdminBindingResult{}, domain.NewDomainError(domain.ErrPersistenceFailure, "shared writer admission failed")
	}
	// HTTP admin actors use a server-derived scope key. Revalidate live admin
	// status and every relation in this transaction before replay lookup; a
	// revoked relation must never receive a previously committed response.
	if strings.HasPrefix(actor.ScopeKey, "admin-binding:") {
		if err := revalidateAdminSession(tx, actor); err != nil {
			return domain.AdminBindingResult{}, err
		}
		if err := revalidateAdminActor(tx, actor); err != nil {
			return domain.AdminBindingResult{}, err
		}
	}
	// Replay lookup is the only read before authority planning. New operations
	// carry authoritative Client provenance at INSERT time, so the same writer
	// remains compatible with the future v6 NOT NULL column.
	existing, lookupErr := persistence.LoadAdminBindingOperation(tx, actor.ActorID, actor.ScopeKey, string(action), key)
	if lookupErr == nil {
		if !bytes.Equal(existing.CanonicalRequestHash, hash) {
			return domain.AdminBindingResult{}, domainError(persistence.ErrIdempotencyKeyReused)
		}
		if existing.CommittedAt == nil || len(existing.CommittedResponse) == 0 {
			return domain.AdminBindingResult{}, domain.NewDomainError(domain.ErrConcurrentTransition, "idempotency operation is not committed")
		}
		var replay domain.AdminBindingResult
		if err := json.Unmarshal(existing.CommittedResponse, &replay); err != nil {
			return domain.AdminBindingResult{}, domain.NewDomainError(domain.ErrPersistenceFailure, "committed operation result is invalid")
		}
		if replay.OperationID != existing.OperationID {
			return domain.AdminBindingResult{}, domain.NewDomainError(domain.ErrPersistenceFailure, "committed operation result identity is invalid")
		}
		return replay, nil
	}
	if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
		return domain.AdminBindingResult{}, domainError(lookupErr)
	}
	if resolveClient == nil {
		return domain.AdminBindingResult{}, domain.NewDomainError(domain.ErrPersistenceFailure, "authoritative operation Client resolver is required")
	}
	clientID, err := resolveClient(tx)
	if err != nil || clientID == 0 {
		if err == nil {
			err = errors.New("authoritative operation Client provenance is required")
		}
		return domain.AdminBindingResult{}, domainError(err)
	}
	snapshot, err := json.Marshal(actor.Scope)
	if err != nil {
		return domain.AdminBindingResult{}, err
	}
	operation := domain.AdminBindingOperation{
		ID:                   uuid.New(),
		OperationID:          uuid.New(),
		IdempotencyKey:       key,
		Operation:            string(action),
		ScopeKey:             actor.ScopeKey,
		ActorID:              actor.ActorID,
		ScopeSnapshot:        snapshot,
		CanonicalRequestHash: append([]byte(nil), hash...),
		ClientID:             &clientID,
	}
	existing, claimed, err := persistence.ClaimAdminBindingOperation(tx, &operation)
	if err != nil {
		return domain.AdminBindingResult{}, domainError(err)
	}
	if !claimed {
		if existing.CommittedAt == nil || len(existing.CommittedResponse) == 0 {
			return domain.AdminBindingResult{}, domain.NewDomainError(domain.ErrConcurrentTransition, "idempotency operation is not committed")
		}
		var replay domain.AdminBindingResult
		if err := json.Unmarshal(existing.CommittedResponse, &replay); err != nil {
			return domain.AdminBindingResult{}, domain.NewDomainError(domain.ErrPersistenceFailure, "committed operation result is invalid")
		}
		if replay.OperationID != existing.OperationID {
			return domain.AdminBindingResult{}, domain.NewDomainError(domain.ErrPersistenceFailure, "committed operation result identity is invalid")
		}
		return replay, nil
	}
	if e.hooks.AfterOperationClaim != nil {
		if err := e.hooks.AfterOperationClaim(); err != nil {
			return domain.AdminBindingResult{}, err
		}
	}
	result, err := work(tx, operation)
	if err != nil {
		return domain.AdminBindingResult{}, err
	}
	return result, nil
}

func (e *Executor) createMeasurementPointInTransaction(ctx context.Context, tx *gorm.DB, cmd domain.CreateMeasurementPointCommand, operation domain.AdminBindingOperation) (domain.AdminBindingResult, error) {
	planner := New(persistence.NewTransactionLookup(tx))
	plan, err := planner.CreateMeasurementPoint(ctx, cmd)
	if err != nil {
		return domain.AdminBindingResult{}, err
	}
	if err := e.setOperationClient(tx, &operation, plan.Audit.ClientID); err != nil {
		return domain.AdminBindingResult{}, err
	}
	point := domain.MeasurementPoint{ID: plan.MeasurementPointID, ShopID: plan.ShopID, Name: plan.Name}
	if err := tx.Create(&point).Error; err != nil {
		return domain.AdminBindingResult{}, err
	}
	if err := e.afterMutation(); err != nil {
		return domain.AdminBindingResult{}, err
	}
	intent := plan.Audit
	intent.MeasurementPointID = uuidPtr(point.ID)
	if err := e.persistAudit(tx, operation, intent, nil); err != nil {
		return domain.AdminBindingResult{}, err
	}
	if err := e.afterAudit(); err != nil {
		return domain.AdminBindingResult{}, err
	}
	result := domain.AdminBindingResult{OperationID: operation.OperationID, Action: domain.ActionCreateMeasurementPoint, MeasurementPointID: uuidPtr(point.ID)}
	if err := e.persistResult(tx, operation, result); err != nil {
		return domain.AdminBindingResult{}, err
	}
	return result, e.afterResult()
}

func (e *Executor) bindInTransaction(ctx context.Context, tx *gorm.DB, cmd domain.BindDeviceCommand, operation domain.AdminBindingOperation) (domain.AdminBindingResult, error) {
	planner := New(persistence.NewTransactionLookup(tx))
	initial, err := planner.BindDevice(ctx, cmd)
	if err != nil {
		return domain.AdminBindingResult{}, err
	}
	if err := e.lockDevices(tx, []uint{initial.DeviceID}); err != nil {
		return domain.AdminBindingResult{}, err
	}
	if err := persistence.LockMeasurementPointsForUpdate(tx, []uuid.UUID{*initial.TargetMeasurementPointID}); err != nil {
		return domain.AdminBindingResult{}, err
	}
	if err := e.afterLocks(); err != nil {
		return domain.AdminBindingResult{}, err
	}
	plan, err := planner.BindDevice(ctx, cmd)
	if err != nil {
		return domain.AdminBindingResult{}, err
	}
	if err := e.setOperationClient(tx, &operation, plan.Audit.ClientID); err != nil {
		return domain.AdminBindingResult{}, err
	}
	t, err := persistence.DatabaseTransitionTime(tx)
	if err != nil {
		return domain.AdminBindingResult{}, err
	}
	assignment := domain.DeviceAssignment{ID: uuid.New(), DeviceID: plan.DeviceID, MeasurementPointID: *plan.TargetMeasurementPointID, ValidFrom: t}
	if err := tx.Create(&assignment).Error; err != nil {
		return domain.AdminBindingResult{}, err
	}
	if err := e.afterMutation(); err != nil {
		return domain.AdminBindingResult{}, err
	}
	intent := plan.Audit
	intent.NewAssignmentID = uuidPtr(assignment.ID)
	if err := e.persistAudit(tx, operation, intent, &t); err != nil {
		return domain.AdminBindingResult{}, err
	}
	if err := e.afterAudit(); err != nil {
		return domain.AdminBindingResult{}, err
	}
	result := domain.AdminBindingResult{OperationID: operation.OperationID, Action: domain.ActionBind, DeviceID: uintPtr(plan.DeviceID), NewMeasurementPointID: uuidPtr(*plan.TargetMeasurementPointID), NewAssignmentID: uuidPtr(assignment.ID), EffectiveAt: timePtr(t)}
	if err := e.persistResult(tx, operation, result); err != nil {
		return domain.AdminBindingResult{}, err
	}
	return result, e.afterResult()
}

func (e *Executor) replaceInTransaction(ctx context.Context, tx *gorm.DB, cmd domain.ReplaceDeviceCommand, operation domain.AdminBindingOperation) (domain.AdminBindingResult, error) {
	planner := New(persistence.NewTransactionLookup(tx))
	initial, err := planner.ReplaceDevice(ctx, cmd)
	if err != nil {
		return domain.AdminBindingResult{}, err
	}
	if err := e.lockDevices(tx, []uint{initial.DeviceID, *initial.ReplacementDeviceID}); err != nil {
		return domain.AdminBindingResult{}, err
	}
	if err := persistence.LockMeasurementPointsForUpdate(tx, []uuid.UUID{*initial.TargetMeasurementPointID}); err != nil {
		return domain.AdminBindingResult{}, err
	}
	if err := persistence.LockAssignmentsForUpdate(tx, []uuid.UUID{*initial.CurrentAssignmentID}); err != nil {
		return domain.AdminBindingResult{}, err
	}
	if err := e.afterLocks(); err != nil {
		return domain.AdminBindingResult{}, err
	}
	plan, err := planner.ReplaceDevice(ctx, cmd)
	if err != nil {
		return domain.AdminBindingResult{}, err
	}
	if err := e.setOperationClient(tx, &operation, plan.Audit.ClientID); err != nil {
		return domain.AdminBindingResult{}, err
	}
	start := assignmentStart(tx, *plan.CurrentAssignmentID)
	t, err := persistence.DatabaseTransitionTime(tx)
	if err != nil {
		return domain.AdminBindingResult{}, err
	}
	if start.IsZero() || !t.After(start) {
		return domain.AdminBindingResult{}, domain.NewDomainError(domain.ErrInvalidEffectiveTime, "database transition time does not advance the current assignment")
	}
	conflict, err := persistence.HasCommittedTelemetryConflict(tx, *plan.CurrentAssignmentID, t)
	if err != nil {
		return domain.AdminBindingResult{}, err
	}
	if conflict {
		return domain.AdminBindingResult{}, domain.NewDomainError(domain.ErrAssignmentTimeConflict, "committed telemetry is at or after the transition boundary")
	}
	if err := closeAssignment(tx, *plan.CurrentAssignmentID, t); err != nil {
		return domain.AdminBindingResult{}, err
	}
	newAssignment := domain.DeviceAssignment{ID: uuid.New(), DeviceID: *plan.ReplacementDeviceID, MeasurementPointID: *plan.TargetMeasurementPointID, ValidFrom: t}
	if err := tx.Create(&newAssignment).Error; err != nil {
		return domain.AdminBindingResult{}, err
	}
	if err := e.afterMutation(); err != nil {
		return domain.AdminBindingResult{}, err
	}
	intent := plan.Audit
	intent.NewAssignmentID = uuidPtr(newAssignment.ID)
	if err := e.persistAudit(tx, operation, intent, &t); err != nil {
		return domain.AdminBindingResult{}, err
	}
	if err := e.afterAudit(); err != nil {
		return domain.AdminBindingResult{}, err
	}
	result := domain.AdminBindingResult{OperationID: operation.OperationID, Action: domain.ActionReplace, DeviceID: uintPtr(plan.DeviceID), ReplacementDeviceID: uintPtr(*plan.ReplacementDeviceID), OldMeasurementPointID: uuidPtr(*plan.SourceMeasurementPointID), NewMeasurementPointID: uuidPtr(*plan.TargetMeasurementPointID), OldAssignmentID: uuidPtr(*plan.CurrentAssignmentID), NewAssignmentID: uuidPtr(newAssignment.ID), EffectiveAt: timePtr(t)}
	if err := e.persistResult(tx, operation, result); err != nil {
		return domain.AdminBindingResult{}, err
	}
	return result, e.afterResult()
}

func (e *Executor) relocateInTransaction(ctx context.Context, tx *gorm.DB, cmd domain.RelocateDeviceCommand, operation domain.AdminBindingOperation) (domain.AdminBindingResult, error) {
	planner := New(persistence.NewTransactionLookup(tx))
	initial, err := planner.RelocateDevice(ctx, cmd)
	if err != nil {
		return domain.AdminBindingResult{}, err
	}
	if err := e.lockDevices(tx, []uint{initial.DeviceID}); err != nil {
		return domain.AdminBindingResult{}, err
	}
	if err := persistence.LockMeasurementPointsForUpdate(tx, []uuid.UUID{*initial.SourceMeasurementPointID, *initial.TargetMeasurementPointID}); err != nil {
		return domain.AdminBindingResult{}, err
	}
	if err := persistence.LockAssignmentsForUpdate(tx, []uuid.UUID{*initial.CurrentAssignmentID}); err != nil {
		return domain.AdminBindingResult{}, err
	}
	if err := e.afterLocks(); err != nil {
		return domain.AdminBindingResult{}, err
	}
	plan, err := planner.RelocateDevice(ctx, cmd)
	if err != nil {
		return domain.AdminBindingResult{}, err
	}
	if err := e.setOperationClient(tx, &operation, plan.Audit.ClientID); err != nil {
		return domain.AdminBindingResult{}, err
	}
	start := assignmentStart(tx, *plan.CurrentAssignmentID)
	t, err := persistence.DatabaseTransitionTime(tx)
	if err != nil {
		return domain.AdminBindingResult{}, err
	}
	if start.IsZero() || !t.After(start) {
		return domain.AdminBindingResult{}, domain.NewDomainError(domain.ErrInvalidEffectiveTime, "database transition time does not advance the current assignment")
	}
	conflict, err := persistence.HasCommittedTelemetryConflict(tx, *plan.CurrentAssignmentID, t)
	if err != nil {
		return domain.AdminBindingResult{}, err
	}
	if conflict {
		return domain.AdminBindingResult{}, domain.NewDomainError(domain.ErrAssignmentTimeConflict, "committed telemetry is at or after the transition boundary")
	}
	if err := closeAssignment(tx, *plan.CurrentAssignmentID, t); err != nil {
		return domain.AdminBindingResult{}, err
	}
	newAssignment := domain.DeviceAssignment{ID: uuid.New(), DeviceID: plan.DeviceID, MeasurementPointID: *plan.TargetMeasurementPointID, ValidFrom: t}
	if err := tx.Create(&newAssignment).Error; err != nil {
		return domain.AdminBindingResult{}, err
	}
	if err := e.afterMutation(); err != nil {
		return domain.AdminBindingResult{}, err
	}
	intent := plan.Audit
	intent.NewAssignmentID = uuidPtr(newAssignment.ID)
	if err := e.persistAudit(tx, operation, intent, &t); err != nil {
		return domain.AdminBindingResult{}, err
	}
	if err := e.afterAudit(); err != nil {
		return domain.AdminBindingResult{}, err
	}
	result := domain.AdminBindingResult{OperationID: operation.OperationID, Action: domain.ActionRelocate, DeviceID: uintPtr(plan.DeviceID), OldMeasurementPointID: uuidPtr(*plan.SourceMeasurementPointID), NewMeasurementPointID: uuidPtr(*plan.TargetMeasurementPointID), OldAssignmentID: uuidPtr(*plan.CurrentAssignmentID), NewAssignmentID: uuidPtr(newAssignment.ID), EffectiveAt: timePtr(t)}
	if err := e.persistResult(tx, operation, result); err != nil {
		return domain.AdminBindingResult{}, err
	}
	return result, e.afterResult()
}

func (e *Executor) unbindInTransaction(ctx context.Context, tx *gorm.DB, cmd domain.UnbindDeviceCommand, operation domain.AdminBindingOperation) (domain.AdminBindingResult, error) {
	planner := New(persistence.NewTransactionLookup(tx))
	initial, err := planner.UnbindDevice(ctx, cmd)
	if err != nil {
		return domain.AdminBindingResult{}, err
	}
	if err := e.lockDevices(tx, []uint{initial.DeviceID}); err != nil {
		return domain.AdminBindingResult{}, err
	}
	if err := persistence.LockMeasurementPointsForUpdate(tx, []uuid.UUID{*initial.SourceMeasurementPointID}); err != nil {
		return domain.AdminBindingResult{}, err
	}
	if err := persistence.LockAssignmentsForUpdate(tx, []uuid.UUID{*initial.CurrentAssignmentID}); err != nil {
		return domain.AdminBindingResult{}, err
	}
	if err := e.afterLocks(); err != nil {
		return domain.AdminBindingResult{}, err
	}
	plan, err := planner.UnbindDevice(ctx, cmd)
	if err != nil {
		return domain.AdminBindingResult{}, err
	}
	if err := e.setOperationClient(tx, &operation, plan.Audit.ClientID); err != nil {
		return domain.AdminBindingResult{}, err
	}
	start := assignmentStart(tx, *plan.CurrentAssignmentID)
	t, err := persistence.DatabaseTransitionTime(tx)
	if err != nil {
		return domain.AdminBindingResult{}, err
	}
	if start.IsZero() || !t.After(start) {
		return domain.AdminBindingResult{}, domain.NewDomainError(domain.ErrInvalidEffectiveTime, "database transition time does not advance the current assignment")
	}
	conflict, err := persistence.HasCommittedTelemetryConflict(tx, *plan.CurrentAssignmentID, t)
	if err != nil {
		return domain.AdminBindingResult{}, err
	}
	if conflict {
		return domain.AdminBindingResult{}, domain.NewDomainError(domain.ErrAssignmentTimeConflict, "committed telemetry is at or after the transition boundary")
	}
	if err := closeAssignment(tx, *plan.CurrentAssignmentID, t); err != nil {
		return domain.AdminBindingResult{}, err
	}
	if err := e.afterMutation(); err != nil {
		return domain.AdminBindingResult{}, err
	}
	if err := e.persistAudit(tx, operation, plan.Audit, &t); err != nil {
		return domain.AdminBindingResult{}, err
	}
	if err := e.afterAudit(); err != nil {
		return domain.AdminBindingResult{}, err
	}
	result := domain.AdminBindingResult{OperationID: operation.OperationID, Action: domain.ActionUnbind, DeviceID: uintPtr(plan.DeviceID), OldMeasurementPointID: uuidPtr(*plan.SourceMeasurementPointID), OldAssignmentID: uuidPtr(*plan.CurrentAssignmentID), EffectiveAt: timePtr(t)}
	if err := e.persistResult(tx, operation, result); err != nil {
		return domain.AdminBindingResult{}, err
	}
	return result, e.afterResult()
}

func (e *Executor) persistAudit(tx *gorm.DB, operation domain.AdminBindingOperation, intent domain.AuditIntent, effectiveAt *time.Time) error {
	if intent.ClientID == nil || *intent.ClientID == 0 {
		return domain.NewDomainError(domain.ErrTenantScopeDenied, "authoritative operation Client is unavailable")
	}
	if operation.ClientID == nil || *operation.ClientID != *intent.ClientID {
		return domain.NewDomainError(domain.ErrTenantScopeDenied, "operation and audit Client provenance differ")
	}
	audit := domain.AdminBindingAudit{OperationID: operation.OperationID, RequestIdentity: intent.RequestIdentity, ActorID: intent.ActorID, ScopeKey: intent.ScopeKey, ScopeSnapshot: mustScopeJSON(intent.ScopeSnapshot), ClientID: cloneUintPtr(intent.ClientID), Action: string(intent.Action), EffectiveAt: effectiveAt, ShopID: cloneUintPtr(intent.ShopID), MeasurementPointID: cloneUUID(intent.MeasurementPointID), DeviceID: cloneUintPtr(intent.DeviceID), DeviceSerialNumber: stringPtr(intent.DeviceSerialNumber), DeviceMAC: stringPtr(intent.DeviceMAC), OldMeasurementPointID: cloneUUID(intent.OldMeasurementPointID), NewMeasurementPointID: cloneUUID(intent.NewMeasurementPointID), OldAssignmentID: cloneUUID(intent.OldAssignmentID), NewAssignmentID: cloneUUID(intent.NewAssignmentID), Reason: intent.Reason}
	if audit.DeviceSerialNumber != nil && *audit.DeviceSerialNumber == "" {
		audit.DeviceSerialNumber = nil
	}
	if audit.DeviceMAC != nil && *audit.DeviceMAC == "" {
		audit.DeviceMAC = nil
	}
	return persistence.AppendAdminBindingAudit(tx, &audit)
}

func (e *Executor) setOperationClient(tx *gorm.DB, operation *domain.AdminBindingOperation, clientID *uint) error {
	if operation == nil || clientID == nil || *clientID == 0 {
		return domain.NewDomainError(domain.ErrTenantScopeDenied, "authoritative operation Client is unavailable")
	}
	if err := persistence.SetAdminBindingOperationClientID(tx, operation.OperationID, *clientID); err != nil {
		if errors.Is(err, persistence.ErrOperationClientConflict) {
			return domain.NewDomainError(domain.ErrTenantScopeDenied, "operation Client provenance could not be established")
		}
		return domainError(err)
	}
	operation.ClientID = cloneUintPtr(clientID)
	return nil
}

func (e *Executor) persistResult(tx *gorm.DB, operation domain.AdminBindingOperation, result domain.AdminBindingResult) error {
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return persistence.CommitAdminBindingOperation(tx, operation.OperationID, body, time.Now().UTC())
}

func (e *Executor) lockDevices(tx *gorm.DB, ids []uint) error {
	if e.hooks.BeforeDeviceLock != nil {
		if err := e.hooks.BeforeDeviceLock(tx); err != nil {
			return err
		}
	}
	return persistence.LockDevicesForUpdate(tx, ids)
}

func (e *Executor) afterLocks() error {
	if e.hooks.AfterLocks != nil {
		return e.hooks.AfterLocks()
	}
	return nil
}
func (e *Executor) afterMutation() error {
	if e.hooks.AfterMutation != nil {
		return e.hooks.AfterMutation()
	}
	return nil
}
func (e *Executor) afterAudit() error {
	if e.hooks.AfterAudit != nil {
		return e.hooks.AfterAudit()
	}
	return nil
}
func (e *Executor) afterResult() error {
	if e.hooks.AfterResult != nil {
		return e.hooks.AfterResult()
	}
	return nil
}

func closeAssignment(tx *gorm.DB, id uuid.UUID, effectiveAt time.Time) error {
	result := tx.Model(&domain.DeviceAssignment{}).Where("id = ? AND valid_to IS NULL", id).Update("valid_to", effectiveAt)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return domain.NewDomainError(domain.ErrConcurrentTransition, "current assignment changed while it was being closed")
	}
	return nil
}

// assignmentStart reads the already-locked assignment before the single clock
// sample. A zero value is treated as invalid by the caller.
func assignmentStart(tx *gorm.DB, id uuid.UUID) time.Time {
	var assignment domain.DeviceAssignment
	if tx.Select("valid_from").First(&assignment, "id = ?", id).Error != nil {
		return time.Time{}
	}
	return assignment.ValidFrom
}

func canonicalHash(action domain.BindingAction, value interface{}) ([]byte, error) {
	body, err := json.Marshal(struct {
		Version string               `json:"version"`
		Action  domain.BindingAction `json:"action"`
		Input   interface{}          `json:"input"`
	}{"admin-binding/v1", action, value})
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	return sum[:], nil
}

type canonicalDeviceRef struct {
	DeviceID     *uint   `json:"device_id"`
	SerialNumber *string `json:"serial_number"`
	MAC          *string `json:"mac"`
}

func canonicalRef(ref domain.DeviceRef) canonicalDeviceRef {
	return canonicalDeviceRef{ref.DeviceID, ref.SerialNumber, ref.MAC}
}
func canonicalCreateHash(cmd domain.CreateMeasurementPointCommand) ([]byte, error) {
	return canonicalHash(domain.ActionCreateMeasurementPoint, struct {
		ShopID uint   `json:"shop_id"`
		Name   string `json:"name"`
	}{cmd.ShopID, cmd.Name})
}
func canonicalBindHash(cmd domain.BindDeviceCommand) ([]byte, error) {
	return canonicalHash(domain.ActionBind, struct {
		DeviceRef          canonicalDeviceRef `json:"device_ref"`
		MeasurementPointID uuid.UUID          `json:"measurement_point_id"`
		Reason             string             `json:"reason"`
	}{canonicalRef(cmd.DeviceRef), cmd.MeasurementPointID, cmd.Reason})
}
func canonicalReplaceHash(cmd domain.ReplaceDeviceCommand) ([]byte, error) {
	return canonicalHash(domain.ActionReplace, struct {
		CurrentAssignmentID  uuid.UUID          `json:"current_assignment_id"`
		ReplacementDeviceRef canonicalDeviceRef `json:"replacement_device_ref"`
		Reason               string             `json:"reason"`
	}{cmd.CurrentAssignmentID, canonicalRef(cmd.ReplacementDeviceRef), cmd.Reason})
}
func canonicalRelocateHash(cmd domain.RelocateDeviceCommand) ([]byte, error) {
	return canonicalHash(domain.ActionRelocate, struct {
		CurrentAssignmentID      uuid.UUID `json:"current_assignment_id"`
		TargetMeasurementPointID uuid.UUID `json:"target_measurement_point_id"`
		Reason                   string    `json:"reason"`
	}{cmd.CurrentAssignmentID, cmd.TargetMeasurementPointID, cmd.Reason})
}
func canonicalUnbindHash(cmd domain.UnbindDeviceCommand) ([]byte, error) {
	return canonicalHash(domain.ActionUnbind, struct {
		CurrentAssignmentID uuid.UUID `json:"current_assignment_id"`
		Reason              string    `json:"reason"`
	}{cmd.CurrentAssignmentID, cmd.Reason})
}

func mustScopeJSON(scope domain.ScopeSnapshot) json.RawMessage {
	value, _ := json.Marshal(scope)
	return value
}
func timePtr(value time.Time) *time.Time { return &value }
func cloneUintPtr(value *uint) *uint {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
func stringPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func domainError(err error) error {
	if err == nil {
		return nil
	}
	if domain.CodeOf(err) != "" {
		return err
	}
	if errors.Is(err, persistence.ErrIdempotencyKeyReused) {
		return domain.NewDomainError(domain.ErrIdempotencyKeyReused, "idempotency key was used for a different request")
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.NewDomainError(domain.ErrConcurrentTransition, "a locked binding resource no longer exists")
	}
	if pgError, ok := postgresError(err); ok {
		switch pgError.ConstraintName {
		case "device_assignments_device_no_overlap", "device_assignments_measurement_point_no_overlap":
			return domain.NewDomainError(domain.ErrOverlappingAssignment, "assignment interval overlaps an existing interval")
		}
	}
	return &domain.DomainError{Code: domain.ErrPersistenceFailure, Message: "admin binding transaction failed", Cause: err}
}

func postgresError(err error) (*pgconn.PgError, bool) {
	var value *pgconn.PgError
	if errors.As(err, &value) {
		return value, true
	}
	return nil, false
}
func isTransientPostgresError(err error) bool {
	value, ok := postgresError(err)
	return ok && (value.Code == "40P01" || value.Code == "40001")
}
