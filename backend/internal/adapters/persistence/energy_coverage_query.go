package persistence

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"power-iot-backend/internal/core/coverage"
)

type EnergyCoverageWindow struct {
	Kwh       *float64
	Watermark *time.Time
	State     coverage.State
}

type EnergyCoverageProjection struct {
	Today    EnergyCoverageWindow
	Month    EnergyCoverageWindow
	Snapshot time.Time
}

type EnergyCoverageQueryRepository struct{ db *gorm.DB }

func NewEnergyCoverageQueryRepository(db *gorm.DB) *EnergyCoverageQueryRepository {
	return &EnergyCoverageQueryRepository{db: db}
}

// FindMeasurementPointEnergy evaluates Today and Month in one PostgreSQL
// repeatable-read snapshot. received_at is deliberately not used as MVCC
// visibility evidence.
func (r *EnergyCoverageQueryRepository) FindMeasurementPointEnergy(ctx context.Context, pointID uuid.UUID, now func() time.Time) (EnergyCoverageProjection, error) {
	if r == nil || r.db == nil || pointID == uuid.Nil || now == nil {
		return EnergyCoverageProjection{}, errors.New("measurement point energy query is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	db, err := r.db.DB()
	if err != nil {
		return EnergyCoverageProjection{}, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return EnergyCoverageProjection{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	// Establish the PostgreSQL snapshot before capturing the application
	// temporal cap used by both period calculations.
	var dbSnapshot time.Time
	if err := tx.QueryRowContext(ctx, "SELECT transaction_timestamp()").Scan(&dbSnapshot); err != nil {
		return EnergyCoverageProjection{}, err
	}
	_ = dbSnapshot
	requestSnapshot := now().UTC()
	loc, err := time.LoadLocation(coverage.BusinessTimezone)
	if err != nil {
		return EnergyCoverageProjection{}, err
	}
	local := requestSnapshot.In(loc)
	todayStart := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc).UTC()
	monthStart := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, loc).UTC()

	assignments, err := loadAssignments(ctx, tx, pointID, monthStart, requestSnapshot)
	if err != nil {
		return EnergyCoverageProjection{}, err
	}
	evidence, err := loadEvidence(ctx, tx, pointID, monthStart, requestSnapshot, assignments)
	if err != nil {
		return EnergyCoverageProjection{}, err
	}
	protocol0, err := loadProtocol0Barriers(ctx, tx, pointID, monthStart, requestSnapshot)
	if err != nil {
		return EnergyCoverageProjection{}, err
	}
	evidence = append(evidence, protocol0...)
	today := coverage.Evaluate(todayStart, requestSnapshot, append([]coverage.Evidence(nil), evidence...))
	month := coverage.Evaluate(monthStart, requestSnapshot, append([]coverage.Evidence(nil), evidence...))
	out := EnergyCoverageProjection{Snapshot: requestSnapshot, Today: windowFromResult(today), Month: windowFromResult(month)}
	if err := tx.Commit(); err != nil {
		return EnergyCoverageProjection{}, err
	}
	committed = true
	return out, nil
}

func windowFromResult(result coverage.Result) EnergyCoverageWindow {
	return EnergyCoverageWindow{Kwh: result.Kwh, Watermark: result.ThroughAt, State: result.State}
}

type assignmentSegment struct {
	DeviceID uint
	From     time.Time
	To       *time.Time
}

func loadAssignments(ctx context.Context, tx *sql.Tx, pointID uuid.UUID, from, to time.Time) ([]assignmentSegment, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT device_id, valid_from, valid_to
FROM device_assignments
WHERE measurement_point_id = $1
  AND valid_from < $3
  AND (valid_to IS NULL OR valid_to > $2)
ORDER BY valid_from`, pointID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []assignmentSegment
	for rows.Next() {
		var deviceID uint
		var start time.Time
		var end sql.NullTime
		if err := rows.Scan(&deviceID, &start, &end); err != nil {
			return nil, err
		}
		var endPtr *time.Time
		if end.Valid {
			value := end.Time.UTC()
			endPtr = &value
		}
		out = append(out, assignmentSegment{DeviceID: deviceID, From: start.UTC(), To: endPtr})
	}
	return out, rows.Err()
}

func assignmentCovers(assignments []assignmentSegment, deviceID uint, start, end time.Time) bool {
	count := 0
	for _, assignment := range assignments {
		if assignment.DeviceID != deviceID || assignment.From.After(start) {
			continue
		}
		if assignment.To == nil || !end.After(*assignment.To) {
			count++
		}
	}
	return count == 1
}

func loadEvidence(ctx context.Context, tx *sql.Tx, pointID uuid.UUID, from, to time.Time, assignments []assignmentSegment) ([]coverage.Evidence, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT pr.recorded_at, pr.interval_start, pr.interval_end,
       pr.device_id, pr.boot_counter, pr.sequence, pr.energy_delta_kwh,
       (k.id IS NOT NULL), COALESCE(k.conflict_detected, false),
       k.canonical_coverage_digest, pr.coverage_version
FROM power_readings AS pr
LEFT JOIN telemetry_ingest_keys AS k
  ON k.device_id = pr.device_id
 AND k.boot_counter = pr.boot_counter
 AND k.sequence = pr.sequence
WHERE pr.measurement_point_id = $1
  AND pr.recorded_at >= $2
  AND pr.recorded_at < $3
ORDER BY pr.recorded_at, pr.id`, pointID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []coverage.Evidence
	for rows.Next() {
		var recorded time.Time
		var start, end sql.NullTime
		var deviceID uint
		var boot, sequence sql.NullInt64
		var energy sql.NullString
		var provenance bool
		var conflict bool
		var digest []byte
		var version sql.NullInt64
		if err := rows.Scan(&recorded, &start, &end, &deviceID, &boot, &sequence, &energy, &provenance, &conflict, &digest, &version); err != nil {
			return nil, err
		}
		if !version.Valid || version.Int64 != coverage.ProfileVersion {
			out = append(out, coverage.Evidence{Start: recorded.UTC(), Barrier: coverage.Unknown})
			continue
		}
		if conflict {
			out = append(out, coverage.Evidence{Start: recorded.UTC(), Barrier: coverage.Ambiguous})
			continue
		}
		if !provenance || len(digest) != 32 {
			out = append(out, coverage.Evidence{Start: recorded.UTC(), Barrier: coverage.Unknown})
			continue
		}
		if !start.Valid || !end.Valid || !boot.Valid || !sequence.Valid || !energy.Valid {
			out = append(out, coverage.Evidence{Start: recorded.UTC(), Barrier: coverage.Unknown})
			continue
		}
		value, err := strconv.ParseFloat(energy.String, 64)
		if err != nil {
			return nil, err
		}
		startUTC, endUTC := start.Time.UTC(), end.Time.UTC()
		out = append(out, coverage.Evidence{
			Start: startUTC, End: endUTC, DeviceID: deviceID,
			BootCounter: boot.Int64, Sequence: sequence.Int64,
			EnergyKwh: value, Conflict: conflict,
			Attributable: assignmentCovers(assignments, deviceID, startUTC, endUTC),
		})
	}
	return out, rows.Err()
}

func loadProtocol0Barriers(ctx context.Context, tx *sql.Tx, pointID uuid.UUID, from, to time.Time) ([]coverage.Evidence, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT pr.recorded_at
FROM power_readings AS pr
JOIN device_assignments AS da
  ON da.device_id = pr.device_id
 AND da.valid_from <= pr.recorded_at
 AND (da.valid_to IS NULL OR pr.recorded_at < da.valid_to)
WHERE pr.protocol_version = 0
  AND da.measurement_point_id = $1
  AND pr.recorded_at >= $2
  AND pr.recorded_at < $3
ORDER BY pr.recorded_at`, pointID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []coverage.Evidence
	for rows.Next() {
		var recorded time.Time
		if err := rows.Scan(&recorded); err != nil {
			return nil, err
		}
		out = append(out, coverage.Evidence{Start: recorded.UTC(), Barrier: coverage.Unknown})
	}
	return out, rows.Err()
}
