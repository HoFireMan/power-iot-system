package migrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-migrate/migrate/v4/database/postgres"
)

// RunSecuritySchemaPreflight inventories migration inputs from one PostgreSQL
// REPEATABLE READ, READ ONLY snapshot after shared admission. It is diagnostic
// only and never authorizes protected work.
func RunSecuritySchemaPreflight(ctx context.Context, dsn string) (LegacyDataPreflightResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	result := LegacyDataPreflightResult{
		SnapshotMode:         SnapshotRepeatableReadOnly,
		AccountEligibility:   RepresentationNotRepresented,
		DeviceOwnerAuthority: RepresentationNotRepresented,
		WriterFence:          WriterFenceRequiresMigrationOrchestration,
		WriterFenceDecision:  AssessSecuritySchemaWriterFence(),
	}

	parsed, err := parsePostgresDatabaseURL(dsn)
	if err != nil {
		return result, err
	}
	expectedVersion, err := latestEmbeddedMigrationVersion()
	if err != nil {
		return result, err
	}
	result.Migration.ExpectedVersion = expectedVersion

	db, err := sql.Open("postgres", parsed.driverURL)
	if err != nil {
		return result, fmt.Errorf("open PostgreSQL for security schema preflight: %w", err)
	}
	defer db.Close()

	tx, err := db.BeginTx(ctx, &sql.TxOptions{
		Isolation: sql.LevelRepeatableRead,
		ReadOnly:  true,
	})
	if err != nil {
		return result, fmt.Errorf("begin security schema preflight snapshot: %w", err)
	}
	defer tx.Rollback()
	if err := AcquireSharedWriterFence(ctx, tx); err != nil {
		return result, fmt.Errorf("admit security schema preflight snapshot: %w", err)
	}

	var isolation, readOnly string
	if err := tx.QueryRowContext(ctx, `
		SELECT current_setting('transaction_isolation'),
		       current_setting('transaction_read_only'),
		       transaction_timestamp()`).Scan(&isolation, &readOnly, &result.ObservedAt); err != nil {
		return result, fmt.Errorf("verify security schema preflight snapshot: %w", err)
	}
	result.ObservedAt = result.ObservedAt.UTC()
	result.ReadOnlyVerified = strings.EqualFold(isolation, "repeatable read") && strings.EqualFold(readOnly, "on")
	if !result.ReadOnlyVerified {
		return result, fmt.Errorf("security schema preflight requires repeatable read/read only; got isolation=%q read_only=%q", isolation, readOnly)
	}

	metadataRows, err := readMigrationMetadata(ctx, tx, parsed.config)
	if err != nil {
		return result, err
	}
	result.Migration.MetadataRowCount = len(metadataRows)
	if len(metadataRows) == 1 {
		result.Migration.ActualVersion = metadataRows[0].version
		result.Migration.Dirty = metadataRows[0].dirty
	} else {
		result.Migration.ActualVersion = -1
	}
	if stateErr := classifyMigrationMetadata(metadataRows, expectedVersion); stateErr != nil {
		if commitErr := tx.Commit(); commitErr != nil {
			return result, errors.Join(stateErr, fmt.Errorf("finish security schema preflight snapshot: %w", commitErr))
		}
		return result, stateErr
	}

	if err := collectShopClientFacts(ctx, tx, &result.ShopClient); err != nil {
		return result, err
	}
	if err := collectMembershipFacts(ctx, tx, &result.Membership); err != nil {
		return result, err
	}
	if err := collectCurrentShopFacts(ctx, tx, &result.CurrentShop); err != nil {
		return result, err
	}
	if err := collectUserEligibilityFacts(ctx, tx, &result.Users); err != nil {
		return result, err
	}
	if err := collectAssignmentFacts(ctx, tx, &result.Assignments, result.ObservedAt); err != nil {
		return result, err
	}
	if err := collectDeviceOwnershipFacts(ctx, tx, &result.Devices, result.ObservedAt); err != nil {
		return result, err
	}
	if err := collectAuditProvenanceFacts(ctx, tx, &result.AuditProvenance); err != nil {
		return result, err
	}
	if err := collectOperationProvenanceFacts(ctx, tx, result.AuditProvenance.Facts, &result.OperationProvenance); err != nil {
		return result, err
	}

	if err := tx.Commit(); err != nil {
		return result, fmt.Errorf("finish security schema preflight snapshot: %w", err)
	}
	return result, nil
}

func readMigrationMetadata(ctx context.Context, tx *sql.Tx, config *postgres.Config) ([]migrationMetadata, error) {
	if config == nil {
		return nil, errors.New("migration metadata configuration is required")
	}
	var schemaName string
	if err := tx.QueryRowContext(ctx, "SELECT current_schema()").Scan(&schemaName); err != nil {
		return nil, fmt.Errorf("resolve migration metadata schema for security schema preflight: %w", err)
	}
	metadataSchema, metadataTable, err := migrationMetadataIdentifiers(config, schemaName)
	if err != nil {
		return nil, fmt.Errorf("resolve migration metadata table for security schema preflight: %w", err)
	}
	query := "SELECT version, dirty FROM " + quotedMigrationTable(metadataSchema, metadataTable) + " ORDER BY version"
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("read migration state for security schema preflight: %w", err)
	}
	defer rows.Close()

	var metadata []migrationMetadata
	for rows.Next() {
		var row migrationMetadata
		if err := rows.Scan(&row.version, &row.dirty); err != nil {
			return nil, fmt.Errorf("scan migration state for security schema preflight: %w", err)
		}
		metadata = append(metadata, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate migration state for security schema preflight: %w", err)
	}
	return metadata, nil
}

func collectShopClientFacts(ctx context.Context, tx *sql.Tx, report *ShopClientReport) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT shop.id, shop.client_id, client.id IS NOT NULL
		FROM shops AS shop
		LEFT JOIN clients AS client ON client.id = shop.client_id
		ORDER BY shop.id`)
	if err != nil {
		return fmt.Errorf("inventory Shop to Client facts: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var fact ShopClientFact
		var clientID sql.NullInt64
		if err := rows.Scan(&fact.ShopID, &clientID, &fact.ClientExists); err != nil {
			return fmt.Errorf("scan Shop to Client fact: %w", err)
		}
		fact.ClientID = int64Pointer(clientID)
		report.TotalCount++
		switch {
		case !clientID.Valid:
			fact.Disposition = PreflightBlockingIntegrity
			fact.Reason = "null_client_id"
			report.NullClientIDCount++
		case !fact.ClientExists:
			fact.Disposition = PreflightBlockingIntegrity
			fact.Reason = "orphan_client_id"
			report.OrphanClientIDCount++
		default:
			fact.Disposition = PreflightReadyFacts
			report.ReadyCount++
		}
		report.Facts = append(report.Facts, fact)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate Shop to Client facts: %w", err)
	}
	return nil
}

func collectMembershipFacts(ctx context.Context, tx *sql.Tx, report *MembershipReport) error {
	rows, err := tx.QueryContext(ctx, `
		WITH duplicate_memberships AS (
			SELECT user_id, shop_id
			FROM user_shop_relations
			GROUP BY user_id, shop_id
			HAVING count(*) > 1
		)
		SELECT relation.id,
		       relation.user_id,
		       relation.shop_id,
		       relation.shop_role,
		       app_user.id IS NOT NULL,
		       shop.id IS NOT NULL,
		       shop.client_id,
		       client.id IS NOT NULL,
		       duplicate_membership.user_id IS NOT NULL
		FROM user_shop_relations AS relation
		LEFT JOIN users AS app_user ON app_user.id = relation.user_id
		LEFT JOIN shops AS shop ON shop.id = relation.shop_id
		LEFT JOIN clients AS client ON client.id = shop.client_id
		LEFT JOIN duplicate_memberships AS duplicate_membership
		  ON duplicate_membership.user_id = relation.user_id
		 AND duplicate_membership.shop_id = relation.shop_id
		ORDER BY relation.id`)
	if err != nil {
		return fmt.Errorf("inventory membership facts: %w", err)
	}
	defer rows.Close()

	type membershipKey struct{ userID, shopID int64 }
	duplicateGroups := make(map[membershipKey]struct{})
	for rows.Next() {
		var fact MembershipFact
		var clientID sql.NullInt64
		if err := rows.Scan(
			&fact.RelationID,
			&fact.UserID,
			&fact.ShopID,
			&fact.ShopRole,
			&fact.UserExists,
			&fact.ShopExists,
			&clientID,
			&fact.ShopClientExists,
			&fact.DuplicateLogicalMembership,
		); err != nil {
			return fmt.Errorf("scan membership fact: %w", err)
		}
		fact.ShopClientID = int64Pointer(clientID)
		report.TotalCount++
		if !fact.UserExists {
			report.OrphanUserCount++
		}
		if !fact.ShopExists {
			report.OrphanShopCount++
		}
		invalidClient := fact.ShopExists && (!clientID.Valid || !fact.ShopClientExists)
		if invalidClient {
			report.InvalidShopClientCount++
		}
		if fact.DuplicateLogicalMembership {
			duplicateGroups[membershipKey{fact.UserID, fact.ShopID}] = struct{}{}
		}

		switch {
		case !fact.UserExists && !fact.ShopExists:
			fact.Disposition = PreflightBlockingIntegrity
			fact.Reason = "orphan_user_and_shop"
		case !fact.UserExists:
			fact.Disposition = PreflightBlockingIntegrity
			fact.Reason = "orphan_user_id"
		case !fact.ShopExists:
			fact.Disposition = PreflightBlockingIntegrity
			fact.Reason = "orphan_shop_id"
		case !clientID.Valid:
			fact.Disposition = PreflightBlockingIntegrity
			fact.Reason = "shop_null_client_id"
		case !fact.ShopClientExists:
			fact.Disposition = PreflightBlockingIntegrity
			fact.Reason = "shop_orphan_client_id"
		case fact.DuplicateLogicalMembership:
			fact.Disposition = PreflightBlockingIntegrity
			fact.Reason = "duplicate_logical_membership"
		default:
			fact.Disposition = PreflightReadyFacts
			report.ReadyCount++
		}
		report.Facts = append(report.Facts, fact)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate membership facts: %w", err)
	}
	report.DuplicateLogicalMembershipCount = int64(len(duplicateGroups))
	return nil
}

func collectCurrentShopFacts(ctx context.Context, tx *sql.Tx, report *CurrentShopReport) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT app_user.id,
		       app_user.current_shop_id,
		       shop.id IS NOT NULL,
		       shop.client_id,
		       client.id IS NOT NULL,
		       EXISTS (
		           SELECT 1
		           FROM user_shop_relations AS relation
		           WHERE relation.user_id = app_user.id
		             AND relation.shop_id = app_user.current_shop_id)
		FROM users AS app_user
		LEFT JOIN shops AS shop ON shop.id = app_user.current_shop_id
		LEFT JOIN clients AS client ON client.id = shop.client_id
		ORDER BY app_user.id`)
	if err != nil {
		return fmt.Errorf("inventory current_shop_id facts: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var fact CurrentShopFact
		var currentShopID, clientID sql.NullInt64
		if err := rows.Scan(&fact.UserID, &currentShopID, &fact.ShopExists, &clientID, &fact.ClientExists, &fact.MembershipExists); err != nil {
			return fmt.Errorf("scan current_shop_id fact: %w", err)
		}
		fact.CurrentShopID = int64Pointer(currentShopID)
		fact.ClientID = int64Pointer(clientID)
		report.TotalCount++
		switch {
		case !currentShopID.Valid:
			fact.Disposition = PreflightReadyFacts
			fact.Reason = "no_current_shop_preference"
			report.NullCurrentShopIDCount++
			report.ReadyCount++
		case !fact.ShopExists:
			fact.Disposition = PreflightBlockingIntegrity
			fact.Reason = "orphan_current_shop_id"
			report.StaleReferenceCount++
		case !clientID.Valid:
			fact.Disposition = PreflightBlockingIntegrity
			fact.Reason = "current_shop_null_client_id"
			report.InvalidShopClientCount++
		case !fact.ClientExists:
			fact.Disposition = PreflightBlockingIntegrity
			fact.Reason = "current_shop_orphan_client_id"
			report.InvalidShopClientCount++
		default:
			fact.Disposition = PreflightReadyFacts
			report.ReadyCount++
		}
		report.Facts = append(report.Facts, fact)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate current_shop_id facts: %w", err)
	}
	return nil
}

func collectUserEligibilityFacts(ctx context.Context, tx *sql.Tx, report *UserEligibilityReport) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id,
		       password_hash IS NOT NULL AND password_hash <> '',
		       is_admin
		FROM users
		ORDER BY id`)
	if err != nil {
		return fmt.Errorf("inventory explicit-review User facts: %w", err)
	}
	defer rows.Close()

	// Migration 000004 contains no users.auth_enabled column. Do not probe for
	// or synthesize a future account-eligibility representation here.
	report.AuthEnabledColumnPresent = false
	for rows.Next() {
		fact := UserEligibilityFact{
			AutoApproved:   false,
			ReviewRequired: true,
			Reason:         "explicit_review_required",
		}
		if err := rows.Scan(&fact.ID, &fact.PasswordHashPresent, &fact.IsAdmin); err != nil {
			return fmt.Errorf("scan explicit-review User fact: %w", err)
		}
		report.TotalCount++
		report.ReviewRequiredCount++
		report.Facts = append(report.Facts, fact)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate explicit-review User facts: %w", err)
	}
	return nil
}

func collectAssignmentFacts(ctx context.Context, tx *sql.Tx, report *AssignmentIntegrityReport, observedAt time.Time) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT assignment.id::text,
		       assignment.device_id,
		       assignment.measurement_point_id::text,
		       assignment.valid_from,
		       assignment.valid_to,
		       count(assignment.id) FILTER (WHERE assignment.valid_from <= $1 AND (assignment.valid_to IS NULL OR $1 < assignment.valid_to))
		           OVER (PARTITION BY assignment.device_id),
		       count(assignment.id) FILTER (WHERE assignment.valid_from <= $1 AND (assignment.valid_to IS NULL OR $1 < assignment.valid_to))
		           OVER (PARTITION BY assignment.measurement_point_id),
		       device.id IS NOT NULL,
		       measurement_point.id IS NOT NULL,
		       measurement_point.shop_id,
		       shop.id IS NOT NULL,
		       shop.client_id,
		       client.id IS NOT NULL
		FROM device_assignments AS assignment
		LEFT JOIN devices AS device ON device.id = assignment.device_id
		LEFT JOIN measurement_points AS measurement_point ON measurement_point.id = assignment.measurement_point_id
		LEFT JOIN shops AS shop ON shop.id = measurement_point.shop_id
		LEFT JOIN clients AS client ON client.id = shop.client_id
		ORDER BY assignment.id`, observedAt.UTC())
	if err != nil {
		return fmt.Errorf("inventory Device assignment facts: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var fact AssignmentIntegrityFact
		var validTo sql.NullTime
		var shopID, clientID sql.NullInt64
		var deviceExists, measurementPointExists, shopExists, clientExists bool
		if err := rows.Scan(
			&fact.AssignmentID,
			&fact.DeviceID,
			&fact.MeasurementPointID,
			&fact.ValidFrom,
			&validTo,
			&fact.ActiveDeviceAssignmentCount,
			&fact.ActiveMeasurementPointCount,
			&deviceExists,
			&measurementPointExists,
			&shopID,
			&shopExists,
			&clientID,
			&clientExists,
		); err != nil {
			return fmt.Errorf("scan Device assignment fact: %w", err)
		}
		fact.ValidFrom = fact.ValidFrom.UTC()
		fact.ValidTo = timePointer(validTo)
		fact.ShopID = int64Pointer(shopID)
		fact.ClientID = int64Pointer(clientID)
		fact.FutureStart = fact.ValidFrom.After(observedAt)
		fact.Active = !fact.FutureStart && (validTo.Valid == false || observedAt.Before(validTo.Time))
		report.TotalCount++
		if fact.Active {
			report.ActiveCount++
		}

		switch {
		case !deviceExists:
			fact.Disposition = PreflightBlockingIntegrity
			fact.Reason = "orphan_device_id"
		case !measurementPointExists:
			fact.Disposition = PreflightBlockingIntegrity
			fact.Reason = "orphan_measurement_point_id"
		case !shopExists:
			fact.Disposition = PreflightBlockingIntegrity
			fact.Reason = "orphan_measurement_point_shop_id"
		case !clientID.Valid:
			fact.Disposition = PreflightBlockingIntegrity
			fact.Reason = "assignment_shop_null_client_id"
		case !clientExists:
			fact.Disposition = PreflightBlockingIntegrity
			fact.Reason = "assignment_shop_orphan_client_id"
		case fact.FutureStart:
			fact.Disposition = PreflightBlockingIntegrity
			fact.Reason = "future_open_assignment"
		case fact.ActiveDeviceAssignmentCount > 1:
			fact.Disposition = PreflightBlockingIntegrity
			fact.Reason = "multiple_active_device_assignments"
		case fact.ActiveMeasurementPointCount > 1:
			fact.Disposition = PreflightBlockingIntegrity
			fact.Reason = "multiple_active_measurement_point_assignments"
		case validTo.Valid && !validTo.Time.After(fact.ValidFrom):
			fact.Disposition = PreflightBlockingIntegrity
			fact.Reason = "invalid_assignment_interval"
		default:
			fact.Disposition = PreflightReadyFacts
			report.ReadyCount++
		}
		if fact.Disposition == PreflightBlockingIntegrity {
			report.BlockingIntegrityCount++
		}
		report.Facts = append(report.Facts, fact)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate Device assignment facts: %w", err)
	}
	return nil
}

func collectDeviceOwnershipFacts(ctx context.Context, tx *sql.Tx, report *DeviceOwnershipReport, observedAt time.Time) error {
	rows, err := tx.QueryContext(ctx, `
		WITH assignment_rollup AS (
			SELECT device.id AS device_id,
			       count(assignment.id) AS historical_assignment_count,
			       count(assignment.id) FILTER (WHERE assignment.valid_from > $1) AS future_assignment_count,
			       count(assignment.id) FILTER (WHERE assignment.valid_from <= $1 AND (assignment.valid_to IS NULL OR $1 < assignment.valid_to)) AS active_assignment_count,
			       count(assignment.id) FILTER (
			           WHERE assignment.valid_from <= $1
			             AND (assignment.valid_to IS NULL OR $1 < assignment.valid_to)
			             AND measurement_point.id IS NOT NULL
			             AND shop.id IS NOT NULL
			             AND shop.client_id IS NOT NULL
			             AND client.id IS NOT NULL) AS valid_active_count,
			       count(DISTINCT client.id) FILTER (
			           WHERE assignment.valid_from <= $1
			             AND (assignment.valid_to IS NULL OR $1 < assignment.valid_to)
			             AND measurement_point.id IS NOT NULL
			             AND shop.id IS NOT NULL
			             AND shop.client_id IS NOT NULL
			             AND client.id IS NOT NULL) AS candidate_client_count,
			       min(client.id) FILTER (
			           WHERE assignment.valid_from <= $1
			             AND (assignment.valid_to IS NULL OR $1 < assignment.valid_to)
			             AND measurement_point.id IS NOT NULL
			             AND shop.id IS NOT NULL
			             AND shop.client_id IS NOT NULL
			             AND client.id IS NOT NULL) AS candidate_client_id
			FROM devices AS device
			LEFT JOIN device_assignments AS assignment ON assignment.device_id = device.id
			LEFT JOIN measurement_points AS measurement_point ON measurement_point.id = assignment.measurement_point_id
			LEFT JOIN shops AS shop ON shop.id = measurement_point.shop_id
			LEFT JOIN clients AS client ON client.id = shop.client_id
			GROUP BY device.id
		)
		SELECT device.id,
		       device.shop_id,
		       compatibility_shop.id IS NOT NULL,
		       assignment_rollup.historical_assignment_count,
		       assignment_rollup.future_assignment_count,
		       assignment_rollup.active_assignment_count,
		       assignment_rollup.valid_active_count,
		       assignment_rollup.candidate_client_count,
		       assignment_rollup.candidate_client_id
		FROM devices AS device
		LEFT JOIN shops AS compatibility_shop ON compatibility_shop.id = device.shop_id
		JOIN assignment_rollup ON assignment_rollup.device_id = device.id
		ORDER BY device.id`, observedAt.UTC())
	if err != nil {
		return fmt.Errorf("inventory Device owner candidate facts: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var fact DeviceOwnershipFact
		var validActiveCount, candidateClientCount int64
		var candidateClientID sql.NullInt64
		if err := rows.Scan(
			&fact.DeviceID,
			&fact.CompatibilityShopID,
			&fact.CompatibilityShopReferenceValid,
			&fact.HistoricalAssignmentCount,
			&fact.FutureAssignmentCount,
			&fact.ActiveAssignmentCount,
			&validActiveCount,
			&candidateClientCount,
			&candidateClientID,
		); err != nil {
			return fmt.Errorf("scan Device owner candidate fact: %w", err)
		}
		// Device.ShopID is inspected only for broken legacy referential facts. It
		// is never used to select CandidateClientID.
		fact.CompatibilityShopReferenceUsed = false
		report.TotalCount++

		candidate := fact.ActiveAssignmentCount == 1 && validActiveCount == 1 && candidateClientCount == 1 && candidateClientID.Valid
		if candidate {
			fact.Classification = DeviceOwnerCandidate
			fact.CandidateClientID = int64Pointer(candidateClientID)
			report.OwnerCandidateCount++
		} else {
			fact.Classification = DeviceManualMappingRequired
			report.ManualMappingRequiredCount++
		}

		switch {
		case !fact.CompatibilityShopReferenceValid:
			fact.Disposition = PreflightBlockingIntegrity
			fact.Reason = "orphan_compatibility_shop_id"
		case fact.FutureAssignmentCount > 0:
			fact.Disposition = PreflightBlockingIntegrity
			fact.Reason = "future_open_assignment"
		case fact.ActiveAssignmentCount > 0 && validActiveCount != fact.ActiveAssignmentCount:
			fact.Disposition = PreflightBlockingIntegrity
			fact.Reason = "incomplete_active_assignment_chain"
		case candidate:
			fact.Disposition = PreflightReadyFacts
		case fact.ActiveAssignmentCount == 0 && fact.HistoricalAssignmentCount == 0:
			fact.Disposition = PreflightReconciliationRequired
			fact.Reason = "no_assignment_history"
		case fact.ActiveAssignmentCount == 0:
			fact.Disposition = PreflightReconciliationRequired
			fact.Reason = "historical_only_assignment"
		case fact.ActiveAssignmentCount > 1:
			fact.Disposition = PreflightBlockingIntegrity
			fact.Reason = "multiple_active_assignments"
		default:
			fact.Disposition = PreflightReconciliationRequired
			fact.Reason = "ambiguous_active_assignment_chain"
		}
		if fact.Disposition == PreflightBlockingIntegrity {
			report.BlockingIntegrityCount++
		}
		report.Facts = append(report.Facts, fact)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate Device owner candidate facts: %w", err)
	}
	return nil
}

func collectAuditProvenanceFacts(ctx context.Context, tx *sql.Tx, report *ProvenanceReport) error {
	rows, err := tx.QueryContext(ctx, `
		WITH explicit_refs (audit_id, source_name, client_id, is_valid) AS (
			SELECT audit.id, 'shop_id', client.id,
			       shop.id IS NOT NULL AND shop.client_id IS NOT NULL AND client.id IS NOT NULL
			FROM admin_binding_audits AS audit
			LEFT JOIN shops AS shop ON shop.id = audit.shop_id
			LEFT JOIN clients AS client ON client.id = shop.client_id
			WHERE audit.shop_id IS NOT NULL
			UNION ALL
			SELECT audit.id, 'measurement_point_id', client.id,
			       measurement_point.id IS NOT NULL AND shop.id IS NOT NULL
			       AND shop.client_id IS NOT NULL AND client.id IS NOT NULL
			FROM admin_binding_audits AS audit
			LEFT JOIN measurement_points AS measurement_point ON measurement_point.id = audit.measurement_point_id
			LEFT JOIN shops AS shop ON shop.id = measurement_point.shop_id
			LEFT JOIN clients AS client ON client.id = shop.client_id
			WHERE audit.measurement_point_id IS NOT NULL
			UNION ALL
			SELECT audit.id, 'old_measurement_point_id', client.id,
			       measurement_point.id IS NOT NULL AND shop.id IS NOT NULL
			       AND shop.client_id IS NOT NULL AND client.id IS NOT NULL
			FROM admin_binding_audits AS audit
			LEFT JOIN measurement_points AS measurement_point ON measurement_point.id = audit.old_measurement_point_id
			LEFT JOIN shops AS shop ON shop.id = measurement_point.shop_id
			LEFT JOIN clients AS client ON client.id = shop.client_id
			WHERE audit.old_measurement_point_id IS NOT NULL
			UNION ALL
			SELECT audit.id, 'new_measurement_point_id', client.id,
			       measurement_point.id IS NOT NULL AND shop.id IS NOT NULL
			       AND shop.client_id IS NOT NULL AND client.id IS NOT NULL
			FROM admin_binding_audits AS audit
			LEFT JOIN measurement_points AS measurement_point ON measurement_point.id = audit.new_measurement_point_id
			LEFT JOIN shops AS shop ON shop.id = measurement_point.shop_id
			LEFT JOIN clients AS client ON client.id = shop.client_id
			WHERE audit.new_measurement_point_id IS NOT NULL
			UNION ALL
			SELECT audit.id, 'old_assignment_id', client.id,
			       assignment.id IS NOT NULL AND measurement_point.id IS NOT NULL
			       AND shop.id IS NOT NULL AND shop.client_id IS NOT NULL AND client.id IS NOT NULL
			FROM admin_binding_audits AS audit
			LEFT JOIN device_assignments AS assignment ON assignment.id = audit.old_assignment_id
			LEFT JOIN measurement_points AS measurement_point ON measurement_point.id = assignment.measurement_point_id
			LEFT JOIN shops AS shop ON shop.id = measurement_point.shop_id
			LEFT JOIN clients AS client ON client.id = shop.client_id
			WHERE audit.old_assignment_id IS NOT NULL
			UNION ALL
			SELECT audit.id, 'new_assignment_id', client.id,
			       assignment.id IS NOT NULL AND measurement_point.id IS NOT NULL
			       AND shop.id IS NOT NULL AND shop.client_id IS NOT NULL AND client.id IS NOT NULL
			FROM admin_binding_audits AS audit
			LEFT JOIN device_assignments AS assignment ON assignment.id = audit.new_assignment_id
			LEFT JOIN measurement_points AS measurement_point ON measurement_point.id = assignment.measurement_point_id
			LEFT JOIN shops AS shop ON shop.id = measurement_point.shop_id
			LEFT JOIN clients AS client ON client.id = shop.client_id
			WHERE audit.new_assignment_id IS NOT NULL
		)
		SELECT audit.id::text,
		       count(explicit_ref.source_name),
		       count(*) FILTER (WHERE explicit_ref.is_valid),
		       count(*) FILTER (
		           WHERE explicit_ref.source_name IS NOT NULL
		             AND NOT explicit_ref.is_valid),
		       count(DISTINCT explicit_ref.client_id) FILTER (WHERE explicit_ref.is_valid),
		       min(explicit_ref.client_id) FILTER (WHERE explicit_ref.is_valid),
		       app_user.id IS NOT NULL,
		       EXISTS (
		           SELECT 1
		           FROM admin_binding_operations AS operation
		           WHERE operation.operation_id = audit.operation_id
		             AND operation.actor_id = audit.actor_id
		             AND operation.operation = audit.action)
		FROM admin_binding_audits AS audit
		LEFT JOIN users AS app_user ON app_user.id = audit.actor_id
		LEFT JOIN explicit_refs AS explicit_ref ON explicit_ref.audit_id = audit.id
		GROUP BY audit.id, app_user.id
		ORDER BY audit.id`)
	if err != nil {
		return fmt.Errorf("inventory Admin Binding audit provenance: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var fact ProvenanceFact
		var validRefCount, invalidRefCount, candidateClientCount int64
		var candidateClientID sql.NullInt64
		var actorExists, operationMatches bool
		if err := rows.Scan(
			&fact.ID,
			&fact.ExplicitRelationalFactCount,
			&validRefCount,
			&invalidRefCount,
			&candidateClientCount,
			&candidateClientID,
			&actorExists,
			&operationMatches,
		); err != nil {
			return fmt.Errorf("scan Admin Binding audit provenance: %w", err)
		}
		report.TotalCount++
		independentlyDerived := actorExists && operationMatches &&
			fact.ExplicitRelationalFactCount > 0 && invalidRefCount == 0 &&
			validRefCount == fact.ExplicitRelationalFactCount && candidateClientCount == 1 && candidateClientID.Valid
		if independentlyDerived {
			fact.CandidateClientID = int64Pointer(candidateClientID)
			fact.Classification = ProvenanceIndependentlyDerived
			fact.Disposition = PreflightReadyFacts
			report.IndependentlyDerivedCount++
		} else {
			fact.Classification = ProvenanceLegacyUnresolved
			fact.Disposition = PreflightReconciliationRequired
			report.UnresolvedCount++
			switch {
			case !actorExists:
				fact.Reason = "orphan_actor_id"
			case !operationMatches:
				fact.Reason = "operation_relation_unresolved"
			case fact.ExplicitRelationalFactCount == 0:
				fact.Reason = "no_explicit_relational_provenance"
			case invalidRefCount > 0:
				fact.Reason = "invalid_relational_provenance"
			case candidateClientCount != 1:
				fact.Reason = "ambiguous_client_provenance"
			default:
				fact.Reason = "legacy_provenance_unresolved"
			}
		}
		report.Facts = append(report.Facts, fact)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate Admin Binding audit provenance: %w", err)
	}
	return nil
}

func collectOperationProvenanceFacts(ctx context.Context, tx *sql.Tx, auditFacts []ProvenanceFact, report *ProvenanceReport) error {
	audits := make(map[string]ProvenanceFact, len(auditFacts))
	for _, fact := range auditFacts {
		audits[fact.ID] = fact
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT operation.operation_id::text,
		       app_user.id IS NOT NULL,
		       audit.id::text,
		       audit.id IS NOT NULL
		       AND audit.actor_id = operation.actor_id
		       AND audit.action = operation.operation
		FROM admin_binding_operations AS operation
		LEFT JOIN users AS app_user ON app_user.id = operation.actor_id
		LEFT JOIN admin_binding_audits AS audit ON audit.operation_id = operation.operation_id
		ORDER BY operation.operation_id`)
	if err != nil {
		return fmt.Errorf("inventory Admin Binding operation provenance: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var fact ProvenanceFact
		var actorExists bool
		var auditID sql.NullString
		var auditMatches sql.NullBool
		if err := rows.Scan(&fact.ID, &actorExists, &auditID, &auditMatches); err != nil {
			return fmt.Errorf("scan Admin Binding operation provenance: %w", err)
		}
		report.TotalCount++
		auditFact, hasAuditFact := audits[auditID.String]
		independentlyDerived := actorExists && auditID.Valid && auditMatches.Valid && auditMatches.Bool &&
			hasAuditFact && auditFact.Classification == ProvenanceIndependentlyDerived && auditFact.CandidateClientID != nil
		if independentlyDerived {
			clientID := *auditFact.CandidateClientID
			fact.CandidateClientID = &clientID
			fact.ExplicitRelationalFactCount = auditFact.ExplicitRelationalFactCount
			fact.Classification = ProvenanceIndependentlyDerived
			fact.Disposition = PreflightReadyFacts
			report.IndependentlyDerivedCount++
		} else {
			fact.Classification = ProvenanceLegacyUnresolved
			fact.Disposition = PreflightReconciliationRequired
			report.UnresolvedCount++
			switch {
			case !actorExists:
				fact.Reason = "orphan_actor_id"
			case !auditID.Valid:
				fact.Reason = "no_audit_relational_provenance"
			case !auditMatches.Valid || !auditMatches.Bool:
				fact.Reason = "audit_operation_relation_mismatch"
			default:
				fact.Reason = "audit_provenance_unresolved"
			}
		}
		report.Facts = append(report.Facts, fact)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate Admin Binding operation provenance: %w", err)
	}
	return nil
}

func int64Pointer(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}

func timePointer(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time.UTC()
	return &result
}

// RunExclusiveOwnedSecuritySchemaRecheck reruns the complete diagnostic
// inventory in a fresh repeatable-read/read-only transaction while the same
// pinned session owns the exclusive fence. The returned snapshot is evidence
// for a future protected operation, never a caller-forgeable approval.
func RunExclusiveOwnedSecuritySchemaRecheck(ctx context.Context, dsn string) (result LegacyDataPreflightResult, err error) {
	result = LegacyDataPreflightResult{
		SnapshotMode:         SnapshotRepeatableReadOnly,
		AccountEligibility:   RepresentationNotRepresented,
		DeviceOwnerAuthority: RepresentationNotRepresented,
		WriterFence:          WriterFenceRequiresMigrationOrchestration,
		WriterFenceDecision:  AssessSecuritySchemaWriterFence(),
	}
	if ctx == nil {
		ctx = context.Background()
	}
	parsed, err := parsePostgresDatabaseURL(dsn)
	if err != nil {
		return result, err
	}
	result.Migration.ExpectedVersion, err = latestEmbeddedMigrationVersion()
	if err != nil {
		return result, err
	}
	if err := WithExclusiveWriterFence(ctx, dsn, func(fence *ExclusiveWriterFence) error {
		capability, err := fence.Capability()
		if err != nil {
			return err
		}
		if err := RequireProtectedWork(capability); err != nil {
			return err
		}
		tx, err := fence.Conn().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var isolation, readOnly string
		if err := tx.QueryRowContext(ctx, `SELECT current_setting('transaction_isolation'), current_setting('transaction_read_only'), transaction_timestamp()`).Scan(&isolation, &readOnly, &result.ObservedAt); err != nil {
			return err
		}
		result.ObservedAt = result.ObservedAt.UTC()
		result.ReadOnlyVerified = strings.EqualFold(isolation, "repeatable read") && strings.EqualFold(readOnly, "on")
		if !result.ReadOnlyVerified {
			return fmt.Errorf("exclusive-owned recheck requires repeatable read/read only; got isolation=%q read_only=%q", isolation, readOnly)
		}
		metadataRows, err := readMigrationMetadata(ctx, tx, parsed.config)
		if err != nil {
			return err
		}
		result.Migration.MetadataRowCount = len(metadataRows)
		if len(metadataRows) == 1 {
			result.Migration.ActualVersion = metadataRows[0].version
			result.Migration.Dirty = metadataRows[0].dirty
		} else {
			result.Migration.ActualVersion = -1
		}
		if err := classifyMigrationMetadata(metadataRows, result.Migration.ExpectedVersion); err != nil {
			return err
		}
		if err := collectShopClientFacts(ctx, tx, &result.ShopClient); err != nil {
			return err
		}
		if err := collectMembershipFacts(ctx, tx, &result.Membership); err != nil {
			return err
		}
		if err := collectCurrentShopFacts(ctx, tx, &result.CurrentShop); err != nil {
			return err
		}
		if err := collectUserEligibilityFacts(ctx, tx, &result.Users); err != nil {
			return err
		}
		if err := collectAssignmentFacts(ctx, tx, &result.Assignments, result.ObservedAt); err != nil {
			return err
		}
		if err := collectDeviceOwnershipFacts(ctx, tx, &result.Devices, result.ObservedAt); err != nil {
			return err
		}
		if err := collectAuditProvenanceFacts(ctx, tx, &result.AuditProvenance); err != nil {
			return err
		}
		if err := collectOperationProvenanceFacts(ctx, tx, result.AuditProvenance.Facts, &result.OperationProvenance); err != nil {
			return err
		}
		return tx.Commit()
	}); err != nil {
		return result, err
	}
	return result, nil
}
