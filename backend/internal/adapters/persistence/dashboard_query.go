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

var ErrDashboardNotFound = errors.New("dashboard not found")

type DashboardShopProjection struct {
	ID   uint
	Code string
	Name string
}

type DashboardDeviceProjection struct {
	ID                 uint // internal backend identity; never serialized by HTTP
	MeasurementPointID uuid.UUID
	Name               string
	IsOnline           bool
	LastSeen           *time.Time
}

type DashboardProjection struct {
	Shop          DashboardShopProjection
	Devices       []DashboardDeviceProjection
	CurrentPowerW *float64
	DailyKwh      *float64
	MonthlyKwh    *float64
	Snapshot      time.Time
}

type DashboardQuery interface {
	FindDashboard(context.Context, uint, uint, func() time.Time) (DashboardProjection, error)
}

type DashboardQueryRepository struct{ db *gorm.DB }

type dashboardQueryRow struct {
	ShopID             uint
	ShopCode           string
	ShopName           string
	DeviceID           sql.NullInt64
	MeasurementPointID uuid.NullUUID
	DeviceName         sql.NullString
	DeviceOnline       sql.NullBool
	DeviceSeen         sql.NullTime
	CurrentPower       sql.NullString
	DailyEnergy        sql.NullString
	MonthlyEnergy      sql.NullString
}

var _ DashboardQuery = (*DashboardQueryRepository)(nil)

func NewDashboardQueryRepository(db *gorm.DB) *DashboardQueryRepository {
	return &DashboardQueryRepository{db: db}
}

// FindDashboard authorizes the requested shop only through the authenticated
// user's explicit relation and active Shop row. Device scope follows the
// current assignment -> measurement point -> shop -> device path. Device.ShopID
// is deliberately absent from this query. Current power retains its complete
// fresh coverage semantics; energy is a best-effort sum of individually valid
// observed intervals across the requested local-day and local-month windows.
//
// The read transaction establishes the PostgreSQL MVCC snapshot before the
// application clock is sampled. Both metrics and both energy periods therefore
// use one coherent request view.
func (r *DashboardQueryRepository) FindDashboard(ctx context.Context, userID, shopID uint, now func() time.Time) (DashboardProjection, error) {
	if r == nil || r.db == nil || userID == 0 || shopID == 0 || now == nil {
		return DashboardProjection{}, ErrDashboardNotFound
	}
	if ctx == nil {
		ctx = context.Background()
	}
	db, err := r.db.DB()
	if err != nil {
		return DashboardProjection{}, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return DashboardProjection{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	var dbSnapshot time.Time
	if err := tx.QueryRowContext(ctx, "SELECT transaction_timestamp()").Scan(&dbSnapshot); err != nil {
		return DashboardProjection{}, err
	}
	_ = dbSnapshot
	snapshotNow := now().UTC()
	loc, err := time.LoadLocation(coverage.BusinessTimezone)
	if err != nil {
		return DashboardProjection{}, err
	}
	local := snapshotNow.In(loc)
	todayStart := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc).UTC()
	monthStart := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, loc).UTC()
	freshSince := snapshotNow.Add(-120 * time.Second)

	var rows []dashboardQueryRow
	query := `
WITH authorized_shop AS (
	SELECT s.id, s.code, s.name
	FROM shops AS s
	JOIN user_shop_relations AS relation ON relation.shop_id = s.id
	WHERE relation.user_id = $1
	  AND s.id = $2
	  AND s.is_active = TRUE
), current_assignments AS (
	SELECT assignment.id AS assignment_id,
	       assignment.device_id,
	       assignment.measurement_point_id,
	       assignment.valid_from,
	       assignment.valid_to
	FROM authorized_shop
	JOIN measurement_points AS measurement_point
	  ON measurement_point.shop_id = authorized_shop.id
	JOIN device_assignments AS assignment
	  ON assignment.measurement_point_id = measurement_point.id
	 AND assignment.valid_from <= $3
	 AND (assignment.valid_to IS NULL OR $4 < assignment.valid_to)
	JOIN devices AS device ON device.id = assignment.device_id
), accepted_readings AS (
	SELECT current_assignment.assignment_id,
	       reading.active_power,
	       row_number() OVER (
	         PARTITION BY current_assignment.assignment_id
	         ORDER BY reading.received_at DESC, reading.id DESC
	       ) AS reading_rank
	FROM current_assignments AS current_assignment
	JOIN power_readings AS reading
	  ON reading.device_id = current_assignment.device_id
	 AND reading.measurement_point_id = current_assignment.measurement_point_id
	 AND reading.received_at >= current_assignment.valid_from
	 AND (current_assignment.valid_to IS NULL OR reading.received_at < current_assignment.valid_to)
	WHERE reading.received_at >= $5
	  AND reading.received_at <= $6
	  AND reading.active_power IS NOT NULL
), latest_accepted AS (
	SELECT assignment_id, active_power
	FROM accepted_readings
	WHERE reading_rank = 1
), coverage AS (
	SELECT count(current_assignment.assignment_id) AS assignment_count,
	       count(latest.assignment_id) AS covered_count,
	       coalesce(sum(latest.active_power), 0::numeric) AS power_sum
	FROM current_assignments AS current_assignment
	LEFT JOIN latest_accepted AS latest ON latest.assignment_id = current_assignment.assignment_id
), energy_candidates AS (
	SELECT pr.id, pr.measurement_point_id, pr.recorded_at,
	       pr.interval_start, pr.interval_end, pr.energy_delta_kwh
	FROM authorized_shop AS shop
	JOIN measurement_points AS mp ON mp.shop_id = shop.id
	JOIN power_readings AS pr ON pr.measurement_point_id = mp.id
	JOIN telemetry_ingest_keys AS ingest_key
	  ON ingest_key.device_id = pr.device_id
	 AND ingest_key.boot_counter = pr.boot_counter
	 AND ingest_key.sequence = pr.sequence
	WHERE pr.recorded_at >= $7
	  AND pr.recorded_at < $6
	  AND pr.coverage_version = 1
	  AND pr.protocol_version = 1
	  AND pr.interval_start IS NOT NULL
	  AND pr.interval_end IS NOT NULL
	  AND pr.interval_start < pr.interval_end
	  AND pr.recorded_at = pr.interval_start
	  AND pr.energy_delta_kwh IS NOT NULL
	  AND pr.energy_delta_kwh >= 0
	  AND ingest_key.conflict_detected = FALSE
	  AND octet_length(ingest_key.canonical_coverage_digest) = 32
	  AND 1 = (
		SELECT count(*)
		FROM device_assignments AS attribution
		WHERE attribution.device_id = pr.device_id
		  AND attribution.measurement_point_id = pr.measurement_point_id
		  AND attribution.valid_from <= pr.interval_start
		  AND (attribution.valid_to IS NULL OR pr.interval_end <= attribution.valid_to)
	  )
), unique_energy_intervals AS (
	SELECT measurement_point_id, recorded_at, interval_start, interval_end,
	       min(energy_delta_kwh) AS energy_delta_kwh
	FROM energy_candidates
	GROUP BY measurement_point_id, recorded_at, interval_start, interval_end
	HAVING count(*) = 1
), non_overlapping_energy AS (
	SELECT candidate.*
	FROM unique_energy_intervals AS candidate
	WHERE NOT EXISTS (
		SELECT 1
		FROM unique_energy_intervals AS other
		WHERE other.measurement_point_id = candidate.measurement_point_id
		  AND other.interval_start < candidate.interval_end
		  AND candidate.interval_start < other.interval_end
		  AND (other.interval_start, other.interval_end) <> (candidate.interval_start, candidate.interval_end)
	)
), energy AS (
	SELECT
		SUM(energy_delta_kwh) FILTER (WHERE recorded_at >= $8) AS daily_energy,
		SUM(energy_delta_kwh) AS monthly_energy
	FROM non_overlapping_energy
)
SELECT authorized_shop.id AS shop_id,
       authorized_shop.code AS shop_code,
       authorized_shop.name AS shop_name,
       device.id AS device_id,
       current_assignment.measurement_point_id AS measurement_point_id,
       device.name AS device_name,
       device.is_online AS device_online,
       device.last_seen AS device_seen,
       CASE
         WHEN coverage.assignment_count > 0
          AND coverage.assignment_count = coverage.covered_count
         THEN coverage.power_sum
         ELSE NULL
       END AS current_power,
       energy.daily_energy,
       energy.monthly_energy
FROM authorized_shop
CROSS JOIN coverage
CROSS JOIN energy
LEFT JOIN current_assignments AS current_assignment ON TRUE
LEFT JOIN devices AS device ON device.id = current_assignment.device_id
ORDER BY device.id ASC`
	rowsResult, err := tx.QueryContext(ctx, query,
		userID, shopID, snapshotNow, snapshotNow, freshSince, snapshotNow,
		monthStart, todayStart,
	)
	if err != nil {
		return DashboardProjection{}, err
	}
	defer rowsResult.Close()
	if err := rowsResult.Err(); err != nil {
		return DashboardProjection{}, err
	}
	if err := scanDashboardRows(rowsResult, &rows); err != nil {
		return DashboardProjection{}, err
	}
	if len(rows) == 0 {
		return DashboardProjection{}, ErrDashboardNotFound
	}
	out := DashboardProjection{
		Shop:     DashboardShopProjection{ID: rows[0].ShopID, Code: rows[0].ShopCode, Name: rows[0].ShopName},
		Devices:  make([]DashboardDeviceProjection, 0, len(rows)),
		Snapshot: snapshotNow,
	}
	for _, row := range rows {
		if err := mergeDashboardNumeric(&out.CurrentPowerW, row.CurrentPower); err != nil {
			return DashboardProjection{}, err
		}
		if err := mergeDashboardNumeric(&out.DailyKwh, row.DailyEnergy); err != nil {
			return DashboardProjection{}, err
		}
		if err := mergeDashboardNumeric(&out.MonthlyKwh, row.MonthlyEnergy); err != nil {
			return DashboardProjection{}, err
		}
		if !row.DeviceID.Valid {
			continue
		}
		if row.DeviceID.Int64 <= 0 || uint64(uint(row.DeviceID.Int64)) != uint64(row.DeviceID.Int64) || !row.MeasurementPointID.Valid || row.MeasurementPointID.UUID == uuid.Nil {
			return DashboardProjection{}, ErrDashboardNotFound
		}
		device := DashboardDeviceProjection{ID: uint(row.DeviceID.Int64), MeasurementPointID: row.MeasurementPointID.UUID, Name: row.DeviceName.String, IsOnline: row.DeviceOnline.Bool}
		if row.DeviceSeen.Valid {
			seen := row.DeviceSeen.Time.UTC()
			device.LastSeen = &seen
		}
		out.Devices = append(out.Devices, device)
	}
	if err := tx.Commit(); err != nil {
		return DashboardProjection{}, err
	}
	committed = true
	return out, nil
}

func scanDashboardRows(rows *sql.Rows, destination *[]dashboardQueryRow) error {
	for rows.Next() {
		var row dashboardQueryRow
		if err := rows.Scan(&row.ShopID, &row.ShopCode, &row.ShopName, &row.DeviceID, &row.MeasurementPointID, &row.DeviceName, &row.DeviceOnline, &row.DeviceSeen, &row.CurrentPower, &row.DailyEnergy, &row.MonthlyEnergy); err != nil {
			return err
		}
		*destination = append(*destination, row)
	}
	return rows.Err()
}

func mergeDashboardNumeric(destination **float64, raw sql.NullString) error {
	if !raw.Valid {
		return nil
	}
	value, err := strconv.ParseFloat(raw.String, 64)
	if err != nil {
		return err
	}
	if *destination == nil {
		*destination = &value
		return nil
	}
	if **destination != value {
		return errors.New("inconsistent dashboard numeric projection")
	}
	return nil
}
