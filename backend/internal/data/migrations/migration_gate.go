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
	ErrMigrationGateDirty              = errors.New("migration admission refused: dirty metadata")
	ErrMigrationGateAmbiguous          = errors.New("migration admission refused: ambiguous metadata")
	ErrMigrationGateFuture             = errors.New("migration admission refused: unsupported future schema")
	ErrProtectedV5Upgrade              = errors.New("migration admission refused: protected v5 upgrade requires the dedicated A3 runner")
	ErrGuardedV6Down                   = errors.New("migration DOWN refused: clean v6 downgrade is protected")
	ErrCleanV5MetadataRequired         = errors.New("security reconciliation requires clean v5 migration metadata")
	ErrExternalWriterAdmissionRequired = errors.New("protected work requires external writer drain/deny evidence")
)

type ExternalWriterAdmission struct {
	ManagedCooperativeWriters bool
	DirectSQLControlled       bool
	OperationalDrainEvidence  bool
}

// AssessExternalWriterAdmission is deliberately conservative: the repository
// can serialize cooperating Go writers, but has no application-owned control
// over direct SQL or deployment drain/deny evidence. D3/D6 must require and
// supply those external prerequisites before protected cutover.
func AssessExternalWriterAdmission() ExternalWriterAdmission {
	return ExternalWriterAdmission{ManagedCooperativeWriters: true}
}

func RequireExternalWriterAdmission(admission ExternalWriterAdmission) error {
	if !admission.ManagedCooperativeWriters || !admission.DirectSQLControlled || !admission.OperationalDrainEvidence {
		return ErrExternalWriterAdmissionRequired
	}
	return nil
}

type migrationGateAction string

const (
	migrationGateUp   migrationGateAction = "up"
	migrationGateDown migrationGateAction = "down"
)

// MigrationMetadataSnapshot is a non-authorizing observation of the configured
// migration metadata relation. A3 authorization must additionally prove the
// catalog and semantic invariants owned by later units.
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
	if conn == nil {
		return MigrationMetadataSnapshot{}, errors.New("migration metadata inspection requires a pinned connection")
	}
	if config == nil {
		return MigrationMetadataSnapshot{}, errors.New("migration metadata configuration is required")
	}
	var currentSchema string
	if err := conn.QueryRowContext(ctx, "SELECT current_schema()").Scan(&currentSchema); err != nil {
		return MigrationMetadataSnapshot{}, fmt.Errorf("resolve migration metadata schema: %w", err)
	}
	schemaName, tableName, err := migrationMetadataIdentifiers(config, currentSchema)
	if err != nil {
		return MigrationMetadataSnapshot{}, err
	}
	qualifiedTable := quotedMigrationTable(schemaName, tableName)
	var relation sql.NullString
	if err := conn.QueryRowContext(ctx, "SELECT to_regclass($1)", qualifiedTable).Scan(&relation); err != nil {
		return MigrationMetadataSnapshot{}, fmt.Errorf("inspect migration metadata relation: %w", err)
	}
	catalogVersion, err := migrationCatalogVersion(ctx, conn, currentSchema, schemaName, tableName)
	if err != nil {
		return MigrationMetadataSnapshot{}, err
	}
	if !relation.Valid || relation.String == "" {
		return MigrationMetadataSnapshot{CatalogEmpty: catalogVersion == 0, CatalogVersion: catalogVersion}, nil
	}

	rows, err := conn.QueryContext(ctx, "SELECT version, dirty FROM "+qualifiedTable+" ORDER BY version")
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

func migrationCatalogVersion(ctx context.Context, conn *sql.Conn, schemaName, metadataSchema, metadataTable string) (int, error) {
	rows, err := conn.QueryContext(ctx, `
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
	if action == migrationGateUp && snapshot.Version == protectedSchemaVersion && embeddedLatest > protectedSchemaVersion {
		return migrationGateError(ErrProtectedV5Upgrade, action, snapshot)
	}
	return nil
}

func migrationGateActionName(action migrationGateAction) string { return string(action) }

// inspectCleanV5Metadata is the D1 admission seam consumed by the
// reconciliation command. It checks only metadata authority; the protected
// executor remains responsible for the exclusive fence, fresh facts, catalog,
// and semantic readiness proof.
func inspectCleanV5Metadata(ctx context.Context, databaseURL string) error {
	parsed, err := parsePostgresDatabaseURL(databaseURL)
	if err != nil {
		return err
	}
	db, err := sql.Open("postgres", parsed.driverURL)
	if err != nil {
		return errors.New("open PostgreSQL for migration admission")
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return errors.New("begin migration admission inspection")
	}
	defer tx.Rollback()
	if err := AcquireSharedWriterFence(ctx, tx); err != nil {
		return fmt.Errorf("acquire shared writer admission: %w", err)
	}
	var currentSchema string
	if err := tx.QueryRowContext(ctx, "SELECT current_schema()").Scan(&currentSchema); err != nil {
		return err
	}
	schemaName, tableName, err := migrationMetadataIdentifiers(parsed.config, currentSchema)
	if err != nil {
		return err
	}
	qualifiedTable := quotedMigrationTable(schemaName, tableName)
	var relation sql.NullString
	if err := tx.QueryRowContext(ctx, "SELECT to_regclass($1)", qualifiedTable).Scan(&relation); err != nil {
		return err
	}
	if !relation.Valid || relation.String == "" {
		return ErrCleanV5MetadataRequired
	}
	var snapshot MigrationMetadataSnapshot
	rows, err := tx.QueryContext(ctx, "SELECT version, dirty FROM "+qualifiedTable+" ORDER BY version")
	if err != nil {
		return err
	}
	for rows.Next() {
		var version int
		var dirty bool
		if err := rows.Scan(&version, &dirty); err != nil {
			rows.Close()
			return err
		}
		snapshot.Exists = true
		snapshot.RowCount++
		if snapshot.RowCount == 1 {
			snapshot.Version, snapshot.Dirty, snapshot.HasVersion = version, dirty, true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if err := classifyMigrationAdmission(snapshot, migrationGateUp, protectedSchemaVersion); err != nil {
		return err
	}
	if snapshot.RowCount != 1 || snapshot.Version != protectedSchemaVersion || snapshot.Dirty {
		return fmt.Errorf("%w: %s", ErrCleanV5MetadataRequired, snapshot)
	}
	return nil
}

// ConfiguredMigrationTable returns the quoted authoritative metadata relation
// for a DSN using an already-pinned connection. It performs no initialization.
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

// RequireCleanV5Metadata is the explicit D1 admission seam for A2 command
// entrypoints. It does not perform reconciliation or claim the later D2/D3
// catalog/readiness proof.
func RequireCleanV5Metadata(ctx context.Context, databaseURL string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return inspectCleanV5Metadata(ctx, databaseURL)
}
