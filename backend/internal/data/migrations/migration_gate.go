package migrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4/database/postgres"
)

const protectedSchemaVersion = 5

var (
	ErrMigrationGateDirty     = errors.New("migration admission refused: dirty metadata")
	ErrMigrationGateAmbiguous = errors.New("migration admission refused: ambiguous metadata")
	ErrMigrationGateFuture    = errors.New("migration admission refused: unsupported future schema")
	ErrGuardedV6Down          = errors.New("migration DOWN refused: clean v6 downgrade is protected")
)

type migrationGateAction string

const (
	migrationGateUp   migrationGateAction = "up"
	migrationGateDown migrationGateAction = "down"
)

// MigrationMetadataSnapshot is a non-authorizing observation of the configured
// migration metadata relation. Runtime admission uses it only as one input to
// the public schema contract; protected migration authority is separate.
type MigrationMetadataQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type MigrationMetadataSnapshot struct {
	Exists         bool
	CatalogEmpty   bool
	CatalogVersion int
	RowCount       int
	Version        int
	Dirty          bool
	HasVersion     bool
}

func (s MigrationMetadataSnapshot) String() string {
	if !s.Exists {
		return fmt.Sprintf("metadata=absent catalog_empty=%t catalog_version=%d", s.CatalogEmpty, s.CatalogVersion)
	}
	if !s.HasVersion {
		return fmt.Sprintf("metadata=present rows=%d", s.RowCount)
	}
	return fmt.Sprintf("metadata=present rows=%d version=%d dirty=%t catalog_version=%d", s.RowCount, s.Version, s.Dirty, s.CatalogVersion)
}

// MigrationGateError preserves a stable errors.Is category while keeping
// operator diagnostics precise and credential-free.
type MigrationGateError struct {
	Reason   error
	Action   migrationGateAction
	Snapshot MigrationMetadataSnapshot
}

func (e *MigrationGateError) Error() string {
	if e == nil {
		return "migration admission failed"
	}
	return fmt.Sprintf("%s (%s, %s)", e.Reason, e.Action, e.Snapshot)
}

func (e *MigrationGateError) Unwrap() error { return e.Reason }

func migrationGateError(reason error, action migrationGateAction, snapshot MigrationMetadataSnapshot) error {
	return &MigrationGateError{Reason: reason, Action: action, Snapshot: snapshot}
}

// inspectMigrationMetadata reads the existing configured relation without
// initializing the migration driver. It deliberately counts every row; the
// driver's informational Version method is not an admission authority.
func inspectMigrationMetadata(ctx context.Context, conn *sql.Conn, config *postgres.Config) (MigrationMetadataSnapshot, error) {
	return inspectMigrationMetadataOn(ctx, conn, config)
}

// InspectMigrationMetadata exposes the read-only metadata observation to the
// private migration authority without exposing that authority to runtime
// packages.
func InspectMigrationMetadata(ctx context.Context, q MigrationMetadataQueryer, config *postgres.Config) (MigrationMetadataSnapshot, error) {
	return inspectMigrationMetadataOn(ctx, q, config)
}

// inspectMigrationMetadataOn is the transaction/pinned-session adapter for the
// canonical metadata inspection logic. It intentionally performs no driver
// initialization, so the same observation is valid before and inside the
// protected transaction.
func inspectMigrationMetadataOn(ctx context.Context, q MigrationMetadataQueryer, config *postgres.Config) (MigrationMetadataSnapshot, error) {
	if q == nil {
		return MigrationMetadataSnapshot{}, errors.New("migration metadata inspection requires a queryer")
	}
	if config == nil {
		return MigrationMetadataSnapshot{}, errors.New("migration metadata configuration is required")
	}
	var currentSchema string
	if err := q.QueryRowContext(ctx, "SELECT current_schema()").Scan(&currentSchema); err != nil {
		return MigrationMetadataSnapshot{}, fmt.Errorf("resolve migration metadata schema: %w", err)
	}
	schemaName, tableName, err := migrationMetadataIdentifiers(config, currentSchema)
	if err != nil {
		return MigrationMetadataSnapshot{}, err
	}
	qualifiedTable := quotedMigrationTable(schemaName, tableName)
	var relation sql.NullString
	if err := q.QueryRowContext(ctx, "SELECT to_regclass($1)", qualifiedTable).Scan(&relation); err != nil {
		return MigrationMetadataSnapshot{}, fmt.Errorf("inspect migration metadata relation: %w", err)
	}
	catalogVersion, err := migrationCatalogVersion(ctx, q, currentSchema, schemaName, tableName)
	if err != nil {
		return MigrationMetadataSnapshot{}, err
	}
	if !relation.Valid || relation.String == "" {
		return MigrationMetadataSnapshot{CatalogEmpty: catalogVersion == 0, CatalogVersion: catalogVersion}, nil
	}

	rows, err := q.QueryContext(ctx, "SELECT version, dirty FROM "+qualifiedTable+" ORDER BY version")
	if err != nil {
		return MigrationMetadataSnapshot{Exists: true, CatalogEmpty: catalogVersion == 0, CatalogVersion: catalogVersion}, fmt.Errorf("read migration metadata: %w", err)
	}
	defer rows.Close()
	snapshot := MigrationMetadataSnapshot{Exists: true, CatalogEmpty: catalogVersion == 0, CatalogVersion: catalogVersion}
	for rows.Next() {
		var version int
		var dirty bool
		if err := rows.Scan(&version, &dirty); err != nil {
			return snapshot, fmt.Errorf("scan migration metadata: %w", err)
		}
		snapshot.RowCount++
		if snapshot.RowCount == 1 {
			snapshot.Version = version
			snapshot.Dirty = dirty
			snapshot.HasVersion = true
		}
	}
	if err := rows.Err(); err != nil {
		return snapshot, fmt.Errorf("iterate migration metadata: %w", err)
	}
	return snapshot, nil
}

func migrationCatalogVersion(ctx context.Context, q MigrationMetadataQueryer, schemaName, metadataSchema, metadataTable string) (int, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT c.relname
		FROM pg_class AS c
		JOIN pg_namespace AS n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relkind IN ('r', 'p')
		ORDER BY c.relname`, schemaName)
	if err != nil {
		return 0, fmt.Errorf("inspect bootstrap catalog: %w", err)
	}
	defer rows.Close()
	actual := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return 0, fmt.Errorf("scan bootstrap catalog: %w", err)
		}
		if !(schemaName == metadataSchema && name == metadataTable) {
			actual[name] = true
		}
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate bootstrap catalog: %w", err)
	}
	versions := []struct {
		version int
		tables  []string
	}{
		{1, []string{"alert_logs", "clients", "daily_usages", "device_alert_settings", "device_assignments", "device_types", "devices", "measurement_points", "power_readings", "shops", "system_configs", "telemetry_ingest_keys", "user_shop_relations", "users"}},
		{4, []string{"admin_binding_audits", "admin_binding_operations", "alert_logs", "clients", "daily_usages", "device_alert_settings", "device_assignments", "device_types", "devices", "measurement_points", "power_readings", "shops", "system_configs", "telemetry_ingest_keys", "user_shop_relations", "users"}},
		{5, []string{"admin_binding_audits", "admin_binding_operations", "alert_logs", "clients", "daily_usages", "device_alert_settings", "device_assignments", "device_types", "devices", "measurement_points", "power_readings", "refresh_sessions", "refresh_tokens", "shops", "system_configs", "telemetry_ingest_keys", "user_shop_relations", "users"}},
	}
	for _, candidate := range versions {
		expected := make(map[string]bool, len(candidate.tables))
		for _, table := range candidate.tables {
			expected[table] = true
		}
		if len(actual) != len(expected) {
			continue
		}
		match := true
		for table := range actual {
			if !expected[table] {
				match = false
				break
			}
		}
		if match {
			return candidate.version, nil
		}
	}
	if len(actual) == 0 {
		return 0, nil
	}
	// Versions 2 and 3 intentionally retain the version-1 table set. The
	// metadata row is therefore the authority for distinguishing those exact
	// historical catalogs, while the table-set proof rejects partial/mixed
	// schemas before generic initialization.
	versionOneTables := map[string]bool{}
	for _, table := range versions[0].tables {
		versionOneTables[table] = true
	}
	if len(actual) == len(versionOneTables) {
		match := true
		for table := range actual {
			if !versionOneTables[table] {
				match = false
				break
			}
		}
		if match {
			return 1, nil
		}
	}
	return -1, nil
}

func classifyMigrationAdmission(snapshot MigrationMetadataSnapshot, action migrationGateAction, embeddedLatest int) error {
	// embeddedLatest may be ahead of the capped generic target. A clean V5
	// no-op remains allowed; the generic route still never targets or applies
	// the protected V5->V6 body.
	_ = embeddedLatest
	if !snapshot.Exists || snapshot.RowCount == 0 {
		if action == migrationGateUp && snapshot.CatalogVersion >= 0 && snapshot.CatalogVersion <= 4 {
			return nil // only exact empty/recognized pre-v5 catalogs may initialize metadata
		}
		return migrationGateError(ErrMigrationGateAmbiguous, action, snapshot)
	}
	if snapshot.RowCount != 1 || !snapshot.HasVersion {
		return migrationGateError(ErrMigrationGateAmbiguous, action, snapshot)
	}
	if snapshot.Dirty {
		return migrationGateError(ErrMigrationGateDirty, action, snapshot)
	}
	if snapshot.Version > 6 || snapshot.Version < 0 {
		return migrationGateError(ErrMigrationGateFuture, action, snapshot)
	}
	if snapshot.Version == 0 && snapshot.CatalogVersion != 0 {
		return migrationGateError(ErrMigrationGateAmbiguous, action, snapshot)
	}
	if action == migrationGateDown && snapshot.Version == 6 {
		return migrationGateError(ErrGuardedV6Down, action, snapshot)
	}
	if snapshot.Version > protectedSchemaVersion {
		return migrationGateError(ErrMigrationGateFuture, action, snapshot)
	}
	if snapshot.Version >= 1 && snapshot.Version <= 4 && snapshot.CatalogVersion != snapshot.Version && !(snapshot.Version == 2 || snapshot.Version == 3) {
		return migrationGateError(ErrMigrationGateAmbiguous, action, snapshot)
	}
	if snapshot.Version >= 1 && snapshot.Version <= 4 && snapshot.CatalogVersion != 1 && (snapshot.Version == 2 || snapshot.Version == 3) {
		return migrationGateError(ErrMigrationGateAmbiguous, action, snapshot)
	}
	if snapshot.Version == protectedSchemaVersion && snapshot.CatalogVersion != protectedSchemaVersion {
		return migrationGateError(ErrMigrationGateAmbiguous, action, snapshot)
	}
	return nil
}

func migrationGateActionName(action migrationGateAction) string { return string(action) }

// ConfiguredMigrationTable returns the quoted migration metadata relation for
// a DSN using an already-pinned connection. It performs no initialization.
// Private migration authorities use this observation seam without importing
// runtime or application packages.
func ConfiguredMigrationTable(ctx context.Context, databaseURL string, conn *sql.Conn) (string, error) {
	parsed, err := parsePostgresDatabaseURL(databaseURL)
	if err != nil {
		return "", err
	}
	if conn == nil {
		return "", errors.New("migration metadata table requires a pinned connection")
	}
	var currentSchema string
	if err := conn.QueryRowContext(ctx, "SELECT current_schema()").Scan(&currentSchema); err != nil {
		return "", err
	}
	schemaName, tableName, err := migrationMetadataIdentifiers(parsed.config, currentSchema)
	if err != nil {
		return "", err
	}
	return quotedMigrationTable(schemaName, tableName), nil
}
