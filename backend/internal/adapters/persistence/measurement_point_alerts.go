package persistence

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type MeasurementPointAlertSettingsProjection struct {
	MeasurementPointID uuid.UUID
	QuietHoursStart    string
	QuietHoursEnd      string
	PowerThresholdW    float64
	IsEnabled          bool
	UpdatedAt          time.Time
}

type AlertHistoryProjection struct {
	ID                   uint64
	DeviceID             uint
	DeviceName           string
	SerialNumber         *string
	MeasurementPointID   uuid.UUID
	MeasurementPointName string
	Type                 string
	Message              string
	Voltage              float64
	Current              float64
	Power                float64
	CreatedAt            time.Time
}
type AlertHistoryPage struct {
	Items      []AlertHistoryProjection
	NextCursor string
}

var ErrInvalidAlertCursor = errors.New("invalid alert cursor")

type MeasurementPointAlertRepository struct{ db *gorm.DB }

func NewMeasurementPointAlertRepository(db *gorm.DB) *MeasurementPointAlertRepository {
	return &MeasurementPointAlertRepository{db: db}
}

type measurementPointAlertCursor struct {
	CreatedAt time.Time `json:"createdAt"`
	ID        uint64    `json:"id"`
}

func encodeAlertCursor(at time.Time, id uint64) string {
	body, _ := json.Marshal(measurementPointAlertCursor{CreatedAt: at.UTC(), ID: id})
	return base64.RawURLEncoding.EncodeToString(body)
}
func decodeAlertCursor(raw string) (measurementPointAlertCursor, error) {
	var c measurementPointAlertCursor
	body, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return c, ErrInvalidAlertCursor
	}
	if err := json.Unmarshal(body, &c); err != nil || c.ID == 0 || c.CreatedAt.IsZero() {
		return c, ErrInvalidAlertCursor
	}
	return c, nil
}

func (r *MeasurementPointAlertRepository) FindMeasurementPointAlertSettings(ctx context.Context, userID, shopID uint, pointID uuid.UUID) (MeasurementPointAlertSettingsProjection, error) {
	if r == nil || r.db == nil || userID == 0 || shopID == 0 || pointID == uuid.Nil {
		return MeasurementPointAlertSettingsProjection{}, gorm.ErrRecordNotFound
	}
	var row MeasurementPointAlertSettingsProjection
	query := r.db.WithContext(queryContext(ctx)).Raw(`
SELECT p.id AS measurement_point_id,
       COALESCE(s.quiet_hours_start, ''), COALESCE(s.quiet_hours_end, ''),
       COALESCE(s.power_threshold_w, 10.0), COALESCE(s.is_enabled, TRUE), COALESCE(s.updated_at, now())
FROM measurement_points p
JOIN shops sh ON sh.id=p.shop_id AND sh.is_active=TRUE
JOIN user_shop_relations rel ON rel.shop_id=sh.id AND rel.user_id=?
LEFT JOIN measurement_point_alert_settings s ON s.measurement_point_id=p.id
WHERE p.id=? AND p.shop_id=?`, userID, pointID, shopID).Scan(&row)
	if query.Error != nil {
		return row, query.Error
	}
	if query.RowsAffected != 1 {
		return row, gorm.ErrRecordNotFound
	}
	row.MeasurementPointID = pointID
	return row, nil
}

func (r *MeasurementPointAlertRepository) SetMeasurementPointAlertSettings(ctx context.Context, userID, shopID uint, pointID uuid.UUID, start, end string, threshold float64, enabled bool) error {
	if r == nil || r.db == nil || userID == 0 || shopID == 0 || pointID == uuid.Nil {
		return gorm.ErrInvalidDB
	}
	return r.db.WithContext(queryContext(ctx)).Transaction(func(tx *gorm.DB) error {
		var allowed struct{ ID uuid.UUID }
		q := tx.Raw(`SELECT p.id FROM measurement_points p JOIN shops sh ON sh.id=p.shop_id AND sh.is_active=TRUE JOIN user_shop_relations rel ON rel.shop_id=sh.id AND rel.user_id=? JOIN users u ON u.id=rel.user_id AND u.is_admin=TRUE AND u.auth_enabled=TRUE WHERE p.id=? AND p.shop_id=? FOR UPDATE OF p`, userID, pointID, shopID).Scan(&allowed)
		if q.Error != nil {
			return q.Error
		}
		if q.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		if err := tx.Exec(`INSERT INTO measurement_point_alert_settings (measurement_point_id,quiet_hours_start,quiet_hours_end,power_threshold_w,is_enabled,updated_at) VALUES (?,?,?,?,?,now()) ON CONFLICT (measurement_point_id) DO UPDATE SET quiet_hours_start=EXCLUDED.quiet_hours_start, quiet_hours_end=EXCLUDED.quiet_hours_end, power_threshold_w=EXCLUDED.power_threshold_w, is_enabled=EXCLUDED.is_enabled, updated_at=now()`, pointID, start, end, threshold, enabled).Error; err != nil {
			return err
		}
		return tx.Exec(`UPDATE measurement_point_curfew_states SET in_curfew=FALSE, last_event_at=NULL WHERE measurement_point_id=?`, pointID).Error
	})
}

func (r *MeasurementPointAlertRepository) FindAlertHistory(ctx context.Context, userID, shopID uint, pointID *uuid.UUID, limit int, cursor string) (AlertHistoryPage, error) {
	if r == nil || r.db == nil || userID == 0 || shopID == 0 {
		return AlertHistoryPage{}, gorm.ErrRecordNotFound
	}
	if limit < 1 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	var allowed struct{ ID uint }
	q := r.db.WithContext(queryContext(ctx)).Raw(`SELECT sh.id FROM shops sh JOIN user_shop_relations rel ON rel.shop_id=sh.id AND rel.user_id=? JOIN users u ON u.id=rel.user_id AND u.auth_enabled=TRUE WHERE sh.id=? AND sh.is_active=TRUE`, userID, shopID).Scan(&allowed)
	if q.Error != nil {
		return AlertHistoryPage{}, q.Error
	}
	if q.RowsAffected != 1 {
		return AlertHistoryPage{}, gorm.ErrRecordNotFound
	}
	var decoded measurementPointAlertCursor
	var err error
	if cursor != "" {
		decoded, err = decodeAlertCursor(cursor)
		if err != nil {
			return AlertHistoryPage{}, err
		}
	}
	query := r.db.WithContext(queryContext(ctx)).Table("alert_logs a").Select(`a.id, a.device_id, d.name AS device_name, d.serial_number, a.measurement_point_id, p.name AS measurement_point_name, a.type, a.message, a.voltage, a.current, a.power, a.created_at`).Joins("JOIN measurement_points p ON p.id=a.measurement_point_id").Joins("LEFT JOIN devices d ON d.id=a.device_id").Where("p.shop_id=? AND a.measurement_point_id IS NOT NULL AND a.legacy_unresolved=FALSE", shopID)
	if pointID != nil {
		query = query.Where("a.measurement_point_id=?", *pointID)
	}
	if cursor != "" {
		query = query.Where("(a.created_at, a.id) < (?, ?)", decoded.CreatedAt, decoded.ID)
	}
	var rows []AlertHistoryProjection
	if err := query.Order("a.created_at DESC, a.id DESC").Limit(limit + 1).Scan(&rows).Error; err != nil {
		return AlertHistoryPage{}, err
	}
	page := AlertHistoryPage{Items: rows}
	if len(rows) > limit {
		page.Items = rows[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeAlertCursor(last.CreatedAt, last.ID)
	}
	return page, nil
}
