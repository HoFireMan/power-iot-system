package persistence

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
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
}

type DashboardQuery interface {
	FindDashboard(context.Context, uint, uint, time.Time) (DashboardProjection, error)
}

type DashboardQueryRepository struct{ db *gorm.DB }

var _ DashboardQuery = (*DashboardQueryRepository)(nil)

func NewDashboardQueryRepository(db *gorm.DB) *DashboardQueryRepository {
	return &DashboardQueryRepository{db: db}
}

// FindDashboard authorizes the requested shop only through the authenticated
// user's explicit relation and active Shop row. Device scope follows the
// current assignment -> measurement point -> shop -> device path. Device.ShopID
// is deliberately absent from this query. The snapshot instant is supplied by
// the application clock and used for both assignment and reading acceptance.
func (r *DashboardQueryRepository) FindDashboard(ctx context.Context, userID, shopID uint, snapshotNow time.Time) (DashboardProjection, error) {
	if r == nil || r.db == nil || userID == 0 || shopID == 0 {
		return DashboardProjection{}, ErrDashboardNotFound
	}
	snapshotNow = snapshotNow.UTC()
	freshSince := snapshotNow.Add(-120 * time.Second)
	var rows []struct {
		ShopID             uint
		ShopCode           string
		ShopName           string
		DeviceID           sql.NullInt64
		MeasurementPointID uuid.NullUUID
		DeviceName         sql.NullString
		DeviceOnline       sql.NullBool
		DeviceSeen         sql.NullTime
		CurrentPower       sql.NullString
	}
	query := `
WITH authorized_shop AS (
	SELECT s.id, s.code, s.name
	FROM shops AS s
	JOIN user_shop_relations AS relation ON relation.shop_id = s.id
	WHERE relation.user_id = ?
	  AND s.id = ?
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
	 AND assignment.valid_from <= ?
	 AND (assignment.valid_to IS NULL OR ? < assignment.valid_to)
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
	WHERE reading.received_at >= ?
	  AND reading.received_at <= ?
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
       END AS current_power
FROM authorized_shop
CROSS JOIN coverage
LEFT JOIN current_assignments AS current_assignment ON TRUE
LEFT JOIN devices AS device ON device.id = current_assignment.device_id
ORDER BY device.id ASC`
	result := r.db.WithContext(queryContext(ctx)).Raw(query,
		userID, shopID, snapshotNow, snapshotNow, freshSince, snapshotNow,
	).Scan(&rows)
	if result.Error != nil {
		return DashboardProjection{}, result.Error
	}
	if len(rows) == 0 {
		return DashboardProjection{}, ErrDashboardNotFound
	}
	out := DashboardProjection{
		Shop:    DashboardShopProjection{ID: rows[0].ShopID, Code: rows[0].ShopCode, Name: rows[0].ShopName},
		Devices: make([]DashboardDeviceProjection, 0, len(rows)),
	}
	for _, row := range rows {
		if row.CurrentPower.Valid {
			value, err := strconv.ParseFloat(row.CurrentPower.String, 64)
			if err != nil {
				return DashboardProjection{}, err
			}
			if out.CurrentPowerW == nil {
				out.CurrentPowerW = &value
			} else if *out.CurrentPowerW != value {
				// Every result row is produced from the same coverage CTE. A
				// disagreement indicates a broken projection and must not leak
				// an arbitrary aggregate.
				return DashboardProjection{}, errors.New("inconsistent dashboard power projection")
			}
		}
		if !row.DeviceID.Valid {
			continue
		}
		if row.DeviceID.Int64 <= 0 || uint64(uint(row.DeviceID.Int64)) != uint64(row.DeviceID.Int64) || !row.MeasurementPointID.Valid || row.MeasurementPointID.UUID == uuid.Nil {
			return DashboardProjection{}, ErrDashboardNotFound
		}
		device := DashboardDeviceProjection{ID: uint(row.DeviceID.Int64), MeasurementPointID: row.MeasurementPointID.UUID, Name: row.DeviceName.String, IsOnline: row.DeviceOnline.Bool}
		if row.DeviceSeen.Valid {
			seen := row.DeviceSeen.Time
			device.LastSeen = &seen
		}
		out.Devices = append(out.Devices, device)
	}
	return out, nil
}
