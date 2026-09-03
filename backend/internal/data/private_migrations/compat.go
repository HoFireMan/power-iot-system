package migrations

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"strings"
	"time"

	publicmigrations "power-iot-backend/internal/data/migrations"

	"github.com/golang-migrate/migrate/v4/database/postgres"
)

// The private migration authority reuses the public package's generic parser,
// metadata observation, SQL source, and writer fence. It owns only the
// protected D1-L/reconciliation operations in this package.
const (
	protectedSchemaVersion = 5
	cleanupTimeout         = 10 * time.Second
	unlockWriterFenceSQL   = "SELECT pg_advisory_unlock($1::bigint)"
	WriterFenceKey         = publicmigrations.WriterFenceKey
)

// Files contains only protected migration SQL. Public application bootstrap
// embeds the independent 000001-000005 source under the public package.
//
//go:embed sql/*.sql
var Files embed.FS

type migrationMetadataQueryer = publicmigrations.MigrationMetadataQueryer
type MigrationMetadataSnapshot = publicmigrations.MigrationMetadataSnapshot
type ExclusiveWriterFence = publicmigrations.ExclusiveWriterFence
type ExclusiveOwnershipState = publicmigrations.ExclusiveOwnershipState
type ProtectedWorkCapability = publicmigrations.ProtectedWorkCapability

const (
	ExclusiveNotAttempted = publicmigrations.ExclusiveNotAttempted
	ExclusiveWaiting      = publicmigrations.ExclusiveWaiting
	ExclusiveOwned        = publicmigrations.ExclusiveOwned
	ExclusiveReleased     = publicmigrations.ExclusiveReleased
	ExclusiveUnknown      = publicmigrations.ExclusiveUnknown
)

type WriterFenceDecision = publicmigrations.WriterFenceDecision
type WriterFenceDecisionStatus = publicmigrations.WriterFenceDecisionStatus
type WriterFenceReasonCode = publicmigrations.WriterFenceReasonCode
type WriterFenceGate = publicmigrations.WriterFenceGate
type WriterFenceMechanism = publicmigrations.WriterFenceMechanism

const WriterFenceEnforced = publicmigrations.WriterFenceEnforced

type parsedPostgresDatabaseURL struct {
	driverURL string
	config    *postgres.Config
}

func parsePostgresDatabaseURL(databaseURL string) (*parsedPostgresDatabaseURL, error) {
	parsed, err := publicmigrations.ParsePostgresDatabaseURL(databaseURL)
	if err != nil {
		return nil, err
	}
	return &parsedPostgresDatabaseURL{driverURL: parsed.DriverURL, config: parsed.Config}, nil
}

func migrationMetadataIdentifiers(config *postgres.Config, schemaName string) (string, string, error) {
	return publicmigrations.MigrationMetadataIdentifiers(config, schemaName)
}

func quotedMigrationTable(schemaName, tableName string) string {
	return publicmigrations.QuotedMigrationTable(schemaName, tableName)
}

func inspectMigrationMetadataOn(ctx context.Context, q migrationMetadataQueryer, config *postgres.Config) (MigrationMetadataSnapshot, error) {
	return publicmigrations.InspectMigrationMetadata(ctx, q, config)
}

func AcquireSharedWriterFence(ctx context.Context, tx *sql.Tx) error {
	return publicmigrations.AcquireSharedWriterFence(ctx, tx)
}

func pinExclusiveWriterFence(ctx context.Context, parsed *parsedPostgresDatabaseURL) (*ExclusiveWriterFence, error) {
	return publicmigrations.PinExclusiveWriterFence(ctx, parsed.driverURL)
}

func openExclusiveWriterFence(ctx context.Context, parsed *parsedPostgresDatabaseURL) (*ExclusiveWriterFence, error) {
	return publicmigrations.OpenExclusiveWriterFence(ctx, parsed.driverURL)
}

func inspectMigrationMetadata(ctx context.Context, conn *sql.Conn, config *postgres.Config) (MigrationMetadataSnapshot, error) {
	return publicmigrations.InspectMigrationMetadata(ctx, conn, config)
}

func OpenExclusiveWriterFence(ctx context.Context, dsn string) (*ExclusiveWriterFence, error) {
	return publicmigrations.OpenExclusiveWriterFence(ctx, dsn)
}

func WithExclusiveWriterFence(ctx context.Context, dsn string, work func(*ExclusiveWriterFence) error) error {
	return publicmigrations.WithExclusiveWriterFence(ctx, dsn, work)
}

func AssessSecuritySchemaWriterFence() WriterFenceDecision {
	return publicmigrations.AssessSecuritySchemaWriterFence()
}

func RequireProtectedWork(capability ProtectedWorkCapability) error {
	return publicmigrations.RequireProtectedWork(capability)
}

var (
	ErrWriterFenceDecisionRequired       = publicmigrations.ErrWriterFenceDecisionRequired
	ErrWriterFenceNotOwned               = publicmigrations.ErrWriterFenceNotOwned
	ErrWriterFenceUnlockFailed           = publicmigrations.ErrWriterFenceUnlockFailed
	ErrPhysicalConnectionDiscardRequired = publicmigrations.ErrPhysicalConnectionDiscardRequired
	ErrSharedWriterTransactionRequired   = publicmigrations.ErrSharedWriterTransactionRequired
)

type ExternalWriterAdmission struct {
	ManagedCooperativeWriters bool
	DirectSQLControlled       bool
	OperationalDrainEvidence  bool
	evidence                  *externalWriterAdmissionEvidence
}

type externalWriterAdmissionEvidence struct {
	managedCooperativeWriters bool
	directSQLControlled       bool
	operationalDrainEvidence  bool
}

var ErrExternalWriterAdmissionRequired = errors.New("protected work requires external writer drain/deny evidence")

func AssessExternalWriterAdmission() ExternalWriterAdmission {
	return ExternalWriterAdmission{ManagedCooperativeWriters: true}
}

func RequireExternalWriterAdmission(admission ExternalWriterAdmission) error {
	proof := admission.evidence
	if proof == nil || !proof.managedCooperativeWriters || !proof.directSQLControlled || !proof.operationalDrainEvidence {
		return ErrExternalWriterAdmissionRequired
	}
	return nil
}

var ErrCleanV5MetadataRequired = errors.New("security reconciliation requires clean v5 migration metadata")

func Bootstrap(databaseURL string) error { return publicmigrations.Bootstrap(databaseURL) }

var (
	ErrD1LGenericRoute = errors.New("generic migration route is reserved for the private migration authority")
	ErrGuardedDown     = publicmigrations.ErrGuardedDown
)

func rejectD1LGenericRoute(ctx context.Context, q migrationMetadataQueryer, config *postgres.Config) error {
	if config != nil && config.MigrationsTableQuoted {
		schema, table, ok := parseQuotedMigrationTable(config.MigrationsTable)
		if ok && schema == "security_control" && table == "control_schema_migrations" {
			return ErrD1LGenericRoute
		}
	}
	if q == nil {
		return errors.New("private migration route inspection requires a queryer")
	}
	var present bool
	if err := q.QueryRowContext(ctx, "SELECT to_regclass('security_control.control_schema_migrations') IS NOT NULL").Scan(&present); err != nil {
		return err
	}
	if present {
		return ErrD1LGenericRoute
	}
	return nil
}

func parseQuotedMigrationTable(value string) (string, string, bool) {
	if len(value) < 2 || value[0] != '"' {
		return "", "", false
	}
	first := strings.IndexByte(value[1:], '"')
	if first < 0 {
		return "", "", false
	}
	first++
	if first == len(value)-1 {
		return "", value[1:first], true
	}
	if value[first+1] != '.' || first+2 >= len(value) || value[first+2] != '"' {
		return "", "", false
	}
	secondStart := first + 2
	second := strings.IndexByte(value[secondStart+1:], '"')
	if second < 0 {
		return "", "", false
	}
	second += secondStart + 1
	if second != len(value)-1 {
		return "", "", false
	}
	return value[1:first], value[secondStart+1 : second], true
}

func privateRouteAllowed(databaseURL string) error {
	parsed, err := parsePostgresDatabaseURL(databaseURL)
	if err != nil {
		return err
	}
	db, err := sql.Open("postgres", parsed.driverURL)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return rejectD1LGenericRoute(ctx, conn, parsed.config)
}

func Up(databaseURL string) error {
	if err := privateRouteAllowed(databaseURL); err != nil {
		return err
	}
	return publicmigrations.Up(databaseURL)
}

func Down(databaseURL string) error {
	if err := privateRouteAllowed(databaseURL); err != nil {
		return err
	}
	return publicmigrations.Down(databaseURL)
}

func Version(databaseURL string) (uint, bool, error) {
	if err := privateRouteAllowed(databaseURL); err != nil {
		return 0, false, err
	}
	return publicmigrations.Version(databaseURL)
}

func trustedExternalWriterAdmissionForTest() ExternalWriterAdmission {
	return ExternalWriterAdmission{
		ManagedCooperativeWriters: true,
		DirectSQLControlled:       true,
		OperationalDrainEvidence:  true,
		evidence: &externalWriterAdmissionEvidence{
			managedCooperativeWriters: true,
			directSQLControlled:       true,
			operationalDrainEvidence:  true,
		},
	}
}

func ConfiguredMigrationTable(ctx context.Context, databaseURL string, conn *sql.Conn) (string, error) {
	return publicmigrations.ConfiguredMigrationTable(ctx, databaseURL, conn)
}
