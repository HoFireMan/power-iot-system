package migrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lib/pq"
)

// RuntimeAdmissionDisposition is the closed-world decision consumed by the
// backend process before it starts serving. It describes only the public
// application schema; operator-controlled schema work is not part of this
// package.
type RuntimeAdmissionDisposition string

const (
	RuntimeServeV6                  RuntimeAdmissionDisposition = "SERVE_CLEAN_V6"
	RuntimeServeB02                 RuntimeAdmissionDisposition = "SERVE_CLEAN_B02"
	RuntimeProtectedMigrationNeeded RuntimeAdmissionDisposition = "PROTECTED_MIGRATION_REQUIRED"
)

type RuntimeSchemaState string

const (
	RuntimeSchemaCleanV5  RuntimeSchemaState = "CLEAN_V5"
	RuntimeSchemaCleanV6  RuntimeSchemaState = "CLEAN_V6"
	RuntimeSchemaCleanB02 RuntimeSchemaState = "CLEAN_B02"
	RuntimeSchemaDirty    RuntimeSchemaState = "DIRTY"
	RuntimeSchemaUnknown  RuntimeSchemaState = "UNKNOWN"
)

type RuntimeCatalogState string

const (
	RuntimeCatalogExact   RuntimeCatalogState = "EXACT_RUNTIME_SCHEMA"
	RuntimeCatalogPartial RuntimeCatalogState = "PARTIAL_RUNTIME_SCHEMA"
	RuntimeCatalogEmpty   RuntimeCatalogState = "EMPTY_RUNTIME_SCHEMA"
)

var (
	ErrRuntimeProtectedMigrationRequired = errors.New("backend startup requires the protected migration entry")
	ErrRuntimeAdmissionRefused           = errors.New("backend startup refused by closed-world admission")
)

// RuntimeAdmissionReport contains non-secret application startup evidence.
// It intentionally has no operator topology or control-plane fields.
type RuntimeAdmissionReport struct {
	Disposition RuntimeAdmissionDisposition
	Version     uint
	Dirty       bool
	State       RuntimeSchemaState
	Catalog     RuntimeCatalogState
	Metadata    MigrationMetadataSnapshot
}

// ClassifyRuntimeAdmission is the pure closed-world startup matrix. A clean
// V5 database is handed to the private protected migration operator; only
// clean V6 and B-02 application schemas may serve.
func ClassifyRuntimeAdmission(state RuntimeSchemaState) (RuntimeAdmissionDisposition, error) {
	switch state {
	case RuntimeSchemaCleanV6:
		return RuntimeServeV6, nil
	case RuntimeSchemaCleanB02:
		return RuntimeServeB02, nil
	case RuntimeSchemaCleanV5:
		return RuntimeProtectedMigrationNeeded, ErrRuntimeProtectedMigrationRequired
	default:
		return "", fmt.Errorf("%w: state=%s", ErrRuntimeAdmissionRefused, state)
	}
}

// BootstrapAndAdmit performs generic application-schema bootstrap through V5
// and then admits only a clean pre-existing V6 or B-02 runtime schema. The
// operator-controlled schema work remains outside this package.
func BootstrapAndAdmit(ctx context.Context, databaseURL string) (RuntimeAdmissionReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	version, dirty, err := Version(databaseURL)
	if err != nil {
		return RuntimeAdmissionReport{}, fmt.Errorf("inspect startup migration metadata: %w", err)
	}
	if dirty {
		return RuntimeAdmissionReport{Version: version, Dirty: true, State: RuntimeSchemaDirty}, fmt.Errorf("%w: dirty metadata version=%d", ErrRuntimeAdmissionRefused, version)
	}
	if version < protectedSchemaVersion {
		if err := Bootstrap(databaseURL); err != nil {
			return RuntimeAdmissionReport{Version: version}, fmt.Errorf("generic bootstrap to clean V5 failed: %w", err)
		}
		version, dirty, err = Version(databaseURL)
		if err != nil {
			return RuntimeAdmissionReport{}, fmt.Errorf("reinspect post-bootstrap metadata: %w", err)
		}
		if dirty {
			return RuntimeAdmissionReport{Version: version, Dirty: true, State: RuntimeSchemaDirty}, fmt.Errorf("%w: bootstrap left dirty metadata version=%d", ErrRuntimeAdmissionRefused, version)
		}
	}
	if version > protectedSchemaVersion+2 {
		return RuntimeAdmissionReport{Version: version, State: RuntimeSchemaUnknown}, fmt.Errorf("%w: future metadata version=%d", ErrRuntimeAdmissionRefused, version)
	}

	catalog, metadata, err := inspectRuntimeCatalog(ctx, databaseURL, version)
	if err != nil {
		return RuntimeAdmissionReport{Version: version}, fmt.Errorf("inspect runtime schema: %w", err)
	}
	report := RuntimeAdmissionReport{Version: version, Dirty: dirty, Catalog: catalog, Metadata: metadata}
	switch version {
	case protectedSchemaVersion:
		report.State = RuntimeSchemaCleanV5
	case protectedSchemaVersion + 1:
		report.State = RuntimeSchemaCleanV6
	case protectedSchemaVersion + 2:
		report.State = RuntimeSchemaCleanB02
	default:
		report.State = RuntimeSchemaUnknown
	}
	if catalog != RuntimeCatalogExact {
		report.State = RuntimeSchemaUnknown
	}
	disposition, classifyErr := ClassifyRuntimeAdmission(report.State)
	report.Disposition = disposition
	if classifyErr != nil {
		return report, classifyErr
	}
	return report, nil
}

// inspectRuntimeCatalog checks only stable application tables. It deliberately
// does not inspect protected migration, reconciliation, or provider catalogs.
func inspectRuntimeCatalog(ctx context.Context, databaseURL string, version uint) (RuntimeCatalogState, MigrationMetadataSnapshot, error) {
	parsed, err := parsePostgresDatabaseURL(databaseURL)
	if err != nil {
		return RuntimeCatalogEmpty, MigrationMetadataSnapshot{}, err
	}
	db, err := sql.Open("postgres", parsed.driverURL)
	if err != nil {
		return RuntimeCatalogEmpty, MigrationMetadataSnapshot{}, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return RuntimeCatalogEmpty, MigrationMetadataSnapshot{}, err
	}
	defer tx.Rollback()
	if err := AcquireSharedWriterFence(ctx, tx); err != nil {
		return RuntimeCatalogEmpty, MigrationMetadataSnapshot{}, err
	}
	metadata, err := inspectMigrationMetadataOn(ctx, tx, parsed.config)
	if err != nil {
		return RuntimeCatalogEmpty, metadata, err
	}
	expectedTables := runtimeTables
	if version == protectedSchemaVersion+1 {
		expectedTables = runtimeV6Tables
	} else if version == protectedSchemaVersion+2 {
		expectedTables = runtimeB02Tables
	}
	const query = `SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name = ANY($1)`
	var count int
	if err := tx.QueryRowContext(ctx, query, pq.Array(expectedTables)).Scan(&count); err != nil {
		return RuntimeCatalogEmpty, metadata, err
	}
	if count == 0 {
		return RuntimeCatalogEmpty, metadata, nil
	}
	if count != len(expectedTables) {
		return RuntimeCatalogPartial, metadata, nil
	}
	if version == protectedSchemaVersion+2 {
		complete, err := verifyRuntimeB02Catalog(ctx, tx)
		if err != nil {
			return RuntimeCatalogPartial, metadata, err
		}
		if !complete {
			return RuntimeCatalogPartial, metadata, nil
		}
	}
	if err := tx.Commit(); err != nil {
		return RuntimeCatalogEmpty, metadata, err
	}
	return RuntimeCatalogExact, metadata, nil
}

func verifyRuntimeB02Catalog(ctx context.Context, q MigrationMetadataQueryer) (bool, error) {
	const columnQuery = `
SELECT count(*)
FROM information_schema.columns AS c
JOIN (VALUES
  ('power_readings', 'coverage_version', 'bigint', 'YES'),
  ('power_readings', 'interval_start', 'timestamp with time zone', 'YES'),
  ('power_readings', 'interval_end', 'timestamp with time zone', 'YES'),
  ('telemetry_ingest_keys', 'canonical_coverage_digest', 'bytea', 'YES'),
  ('telemetry_ingest_keys', 'conflict_detected', 'boolean', 'NO'),
  ('alert_logs', 'measurement_point_id', 'uuid', 'YES'),
  ('alert_logs', 'legacy_unresolved', 'boolean', 'NO'),
  ('daily_usages', 'measurement_point_id', 'uuid', 'YES'),
  ('daily_usages', 'legacy_unresolved', 'boolean', 'NO'),
  ('daily_usages', 'device_id', 'bigint', 'YES'),
  ('devices', 'lifecycle_status', 'character varying', 'NO')
) AS expected(table_name, column_name, data_type, is_nullable)
  ON expected.table_name = c.table_name AND expected.column_name = c.column_name
WHERE c.table_schema = current_schema() AND c.data_type = expected.data_type
  AND c.is_nullable = expected.is_nullable`
	var columns int
	if err := q.QueryRowContext(ctx, columnQuery).Scan(&columns); err != nil {
		return false, err
	}
	if columns != 11 {
		return false, nil
	}
	var hypertables, timeDimensions int
	if err := q.QueryRowContext(ctx, `
SELECT count(*) FROM timescaledb_information.hypertables
WHERE hypertable_schema=current_schema() AND hypertable_name='power_readings'`).Scan(&hypertables); err != nil {
		return false, err
	}
	if err := q.QueryRowContext(ctx, `
SELECT count(*) FROM timescaledb_information.dimensions
WHERE hypertable_schema=current_schema() AND hypertable_name='power_readings'
  AND dimension_type='Time' AND column_name='recorded_at'`).Scan(&timeDimensions); err != nil {
		return false, err
	}
	return hypertables == 1 && timeDimensions == 1, nil
}

var runtimeTables = []string{
	"admin_binding_audits", "admin_binding_operations", "alert_logs", "clients",
	"daily_usages", "device_alert_settings", "device_assignments", "device_types",
	"devices", "measurement_points", "power_readings", "refresh_sessions",
	"refresh_tokens", "shops", "system_configs", "telemetry_ingest_keys",
	"user_shop_relations", "users",
}

var runtimeV6Tables = append(append([]string(nil), runtimeTables...), "d4_operation_journal", "d4_operation_ledger")

var runtimeB02Tables = append(append([]string(nil), runtimeV6Tables...),
	"carbon_factor_sets", "carbon_factor_rates", "electricity_rate_sets",
	"electricity_tariff_plans", "electricity_rate_plans", "electricity_rate_tiers",
	"shop_billing_assignments", "measurement_point_alert_settings", "measurement_point_curfew_states",
)
