package alerts

import (
	"context"
	"errors"
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
	DailyLimitKwh      *float64
	MonthlyLimitKwh    *float64
	NonUsageStartTime  string
	NonUsageEndTime    string
	IsEnabled          bool
	UpdatedAt          time.Time
}
type SettingsUpdate struct {
	DailyLimitKwh     *float64
	MonthlyLimitKwh   *float64
	NonUsageStartTime string
	NonUsageEndTime   string
	IsEnabled         bool
}
type Alert struct {
	ID                   string
	MeasurementPointID   string
	MeasurementPointName string
	Type                 string
	Message              string
	Voltage              float64
	Current              float64
	Power                float64
	IsRead               bool
	RecordedAt           time.Time
}
type HistoryPage struct {
	Items      []Alert
	NextCursor string
}

type Repository interface {
	FindMeasurementPointAlertSettings(context.Context, uint, uuid.UUID) (persistence.MeasurementPointAlertSettingsProjection, error)
	SetMeasurementPointAlertSettings(context.Context, uint, uuid.UUID, *float64, *float64, string, string, bool) error
	FindAlertHistory(context.Context, uint, uint, int, string) (persistence.AlertHistoryPage, error)
}
type Service struct{ repository Repository }

func New(repository Repository) *Service { return &Service{repository: repository} }

func (s *Service) GetSettings(ctx context.Context, userID uint, pointID string) (Settings, error) {
	id, err := parseUUID(pointID)
	if err != nil || userID == 0 {
		return Settings{}, ErrSettingsNotFound
	}
	if s == nil || s.repository == nil {
		return Settings{}, ErrSettingsNotFound
	}
	row, err := s.repository.FindMeasurementPointAlertSettings(ctx, userID, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Settings{}, ErrSettingsNotFound
	}
	if err != nil {
		return Settings{}, err
	}
	return Settings{MeasurementPointID: row.MeasurementPointID.String(), DailyLimitKwh: row.DailyLimitKwh, MonthlyLimitKwh: row.MonthlyLimitKwh, NonUsageStartTime: row.NonUsageStartTime, NonUsageEndTime: row.NonUsageEndTime, IsEnabled: row.IsEnabled, UpdatedAt: row.UpdatedAt}, nil
}
func (s *Service) UpdateSettings(ctx context.Context, userID uint, pointID string, update SettingsUpdate) error {
	id, err := parseUUID(pointID)
	if err != nil || userID == 0 || !validUpdate(update) {
		return ErrInvalidSettings
	}
	if s == nil || s.repository == nil {
		return errors.New("alert settings repository unavailable")
	}
	err = s.repository.SetMeasurementPointAlertSettings(ctx, userID, id, update.DailyLimitKwh, update.MonthlyLimitKwh, update.NonUsageStartTime, update.NonUsageEndTime, update.IsEnabled)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrSettingsNotFound
	}
	return err
}
func (s *Service) History(ctx context.Context, userID, shopID uint, limit int, cursor string) (HistoryPage, error) {
	if s == nil || s.repository == nil || userID == 0 || shopID == 0 {
		return HistoryPage{}, ErrHistoryNotFound
	}
	row, err := s.repository.FindAlertHistory(ctx, userID, shopID, limit, cursor)
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
		out.Items = append(out.Items, Alert{ID: strconv.FormatUint(item.ID, 10), MeasurementPointID: item.MeasurementPointID.String(), MeasurementPointName: item.MeasurementPointName, Type: item.Type, Message: item.Message, Voltage: item.Voltage, Current: item.Current, Power: item.Power, IsRead: item.IsRead, RecordedAt: item.RecordedAt})
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
	if update.DailyLimitKwh != nil && (*update.DailyLimitKwh < 0 || *update.DailyLimitKwh > 1e9) {
		return false
	}
	if update.MonthlyLimitKwh != nil && (*update.MonthlyLimitKwh < 0 || *update.MonthlyLimitKwh > 1e9) {
		return false
	}
	if (update.NonUsageStartTime == "") != (update.NonUsageEndTime == "") {
		return false
	}
	return (update.NonUsageStartTime == "" || clockPattern.MatchString(update.NonUsageStartTime)) && (update.NonUsageEndTime == "" || clockPattern.MatchString(update.NonUsageEndTime))
}
