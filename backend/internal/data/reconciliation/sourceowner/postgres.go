package sourceowner

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// ReadOnlyConnection is deliberately query-only. The source-owner collector
// cannot expose Exec/Prepare/write capabilities.
type ReadOnlyConnection interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type PostgresFactCollector struct {
	DB            *gorm.DB
	metadataTable string
}

// PostgresSourceOwner is the owner-controlled trusted collection service. Its
// public request surface contains only invocation binding; the service owns
// the database transaction, source observation time, and query-only adapter.
type PostgresSourceOwner struct {
	db            *gorm.DB
	metadataTable string
}

func NewPostgresSourceOwner(db *gorm.DB) *PostgresSourceOwner {
	return &PostgresSourceOwner{db: db, metadataTable: `"public"."schema_migrations"`}
}

func NewPostgresFactCollector(db *gorm.DB) *PostgresFactCollector {
	return &PostgresFactCollector{DB: db, metadataTable: `"public"."schema_migrations"`}
}

func NewPostgresFactCollectorWithMetadataTable(db *gorm.DB, metadataTable string) *PostgresFactCollector {
	if metadataTable == "" {
		return NewPostgresFactCollector(db)
	}
	return &PostgresFactCollector{DB: db, metadataTable: metadataTable}
}

func (c *PostgresFactCollector) CollectV5(ctx context.Context, asOf time.Time) (FactSet, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if c == nil || c.DB == nil {
		return FactSet{}, errors.New("read-only PostgreSQL database is required")
	}
	return c.collectV5(ctx, asOf, c.DB.WithContext(ctx))
}

func (c *PostgresFactCollector) collectV5(ctx context.Context, asOf time.Time, db *gorm.DB) (FactSet, error) {
	if asOf.IsZero() {
		return FactSet{}, errors.New("fact collection as_of is required")
	}
	var migrationRows int64
	if err := db.Raw("SELECT count(*) FROM " + c.metadataTable).Scan(&migrationRows).Error; err != nil {
		return FactSet{}, fmt.Errorf("count migration metadata: %w", err)
	}
	if migrationRows != 1 {
		return FactSet{}, fmt.Errorf("migration metadata cardinality must be exactly one: count=%d", migrationRows)
	}
	var version int
	var dirty bool
	row := db.Raw("SELECT version, dirty FROM " + c.metadataTable).Row()
	if err := row.Scan(&version, &dirty); err != nil {
		return FactSet{}, fmt.Errorf("verify migration metadata: %w", err)
	}
	if version != 5 || dirty {
		return FactSet{}, fmt.Errorf("migration metadata is not clean v5: version=%d dirty=%t", version, dirty)
	}
	facts := FactSet{SchemaVersion: SchemaVersion, AsOf: asOf.UTC()}
	if err := db.Raw(`SELECT id FROM clients ORDER BY id`).Scan(&facts.Clients).Error; err != nil {
		return FactSet{}, fmt.Errorf("collect clients: %w", err)
	}
	if err := db.Raw(`SELECT id, client_id FROM shops ORDER BY id`).Scan(&facts.Shops).Error; err != nil {
		return FactSet{}, fmt.Errorf("collect shops: %w", err)
	}
	if err := db.Raw(`SELECT id, current_shop_id, auth_enabled FROM users ORDER BY id`).Scan(&facts.Users).Error; err != nil {
		return FactSet{}, fmt.Errorf("collect users: %w", err)
	}
	if err := db.Raw(`SELECT id, user_id, shop_id FROM user_shop_relations ORDER BY id`).Scan(&facts.UserShopRelations).Error; err != nil {
		return FactSet{}, fmt.Errorf("collect user shop relations: %w", err)
	}
	if err := db.Raw(`SELECT id, shop_id, inventory_owner_client_id FROM devices ORDER BY id`).Scan(&facts.Devices).Error; err != nil {
		return FactSet{}, fmt.Errorf("collect devices: %w", err)
	}
	if err := db.Raw(`SELECT id, shop_id FROM measurement_points ORDER BY id`).Scan(&facts.MeasurementPoints).Error; err != nil {
		return FactSet{}, fmt.Errorf("collect measurement points: %w", err)
	}
	if err := db.Raw(`SELECT id, device_id, measurement_point_id, valid_from, valid_to FROM device_assignments ORDER BY id`).Scan(&facts.DeviceAssignments).Error; err != nil {
		return FactSet{}, fmt.Errorf("collect device assignments: %w", err)
	}
	if err := db.Raw(`SELECT id, operation_id, idempotency_key, operation, actor_id, scope_key, scope_snapshot, canonical_request_hash, client_id, committed_response, committed_at, created_at FROM admin_binding_operations ORDER BY operation_id`).Scan(&facts.AdminOperations).Error; err != nil {
		return FactSet{}, fmt.Errorf("collect admin operations: %w", err)
	}
	if err := db.Raw(`SELECT id, operation_id, request_identity, action, actor_id, scope_key, scope_snapshot, occurred_at, effective_at, client_id, shop_id, measurement_point_id, device_id, device_serial_number, device_mac, old_measurement_point_id, new_measurement_point_id, old_assignment_id, new_assignment_id, reason, metadata FROM admin_binding_audits ORDER BY id`).Scan(&facts.AdminAudits).Error; err != nil {
		return FactSet{}, fmt.Errorf("collect admin audits: %w", err)
	}
	return facts, nil
}

func (c *PostgresFactCollector) CollectV5Pinned(ctx context.Context, asOf time.Time, conn ReadOnlyConnection) (FactSet, error) {
	if ctx == nil {
		ctx = context.Background()
	}
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

// CollectTrustedV5 requests fresh source evidence for one invocation. It
// deliberately accepts no FactSet, observation time, or query connection from
// the caller.
func (o *PostgresSourceOwner) CollectTrustedV5(ctx context.Context, binding InvocationBinding) (Evidence, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if o == nil || o.db == nil {
		return Evidence{}, errors.New("trusted PostgreSQL source owner is required")
	}
	sqlDB, err := o.db.DB()
	if err != nil {
		return Evidence{}, fmt.Errorf("open source owner database: %w", err)
	}
	tx, err := sqlDB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return Evidence{}, fmt.Errorf("begin source owner transaction: %w", err)
	}
	defer tx.Rollback()
	collector := &PostgresFactCollector{DB: o.db, metadataTable: o.metadataTable}
	return collector.collectTrustedV5Pinned(ctx, tx, binding)
}

// collectTrustedV5Pinned is intentionally package-private. Only source-owner
// implementation code can turn a pinned source observation into Evidence.
// Observation time comes from the pinned source connection, never a caller.
func (c *PostgresFactCollector) collectTrustedV5Pinned(ctx context.Context, conn ReadOnlyConnection, binding InvocationBinding) (Evidence, error) {
	if conn == nil {
		return Evidence{}, errors.New("pinned read-only connection is required")
	}
	if c == nil || c.DB == nil {
		return Evidence{}, errors.New("read-only PostgreSQL database is required")
	}
	var observedAt time.Time
	if err := conn.QueryRowContext(ctx, "SELECT transaction_timestamp()").Scan(&observedAt); err != nil {
		return Evidence{}, fmt.Errorf("observe source transaction time: %w", err)
	}
	facts, err := c.CollectV5Pinned(ctx, observedAt.UTC(), conn)
	if err != nil {
		return Evidence{}, err
	}
	return newEvidence(facts, binding)
}

type queryOnlyConnPool struct{ ReadOnlyConnection }

func (p queryOnlyConnPool) PrepareContext(context.Context, string) (*sql.Stmt, error) {
	return nil, errors.New("read-only collector cannot prepare writes")
}
func (p queryOnlyConnPool) ExecContext(context.Context, string, ...interface{}) (sql.Result, error) {
	return nil, errors.New("read-only collector cannot execute writes")
}

func (c *PostgresFactCollector) Collect(ctx context.Context, asOf time.Time) (FactSet, error) {
	return c.CollectV5(ctx, asOf)
}
