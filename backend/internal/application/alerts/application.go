package alerts

import (
	"context"
	"errors"
	"math"
	"regexp"
	"strconv"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"power-iot-backend/internal/adapters/persistence"
)

var (
	ErrSettingsNotFound = errors.New("alert settings not found")
	ErrHistoryNotFound  = errors.New("alert history shop not found")
	ErrInvalidSettings  = errors.New("invalid alert settings")
	ErrInvalidCursor    = errors.New("invalid alert cursor")
)

type Settings struct {
	MeasurementPointID string
	IsEnabled          bool
	QuietHoursStart    string
	QuietHoursEnd      string
	PowerThresholdW    float64
	UpdatedAt          time.Time
}
type SettingsUpdate struct {
	IsEnabled       bool
	QuietHoursStart string
	QuietHoursEnd   string
	PowerThresholdW float64
}
type Alert struct {
	ID                   string
	DeviceID             uint
	DeviceName           string
	SerialNumber         *string
	MeasurementPointID   string
	MeasurementPointName string
	Type                 string
	Message              string
	Voltage              float64
	Current              float64
	Power                float64
	CreatedAt            time.Time
}
type HistoryPage struct {
	Items      []Alert
	NextCursor string
}

type Repository interface {
	FindMeasurementPointAlertSettings(context.Context, uint, uint, uuid.UUID) (persistence.MeasurementPointAlertSettingsProjection, error)
	SetMeasurementPointAlertSettings(context.Context, uint, uint, uuid.UUID, string, string, float64, bool) error
	FindAlertHistory(context.Context, uint, uint, *uuid.UUID, int, string) (persistence.AlertHistoryPage, error)
}
type Service struct{ repository Repository }

func New(repository Repository) *Service { return &Service{repository: repository} }

func (s *Service) GetSettings(ctx context.Context, userID, shopID uint, pointID string) (Settings, error) {
	id, err := parseUUID(pointID)
	if err != nil || userID == 0 || shopID == 0 || s == nil || s.repository == nil {
		return Settings{}, ErrSettingsNotFound
	}
	row, err := s.repository.FindMeasurementPointAlertSettings(ctx, userID, shopID, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Settings{}, ErrSettingsNotFound
	}
	if err != nil {
		return Settings{}, err
	}
	return Settings{MeasurementPointID: row.MeasurementPointID.String(), IsEnabled: row.IsEnabled, QuietHoursStart: row.QuietHoursStart, QuietHoursEnd: row.QuietHoursEnd, PowerThresholdW: row.PowerThresholdW, UpdatedAt: row.UpdatedAt}, nil
}

func (s *Service) UpdateSettings(ctx context.Context, userID, shopID uint, pointID string, update SettingsUpdate) error {
	id, err := parseUUID(pointID)
	if err != nil || userID == 0 || shopID == 0 || !validUpdate(update) {
		return ErrInvalidSettings
	}
	if s == nil || s.repository == nil {
		return errors.New("alert settings repository unavailable")
	}
	err = s.repository.SetMeasurementPointAlertSettings(ctx, userID, shopID, id, update.QuietHoursStart, update.QuietHoursEnd, update.PowerThresholdW, update.IsEnabled)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrSettingsNotFound
	}
	return err
}

func (s *Service) History(ctx context.Context, userID, shopID uint, pointRef string, limit int, cursor string) (HistoryPage, error) {
	if s == nil || s.repository == nil || userID == 0 || shopID == 0 {
		return HistoryPage{}, ErrHistoryNotFound
	}
	var pointID *uuid.UUID
	if pointRef != "" {
		id, err := parseUUID(pointRef)
		if err != nil {
			return HistoryPage{}, ErrInvalidCursor
		}
		pointID = &id
	}
	row, err := s.repository.FindAlertHistory(ctx, userID, shopID, pointID, limit, cursor)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return HistoryPage{}, ErrHistoryNotFound
	}
	if errors.Is(err, persistence.ErrInvalidAlertCursor) {
		return HistoryPage{}, ErrInvalidCursor
	}
	if err != nil {
		return HistoryPage{}, err
	}
	out := HistoryPage{NextCursor: row.NextCursor, Items: make([]Alert, 0, len(row.Items))}
	for _, item := range row.Items {
		out.Items = append(out.Items, Alert{ID: strconv.FormatUint(item.ID, 10), DeviceID: item.DeviceID, DeviceName: item.DeviceName, SerialNumber: item.SerialNumber, MeasurementPointID: item.MeasurementPointID.String(), MeasurementPointName: item.MeasurementPointName, Type: item.Type, Message: item.Message, Voltage: item.Voltage, Current: item.Current, Power: item.Power, CreatedAt: item.CreatedAt})
	}
	return out, nil
}

func parseUUID(raw string) (uuid.UUID, error) {
	if raw != stringTrim(raw) {
		return uuid.Nil, errors.New("invalid uuid")
	}
	id, err := uuid.Parse(raw)
	if err != nil || id == uuid.Nil {
		return uuid.Nil, errors.New("invalid uuid")
	}
	return id, nil
}

func stringTrim(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n' || s[0] == '\r') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t' || s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

var clockPattern = regexp.MustCompile(`^(?:[01][0-9]|2[0-3]):[0-5][0-9]$`)

func validUpdate(update SettingsUpdate) bool {
	if math.IsNaN(update.PowerThresholdW) || math.IsInf(update.PowerThresholdW, 0) || update.PowerThresholdW <= 0 {
		return false
	}
	if (update.QuietHoursStart == "") != (update.QuietHoursEnd == "") {
		return false
	}
	if update.QuietHoursStart != "" && update.QuietHoursStart == update.QuietHoursEnd {
		return false
	}
	return (update.QuietHoursStart == "" || clockPattern.MatchString(update.QuietHoursStart)) && (update.QuietHoursEnd == "" || clockPattern.MatchString(update.QuietHoursEnd))
}
