package persistence

import (
	"context"
	"database/sql"
	"errors"
	"math/big"
	"strconv"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	core "power-iot-backend/internal/core/billingenergy"
	"power-iot-backend/internal/core/coverage"
)

var ErrBillingEnergyAccess = errors.New("billing energy shop access denied")

// BillingEnergyQueryRepository is the PostgreSQL adapter behind the small
// application Billing Energy interface. All authority is loaded in one
// repeatable-read, read-only transaction.
type BillingEnergyQueryRepository struct{ db *gorm.DB }

func NewBillingEnergyQueryRepository(db *gorm.DB) *BillingEnergyQueryRepository {
	return &BillingEnergyQueryRepository{db: db}
}

var _ interface {
	FindBillingEnergy(context.Context, uint, uint, core.BillingMonth, func() time.Time) (core.Facts, error)
} = (*BillingEnergyQueryRepository)(nil)

type billingAssignment struct {
	deviceID uint
	interval core.Interval
}

type billingCandidate struct {
	pointID      uuid.UUID
	deviceID     uint
	boot         int64
	sequence     int64
	start        time.Time
	end          time.Time
	energyText   sql.NullString
	coverage     sql.NullInt64
	protocol     int
	recordedAt   time.Time
	provenance   bool
	conflict     bool
	digestLength int
}

func (r *BillingEnergyQueryRepository) FindBillingEnergy(ctx context.Context, userID, shopID uint, month core.BillingMonth, now func() time.Time) (core.Facts, error) {
	if r == nil || r.db == nil || userID == 0 || shopID == 0 || now == nil {
		return core.Facts{}, ErrBillingEnergyAccess
	}
	if ctx == nil {
		ctx = context.Background()
	}
	database, err := r.db.DB()
	if err != nil {
		return core.Facts{}, err
	}
	tx, err := database.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return core.Facts{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	var databaseSnapshot time.Time
	if err := tx.QueryRowContext(ctx, "SELECT transaction_timestamp()").Scan(&databaseSnapshot); err != nil {
		return core.Facts{}, err
	}
	snapshot := now().UTC()
	pointIDs, err := authorizedBillingPoints(ctx, tx, userID, shopID)
	if err != nil {
		return core.Facts{}, err
	}
	period, err := month.Period(snapshot)
	if err != nil {
		return core.Facts{}, err
	}
	assignments, err := loadBillingAssignments(ctx, tx, shopID, period)
	if err != nil {
		return core.Facts{}, err
	}
	candidates, err := loadBillingCandidates(ctx, tx, shopID, period)
	if err != nil {
		return core.Facts{}, err
	}
	points := make([]core.PointFacts, 0, len(pointIDs))
	for _, pointID := range pointIDs {
		pointAssignments := assignments[pointID]
		expectedIntervals := make([]core.Interval, 0, len(pointAssignments))
		for _, assignment := range pointAssignments {
			expectedIntervals = append(expectedIntervals, assignment.interval)
		}
		expected := core.UnionDuration(expectedIntervals, period.Start, period.Cutoff)
		observed, warnings := evaluatePointCandidates(pointID, candidates[pointID], pointAssignments, period)
		point := core.PointFacts{
			MeasurementPointID: pointID.String(),
			ExpectedDuration:   expected,
			ObservedDuration:   observed.Duration,
			UsageMicros:        observed.EnergyMicros,
			Warnings:           warnings,
		}
		if expected == 0 {
			point.Warnings = append(point.Warnings, core.WarningNoExpectedMonitoringWindow)
		} else if observed.Duration < expected {
			point.Warnings = append(point.Warnings, core.WarningPartialMonitoringData)
		}
		points = append(points, point)
	}
	result := core.Aggregate(shopID, period.Month.String(), points)
	result.PeriodStart = period.Start
	result.PeriodEnd = period.End
	result.Cutoff = period.Cutoff
	result.Snapshot = snapshot
	if err := tx.Commit(); err != nil {
		return core.Facts{}, err
	}
	committed = true
	return result, nil
}

func authorizedBillingPoints(ctx context.Context, tx *sql.Tx, userID, shopID uint) ([]uuid.UUID, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT mp.id
FROM measurement_points AS mp
JOIN shops AS s ON s.id = mp.shop_id AND s.is_active = TRUE
JOIN user_shop_relations AS relation
  ON relation.shop_id = s.id AND relation.user_id = $1
WHERE s.id = $2
ORDER BY mp.id`, userID, shopID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var points []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		points = append(points, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(points) == 0 {
		var authorized bool
		if err := tx.QueryRowContext(ctx, `
SELECT EXISTS (
    SELECT 1 FROM shops AS s
    JOIN user_shop_relations AS relation ON relation.shop_id = s.id AND relation.user_id = $1
    WHERE s.id = $2 AND s.is_active = TRUE
)`, userID, shopID).Scan(&authorized); err != nil {
			return nil, err
		}
		if !authorized {
			return nil, ErrBillingEnergyAccess
		}
	}
	return points, nil
}

func loadBillingAssignments(ctx context.Context, tx *sql.Tx, shopID uint, period core.Period) (map[uuid.UUID][]billingAssignment, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT da.measurement_point_id, da.device_id, da.valid_from, da.valid_to
FROM device_assignments AS da
JOIN measurement_points AS mp ON mp.id = da.measurement_point_id AND mp.shop_id = $1
WHERE da.valid_from < $3
  AND (da.valid_to IS NULL OR da.valid_to > $2)
ORDER BY da.measurement_point_id, da.valid_from, da.id`, shopID, period.Start, period.Cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	assignments := make(map[uuid.UUID][]billingAssignment)
	for rows.Next() {
		var pointID uuid.UUID
		var deviceID uint
		var start time.Time
		var end sql.NullTime
		if err := rows.Scan(&pointID, &deviceID, &start, &end); err != nil {
			return nil, err
		}
		interval := core.Interval{Start: start.UTC(), End: period.Cutoff}
		if end.Valid {
			interval.End = end.Time.UTC()
		}
		assignments[pointID] = append(assignments[pointID], billingAssignment{deviceID: deviceID, interval: interval})
	}
	return assignments, rows.Err()
}

func loadBillingCandidates(ctx context.Context, tx *sql.Tx, shopID uint, period core.Period) (map[uuid.UUID][]billingCandidate, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT pr.measurement_point_id, pr.device_id, pr.boot_counter, pr.sequence,
       pr.interval_start, pr.interval_end, pr.energy_delta_kwh::text,
       pr.coverage_version, pr.protocol_version, pr.recorded_at,
       (k.id IS NOT NULL), COALESCE(k.conflict_detected, false),
       COALESCE(octet_length(k.canonical_coverage_digest), 0)
FROM power_readings AS pr
JOIN measurement_points AS mp ON mp.id = pr.measurement_point_id AND mp.shop_id = $1
LEFT JOIN telemetry_ingest_keys AS k
  ON k.device_id = pr.device_id
 AND k.boot_counter = pr.boot_counter
 AND k.sequence = pr.sequence
WHERE (
    pr.coverage_version = $4
    AND pr.interval_start < $3
    AND pr.interval_end > $2
) OR (
    pr.coverage_version IS DISTINCT FROM $4
    AND pr.recorded_at >= $2
    AND pr.recorded_at < $3
)
ORDER BY pr.measurement_point_id, pr.interval_start NULLS LAST, pr.recorded_at, pr.id`, shopID, period.Start, period.Cutoff, coverage.ProfileVersion)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[uuid.UUID][]billingCandidate)
	for rows.Next() {
		var candidate billingCandidate
		var pointID uuid.UUID
		var boot, sequence sql.NullInt64
		var start, end sql.NullTime
		var provenance bool
		if err := rows.Scan(&pointID, &candidate.deviceID, &boot, &sequence, &start, &end, &candidate.energyText,
			&candidate.coverage, &candidate.protocol, &candidate.recordedAt, &provenance, &candidate.conflict, &candidate.digestLength); err != nil {
			return nil, err
		}
		candidate.pointID = pointID
		candidate.provenance = provenance
		candidate.recordedAt = candidate.recordedAt.UTC()
		if boot.Valid {
			candidate.boot = boot.Int64
		}
		if sequence.Valid {
			candidate.sequence = sequence.Int64
		}
		if start.Valid {
			candidate.start = start.Time.UTC()
		}
		if end.Valid {
			candidate.end = end.Time.UTC()
		}
		out[pointID] = append(out[pointID], candidate)
	}
	return out, rows.Err()
}

func evaluatePointCandidates(pointID uuid.UUID, candidates []billingCandidate, assignments []billingAssignment, period core.Period) (core.ObservedFacts, []core.WarningCode) {
	groups := make(map[string][]billingCandidate)
	warnings := make([]core.WarningCode, 0)
	for _, candidate := range candidates {
		if candidate.coverage.Valid && candidate.coverage.Int64 == coverage.ProfileVersion && candidate.protocol == int(coverage.ProfileVersion) {
			key := strconv.FormatUint(uint64(candidate.deviceID), 10) + ":" + strconv.FormatInt(candidate.boot, 10) + ":" + strconv.FormatInt(candidate.sequence, 10)
			groups[key] = append(groups[key], candidate)
			continue
		}
		warnings = appendWarning(warnings, core.WarningLegacyEvidence)
	}
	observed := make([]core.ObservedInterval, 0)
	for _, group := range groups {
		canonical, valid := canonicalCandidate(group)
		if !valid {
			warnings = appendWarning(warnings, core.WarningConflictingTelemetry)
			continue
		}
		if canonical.conflict {
			warnings = appendWarning(warnings, core.WarningConflictingTelemetry)
			continue
		}
		if !canonical.provenance || canonical.digestLength != 32 {
			warnings = appendWarning(warnings, core.WarningLegacyEvidence)
			continue
		}
		if !canonical.start.Before(canonical.end) || canonical.start.Before(period.Start) || canonical.end.After(period.Cutoff) || !canonical.recordedAt.Equal(canonical.start) {
			warnings = appendWarning(warnings, core.WarningLegacyEvidence)
			continue
		}
		energy, ok := parseEnergyMicros(canonical.energyText)
		if !ok {
			warnings = appendWarning(warnings, core.WarningLegacyEvidence)
			continue
		}
		coverageCount := 0
		deviceCoverageCount := 0
		for _, assignment := range assignments {
			if !assignment.interval.Start.After(canonical.start) && !canonical.end.After(assignment.interval.End) {
				coverageCount++
				if assignment.deviceID == canonical.deviceID {
					deviceCoverageCount++
				}
			}
		}
		if coverageCount != 1 || deviceCoverageCount != 1 {
			if coverageCount > 1 {
				warnings = appendWarning(warnings, core.WarningAmbiguousAssignment)
			} else {
				warnings = appendWarning(warnings, core.WarningUnattributableEvidence)
			}
			continue
		}
		observed = append(observed, core.ObservedInterval{Interval: core.Interval{Start: canonical.start, End: canonical.end}, EnergyMicros: energy})
	}
	facts, overlapWarnings := core.EvaluateObserved(observed, period.Start, period.Cutoff)
	for _, warning := range overlapWarnings {
		warnings = appendWarning(warnings, warning)
	}
	return facts, warnings
}

func canonicalCandidate(group []billingCandidate) (billingCandidate, bool) {
	if len(group) == 0 {
		return billingCandidate{}, false
	}
	canonical := group[0]
	for _, candidate := range group[1:] {
		if candidate.start != canonical.start || candidate.end != canonical.end || candidate.energyText.String != canonical.energyText.String || candidate.energyText.Valid != canonical.energyText.Valid {
			return billingCandidate{}, false
		}
	}
	return canonical, true
}

func appendWarning(warnings []core.WarningCode, warning core.WarningCode) []core.WarningCode {
	for _, existing := range warnings {
		if existing == warning {
			return warnings
		}
	}
	return append(warnings, warning)
}

func parseEnergyMicros(value sql.NullString) (int64, bool) {
	if !value.Valid || value.String == "" {
		return 0, false
	}
	rat, ok := new(big.Rat).SetString(value.String)
	if !ok || rat.Sign() < 0 {
		return 0, false
	}
	scaled := new(big.Rat).Mul(rat, big.NewRat(1_000_000, 1))
	if !scaled.IsInt() || !scaled.Num().IsInt64() {
		return 0, false
	}
	return scaled.Num().Int64(), true
}
