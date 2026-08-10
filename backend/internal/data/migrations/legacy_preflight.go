package migrations

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
	"time"
)

// PreflightStatus is a fail-closed classification for either an individual
// legacy fact or the complete preflight inventory.
type PreflightStatus string

const (
	PreflightReadyFacts             PreflightStatus = "READY_FACTS"
	PreflightReconciliationRequired PreflightStatus = "RECONCILIATION_REQUIRED"
	PreflightBlockingIntegrity      PreflightStatus = "BLOCKING_INTEGRITY"
)

// SnapshotMode describes the database snapshot used for the inventory.
type SnapshotMode string

const SnapshotRepeatableReadOnly SnapshotMode = "REPEATABLE_READ_READ_ONLY"

// AuthorityRepresentation deliberately has only the legacy-safe state. The
// zero value is intentional: migration 000004 represents neither account
// eligibility nor current Device owner authority.
type AuthorityRepresentation uint8

const RepresentationNotRepresented AuthorityRepresentation = iota

func (r AuthorityRepresentation) String() string {
	return "NOT_REPRESENTED"
}

func (r AuthorityRepresentation) MarshalText() ([]byte, error) {
	return []byte(r.String()), nil
}

// WriterFenceClassification does not claim that this read-only inventory
// fenced concurrent writers. Its zero value is the only valid classification.
type WriterFenceClassification uint8

const WriterFenceRequiresMigrationOrchestration WriterFenceClassification = iota

func (w WriterFenceClassification) String() string {
	return "WRITER_FENCE_REQUIRES_MIGRATION_ORCHESTRATION"
}

func (w WriterFenceClassification) MarshalText() ([]byte, error) {
	return []byte(w.String()), nil
}

var (
	ErrDirtyMigrationState          = errors.New("security schema preflight found a dirty migration state")
	ErrUnexpectedMigrationVersion   = errors.New("security schema preflight found an unexpected migration version")
	ErrMigrationMetadataCardinality = errors.New("security schema preflight found invalid migration metadata cardinality")
)

// MigrationStateReport records the embedded migration contract and the state
// observed inside the same read-only snapshot as the legacy facts.
type MigrationStateReport struct {
	ExpectedVersion  int  `json:"expected_version"`
	ActualVersion    int  `json:"actual_version"`
	Dirty            bool `json:"dirty"`
	MetadataRowCount int  `json:"metadata_row_count"`
}

type ShopClientFact struct {
	ShopID       int64           `json:"shop_id"`
	ClientID     *int64          `json:"client_id,omitempty"`
	ClientExists bool            `json:"client_exists"`
	Disposition  PreflightStatus `json:"disposition"`
	Reason       string          `json:"reason,omitempty"`
}

type ShopClientReport struct {
	TotalCount          int64            `json:"total_count"`
	ReadyCount          int64            `json:"ready_count"`
	NullClientIDCount   int64            `json:"null_client_id_count"`
	OrphanClientIDCount int64            `json:"orphan_client_id_count"`
	Facts               []ShopClientFact `json:"facts"`
}

type MembershipFact struct {
	RelationID                 int64           `json:"relation_id"`
	UserID                     int64           `json:"user_id"`
	ShopID                     int64           `json:"shop_id"`
	UserExists                 bool            `json:"user_exists"`
	ShopExists                 bool            `json:"shop_exists"`
	ShopClientID               *int64          `json:"shop_client_id,omitempty"`
	ShopClientExists           bool            `json:"shop_client_exists"`
	ShopRole                   string          `json:"shop_role"`
	DuplicateLogicalMembership bool            `json:"duplicate_logical_membership"`
	Disposition                PreflightStatus `json:"disposition"`
	Reason                     string          `json:"reason,omitempty"`
}

type MembershipReport struct {
	TotalCount                      int64            `json:"total_count"`
	ReadyCount                      int64            `json:"ready_count"`
	OrphanUserCount                 int64            `json:"orphan_user_count"`
	OrphanShopCount                 int64            `json:"orphan_shop_count"`
	InvalidShopClientCount          int64            `json:"invalid_shop_client_count"`
	DuplicateLogicalMembershipCount int64            `json:"duplicate_logical_membership_count"`
	Facts                           []MembershipFact `json:"facts"`
}

type CurrentShopFact struct {
	UserID           int64           `json:"user_id"`
	CurrentShopID    *int64          `json:"current_shop_id,omitempty"`
	ShopExists       bool            `json:"shop_exists"`
	ClientID         *int64          `json:"client_id,omitempty"`
	ClientExists     bool            `json:"client_exists"`
	MembershipExists bool            `json:"membership_exists"`
	Disposition      PreflightStatus `json:"disposition"`
	Reason           string          `json:"reason,omitempty"`
}

type CurrentShopReport struct {
	TotalCount             int64             `json:"total_count"`
	ReadyCount             int64             `json:"ready_count"`
	NullCurrentShopIDCount int64             `json:"null_current_shop_id_count"`
	StaleReferenceCount    int64             `json:"stale_reference_count"`
	InvalidShopClientCount int64             `json:"invalid_shop_client_count"`
	Facts                  []CurrentShopFact `json:"facts"`
}

type UserEligibilityFact struct {
	ID                  int64  `json:"id"`
	PasswordHashPresent bool   `json:"password_hash_present"`
	IsAdmin             bool   `json:"is_admin"`
	AutoApproved        bool   `json:"auto_approved"`
	ReviewRequired      bool   `json:"review_required"`
	Reason              string `json:"reason,omitempty"`
}

type UserEligibilityReport struct {
	TotalCount               int64                 `json:"total_count"`
	ReviewRequiredCount      int64                 `json:"review_required_count"`
	AuthEnabledColumnPresent bool                  `json:"auth_enabled_column_present"`
	Facts                    []UserEligibilityFact `json:"facts"`
}

type DeviceOwnershipClassification string

const (
	DeviceOwnerCandidate        DeviceOwnershipClassification = "DEVICE_OWNER_CANDIDATE"
	DeviceManualMappingRequired DeviceOwnershipClassification = "MANUAL_MAPPING_REQUIRED"
)

type DeviceOwnershipFact struct {
	DeviceID                        int64                         `json:"device_id"`
	CompatibilityShopID             int64                         `json:"compatibility_shop_id"`
	CompatibilityShopReferenceValid bool                          `json:"compatibility_shop_reference_valid"`
	CompatibilityShopReferenceUsed  bool                          `json:"compatibility_shop_reference_used"`
	ActiveAssignmentCount           int64                         `json:"active_assignment_count"`
	HistoricalAssignmentCount       int64                         `json:"historical_assignment_count"`
	FutureAssignmentCount           int64                         `json:"future_assignment_count"`
	CandidateClientID               *int64                        `json:"candidate_client_id,omitempty"`
	Classification                  DeviceOwnershipClassification `json:"classification"`
	Disposition                     PreflightStatus               `json:"disposition"`
	Reason                          string                        `json:"reason,omitempty"`
}

type DeviceOwnershipReport struct {
	TotalCount                 int64                 `json:"total_count"`
	OwnerCandidateCount        int64                 `json:"owner_candidate_count"`
	ManualMappingRequiredCount int64                 `json:"manual_mapping_required_count"`
	BlockingIntegrityCount     int64                 `json:"blocking_integrity_count"`
	Facts                      []DeviceOwnershipFact `json:"facts"`
}

type AssignmentIntegrityFact struct {
	AssignmentID                string          `json:"assignment_id"`
	DeviceID                    int64           `json:"device_id"`
	ActiveDeviceAssignmentCount int64           `json:"active_device_assignment_count"`
	ActiveMeasurementPointCount int64           `json:"active_measurement_point_count"`
	MeasurementPointID          string          `json:"measurement_point_id"`
	ShopID                      *int64          `json:"shop_id,omitempty"`
	ClientID                    *int64          `json:"client_id,omitempty"`
	ValidFrom                   time.Time       `json:"valid_from"`
	ValidTo                     *time.Time      `json:"valid_to,omitempty"`
	Active                      bool            `json:"active"`
	FutureStart                 bool            `json:"future_start"`
	Disposition                 PreflightStatus `json:"disposition"`
	Reason                      string          `json:"reason,omitempty"`
}

type AssignmentIntegrityReport struct {
	TotalCount             int64                     `json:"total_count"`
	ActiveCount            int64                     `json:"active_count"`
	ReadyCount             int64                     `json:"ready_count"`
	BlockingIntegrityCount int64                     `json:"blocking_integrity_count"`
	Facts                  []AssignmentIntegrityFact `json:"facts"`
}

type ProvenanceClassification string

const (
	ProvenanceIndependentlyDerived ProvenanceClassification = "INDEPENDENTLY_DERIVED"
	ProvenanceLegacyUnresolved     ProvenanceClassification = "LEGACY_UNRESOLVED"
)

type ProvenanceFact struct {
	ID                          string                   `json:"id"`
	CandidateClientID           *int64                   `json:"candidate_client_id,omitempty"`
	ExplicitRelationalFactCount int64                    `json:"explicit_relational_fact_count"`
	Classification              ProvenanceClassification `json:"classification"`
	Disposition                 PreflightStatus          `json:"disposition"`
	Reason                      string                   `json:"reason,omitempty"`
}

type ProvenanceReport struct {
	TotalCount                int64            `json:"total_count"`
	IndependentlyDerivedCount int64            `json:"independently_derived_count"`
	UnresolvedCount           int64            `json:"unresolved_count"`
	Facts                     []ProvenanceFact `json:"facts"`
}

// LegacyDataPreflightResult is a read-only inventory. Candidate fields are
// migration inputs, never authorization or authentication approvals.
type LegacyDataPreflightResult struct {
	ObservedAt           time.Time                 `json:"observed_at"`
	SnapshotMode         SnapshotMode              `json:"snapshot_mode"`
	ReadOnlyVerified     bool                      `json:"read_only_verified"`
	Migration            MigrationStateReport      `json:"migration"`
	ShopClient           ShopClientReport          `json:"shop_client"`
	Membership           MembershipReport          `json:"membership"`
	CurrentShop          CurrentShopReport         `json:"current_shop"`
	Users                UserEligibilityReport     `json:"users"`
	Devices              DeviceOwnershipReport     `json:"devices"`
	Assignments          AssignmentIntegrityReport `json:"assignments"`
	AuditProvenance      ProvenanceReport          `json:"audit_provenance"`
	OperationProvenance  ProvenanceReport          `json:"operation_provenance"`
	AccountEligibility   AuthorityRepresentation   `json:"account_eligibility"`
	DeviceOwnerAuthority AuthorityRepresentation   `json:"device_owner_authority"`
	WriterFence          WriterFenceClassification `json:"writer_fence"`
}

// Disposition gives integrity blockers precedence. Even a clean snapshot still
// requires reconciliation/orchestration because this operation cannot fence
// writers and migration 000004 has no account-eligibility or owner authority.
func (r LegacyDataPreflightResult) Disposition() PreflightStatus {
	if r.Migration.MetadataRowCount != 1 || r.Migration.Dirty || r.Migration.ActualVersion != r.Migration.ExpectedVersion ||
		r.ShopClient.NullClientIDCount > 0 || r.ShopClient.OrphanClientIDCount > 0 ||
		r.Membership.OrphanUserCount > 0 || r.Membership.OrphanShopCount > 0 ||
		r.Membership.InvalidShopClientCount > 0 || r.Membership.DuplicateLogicalMembershipCount > 0 ||
		r.CurrentShop.StaleReferenceCount > 0 || r.CurrentShop.InvalidShopClientCount > 0 ||
		r.Devices.BlockingIntegrityCount > 0 || r.Assignments.BlockingIntegrityCount > 0 {
		return PreflightBlockingIntegrity
	}
	return PreflightReconciliationRequired
}

type migrationMetadata struct {
	version int
	dirty   bool
}

func classifyMigrationMetadata(rows []migrationMetadata, expected int) error {
	if len(rows) != 1 {
		return fmt.Errorf("%w: rows=%d", ErrMigrationMetadataCardinality, len(rows))
	}
	return classifyMigrationState(rows[0].version, rows[0].dirty, expected)
}

func classifyMigrationState(actual int, dirty bool, expected int) error {
	if dirty {
		return fmt.Errorf("%w: version=%d", ErrDirtyMigrationState, actual)
	}
	if actual != expected {
		return fmt.Errorf("%w: actual=%d expected=%d", ErrUnexpectedMigrationVersion, actual, expected)
	}
	return nil
}

func latestEmbeddedMigrationVersion() (int, error) {
	entries, err := fs.ReadDir(Files, "sql")
	if err != nil {
		return 0, fmt.Errorf("read embedded migrations: %w", err)
	}

	latest := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		prefix, _, ok := strings.Cut(name, "_")
		if !ok {
			return 0, fmt.Errorf("invalid embedded migration filename %q", name)
		}
		version, err := strconv.Atoi(prefix)
		if err != nil || version <= 0 {
			return 0, fmt.Errorf("invalid embedded migration version in %q", name)
		}
		if version > latest {
			latest = version
		}
	}
	if latest == 0 {
		return 0, errors.New("no embedded up migrations found")
	}
	return latest, nil
}

// MarshalJSON implementations for the zero-valued, closed enums keep the
// public structured report explicit rather than exposing their numeric form.
func (r AuthorityRepresentation) MarshalJSON() ([]byte, error) {
	return json.Marshal(r.String())
}

func (w WriterFenceClassification) MarshalJSON() ([]byte, error) {
	return json.Marshal(w.String())
}
