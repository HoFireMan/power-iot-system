// Package adminbindingaudit exposes the read-only Admin Binding audit history
// capability. It validates transport-neutral filters and maps persistence
// projections without allowing the HTTP layer to become an authorization
// implementation.
package adminbindingaudit

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"power-iot-backend/internal/adapters/persistence"
)

var (
	ErrHistoryNotFound        = errors.New("admin binding audit history shop not found")
	ErrHistoryForbidden       = errors.New("admin binding audit history forbidden")
	ErrAuthenticationRequired = errors.New("admin binding audit history authentication required")
	ErrInvalidCursor          = errors.New("invalid admin binding audit cursor")
	ErrInvalidFilter          = errors.New("invalid admin binding audit history filter")
)

var actions = map[string]bool{
	"create_measurement_point": true,
	"bind":                     true,
	"replace":                  true,
	"relocate":                 true,
	"unbind":                   true,
}

type Audit struct {
	ID                      uuid.UUID
	OperationID             uuid.UUID
	Action                  string
	OccurredAt              time.Time
	EffectiveAt             *time.Time
	Reason                  *string
	ActorID                 uint
	ActorName               string
	MeasurementPointID      *uuid.UUID
	MeasurementPointName    string
	DeviceID                *uint
	DeviceName              string
	DeviceSerialNumber      *string
	DeviceMAC               *string
	OldMeasurementPointID   *uuid.UUID
	OldMeasurementPointName string
	NewMeasurementPointID   *uuid.UUID
	NewMeasurementPointName string
	OldAssignmentID         *uuid.UUID
	NewAssignmentID         *uuid.UUID
}
type HistoryPage struct {
	Items      []Audit
	NextCursor string
}

type Repository interface {
	FindAdminBindingAuditHistory(context.Context, persistence.AdminBindingAuditHistoryQuery) (persistence.AdminBindingAuditHistoryPage, error)
}
type Service struct{ repository Repository }

func New(repository Repository) *Service { return &Service{repository: repository} }

func (s *Service) History(ctx context.Context, userID, shopID uint, action, measurementPointRef, deviceRef string, limit int, cursor string, sessionID uuid.UUID) (HistoryPage, error) {
	if s == nil || s.repository == nil || userID == 0 || shopID == 0 {
		return HistoryPage{}, ErrHistoryNotFound
	}
	q := persistence.AdminBindingAuditHistoryQuery{UserID: userID, ShopID: shopID, SessionID: sessionID, Limit: limit, Cursor: cursor}
	if action != "" {
		if !actions[action] || strings.TrimSpace(action) != action {
			return HistoryPage{}, ErrInvalidFilter
		}
		q.Action = action
	}
	if measurementPointRef != "" {
		if strings.TrimSpace(measurementPointRef) != measurementPointRef {
			return HistoryPage{}, ErrInvalidFilter
		}
		id, err := uuid.Parse(measurementPointRef)
		if err != nil || id == uuid.Nil {
			return HistoryPage{}, ErrInvalidFilter
		}
		q.MeasurementPointID = &id
	}
	if deviceRef != "" {
		if strings.TrimSpace(deviceRef) != deviceRef {
			return HistoryPage{}, ErrInvalidFilter
		}
		value, err := strconv.ParseUint(deviceRef, 10, 64)
		if err != nil || value == 0 || uint64(uint(value)) != value {
			return HistoryPage{}, ErrInvalidFilter
		}
		id := uint(value)
		q.DeviceID = &id
	}
	if cursor != "" && strings.TrimSpace(cursor) != cursor {
		return HistoryPage{}, ErrInvalidCursor
	}
	row, err := s.repository.FindAdminBindingAuditHistory(ctx, q)
	if errors.Is(err, persistence.ErrAdminBindingAuditHistoryNotFound) {
		return HistoryPage{}, ErrHistoryNotFound
	}
	if errors.Is(err, persistence.ErrAdminBindingAuditHistoryForbidden) {
		return HistoryPage{}, ErrHistoryForbidden
	}
	if errors.Is(err, persistence.ErrAdminBindingAuditHistoryAuthenticationRequired) {
		return HistoryPage{}, ErrAuthenticationRequired
	}
	if errors.Is(err, persistence.ErrInvalidAdminBindingAuditCursor) {
		return HistoryPage{}, ErrInvalidCursor
	}
	if err != nil {
		return HistoryPage{}, err
	}
	out := HistoryPage{NextCursor: row.NextCursor, Items: make([]Audit, 0, len(row.Items))}
	for _, item := range row.Items {
		out.Items = append(out.Items, Audit{ID: item.ID, OperationID: item.OperationID, Action: item.Action, OccurredAt: item.OccurredAt, EffectiveAt: item.EffectiveAt, Reason: item.Reason, ActorID: item.ActorID, ActorName: item.ActorName, MeasurementPointID: item.MeasurementPointID, MeasurementPointName: item.MeasurementPointName, DeviceID: item.DeviceID, DeviceName: item.DeviceName, DeviceSerialNumber: item.DeviceSerialNumber, DeviceMAC: item.DeviceMAC, OldMeasurementPointID: item.OldMeasurementPointID, OldMeasurementPointName: item.OldMeasurementPointName, NewMeasurementPointID: item.NewMeasurementPointID, NewMeasurementPointName: item.NewMeasurementPointName, OldAssignmentID: item.OldAssignmentID, NewAssignmentID: item.NewAssignmentID})
	}
	return out, nil
}
