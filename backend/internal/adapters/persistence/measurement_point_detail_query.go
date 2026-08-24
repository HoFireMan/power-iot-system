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

var ErrMeasurementPointNotFound = errors.New("measurement point not found")

type MeasurementPointDetailProjection struct {
	Point              MeasurementPointProjection
	CurrentDevice      *MeasurementPointAssignmentProjection
	AssignmentHistory  []MeasurementPointAssignmentProjection
	CurrentPowerW      *float64
	CurrentPowerSeenAt *time.Time
	ScopedAdmin        bool
}

type MeasurementPointProjection struct {
	ID   uuid.UUID
	Shop DashboardShopProjection
	Name string
}

type MeasurementPointAssignmentProjection struct {
	ID         uuid.UUID
	DeviceID   uint
	Name       string
	MacAddress string
	IsOnline   bool
	LastSeen   *time.Time
	ValidFrom  time.Time
	ValidTo    *time.Time
}

// FindMeasurementPointDetail establishes the database transaction snapshot
// before evaluating the application clock. This keeps the current-assignment
// and current-power cap deterministic without weakening the B-02 repository.
func (r *MeasurementPointDetailQueryRepository) FindMeasurementPointDetail(
	ctx context.Context,
	userID, shopID uint,
	pointID uuid.UUID,
	now func() time.Time,
) (MeasurementPointDetailProjection, error) {
	if r == nil || r.db == nil || userID == 0 || shopID == 0 || pointID == uuid.Nil || now == nil {
		return MeasurementPointDetailProjection{}, ErrMeasurementPointNotFound
	}
	if ctx == nil {
		ctx = context.Background()
	}
	db, err := r.db.DB()
	if err != nil {
		return MeasurementPointDetailProjection{}, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return MeasurementPointDetailProjection{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	var dbSnapshot time.Time
	if err := tx.QueryRowContext(ctx, "SELECT transaction_timestamp()").Scan(&dbSnapshot); err != nil {
		return MeasurementPointDetailProjection{}, err
	}
	_ = dbSnapshot
	snapshot := now().UTC()

	var point struct {
		ID       uuid.UUID
		ShopID   uint
		ShopCode string
		ShopName string
		Name     string
		IsAdmin  bool
	}
	err = tx.QueryRowContext(ctx, `
SELECT mp.id, s.id, s.code, s.name, mp.name, u.is_admin
FROM measurement_points AS mp
JOIN shops AS s ON s.id = mp.shop_id AND s.is_active = TRUE
JOIN user_shop_relations AS relation
  ON relation.shop_id = s.id AND relation.user_id = $1
JOIN users AS u ON u.id = relation.user_id
WHERE s.id = $2 AND mp.id = $3`, userID, shopID, pointID).
		Scan(&point.ID, &point.ShopID, &point.ShopCode, &point.ShopName, &point.Name, &point.IsAdmin)
	if errors.Is(err, sql.ErrNoRows) {
		return MeasurementPointDetailProjection{}, ErrMeasurementPointNotFound
	}
	if err != nil {
		return MeasurementPointDetailProjection{}, err
	}

	out := MeasurementPointDetailProjection{
		Point:             MeasurementPointProjection{ID: point.ID, Name: point.Name, Shop: DashboardShopProjection{ID: point.ShopID, Code: point.ShopCode, Name: point.ShopName}},
		AssignmentHistory: make([]MeasurementPointAssignmentProjection, 0),
		ScopedAdmin:       point.IsAdmin,
	}
	rows, err := tx.QueryContext(ctx, `
SELECT da.id, da.device_id, d.name, d.mac_address, d.is_online, d.last_seen,
       da.valid_from, da.valid_to,
       CASE WHEN da.valid_from <= $2 AND (da.valid_to IS NULL OR $2 < da.valid_to) THEN TRUE ELSE FALSE END AS is_current,
       latest.active_power, latest.received_at
FROM device_assignments AS da
JOIN devices AS d ON d.id = da.device_id
LEFT JOIN LATERAL (
  SELECT pr.active_power, pr.received_at
  FROM power_readings AS pr
  WHERE pr.device_id = da.device_id
    AND pr.measurement_point_id = da.measurement_point_id
    AND pr.received_at >= $3
    AND pr.received_at <= $2
    AND pr.received_at >= da.valid_from
    AND (da.valid_to IS NULL OR pr.received_at < da.valid_to)
    AND pr.active_power IS NOT NULL
  ORDER BY pr.received_at DESC, pr.id DESC
  LIMIT 1
) AS latest ON TRUE
WHERE da.measurement_point_id = $1
ORDER BY da.valid_from DESC, da.id DESC`, pointID, snapshot, snapshot.Add(-120*time.Second))
	if err != nil {
		return MeasurementPointDetailProjection{}, err
	}
	defer rows.Close()
	var currentCount, acceptedCount int
	var powerSum float64
	var latestSeen *time.Time
	for rows.Next() {
		var assignmentID uuid.UUID
		var deviceID uint
		var name, mac string
		var online bool
		var lastSeen sql.NullTime
		var from time.Time
		var to sql.NullTime
		var current bool
		var activePower sql.NullString
		var received sql.NullTime
		if err := rows.Scan(&assignmentID, &deviceID, &name, &mac, &online, &lastSeen, &from, &to, &current, &activePower, &received); err != nil {
			return MeasurementPointDetailProjection{}, err
		}
		assignment := MeasurementPointAssignmentProjection{ID: assignmentID, DeviceID: deviceID, Name: name, MacAddress: mac, IsOnline: online, ValidFrom: from.UTC()}
		if lastSeen.Valid {
			value := lastSeen.Time.UTC()
			assignment.LastSeen = &value
		}
		if to.Valid {
			value := to.Time.UTC()
			assignment.ValidTo = &value
		}
		out.AssignmentHistory = append(out.AssignmentHistory, assignment)
		if !current {
			continue
		}
		currentCount++
		out.CurrentDevice = &assignment
		if activePower.Valid {
			value, err := strconv.ParseFloat(activePower.String, 64)
			if err != nil {
				return MeasurementPointDetailProjection{}, err
			}
			acceptedCount++
			powerSum += value
			if received.Valid && (latestSeen == nil || received.Time.After(*latestSeen)) {
				value := received.Time.UTC()
				latestSeen = &value
			}
		}
	}
	if err := rows.Err(); err != nil {
		return MeasurementPointDetailProjection{}, err
	}
	if currentCount > 0 && acceptedCount == currentCount {
		out.CurrentPowerW = &powerSum
		out.CurrentPowerSeenAt = latestSeen
	}
	if err := tx.Commit(); err != nil {
		return MeasurementPointDetailProjection{}, err
	}
	committed = true
	return out, nil
}

type MeasurementPointDetailQueryRepository struct{ db *gorm.DB }

func NewMeasurementPointDetailQueryRepository(db *gorm.DB) *MeasurementPointDetailQueryRepository {
	return &MeasurementPointDetailQueryRepository{db: db}
}

var _ interface {
	FindMeasurementPointDetail(context.Context, uint, uint, uuid.UUID, func() time.Time) (MeasurementPointDetailProjection, error)
} = (*MeasurementPointDetailQueryRepository)(nil)
