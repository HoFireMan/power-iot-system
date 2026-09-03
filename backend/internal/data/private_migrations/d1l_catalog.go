package migrations

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/golang-migrate/migrate/v4/database/postgres"
)

// D1LCatalogState is a fail-closed observation. It is never an authorization
// decision and the recognizer performs no writes.
type D1LCatalogState string

const (
	D1LAbsent                  D1LCatalogState = "ABSENT"
	D1LV5Base                  D1LCatalogState = "V5_BASE"
	D1LValidV1                 D1LCatalogState = "VALID_V1"
	D1LValidNextLedgerReady    D1LCatalogState = "VALID_NEXT_LEDGER_READY"
	D1LHybrid                  D1LCatalogState = D1LFutureOrHybrid
	D1LInvalidCardinality      D1LCatalogState = D1LPartial
	D1LFuture                  D1LCatalogState = D1LWrongVersion
	D1LExactReady              D1LCatalogState = D1LValidV1 // compatibility alias for the v1 bootstrap seam
	D1LPartial                 D1LCatalogState = "PARTIAL"
	D1LExtra                   D1LCatalogState = D1LWrongPhysicalDefinition
	D1LWrongPhysicalDefinition D1LCatalogState = "WRONG_PHYSICAL_DEFINITION"
	D1LWrongVersion            D1LCatalogState = D1LPartial
	D1LDirty                   D1LCatalogState = "DIRTY"
	D1LWrongTarget             D1LCatalogState = "WRONG_TARGET"
	D1LWrongInstallerDigest    D1LCatalogState = "WRONG_INSTALLER_DIGEST"
	D1LUnreadable              D1LCatalogState = "UNREADABLE"
	D1LFutureOrHybrid          D1LCatalogState = "FUTURE_OR_HYBRID"
	D1LEmptySchema             D1LCatalogState = "EMPTY_SCHEMA"
)

const d1LNextControlVersion int64 = 2

var (
	ErrD1LCatalog       = errors.New("D1-L catalog recognition failed")
	ErrD1LApplicationV5 = errors.New("exact application V5 proof failed")
)

type D1LCatalogObservation struct {
	State                              D1LCatalogState
	ManifestRows                       int
	TargetFingerprint, InstallerDigest []byte
	Detail                             string
}
type d1lQueryer = migrationMetadataQueryer

// RecognizeD1LCatalog inspects the exact application-v5 proof and then the
// reserved, schema-qualified namespace. target and installer are raw 32-byte
// SHA-256 values. The default migration configuration is retained for callers
// that use the canonical schema_migrations relation.
func RecognizeD1LCatalog(ctx context.Context, q d1lQueryer, target, installer []byte) (D1LCatalogObservation, error) {
	return RecognizeD1LCatalogWithConfig(ctx, q, target, installer, &postgres.Config{})
}

// RecognizeD1LCatalogWithConfig is the configured D1-L recognition seam. It
// proves the application V5 catalog before classifying V5_BASE or READY; the
// application proof is deliberately separate from security_control.
func RecognizeD1LCatalogWithConfig(ctx context.Context, q d1lQueryer, target, installer []byte, config *postgres.Config) (D1LCatalogObservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if q == nil || len(target) != 32 || len(installer) != 32 || config == nil {
		return D1LCatalogObservation{State: D1LUnreadable}, fmt.Errorf("%w: invalid recognizer inputs", ErrD1LCatalog)
	}
	metadata, err := inspectMigrationMetadataOn(ctx, q, config)
	if err != nil {
		return D1LCatalogObservation{State: D1LUnreadable, Detail: err.Error()}, err
	}
	if !metadata.Exists || metadata.RowCount != 1 || metadata.Version != protectedSchemaVersion || metadata.Dirty {
		return D1LCatalogObservation{State: D1LPartial, Detail: fmt.Sprintf("configured metadata is not clean v5: %s", metadata)}, nil
	}
	if err := verifyExactV5Application(ctx, q, config); err != nil {
		if errors.Is(err, ErrD1LApplicationV5) {
			return D1LCatalogObservation{State: D1LPartial, Detail: err.Error()}, nil
		}
		return D1LCatalogObservation{State: D1LUnreadable, Detail: err.Error()}, err
	}
	var schemaOID sql.NullInt64
	if err := q.QueryRowContext(ctx, `SELECT oid::bigint FROM pg_namespace WHERE nspname = 'security_control'`).Scan(&schemaOID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return D1LCatalogObservation{State: D1LUnreadable}, err
	}
	if !schemaOID.Valid {
		return D1LCatalogObservation{State: D1LV5Base}, nil
	}
	var objectCount int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM pg_class WHERE relnamespace=$1 AND relkind NOT IN ('t','i')`, schemaOID.Int64).Scan(&objectCount); err != nil {
		return D1LCatalogObservation{State: D1LUnreadable}, err
	}
	if objectCount == 0 {
		return D1LCatalogObservation{State: D1LEmptySchema}, nil
	}
	obs := D1LCatalogObservation{}
	var rows int
	var version int64
	var gotTarget, gotInstaller []byte
	err = q.QueryRowContext(ctx, `SELECT count(*),COALESCE(min(control_version),0) FROM security_control.control_schema_migrations`).Scan(&rows, &version)
	if err != nil {
		return D1LCatalogObservation{State: D1LPartial}, err
	}
	obs.ManifestRows = rows
	if rows == 0 {
		obs.State = D1LPartial
		return obs, nil
	}
	if rows != 1 {
		obs.State = D1LInvalidCardinality
		return obs, nil
	}
	if err := q.QueryRowContext(ctx, `SELECT dirty,target_fingerprint,installer_digest FROM security_control.control_schema_migrations`).Scan(new(bool), &gotTarget, &gotInstaller); err != nil {
		// Read dirty independently below so malformed rows remain classified
		// fail-closed rather than being mistaken for an unreadable database.
		obs.State = D1LPartial
		return obs, err
	}
	var dirty bool
	if err := q.QueryRowContext(ctx, `SELECT dirty FROM security_control.control_schema_migrations`).Scan(&dirty); err != nil {
		obs.State = D1LPartial
		return obs, err
	}
	obs.TargetFingerprint, obs.InstallerDigest = gotTarget, gotInstaller
	ledgerPresent := d1LLedgerObjectsPresent(ctx, q)
	if dirty {
		if version == 1 && !ledgerPresent {
			if err := verifyD1LPhysical(ctx, q, schemaOID.Int64); err != nil {
				obs.State, obs.Detail = classifyD1LPhysicalFailure(err), err.Error()
				return obs, nil
			}
		}
		obs.State = D1LDirty
		return obs, nil
	}
	if !reflect.DeepEqual(gotTarget, target) {
		obs.State = D1LWrongTarget
		return obs, nil
	}
	switch version {
	case 1:
		if ledgerPresent {
			obs.State = D1LHybrid
			obs.Detail = "v1 manifest with additive ledger objects"
			return obs, nil
		}
		if err := verifyD1LPhysical(ctx, q, schemaOID.Int64); err != nil {
			obs.State = classifyD1LPhysicalFailure(err)
			obs.Detail = err.Error()
			return obs, nil
		}
		if !reflect.DeepEqual(gotInstaller, installer) {
			obs.State = D1LWrongInstallerDigest
			return obs, nil
		}
		obs.State = D1LValidV1
		return obs, nil
	case d1LNextControlVersion:
		if !ledgerPresent {
			obs.State = D1LPartial
			obs.Detail = "next-version manifest without ledger objects"
			return obs, nil
		}
		if !reflect.DeepEqual(gotInstaller, d1LLedgerTransitionDigestBytes()) {
			obs.State = D1LWrongInstallerDigest
			return obs, nil
		}
		if err := verifyD1LPhysical(ctx, q, schemaOID.Int64, d1LNextPhysicalExpectation()); err != nil {
			obs.State = classifyD1LPhysicalFailure(err)
			obs.Detail = err.Error()
			return obs, nil
		}
		obs.State = D1LValidNextLedgerReady
		return obs, nil
	default:
		obs.State = D1LFuture
		if ledgerPresent {
			obs.Detail = "unsupported manifest version with ledger objects"
		}
		return obs, nil
	}
}

func d1LLedgerObjectsPresent(ctx context.Context, q d1lQueryer) bool {
	var present bool
	if err := q.QueryRowContext(ctx, `SELECT to_regclass('security_control.admission_provenance') IS NOT NULL`).Scan(&present); err != nil {
		return true // unreadable is fail-closed and must not be admitted as v1
	}
	return present
}

func classifyD1LPhysicalFailure(err error) D1LCatalogState {
	if err == nil {
		return D1LValidV1
	}
	if strings.Contains(err.Error(), "extra relation") || strings.Contains(err.Error(), "extra index") || strings.Contains(err.Error(), "extra column") || strings.Contains(err.Error(), "extra constraint") {
		return D1LExtra
	}
	return D1LWrongPhysicalDefinition
}

func D1LRecognize(ctx context.Context, q d1lQueryer, target, installer []byte) (D1LCatalogObservation, error) {
	return RecognizeD1LCatalog(ctx, q, target, installer)
}

// verifyExactV5Application reuses the protected migration verifier's frozen
// v5 structural proof. D1-L additionally requires that the protected
// application relation inventory is exactly the authoritative 18 relations;
// incidental metadata/table-name inference is not an admission authority.
func verifyExactV5Application(ctx context.Context, q ProtectedMigrationQueryer, config *postgres.Config) error {
	if q == nil || config == nil {
		return fmt.Errorf("%w: application queryer and migration configuration are required", ErrD1LApplicationV5)
	}
	var schema string
	if err := q.QueryRowContext(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
		return err
	}
	tables, err := readCatalogTables(ctx, q, schema, config, MigrationMetadataSnapshot{})
	if err != nil {
		return err
	}
	if !catalogTablesEqual(tables, v5CatalogTables) {
		return fmt.Errorf("%w: application relation inventory is not exact V5", ErrD1LApplicationV5)
	}
	if err := verifySecurityCatalog(ctx, q, schema, false); err != nil {
		return fmt.Errorf("%w: %v", ErrD1LApplicationV5, err)
	}
	return nil
}

var d1lRelations = map[string]string{"control_schema_migrations": "r", "admission_leases": "r", "admission_boundaries": "r", "admission_generation_seq": "S"}
var d1lIndexes = map[string]string{
	"control_schema_migrations_pkey": "control_schema_migrations", "control_schema_migrations_install_id_key": "control_schema_migrations",
	"admission_leases_pkey": "admission_leases", "admission_leases_attempt_id_key": "admission_leases", "admission_leases_operation_generation_key": "admission_leases", "admission_leases_identity_key": "admission_leases", "admission_leases_one_live_generation_idx": "admission_leases",
	"admission_boundaries_pkey": "admission_boundaries", "admission_boundaries_boundary_nonce_key": "admission_boundaries", "admission_boundaries_lease_identity_name_key": "admission_boundaries", "admission_boundaries_one_open_per_lease_idx": "admission_boundaries",
}

type d1lIndexDescriptor struct {
	table           string
	keys            []string
	opclasses       []string
	collations      []string
	unique, primary bool
	predicate       string
}

var d1lIndexDescriptors = map[string]d1lIndexDescriptor{
	"control_schema_migrations_pkey":               {table: "control_schema_migrations", keys: []string{"control_version"}, opclasses: []string{"int8_ops"}, unique: true, primary: true},
	"control_schema_migrations_install_id_key":     {table: "control_schema_migrations", keys: []string{"install_id"}, opclasses: []string{"uuid_ops"}, unique: true},
	"admission_leases_pkey":                        {table: "admission_leases", keys: []string{"lease_id"}, opclasses: []string{"uuid_ops"}, unique: true, primary: true},
	"admission_leases_attempt_id_key":              {table: "admission_leases", keys: []string{"attempt_id"}, opclasses: []string{"uuid_ops"}, unique: true},
	"admission_leases_operation_generation_key":    {table: "admission_leases", keys: []string{"operation_id", "generation"}, opclasses: []string{"uuid_ops", "int8_ops"}, unique: true},
	"admission_leases_identity_key":                {table: "admission_leases", keys: []string{"lease_id", "generation", "attempt_id"}, opclasses: []string{"uuid_ops", "int8_ops", "uuid_ops"}, unique: true},
	"admission_leases_one_live_generation_idx":     {table: "admission_leases", keys: []string{"operation_id"}, opclasses: []string{"uuid_ops"}, unique: true, predicate: "status = ANY (ARRAY['ISSUED'::text, 'ACTIVE'::text, 'QUARANTINE_PENDING'::text]::text[])"},
	"admission_boundaries_pkey":                    {table: "admission_boundaries", keys: []string{"boundary_id"}, opclasses: []string{"uuid_ops"}, unique: true, primary: true},
	"admission_boundaries_boundary_nonce_key":      {table: "admission_boundaries", keys: []string{"boundary_nonce"}, opclasses: []string{"uuid_ops"}, unique: true},
	"admission_boundaries_lease_identity_name_key": {table: "admission_boundaries", keys: []string{"lease_id", "generation", "boundary_name"}, opclasses: []string{"uuid_ops", "int8_ops", "text_ops"}, collations: []string{"", "", "C"}, unique: true},
	"admission_boundaries_one_open_per_lease_idx":  {table: "admission_boundaries", keys: []string{"lease_id", "generation"}, opclasses: []string{"uuid_ops", "int8_ops"}, unique: true, predicate: "(status = 'OPEN'::text)"},
}

// PostgreSQL 15 strips the redundant text[] cast from this frozen predicate
// when it is read back through pg_get_expr. This exact text is accepted only
// for this exact frozen index descriptor; arbitrary no-cast predicates remain
// rejected by SerializeD1LPGExpr and by the comparison below.
var d1lPG15FrozenIndexDeparse = map[string]string{
	"admission_leases_one_live_generation_idx": "(status = ANY (ARRAY['ISSUED'::text, 'ACTIVE'::text, 'QUARANTINE_PENDING'::text]))",
}

var d1lConstraintExpressions = map[string]string{
	"control_schema_migrations_version_check":            "(control_version = 1)",
	"control_schema_migrations_dirty_check":              "(dirty = false)",
	"control_schema_migrations_target_fingerprint_check": "(octet_length(target_fingerprint) = 32)",
	"control_schema_migrations_installer_digest_check":   "(octet_length(installer_digest) = 32)",
	"admission_leases_target_fingerprint_check":          "(octet_length(target_fingerprint) = 32)",
	"admission_leases_evidence_digest_check":             "(octet_length(evidence_digest) = 32)",
	"admission_leases_capability_verifier_digest_check":  "(octet_length(capability_verifier_digest) = 32)",
	"admission_leases_generation_check":                  "(generation > 0)",
	"admission_leases_expires_after_issued_check":        "(expires_at > issued_at)",
	"admission_boundaries_generation_check":              "(generation > 0)",
	"admission_boundaries_expiry_check":                  "(expires_at > started_at)",
	"admission_leases_status_check":                      "(status = ANY (ARRAY['ISSUED'::text, 'ACTIVE'::text, 'QUARANTINE_PENDING'::text, 'QUARANTINED'::text, 'CONSUMED'::text, 'EXPIRED'::text, 'REVOKED'::text]))",
	"admission_boundaries_status_check":                  "(status = ANY (ARRAY['OPEN'::text, 'COMMITTED'::text, 'ROLLED_BACK'::text, 'FAILED'::text, 'UNKNOWN'::text]))",
	"admission_boundaries_name_check":                    "(boundary_name = ANY (ARRAY['A2_COMMIT'::text, 'HANDOFF'::text, 'DIRTY_MARKER_COMMIT'::text, 'DDL_COMMIT'::text, 'FINAL_VERIFY'::text, 'FINAL_METADATA_COMMIT'::text, 'RECOVERY_METADATA_COMMIT'::text]))",
	"admission_leases_terminal_fields_check":             "(((status = ANY (ARRAY['CONSUMED'::text, 'EXPIRED'::text, 'REVOKED'::text])) AND (terminal_at IS NOT NULL) AND (terminal_code IS NOT NULL)) OR ((status = ANY (ARRAY['ISSUED'::text, 'ACTIVE'::text, 'QUARANTINE_PENDING'::text, 'QUARANTINED'::text])) AND (terminal_at IS NULL) AND (terminal_code IS NULL)))",
	"admission_leases_quarantine_fields_check":           "(((status = ANY (ARRAY['ISSUED'::text, 'ACTIVE'::text, 'QUARANTINE_PENDING'::text, 'CONSUMED'::text, 'EXPIRED'::text])) AND (quarantined_at IS NULL) AND (quarantine_code IS NULL)) OR ((status = 'QUARANTINED'::text) AND (quarantined_at IS NOT NULL) AND (quarantine_code IS NOT NULL)) OR ((status = 'REVOKED'::text) AND (((quarantined_at IS NULL) AND (quarantine_code IS NULL)) OR ((quarantined_at IS NOT NULL) AND (quarantine_code IS NOT NULL)))))",
	"admission_leases_recovery_digest_check":             "((recovery_digest IS NULL) OR (octet_length(recovery_digest) = 32))",
	"admission_boundaries_open_fields_check":             "(((status = 'OPEN'::text) AND (closed_at IS NULL) AND (outcome_code IS NULL)) OR ((status = ANY (ARRAY['COMMITTED'::text, 'ROLLED_BACK'::text, 'FAILED'::text, 'UNKNOWN'::text])) AND (closed_at IS NOT NULL) AND (outcome_code IS NOT NULL)))",
	"admission_leases_lifecycle_fields_check":            "(((status = 'ISSUED'::text) AND (activated_at IS NULL) AND (terminal_at IS NULL) AND (terminal_code IS NULL) AND (quarantined_at IS NULL) AND (quarantine_code IS NULL)) OR ((status = ANY (ARRAY['ACTIVE'::text, 'QUARANTINE_PENDING'::text])) AND (activated_at IS NOT NULL) AND (terminal_at IS NULL) AND (terminal_code IS NULL) AND (quarantined_at IS NULL) AND (quarantine_code IS NULL)) OR ((status = 'QUARANTINED'::text) AND (activated_at IS NOT NULL) AND (terminal_at IS NULL) AND (terminal_code IS NULL) AND (quarantined_at IS NOT NULL) AND (quarantine_code IS NOT NULL)) OR ((status = 'CONSUMED'::text) AND (activated_at IS NOT NULL) AND (terminal_at IS NOT NULL) AND (terminal_code IS NOT NULL) AND (quarantined_at IS NULL) AND (quarantine_code IS NULL)) OR ((status = 'EXPIRED'::text) AND (activated_at IS NULL) AND (terminal_at IS NOT NULL) AND (terminal_code IS NOT NULL) AND (quarantined_at IS NULL) AND (quarantine_code IS NULL)) OR ((status = 'REVOKED'::text) AND (terminal_at IS NOT NULL) AND (terminal_code IS NOT NULL) AND (((quarantined_at IS NULL) AND (quarantine_code IS NULL)) OR ((quarantined_at IS NOT NULL) AND (quarantine_code IS NOT NULL)))))",
}

var d1lConstraints = map[string]string{
	"control_schema_migrations_pkey": "control_schema_migrations", "control_schema_migrations_version_check": "control_schema_migrations", "control_schema_migrations_dirty_check": "control_schema_migrations", "control_schema_migrations_target_fingerprint_check": "control_schema_migrations", "control_schema_migrations_installer_digest_check": "control_schema_migrations", "control_schema_migrations_install_id_key": "control_schema_migrations",
	"admission_leases_pkey": "admission_leases", "admission_leases_attempt_id_key": "admission_leases", "admission_leases_operation_generation_key": "admission_leases", "admission_leases_identity_key": "admission_leases", "admission_leases_status_check": "admission_leases", "admission_leases_target_fingerprint_check": "admission_leases", "admission_leases_evidence_digest_check": "admission_leases", "admission_leases_capability_verifier_digest_check": "admission_leases", "admission_leases_generation_check": "admission_leases", "admission_leases_expires_after_issued_check": "admission_leases", "admission_leases_terminal_fields_check": "admission_leases", "admission_leases_quarantine_fields_check": "admission_leases", "admission_leases_lifecycle_fields_check": "admission_leases", "admission_leases_recovery_digest_check": "admission_leases",
	"admission_boundaries_pkey": "admission_boundaries", "admission_boundaries_boundary_nonce_key": "admission_boundaries", "admission_boundaries_lease_identity_name_key": "admission_boundaries", "admission_boundaries_lease_fk": "admission_boundaries", "admission_boundaries_generation_check": "admission_boundaries", "admission_boundaries_name_check": "admission_boundaries", "admission_boundaries_status_check": "admission_boundaries", "admission_boundaries_expiry_check": "admission_boundaries", "admission_boundaries_open_fields_check": "admission_boundaries",
}

type d1lColumnDescriptor struct {
	name, typeSchema, typeName     string
	typmod                         int
	notNull                        bool
	collationSchema, collationName string
	defaultExpr                    string
}

// These descriptors deliberately contain the qualified PostgreSQL type
// identity, rather than format_type output. Numeric catalog OIDs are
// environment identifiers and are not part of the D1L contract.
var d1lColumnDescriptors = map[string][]d1lColumnDescriptor{
	"control_schema_migrations": {
		{name: "control_version", typeSchema: "pg_catalog", typeName: "int8", typmod: -1, notNull: true},
		{name: "dirty", typeSchema: "pg_catalog", typeName: "bool", typmod: -1, notNull: true},
		{name: "target_fingerprint", typeSchema: "pg_catalog", typeName: "bytea", typmod: -1, notNull: true},
		{name: "installer_digest", typeSchema: "pg_catalog", typeName: "bytea", typmod: -1, notNull: true},
		{name: "install_id", typeSchema: "pg_catalog", typeName: "uuid", typmod: -1, notNull: true},
		{name: "installed_at", typeSchema: "pg_catalog", typeName: "timestamptz", typmod: -1, notNull: true},
	},
	"admission_leases": {
		{name: "lease_id", typeSchema: "pg_catalog", typeName: "uuid", typmod: -1, notNull: true},
		{name: "operation_id", typeSchema: "pg_catalog", typeName: "uuid", typmod: -1, notNull: true},
		{name: "attempt_id", typeSchema: "pg_catalog", typeName: "uuid", typmod: -1, notNull: true},
		{name: "generation", typeSchema: "pg_catalog", typeName: "int8", typmod: -1, notNull: true, defaultExpr: "nextval('security_control.admission_generation_seq'::regclass)"},
		{name: "target_fingerprint", typeSchema: "pg_catalog", typeName: "bytea", typmod: -1, notNull: true},
		{name: "evidence_digest", typeSchema: "pg_catalog", typeName: "bytea", typmod: -1, notNull: true},
		{name: "capability_verifier_digest", typeSchema: "pg_catalog", typeName: "bytea", typmod: -1, notNull: true},
		{name: "status", typeSchema: "pg_catalog", typeName: "text", typmod: -1, notNull: true, collationSchema: "pg_catalog", collationName: "C"},
		{name: "issued_at", typeSchema: "pg_catalog", typeName: "timestamptz", typmod: -1, notNull: true},
		{name: "expires_at", typeSchema: "pg_catalog", typeName: "timestamptz", typmod: -1, notNull: true},
		{name: "activated_at", typeSchema: "pg_catalog", typeName: "timestamptz", typmod: -1},
		{name: "terminal_at", typeSchema: "pg_catalog", typeName: "timestamptz", typmod: -1},
		{name: "terminal_code", typeSchema: "pg_catalog", typeName: "text", typmod: -1, collationSchema: "pg_catalog", collationName: "C"},
		{name: "quarantined_at", typeSchema: "pg_catalog", typeName: "timestamptz", typmod: -1},
		{name: "quarantine_code", typeSchema: "pg_catalog", typeName: "text", typmod: -1, collationSchema: "pg_catalog", collationName: "C"},
		{name: "recovery_digest", typeSchema: "pg_catalog", typeName: "bytea", typmod: -1},
	},
	"admission_boundaries": {
		{name: "boundary_id", typeSchema: "pg_catalog", typeName: "uuid", typmod: -1, notNull: true},
		{name: "lease_id", typeSchema: "pg_catalog", typeName: "uuid", typmod: -1, notNull: true},
		{name: "attempt_id", typeSchema: "pg_catalog", typeName: "uuid", typmod: -1, notNull: true},
		{name: "generation", typeSchema: "pg_catalog", typeName: "int8", typmod: -1, notNull: true},
		{name: "boundary_nonce", typeSchema: "pg_catalog", typeName: "uuid", typmod: -1, notNull: true},
		{name: "boundary_name", typeSchema: "pg_catalog", typeName: "text", typmod: -1, notNull: true, collationSchema: "pg_catalog", collationName: "C"},
		{name: "status", typeSchema: "pg_catalog", typeName: "text", typmod: -1, notNull: true, collationSchema: "pg_catalog", collationName: "C"},
		{name: "started_at", typeSchema: "pg_catalog", typeName: "timestamptz", typmod: -1, notNull: true},
		{name: "expires_at", typeSchema: "pg_catalog", typeName: "timestamptz", typmod: -1, notNull: true},
		{name: "closed_at", typeSchema: "pg_catalog", typeName: "timestamptz", typmod: -1},
		{name: "outcome_code", typeSchema: "pg_catalog", typeName: "text", typmod: -1, collationSchema: "pg_catalog", collationName: "C"},
	},
}

type d1lConstraintDescriptor struct {
	table, contype, conkey, confkey, referencedTable, referencedSchema string
	confupdtype, confdeltype, confmatchtype                            string
	conindid                                                           string
}

// d1lConstraintDescriptors bind every expected constraint to its exact
// PostgreSQL identity. Names alone are not sufficient: a same-named CHECK,
// UNIQUE, or FOREIGN KEY must never stand in for another constraint kind.
var d1lConstraintDescriptors = map[string]d1lConstraintDescriptor{
	"control_schema_migrations_pkey":                     {table: "control_schema_migrations", contype: "p", conkey: "{1}", conindid: "control_schema_migrations_pkey"},
	"control_schema_migrations_version_check":            {table: "control_schema_migrations", contype: "c", conkey: "{1}"},
	"control_schema_migrations_dirty_check":              {table: "control_schema_migrations", contype: "c", conkey: "{2}"},
	"control_schema_migrations_target_fingerprint_check": {table: "control_schema_migrations", contype: "c", conkey: "{3}"},
	"control_schema_migrations_installer_digest_check":   {table: "control_schema_migrations", contype: "c", conkey: "{4}"},
	"control_schema_migrations_install_id_key":           {table: "control_schema_migrations", contype: "u", conkey: "{5}", conindid: "control_schema_migrations_install_id_key"},
	"admission_leases_pkey":                              {table: "admission_leases", contype: "p", conkey: "{1}", conindid: "admission_leases_pkey"},
	"admission_leases_attempt_id_key":                    {table: "admission_leases", contype: "u", conkey: "{3}", conindid: "admission_leases_attempt_id_key"},
	"admission_leases_operation_generation_key":          {table: "admission_leases", contype: "u", conkey: "{2,4}", conindid: "admission_leases_operation_generation_key"},
	"admission_leases_identity_key":                      {table: "admission_leases", contype: "u", conkey: "{1,4,3}", conindid: "admission_leases_identity_key"},
	"admission_leases_status_check":                      {table: "admission_leases", contype: "c", conkey: "{8}"},
	"admission_leases_target_fingerprint_check":          {table: "admission_leases", contype: "c", conkey: "{5}"},
	"admission_leases_evidence_digest_check":             {table: "admission_leases", contype: "c", conkey: "{6}"},
	"admission_leases_capability_verifier_digest_check":  {table: "admission_leases", contype: "c", conkey: "{7}"},
	"admission_leases_generation_check":                  {table: "admission_leases", contype: "c", conkey: "{4}"},
	"admission_leases_expires_after_issued_check":        {table: "admission_leases", contype: "c", conkey: "{10,9}"},
	"admission_leases_terminal_fields_check":             {table: "admission_leases", contype: "c", conkey: "{8,12,13}"},
	"admission_leases_quarantine_fields_check":           {table: "admission_leases", contype: "c", conkey: "{8,14,15}"},
	"admission_leases_lifecycle_fields_check":            {table: "admission_leases", contype: "c", conkey: "{8,11,12,13,14,15}"},
	"admission_leases_recovery_digest_check":             {table: "admission_leases", contype: "c", conkey: "{16}"},
	"admission_boundaries_pkey":                          {table: "admission_boundaries", contype: "p", conkey: "{1}", conindid: "admission_boundaries_pkey"},
	"admission_boundaries_boundary_nonce_key":            {table: "admission_boundaries", contype: "u", conkey: "{5}", conindid: "admission_boundaries_boundary_nonce_key"},
	"admission_boundaries_lease_identity_name_key":       {table: "admission_boundaries", contype: "u", conkey: "{2,4,6}", conindid: "admission_boundaries_lease_identity_name_key"},
	"admission_boundaries_lease_fk":                      {table: "admission_boundaries", contype: "f", conkey: "{2,4,3}", confkey: "{1,4,3}", referencedSchema: "security_control", referencedTable: "admission_leases", confupdtype: "r", confdeltype: "r", confmatchtype: "s", conindid: "admission_leases_identity_key"},
	"admission_boundaries_generation_check":              {table: "admission_boundaries", contype: "c", conkey: "{4}"},
	"admission_boundaries_name_check":                    {table: "admission_boundaries", contype: "c", conkey: "{6}"},
	"admission_boundaries_status_check":                  {table: "admission_boundaries", contype: "c", conkey: "{7}"},
	"admission_boundaries_expiry_check":                  {table: "admission_boundaries", contype: "c", conkey: "{9,8}"},
	"admission_boundaries_open_fields_check":             {table: "admission_boundaries", contype: "c", conkey: "{7,10,11}"},
}

var d1lColumns = map[string][]string{
	"control_schema_migrations": {"control_version", "dirty", "target_fingerprint", "installer_digest", "install_id", "installed_at"},
	"admission_leases":          {"lease_id", "operation_id", "attempt_id", "generation", "target_fingerprint", "evidence_digest", "capability_verifier_digest", "status", "issued_at", "expires_at", "activated_at", "terminal_at", "terminal_code", "quarantined_at", "quarantine_code", "recovery_digest"},
	"admission_boundaries":      {"boundary_id", "lease_id", "attempt_id", "generation", "boundary_nonce", "boundary_name", "status", "started_at", "expires_at", "closed_at", "outcome_code"},
}

type d1lPhysicalExpectation struct {
	relations, indexes, constraints map[string]string
	indexDescriptors                map[string]d1lIndexDescriptor
	constraintDescriptors           map[string]d1lConstraintDescriptor
	constraintExpressions           map[string]string
	columns                         map[string][]d1lColumnDescriptor
	indexDeparse                    map[string]string
}

func d1LV1PhysicalExpectation() d1lPhysicalExpectation {
	return d1lPhysicalExpectation{
		relations: d1lRelations, indexes: d1lIndexes, constraints: d1lConstraints,
		indexDescriptors: d1lIndexDescriptors, constraintDescriptors: d1lConstraintDescriptors,
		constraintExpressions: d1lConstraintExpressions, columns: d1lColumnDescriptors,
		indexDeparse: d1lPG15FrozenIndexDeparse,
	}
}

func d1LNextPhysicalExpectation() d1lPhysicalExpectation {
	e := d1LV1PhysicalExpectation()
	e.relations = cloneStringMap(e.relations)
	e.indexes = cloneStringMap(e.indexes)
	e.constraints = cloneStringMap(e.constraints)
	e.indexDescriptors = cloneIndexMap(e.indexDescriptors)
	e.constraintDescriptors = cloneConstraintMap(e.constraintDescriptors)
	e.constraintExpressions = cloneStringMap(e.constraintExpressions)
	e.columns = cloneColumnMap(e.columns)
	e.indexDeparse = cloneStringMap(e.indexDeparse)
	e.relations["admission_provenance"] = "r"
	e.indexes["admission_provenance_pkey"] = "admission_provenance"
	e.indexes["admission_provenance_issue_key"] = "admission_provenance"
	e.indexes["admission_provenance_attempt_id_key"] = "admission_provenance"
	e.indexes["admission_provenance_lease_key"] = "admission_provenance"
	e.indexes["admission_provenance_available_identity"] = "admission_provenance"
	e.indexes["admission_provenance_reserved_identity"] = "admission_provenance"
	e.indexDescriptors["admission_provenance_pkey"] = d1lIndexDescriptor{table: "admission_provenance", keys: []string{"provenance_id", "provenance_version"}, opclasses: []string{"uuid_ops", "int8_ops"}, unique: true, primary: true}
	e.indexDescriptors["admission_provenance_issue_key"] = d1lIndexDescriptor{table: "admission_provenance", keys: []string{"issue_id"}, opclasses: []string{"uuid_ops"}, unique: true}
	e.indexDescriptors["admission_provenance_attempt_id_key"] = d1lIndexDescriptor{table: "admission_provenance", keys: []string{"attempt_id"}, opclasses: []string{"uuid_ops"}, unique: true}
	e.indexDescriptors["admission_provenance_lease_key"] = d1lIndexDescriptor{table: "admission_provenance", keys: []string{"lease_id"}, opclasses: []string{"uuid_ops"}, unique: true}
	e.indexDescriptors["admission_provenance_available_identity"] = d1lIndexDescriptor{table: "admission_provenance", keys: []string{"provenance_id", "provenance_version"}, opclasses: []string{"uuid_ops", "int8_ops"}, unique: true, predicate: "state = 'AVAILABLE'::text"}
	e.indexDescriptors["admission_provenance_reserved_identity"] = d1lIndexDescriptor{table: "admission_provenance", keys: []string{"provenance_id", "provenance_version"}, opclasses: []string{"uuid_ops", "int8_ops"}, unique: true, predicate: "state = 'RESERVED'::text"}
	e.indexDeparse["admission_provenance_available_identity"] = "(state = 'AVAILABLE'::text)"
	e.indexDeparse["admission_provenance_reserved_identity"] = "(state = 'RESERVED'::text)"
	e.constraints["admission_provenance_pkey"] = "admission_provenance"
	e.constraints["admission_provenance_state_check"] = "admission_provenance"
	e.constraints["admission_provenance_version_check"] = "admission_provenance"
	e.constraints["admission_provenance_owner_identity_check"] = "admission_provenance"
	e.constraints["admission_provenance_route_check"] = "admission_provenance"
	e.constraints["admission_provenance_digest_check"] = "admission_provenance"
	e.constraints["admission_provenance_target_nonzero_check"] = "admission_provenance"
	e.constraints["admission_provenance_issue_key"] = "admission_provenance"
	e.constraints["admission_provenance_issue_state_check"] = "admission_provenance"
	e.constraints["admission_provenance_attempt_id_key"] = "admission_provenance"
	e.constraints["admission_provenance_lease_key"] = "admission_provenance"
	e.constraints["admission_provenance_lease_link_check"] = "admission_provenance"
	e.constraints["admission_provenance_reserved_fields_check"] = "admission_provenance"
	e.constraints["admission_provenance_terminal_code_check"] = "admission_provenance"
	e.constraints["admission_provenance_timestamp_order_check"] = "admission_provenance"
	e.constraints["admission_provenance_lease_identity_fk"] = "admission_provenance"
	e.constraints["admission_provenance_lease_operation_fk"] = "admission_provenance"
	e.constraintDescriptors["admission_provenance_pkey"] = d1lConstraintDescriptor{table: "admission_provenance", contype: "p", conkey: "{1,2}", conindid: "admission_provenance_pkey"}
	e.constraintDescriptors["admission_provenance_state_check"] = d1lConstraintDescriptor{table: "admission_provenance", contype: "c", conkey: "{10}"}
	e.constraintDescriptors["admission_provenance_version_check"] = d1lConstraintDescriptor{table: "admission_provenance", contype: "c", conkey: "{2}"}
	e.constraintDescriptors["admission_provenance_owner_identity_check"] = d1lConstraintDescriptor{table: "admission_provenance", contype: "c", conkey: "{3,4}"}
	e.constraintDescriptors["admission_provenance_route_check"] = d1lConstraintDescriptor{table: "admission_provenance", contype: "c", conkey: "{9}"}
	e.constraintDescriptors["admission_provenance_digest_check"] = d1lConstraintDescriptor{table: "admission_provenance", contype: "c", conkey: "{7,8}"}
	e.constraintDescriptors["admission_provenance_target_nonzero_check"] = d1lConstraintDescriptor{table: "admission_provenance", contype: "c", conkey: "{7}"}
	e.constraintDescriptors["admission_provenance_issue_key"] = d1lConstraintDescriptor{table: "admission_provenance", contype: "u", conkey: "{11}", conindid: "admission_provenance_issue_key"}
	e.constraintDescriptors["admission_provenance_issue_state_check"] = d1lConstraintDescriptor{table: "admission_provenance", contype: "c", conkey: "{10,11}"}
	e.constraintDescriptors["admission_provenance_attempt_id_key"] = d1lConstraintDescriptor{table: "admission_provenance", contype: "u", conkey: "{6}", conindid: "admission_provenance_attempt_id_key"}
	e.constraintDescriptors["admission_provenance_lease_key"] = d1lConstraintDescriptor{table: "admission_provenance", contype: "u", conkey: "{12}", conindid: "admission_provenance_lease_key"}
	e.constraintDescriptors["admission_provenance_lease_link_check"] = d1lConstraintDescriptor{table: "admission_provenance", contype: "c", conkey: "{10,12,13}"}
	e.constraintDescriptors["admission_provenance_reserved_fields_check"] = d1lConstraintDescriptor{table: "admission_provenance", contype: "c", conkey: "{10,15,16}"}
	e.constraintDescriptors["admission_provenance_terminal_code_check"] = d1lConstraintDescriptor{table: "admission_provenance", contype: "c", conkey: "{10,17}"}
	e.constraintDescriptors["admission_provenance_timestamp_order_check"] = d1lConstraintDescriptor{table: "admission_provenance", contype: "c", conkey: "{15,14,16}"}
	e.constraintDescriptors["admission_provenance_lease_identity_fk"] = d1lConstraintDescriptor{table: "admission_provenance", contype: "f", conkey: "{12,13,6}", confkey: "{1,4,3}", referencedSchema: "security_control", referencedTable: "admission_leases", confupdtype: "r", confdeltype: "r", confmatchtype: "s", conindid: "admission_leases_identity_key"}
	e.constraintDescriptors["admission_provenance_lease_operation_fk"] = d1lConstraintDescriptor{table: "admission_provenance", contype: "f", conkey: "{5,13}", confkey: "{2,4}", referencedSchema: "security_control", referencedTable: "admission_leases", confupdtype: "r", confdeltype: "r", confmatchtype: "s", conindid: "admission_leases_operation_generation_key"}
	e.constraintExpressions["control_schema_migrations_version_check"] = "(control_version = ANY (ARRAY[(1)::bigint, (2)::bigint]))"
	e.constraintExpressions["admission_provenance_state_check"] = "(state = ANY (ARRAY['AVAILABLE'::text, 'RESERVED'::text, 'CONSUMED'::text, 'INVALIDATED'::text]))"
	e.constraintExpressions["admission_provenance_issue_state_check"] = "(((state = 'AVAILABLE'::text) AND (issue_id IS NULL)) OR ((state = ANY (ARRAY['RESERVED'::text, 'CONSUMED'::text, 'INVALIDATED'::text])) AND (issue_id IS NOT NULL)))"
	e.constraintExpressions["admission_provenance_version_check"] = "(provenance_version > 0)"
	e.constraintExpressions["admission_provenance_owner_identity_check"] = "((owner_identity = 'trusted-post-d1l-upstream'::text) AND (length(owner_identity) > 0) AND (length(owner_version) > 0) AND (btrim(owner_version) <> ''::text))"
	e.constraintExpressions["admission_provenance_route_check"] = "(route_intent = 'D1_ISSUE'::text)"
	e.constraintExpressions["admission_provenance_digest_check"] = "((octet_length(target_fingerprint) = 32) AND (octet_length(evidence_digest) = 32))"
	e.constraintExpressions["admission_provenance_target_nonzero_check"] = "(target_fingerprint <> decode(repeat('00'::text, 32), 'hex'::text))"
	e.constraintExpressions["admission_provenance_lease_link_check"] = "(((state = 'CONSUMED'::text) AND (lease_id IS NOT NULL) AND (lease_generation IS NOT NULL)) OR ((state <> 'CONSUMED'::text) AND (lease_id IS NULL) AND (lease_generation IS NULL)))"
	e.constraintExpressions["admission_provenance_reserved_fields_check"] = "(((state = 'AVAILABLE'::text) AND (reserved_at IS NULL) AND (resolved_at IS NULL)) OR ((state = 'RESERVED'::text) AND (reserved_at IS NOT NULL) AND (resolved_at IS NULL)) OR ((state = 'CONSUMED'::text) AND (reserved_at IS NOT NULL) AND (resolved_at IS NOT NULL)) OR ((state = 'INVALIDATED'::text) AND (reserved_at IS NOT NULL) AND (resolved_at IS NOT NULL)))"
	e.constraintExpressions["admission_provenance_terminal_code_check"] = "(((state = 'INVALIDATED'::text) AND (terminal_code = ANY (ARRAY['T1_ROLLBACK'::text, 'OWNER_INVALIDATED'::text, 'PROVIDER_REJECTED'::text, 'RECOVERY_REQUIRED'::text, 'CONSUME_ABORTED'::text]))) OR ((state <> 'INVALIDATED'::text) AND (terminal_code IS NULL)))"
	e.constraintExpressions["admission_provenance_timestamp_order_check"] = "(((reserved_at IS NULL) OR (reserved_at >= created_at)) AND ((resolved_at IS NULL) OR (resolved_at >= COALESCE(reserved_at, created_at))))"
	e.columns["admission_provenance"] = []d1lColumnDescriptor{
		{name: "provenance_id", typeSchema: "pg_catalog", typeName: "uuid", typmod: -1, notNull: true}, {name: "provenance_version", typeSchema: "pg_catalog", typeName: "int8", typmod: -1, notNull: true}, {name: "owner_identity", typeSchema: "pg_catalog", typeName: "text", typmod: -1, notNull: true, collationSchema: "pg_catalog", collationName: "C"}, {name: "owner_version", typeSchema: "pg_catalog", typeName: "text", typmod: -1, notNull: true, collationSchema: "pg_catalog", collationName: "C"}, {name: "operation_id", typeSchema: "pg_catalog", typeName: "uuid", typmod: -1, notNull: true}, {name: "attempt_id", typeSchema: "pg_catalog", typeName: "uuid", typmod: -1, notNull: true}, {name: "target_fingerprint", typeSchema: "pg_catalog", typeName: "bytea", typmod: -1, notNull: true}, {name: "evidence_digest", typeSchema: "pg_catalog", typeName: "bytea", typmod: -1, notNull: true}, {name: "route_intent", typeSchema: "pg_catalog", typeName: "text", typmod: -1, notNull: true, collationSchema: "pg_catalog", collationName: "C"}, {name: "state", typeSchema: "pg_catalog", typeName: "text", typmod: -1, notNull: true, collationSchema: "pg_catalog", collationName: "C"}, {name: "issue_id", typeSchema: "pg_catalog", typeName: "uuid", typmod: -1}, {name: "lease_id", typeSchema: "pg_catalog", typeName: "uuid", typmod: -1}, {name: "lease_generation", typeSchema: "pg_catalog", typeName: "int8", typmod: -1}, {name: "created_at", typeSchema: "pg_catalog", typeName: "timestamptz", typmod: -1, notNull: true, defaultExpr: "clock_timestamp()"}, {name: "reserved_at", typeSchema: "pg_catalog", typeName: "timestamptz", typmod: -1}, {name: "resolved_at", typeSchema: "pg_catalog", typeName: "timestamptz", typmod: -1}, {name: "terminal_code", typeSchema: "pg_catalog", typeName: "text", typmod: -1, collationSchema: "pg_catalog", collationName: "C"},
	}
	return e
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
func cloneIndexMap(in map[string]d1lIndexDescriptor) map[string]d1lIndexDescriptor {
	out := make(map[string]d1lIndexDescriptor, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
func cloneConstraintMap(in map[string]d1lConstraintDescriptor) map[string]d1lConstraintDescriptor {
	out := make(map[string]d1lConstraintDescriptor, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
func cloneColumnMap(in map[string][]d1lColumnDescriptor) map[string][]d1lColumnDescriptor {
	out := make(map[string][]d1lColumnDescriptor, len(in))
	for k, v := range in {
		out[k] = append([]d1lColumnDescriptor(nil), v...)
	}
	return out
}

func verifyD1LPhysical(ctx context.Context, q d1lQueryer, oid int64, expected ...d1lPhysicalExpectation) error {
	e := d1LV1PhysicalExpectation()
	if len(expected) != 0 {
		e = expected[0]
	}
	rows, err := q.QueryContext(ctx, `SELECT c.relname,c.relkind,c.relpersistence,c.reltablespace,COALESCE(am.amname,''),c.relrowsecurity,c.relforcerowsecurity,c.relreplident,c.relispartition,c.relpartbound IS NULL,c.reloptions FROM pg_class c LEFT JOIN pg_am am ON am.oid=c.relam WHERE c.relnamespace=$1 AND c.relkind NOT IN ('t','i')`, oid)
	if err != nil {
		return err
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var n, k, p string
		var ts int64
		var am string
		var rs, fr, isPart, noPartBound bool
		var repl string
		var opts []byte
		if err := rows.Scan(&n, &k, &p, &ts, &am, &rs, &fr, &repl, &isPart, &noPartBound, &opts); err != nil {
			return err
		}
		if _, ok := e.relations[n]; !ok {
			return fmt.Errorf("extra relation %s", n)
		}
		if seen[n] || e.relations[n] != k || p != "p" || ts != 0 || rs || fr || isPart || !noPartBound || (n != "admission_generation_seq" && repl != "d") || opts != nil {
			return fmt.Errorf("relation %s physical properties", n)
		}
		if n != "admission_generation_seq" && am != "heap" {
			return fmt.Errorf("table %s access method", n)
		}
		seen[n] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	for n := range e.relations {
		var inherited bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_inherits i JOIN pg_class child ON child.oid=i.inhrelid WHERE child.relnamespace=$1 AND child.relname=$2)`, oid, n).Scan(&inherited); err != nil {
			return err
		}
		if inherited {
			return fmt.Errorf("relation %s inheritance", n)
		}
	}
	for n := range e.relations {
		if !seen[n] {
			return fmt.Errorf("missing relation %s", n)
		}
	}
	var count int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM pg_proc WHERE pronamespace=$1`, oid).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("extra function in security_control")
	}
	if err := verifyD1LTriggers(ctx, q, oid, e); err != nil {
		return err
	}
	if err := verifyD1LRules(ctx, q, oid, e); err != nil {
		return err
	}
	if err := verifyD1LNamespaceObjects(ctx, q, oid, e); err != nil {
		return err
	}
	if err := verifyD1LColumns(ctx, q, oid, e); err != nil {
		return err
	}
	if err := verifyD1LSequence(ctx, q, oid); err != nil {
		return err
	}
	if err := verifyD1LConstraints(ctx, q, oid, e); err != nil {
		return err
	}
	if err := verifyD1LIndexes(ctx, q, oid, e); err != nil {
		return err
	}
	return verifyD1LDependencies(ctx, q, oid, e)
}

func verifyD1LTriggers(ctx context.Context, q d1lQueryer, oid int64, _ d1lPhysicalExpectation) error {
	var trigger string
	err := q.QueryRowContext(ctx, `SELECT t.tgname FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid WHERE c.relnamespace=$1 AND c.relname IN ('control_schema_migrations','admission_leases','admission_boundaries','admission_provenance') AND NOT t.tgisinternal ORDER BY t.tgname LIMIT 1`, oid).Scan(&trigger)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("extra user trigger %s", trigger)
}

// verifyD1LRules is deliberately scoped to the three frozen ordinary tables.
// pg_rewrite's generated _RETURN rules belong to views/materialized views,
// never these relkind='r' relations; no internal rule is therefore silently
// admitted in this bounded control dependency. Rules on any other relation,
// including unrelated objects in security_control or another schema, are out
// of scope for this recognizer.
func verifyD1LRules(ctx context.Context, q d1lQueryer, oid int64, _ d1lPhysicalExpectation) error {
	var rule string
	err := q.QueryRowContext(ctx, `
		SELECT r.rulename
		FROM pg_rewrite AS r
		JOIN pg_class AS c ON c.oid=r.ev_class
		JOIN pg_namespace AS n ON n.oid=c.relnamespace
		WHERE n.oid=$1
		  AND c.relkind='r'
		  AND c.relname IN ('control_schema_migrations','admission_leases','admission_boundaries','admission_provenance')
		ORDER BY c.relname,r.rulename
		LIMIT 1`, oid).Scan(&rule)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("extra user rewrite rule %s", rule)
}

// verifyD1LNamespaceObjects inspects only the reserved namespace. PostgreSQL
// creates composite row types (and their array types) for tables; those
// representations are explicitly allowed. User-defined domains, composites,
// enums, collations, and functions are not part of the frozen four-object
// inventory. External objects are intentionally outside this bounded check
// unless an in-scope object depends on them (see verifyD1LDependencies).
func verifyD1LNamespaceObjects(ctx context.Context, q d1lQueryer, oid int64, _ d1lPhysicalExpectation) error {
	rows, err := q.QueryContext(ctx, `SELECT c.oid::bigint,c.reltype::bigint FROM pg_class c WHERE c.relnamespace=$1 AND c.relname IN ('control_schema_migrations','admission_leases','admission_boundaries','admission_provenance','admission_generation_seq')`, oid)
	if err != nil {
		return err
	}
	expectedRelations := make(map[int64]bool)
	rowTypes := make(map[int64]bool)
	for rows.Next() {
		var relationOID, rowTypeOID int64
		if err := rows.Scan(&relationOID, &rowTypeOID); err != nil {
			rows.Close()
			return err
		}
		expectedRelations[relationOID] = true
		if rowTypeOID != 0 {
			rowTypes[rowTypeOID] = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	rows, err = q.QueryContext(ctx, `SELECT t.oid::bigint,t.typrelid::bigint,t.typelem::bigint,t.typtype,t.typname FROM pg_type t WHERE t.typnamespace=$1 ORDER BY t.oid`, oid)
	if err != nil {
		return err
	}
	var userType string
	for rows.Next() {
		var typeOID, relationOID, elementOID int64
		var kind, name string
		if err := rows.Scan(&typeOID, &relationOID, &elementOID, &kind, &name); err != nil {
			rows.Close()
			return err
		}
		if rowTypes[typeOID] || (elementOID != 0 && rowTypes[elementOID]) {
			continue
		}
		if relationOID != 0 && expectedRelations[relationOID] {
			userType = name
			break
		}
		userType = name
		break
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if userType != "" {
		return fmt.Errorf("extra type %s in security_control", userType)
	}

	var collation string
	if err := q.QueryRowContext(ctx, `SELECT collname FROM pg_collation WHERE collnamespace=$1 ORDER BY collname LIMIT 1`, oid).Scan(&collation); err == nil {
		return fmt.Errorf("extra collation %s in security_control", collation)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return nil
}

func verifyD1LColumns(ctx context.Context, q d1lQueryer, oid int64, e d1lPhysicalExpectation) error {
	for table, want := range e.columns {
		rows, err := q.QueryContext(ctx, `SELECT a.attnum,a.attname,t.typname,a.atttypmod,a.attnotnull,a.attisdropped,a.attgenerated,a.attidentity,a.attmissingval,COALESCE(tn.nspname,''),COALESCE(cn.nspname,''),COALESCE(coll.collname,''),pg_get_expr(d.adbin,d.adrelid) FROM pg_attribute a JOIN pg_class c ON c.oid=a.attrelid JOIN pg_type t ON t.oid=a.atttypid JOIN pg_namespace tn ON tn.oid=t.typnamespace LEFT JOIN pg_collation coll ON coll.oid=a.attcollation LEFT JOIN pg_namespace cn ON cn.oid=coll.collnamespace LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum WHERE c.relnamespace=$1 AND c.relname=$2 AND a.attnum>0 ORDER BY a.attnum`, oid, table)
		if err != nil {
			return err
		}
		i := 0
		for rows.Next() {
			var attnum int
			var name, typ, gen, identity, typeSchema, collSchema, collName string
			var typmod int
			var nn, dropped bool
			var missing []byte
			var def sql.NullString
			if err := rows.Scan(&attnum, &name, &typ, &typmod, &nn, &dropped, &gen, &identity, &missing, &typeSchema, &collSchema, &collName, &def); err != nil {
				rows.Close()
				return err
			}
			if i >= len(want) {
				rows.Close()
				return fmt.Errorf("extra column %s.%s", table, name)
			}
			w := want[i]
			if attnum != i+1 || name != w.name || typ != w.typeName || typeSchema != w.typeSchema || typmod != w.typmod || nn != w.notNull || dropped || gen != "" || identity != "" || missing != nil {
				rows.Close()
				return fmt.Errorf("column physical definition %s.%s", table, name)
			}
			if collSchema != w.collationSchema || collName != w.collationName {
				rows.Close()
				return fmt.Errorf("column collation %s.%s", table, name)
			}
			if w.defaultExpr == "" {
				if def.Valid && strings.TrimSpace(def.String) != "" {
					rows.Close()
					return fmt.Errorf("unexpected column default %s.%s", table, name)
				}
			} else {
				if !def.Valid || strings.TrimSpace(def.String) == "" {
					rows.Close()
					return fmt.Errorf("missing column default %s.%s", table, name)
				}
				gotExpr, gotErr := SerializeD1LPGExpr(def.String)
				wantExpr, wantErr := SerializeD1LPGExpr(w.defaultExpr)
				if gotErr != nil || wantErr != nil || !bytes.Equal(gotExpr, wantExpr) {
					rows.Close()
					return fmt.Errorf("column default %s.%s", table, name)
				}
			}
			i++
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if i != len(want) {
			return fmt.Errorf("columns %s", table)
		}
	}
	return nil
}
func verifyD1LSequence(ctx context.Context, q d1lQueryer, oid int64) error {
	var seqOID, typOID, start, increment, min, max, cache int64
	var persistence string
	var tablespace int64
	var cycle bool
	var options []byte
	if err := q.QueryRowContext(ctx, `SELECT c.oid::bigint,s.seqtypid::bigint,s.seqstart,s.seqincrement,s.seqmin,s.seqmax,s.seqcache,s.seqcycle,c.relpersistence,c.reltablespace,c.reloptions FROM pg_class c JOIN pg_sequence s ON s.seqrelid=c.oid WHERE c.relnamespace=$1 AND c.relname='admission_generation_seq' AND c.relkind='S'`, oid).Scan(&seqOID, &typOID, &start, &increment, &min, &max, &cache, &cycle, &persistence, &tablespace, &options); err != nil {
		return fmt.Errorf("sequence definition: %w", err)
	}
	if persistence != "p" || tablespace != 0 || options != nil || start != 1 || increment != 1 || min != 1 || max != 9223372036854775807 || cache != 1 || cycle {
		return fmt.Errorf("sequence physical properties")
	}
	var typeName, typeSchema string
	if err := q.QueryRowContext(ctx, `SELECT t.typname,n.nspname FROM pg_type t JOIN pg_namespace n ON n.oid=t.typnamespace WHERE t.oid=$1`, typOID).Scan(&typeName, &typeSchema); err != nil {
		return err
	}
	if typeSchema != "pg_catalog" || typeName != "int8" {
		return fmt.Errorf("sequence type")
	}
	var owned int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM pg_depend d JOIN pg_class t ON t.oid=d.refobjid JOIN pg_attribute a ON a.attrelid=t.oid AND a.attnum=d.refobjsubid WHERE d.classid='pg_class'::regclass AND d.objid=$1 AND d.refclassid='pg_class'::regclass AND d.deptype='a' AND t.relnamespace=$2 AND t.relname='admission_leases' AND a.attname='generation'`, seqOID, oid).Scan(&owned); err != nil {
		return err
	}
	if owned != 1 {
		return fmt.Errorf("sequence ownership")
	}
	return nil
}
func verifyD1LConstraints(ctx context.Context, q d1lQueryer, oid int64, e d1lPhysicalExpectation) error {
	rows, err := q.QueryContext(ctx, `SELECT c.conname,c.contype,c.convalidated,r.relname,COALESCE(rn.nspname,''),COALESCE(rr.relname,''),COALESCE(rrn.nspname,''),pg_get_expr(c.conbin,c.conrelid),c.confdeltype,c.confupdtype,c.confmatchtype,c.condeferrable,c.condeferred,c.conkey::text,c.confkey::text,COALESCE(ix.relname,''),c.conindid::bigint FROM pg_constraint c JOIN pg_class r ON r.oid=c.conrelid JOIN pg_namespace rn ON rn.oid=r.relnamespace LEFT JOIN pg_class rr ON rr.oid=c.confrelid LEFT JOIN pg_namespace rrn ON rrn.oid=rr.relnamespace LEFT JOIN pg_class ix ON ix.oid=c.conindid AND ix.relnamespace=$1 WHERE c.connamespace=$1`, oid)
	if err != nil {
		return err
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var n, typ, rel, relSchema, refRel, refSchema, del, upd, match, index string
		var localKeys, refKeys, expr sql.NullString
		var valid, def, defd bool
		var indexOID int64
		if err := rows.Scan(&n, &typ, &valid, &rel, &relSchema, &refRel, &refSchema, &expr, &del, &upd, &match, &def, &defd, &localKeys, &refKeys, &index, &indexOID); err != nil {
			return err
		}
		want, ok := e.constraintDescriptors[n]
		if !ok || seen[n] || relSchema != "security_control" || want.table != rel || want.contype != typ || !valid || def || defd {
			return fmt.Errorf("constraint %s properties", n)
		}
		if want.contype == "c" {
			if !localKeys.Valid || localKeys.String != want.conkey || refKeys.Valid {
				return fmt.Errorf("constraint %s local keys", n)
			}
		} else if want.conkey == "" {
			if localKeys.Valid {
				return fmt.Errorf("constraint %s local keys", n)
			}
		} else if !localKeys.Valid || localKeys.String != want.conkey {
			return fmt.Errorf("constraint %s local keys", n)
		}
		if want.contype == "f" {
			if !refKeys.Valid || refKeys.String != want.confkey || refRel != want.referencedTable || refSchema != want.referencedSchema {
				return fmt.Errorf("constraint %s referenced keys", n)
			}
		} else if refKeys.Valid || refRel != "" || refSchema != "" {
			return fmt.Errorf("constraint %s referenced keys", n)
		}
		if want.contype == "f" && (del != want.confdeltype || upd != want.confupdtype || match != want.confmatchtype) {
			return fmt.Errorf("constraint %s referential actions", n)
		}
		if index != want.conindid {
			return fmt.Errorf("constraint %s physical properties", n)
		}
		if want.conindid == "" {
			if indexOID != 0 {
				return fmt.Errorf("constraint %s backing index", n)
			}
		} else if indexOID == 0 {
			return fmt.Errorf("constraint %s backing index", n)
		}
		if want.contype == "c" {
			if !expr.Valid {
				return fmt.Errorf("constraint expression %s is missing", n)
			}
			expected, expressionOK := e.constraintExpressions[n]
			if !expressionOK {
				return fmt.Errorf("constraint expression %s is not frozen", n)
			}
			gotExpr, expressionErr := SerializeD1LPGExpr(expr.String)
			wantExpr, expectedErr := SerializeD1LPGExpr(expected)
			if expressionErr != nil || expectedErr != nil || !bytes.Equal(gotExpr, wantExpr) {
				// PostgreSQL 15 deparses the frozen CHECK ... IN (...) forms
				// without an explicit text[] cast, while the bounded serializer
				// intentionally rejects that shape everywhere else. Permit only
				// the exact frozen deparse text here; the index verifier applies
				// the same identity-scoped boundary to its one frozen predicate.
				if strings.TrimSpace(expr.String) != strings.TrimSpace(expected) {
					return fmt.Errorf("constraint expression %s mismatch", n)
				}
			}
		} else if expr.Valid {
			return fmt.Errorf("unexpected constraint expression %s", n)
		}
		seen[n] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for n := range e.constraints {
		if !seen[n] {
			return fmt.Errorf("missing constraint %s", n)
		}
	}
	return nil
}
func verifyD1LDependencies(ctx context.Context, q d1lQueryer, oid int64, _ d1lPhysicalExpectation) error {
	rows, err := q.QueryContext(ctx, `WITH expected_control AS (
		SELECT c.oid
		FROM pg_class c
		WHERE c.relnamespace=$1
		  AND c.relname IN ('control_schema_migrations','admission_leases','admission_boundaries','admission_generation_seq',
			'control_schema_migrations_pkey','control_schema_migrations_install_id_key',
			'admission_leases_pkey','admission_leases_attempt_id_key','admission_leases_operation_generation_key','admission_leases_identity_key','admission_leases_one_live_generation_idx',
			'admission_boundaries_pkey','admission_boundaries_boundary_nonce_key','admission_boundaries_lease_identity_name_key','admission_boundaries_one_open_per_lease_idx',
			'admission_provenance','admission_provenance_pkey','admission_provenance_issue_key','admission_provenance_attempt_id_key','admission_provenance_lease_key','admission_provenance_available_identity','admission_provenance_reserved_identity')
	), expected_tables AS (
		SELECT oid
		FROM pg_class
		WHERE relnamespace=$1
		  AND relname IN ('control_schema_migrations','admission_leases','admission_boundaries','admission_provenance')
	), generated_types AS (
		SELECT t.oid
		FROM pg_type t
		JOIN expected_control e ON e.oid=t.typrelid
		UNION
		SELECT a.oid
		FROM pg_type a
		JOIN pg_type row_type ON row_type.oid=a.typelem
		JOIN expected_control e ON e.oid=row_type.typrelid
	), sources AS (
		SELECT 'pg_class'::regclass AS classid, c.oid AS objid
		FROM pg_class c
		WHERE c.relnamespace=$1
		UNION
		SELECT 'pg_constraint'::regclass, c.oid
		FROM pg_constraint c
		WHERE c.connamespace=$1
		UNION
		SELECT 'pg_attrdef'::regclass, d.oid
		FROM pg_attrdef d
		JOIN expected_tables t ON t.oid=d.adrelid
		UNION
		SELECT 'pg_trigger'::regclass, t.oid
		FROM pg_trigger t
		JOIN expected_tables r ON r.oid=t.tgrelid
	)
	SELECT CASE
		WHEN d.refclassid='pg_namespace'::regclass THEN 'pg_namespace'
		WHEN d.refclassid='pg_class'::regclass THEN 'pg_class'
		WHEN d.refclassid='pg_type'::regclass THEN 'pg_type'
		WHEN d.refclassid='pg_proc'::regclass THEN 'pg_proc'
		WHEN d.refclassid='pg_collation'::regclass THEN 'pg_collation'
		WHEN d.refclassid='pg_operator'::regclass THEN 'pg_operator'
		WHEN d.refclassid='pg_opclass'::regclass THEN 'pg_opclass'
		ELSE 'other'
	END AS refclass,
	d.refobjid::bigint,
	COALESCE(rn.nspname,''),
	CASE
		WHEN d.refclassid='pg_namespace'::regclass THEN rn.nspname
		WHEN d.refclassid='pg_class'::regclass THEN rc.relname
		WHEN d.refclassid='pg_type'::regclass THEN rt.typname
		WHEN d.refclassid='pg_proc'::regclass THEN rp.proname
		WHEN d.refclassid='pg_collation'::regclass THEN rco.collname
		WHEN d.refclassid='pg_operator'::regclass THEN ro.oprname
		WHEN d.refclassid='pg_opclass'::regclass THEN roc.opcname
		ELSE ''
	END
	FROM pg_depend d
	JOIN sources s ON s.classid=d.classid AND s.objid=d.objid
	LEFT JOIN pg_class rc ON d.refclassid='pg_class'::regclass AND rc.oid=d.refobjid
	LEFT JOIN pg_type rt ON d.refclassid='pg_type'::regclass AND rt.oid=d.refobjid
	LEFT JOIN pg_proc rp ON d.refclassid='pg_proc'::regclass AND rp.oid=d.refobjid
	LEFT JOIN pg_collation rco ON d.refclassid='pg_collation'::regclass AND rco.oid=d.refobjid
	LEFT JOIN pg_operator ro ON d.refclassid='pg_operator'::regclass AND ro.oid=d.refobjid
	LEFT JOIN pg_opclass roc ON d.refclassid='pg_opclass'::regclass AND roc.oid=d.refobjid
	LEFT JOIN pg_namespace rn ON rn.oid = CASE
		WHEN d.refclassid='pg_namespace'::regclass THEN d.refobjid
		WHEN d.refclassid='pg_class'::regclass THEN rc.relnamespace
		WHEN d.refclassid='pg_type'::regclass THEN rt.typnamespace
		WHEN d.refclassid='pg_proc'::regclass THEN rp.pronamespace
		WHEN d.refclassid='pg_collation'::regclass THEN rco.collnamespace
		WHEN d.refclassid='pg_operator'::regclass THEN ro.oprnamespace
		WHEN d.refclassid='pg_opclass'::regclass THEN roc.opcnamespace
	END
	WHERE rn.nspname IS NOT NULL
	  AND NOT (
		(d.refclassid='pg_namespace'::regclass AND d.refobjid IN ($1,'pg_catalog'::regnamespace))
		OR (d.refclassid='pg_class'::regclass AND (rn.nspname IN ('pg_catalog','pg_toast') OR (rn.nspname='security_control' AND d.refobjid IN (SELECT oid FROM expected_control))))
		OR (d.refclassid='pg_type'::regclass AND (rn.nspname='pg_catalog' OR d.refobjid IN (SELECT oid FROM generated_types)))
		OR (d.refclassid IN ('pg_proc'::regclass,'pg_collation'::regclass,'pg_operator'::regclass,'pg_opclass'::regclass) AND rn.nspname='pg_catalog')
	  )
	ORDER BY refclass,d.refobjid
	LIMIT 1`, oid)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		var class, schema, name string
		var objectOID int64
		if err := rows.Scan(&class, &objectOID, &schema, &name); err != nil {
			return err
		}
		return fmt.Errorf("unexpected user dependency %s.%s (%s oid %d)", schema, name, class, objectOID)
	}
	return rows.Err()
}

func verifyD1LIndexKeys(ctx context.Context, q d1lQueryer, oid int64, index string, want d1lIndexDescriptor) error {
	rows, err := q.QueryContext(ctx, `SELECT s,COALESCE(a.attname,''),COALESCE(opn.nspname,''),COALESCE(opc.opcname,''),COALESCE(cn.nspname,''),COALESCE(coll.collname,''),x.indoption[s] FROM pg_index x JOIN pg_class i ON i.oid=x.indexrelid JOIN pg_class t ON t.oid=x.indrelid CROSS JOIN LATERAL generate_subscripts(x.indkey,1) s LEFT JOIN pg_attribute a ON a.attrelid=x.indrelid AND a.attnum=x.indkey[s] LEFT JOIN pg_opclass opc ON opc.oid=x.indclass[s] LEFT JOIN pg_namespace opn ON opn.oid=opc.opcnamespace LEFT JOIN pg_collation coll ON coll.oid=x.indcollation[s] LEFT JOIN pg_namespace cn ON cn.oid=coll.collnamespace WHERE t.relnamespace=$1 AND i.relname=$2 ORDER BY s`, oid, index)
	if err != nil {
		return err
	}
	defer rows.Close()
	pos := 0
	for rows.Next() {
		var n int
		var key, opSchema, opclass, collSchema, collName string
		var option int
		if err := rows.Scan(&n, &key, &opSchema, &opclass, &collSchema, &collName, &option); err != nil {
			return err
		}
		if pos >= len(want.keys) || n != pos || key != want.keys[pos] || opSchema != "pg_catalog" || opclass != want.opclasses[pos] || option != 0 {
			return fmt.Errorf("index %s key definition", index)
		}
		wantCollation := ""
		if pos < len(want.collations) {
			wantCollation = want.collations[pos]
		}
		if wantCollation == "" {
			if collSchema != "" || collName != "" {
				return fmt.Errorf("index %s collation", index)
			}
		} else if collSchema != "pg_catalog" || collName != wantCollation {
			return fmt.Errorf("index %s collation", index)
		}
		pos++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if pos != len(want.keys) {
		return fmt.Errorf("index %s key count", index)
	}
	return nil
}

func verifyD1LIndexes(ctx context.Context, q d1lQueryer, oid int64, e d1lPhysicalExpectation) error {
	rows, err := q.QueryContext(ctx, `SELECT i.relname,t.relname,am.amname,x.indisunique,x.indisprimary,x.indisvalid,x.indisready,x.indislive,x.indnkeyatts,x.indnatts,x.indkey,x.indoption,x.indcollation,x.indclass,pg_get_expr(x.indpred,x.indrelid),i.relpersistence,i.reltablespace,i.reloptions FROM pg_index x JOIN pg_class i ON i.oid=x.indexrelid JOIN pg_class t ON t.oid=x.indrelid JOIN pg_am am ON am.oid=i.relam WHERE t.relnamespace=$1`, oid)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	var keyChecks []struct {
		name string
		desc d1lIndexDescriptor
	}
	for rows.Next() {
		var n, t, am, p string
		var pred sql.NullString
		var uniq, pri, valid, ready, live bool
		var nk, na int
		var key, opt, coll, cls []byte
		var ts int64
		var opts []byte
		if err := rows.Scan(&n, &t, &am, &uniq, &pri, &valid, &ready, &live, &nk, &na, &key, &opt, &coll, &cls, &pred, &p, &ts, &opts); err != nil {
			return err
		}
		want, ok := e.indexes[n]
		if !ok {
			return fmt.Errorf("extra index %s", n)
		}
		desc, descriptorOK := e.indexDescriptors[n]
		if seen[n] || !descriptorOK || want != t || am != "btree" || !valid || !ready || !live || p != "p" || ts != 0 || opts != nil || nk != na || uniq != desc.unique || pri != desc.primary || nk != len(desc.keys) {
			return fmt.Errorf("index %s physical properties", n)
		}
		if (desc.predicate != "") != pred.Valid {
			return fmt.Errorf("index %s predicate", n)
		}
		if pred.Valid {
			gotExpr, gotErr := SerializeD1LPGExpr(pred.String)
			wantExpr, wantErr := SerializeD1LPGExpr(desc.predicate)
			if gotErr != nil || wantErr != nil || !bytes.Equal(gotExpr, wantExpr) {
				if e.indexDeparse[n] != strings.TrimSpace(pred.String) {
					return fmt.Errorf("index %s predicate", n)
				}
			}
		}
		keyChecks = append(keyChecks, struct {
			name string
			desc d1lIndexDescriptor
		}{n, desc})
		seen[n] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, check := range keyChecks {
		if err := verifyD1LIndexKeys(ctx, q, oid, check.name, check.desc); err != nil {
			return err
		}
	}
	for n := range e.indexes {
		if !seen[n] {
			return fmt.Errorf("missing index %s", n)
		}
	}
	return nil
}
