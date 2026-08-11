package reconciliation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// PostgresFactCollector performs only SELECTs. It owns no transaction and has
// no method capable of INSERT/UPDATE/DELETE, making accidental reconciliation
// writes impossible through this A2.1 interface.
type PostgresFactCollector struct{ DB *gorm.DB }

func NewPostgresFactCollector(db *gorm.DB) *PostgresFactCollector {
	return &PostgresFactCollector{DB: db}
}

func (c *PostgresFactCollector) CollectV5(ctx context.Context, asOf time.Time) (FactSet, error) {
	if c == nil || c.DB == nil {
		return FactSet{}, errors.New("read-only PostgreSQL database is required")
	}
	return c.collectV5(ctx, asOf, c.DB.WithContext(ctx))
}

func (c *PostgresFactCollector) collectV5(ctx context.Context, asOf time.Time, db *gorm.DB) (FactSet, error) {
	if asOf.IsZero() {
		return FactSet{}, errors.New("fact collection as_of is required")
	}
	// A v5-shaped table is not sufficient: reconciliation fails closed unless
	// migration metadata proves exactly one clean v5 row. This is SELECT-only
	// and intentionally occurs before any source table is inspected.
	var migrationRows int64
	if err := db.Raw(`SELECT count(*) FROM public.schema_migrations`).Scan(&migrationRows).Error; err != nil {
		return FactSet{}, fmt.Errorf("count migration metadata: %w", err)
	}
	if migrationRows != 1 {
		return FactSet{}, fmt.Errorf("migration metadata cardinality must be exactly one: count=%d", migrationRows)
	}
	var version int
	var dirty bool
	row := db.Raw(`SELECT version, dirty FROM public.schema_migrations`).Row()
	if err := row.Scan(&version, &dirty); err != nil {
		return FactSet{}, fmt.Errorf("verify migration metadata: %w", err)
	}
	if version != 5 || dirty {
		return FactSet{}, fmt.Errorf("migration metadata is not clean v5: version=%d dirty=%t", version, dirty)
	}
	f := FactSet{SchemaVersion: SchemaVersion, AsOf: asOf.UTC()}
	if err := db.Raw(`SELECT id FROM clients ORDER BY id`).Scan(&f.Clients).Error; err != nil {
		return FactSet{}, fmt.Errorf("collect clients: %w", err)
	}
	if err := db.Raw(`SELECT id, client_id FROM shops ORDER BY id`).Scan(&f.Shops).Error; err != nil {
		return FactSet{}, fmt.Errorf("collect shops: %w", err)
	}
	if err := db.Raw(`SELECT id, current_shop_id, auth_enabled FROM users ORDER BY id`).Scan(&f.Users).Error; err != nil {
		return FactSet{}, fmt.Errorf("collect users: %w", err)
	}
	if err := db.Raw(`SELECT id, user_id, shop_id FROM user_shop_relations ORDER BY id`).Scan(&f.UserShopRelations).Error; err != nil {
		return FactSet{}, fmt.Errorf("collect user shop relations: %w", err)
	}
	if err := db.Raw(`SELECT id, shop_id, inventory_owner_client_id FROM devices ORDER BY id`).Scan(&f.Devices).Error; err != nil {
		return FactSet{}, fmt.Errorf("collect devices: %w", err)
	}
	if err := db.Raw(`SELECT id, shop_id FROM measurement_points ORDER BY id`).Scan(&f.MeasurementPoints).Error; err != nil {
		return FactSet{}, fmt.Errorf("collect measurement points: %w", err)
	}
	if err := db.Raw(`SELECT id, device_id, measurement_point_id, valid_from, valid_to FROM device_assignments ORDER BY id`).Scan(&f.DeviceAssignments).Error; err != nil {
		return FactSet{}, fmt.Errorf("collect device assignments: %w", err)
	}
	if err := db.Raw(`SELECT id, operation_id, idempotency_key, operation, actor_id, scope_key, scope_snapshot, canonical_request_hash, client_id, committed_response, committed_at, created_at FROM admin_binding_operations ORDER BY operation_id`).Scan(&f.AdminOperations).Error; err != nil {
		return FactSet{}, fmt.Errorf("collect admin operations: %w", err)
	}
	if err := db.Raw(`SELECT id, operation_id, request_identity, action, actor_id, scope_key, scope_snapshot, occurred_at, effective_at, client_id, shop_id, measurement_point_id, device_id, device_serial_number, device_mac, old_measurement_point_id, new_measurement_point_id, old_assignment_id, new_assignment_id, reason, metadata FROM admin_binding_audits ORDER BY id`).Scan(&f.AdminAudits).Error; err != nil {
		return FactSet{}, fmt.Errorf("collect admin audits: %w", err)
	}
	return f, nil
}

// CollectV5Pinned reads through the exact fenced connection. The adapter
// rejects write methods, while gorm still supplies its SELECT scanning.
func (c *PostgresFactCollector) CollectV5Pinned(ctx context.Context, asOf time.Time, conn ReadOnlyConnection) (FactSet, error) {
	if conn == nil {
		return FactSet{}, errors.New("pinned read-only connection is required")
	}
	if c == nil || c.DB == nil {
		return FactSet{}, errors.New("read-only PostgreSQL database is required")
	}
	pool := queryOnlyConnPool{ReadOnlyConnection: conn}
	db := c.DB.Session(&gorm.Session{NewDB: true}).WithContext(ctx)
	db.Statement.ConnPool = pool
	return c.collectV5(ctx, asOf, db)
}

type queryOnlyConnPool struct{ ReadOnlyConnection }

func (p queryOnlyConnPool) PrepareContext(context.Context, string) (*sql.Stmt, error) {
	return nil, errors.New("read-only collector cannot prepare writes")
}
func (p queryOnlyConnPool) ExecContext(context.Context, string, ...interface{}) (sql.Result, error) {
	return nil, errors.New("read-only collector cannot execute writes")
}

// Collect is an ergonomic alias retained for callers that do not need to
// mention the schema version.
func (c *PostgresFactCollector) Collect(ctx context.Context, asOf time.Time) (FactSet, error) {
	return c.CollectV5(ctx, asOf)
}

// ExclusiveReadOnlyDB is a marker used by orchestration code to make the
// required fence ordering visible at the call site.
type ExclusiveReadOnlyDB interface {
	DB() *gorm.DB
	ExclusiveReconciliationFence() bool
}

// SQLFactCollector is a function seam for sqlmock and other read-only test
// harnesses. It must return the same v5 facts as PostgresFactCollector.
type SQLFactCollector interface {
	CollectV5(context.Context, time.Time) (FactSet, error)
}
