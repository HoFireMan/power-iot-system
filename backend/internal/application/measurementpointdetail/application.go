// Package measurementpointdetail exposes the protected, read-only
// Measurement Point-centered detail capability.
package measurementpointdetail

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"power-iot-backend/internal/adapters/persistence"
	"power-iot-backend/internal/application/energycoverage"
)

var ErrMeasurementPointNotFound = errors.New("measurement point not found")

type Shop struct{ Code, Name string }
type Point struct {
	ID, Name string
	Shop     Shop
}
type Device struct {
	DisplayName string
	Mac         string
	IsOnline    bool
	LastSeen    *time.Time
}
type Assignment struct {
	DisplayName string
	Mac         string
	ValidFrom   time.Time
	ValidTo     *time.Time
}
type EnergyWindow struct {
	Kwh             *float64
	CompleteThrough *time.Time
}
type TechnicalInfo struct {
	MeasurementPointID string
	DeviceID           *string
}
type Detail struct {
	Point              Point
	Status             string
	CurrentPowerW      *float64
	CurrentPowerSeenAt *time.Time
	Today              EnergyWindow
	Month              EnergyWindow
	CurrentDevice      *Device
	AssignmentHistory  []Assignment
	TechnicalInfo      *TechnicalInfo
}

type Query interface {
	FindMeasurementPointDetail(context.Context, uint, uint, uuid.UUID, func() time.Time) (persistence.MeasurementPointDetailProjection, error)
}

type Service struct {
	query  Query
	energy *energycoverage.Service
	now    func() time.Time
}

func New(query Query, energy energycoverage.Query, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{query: query, energy: energycoverage.New(energy, now), now: now}
}

func (s *Service) Get(ctx context.Context, userID, shopID uint, pointID uuid.UUID) (Detail, error) {
	if s == nil || s.query == nil || userID == 0 || shopID == 0 || pointID == uuid.Nil {
		return Detail{}, ErrMeasurementPointNotFound
	}
	requestSnapshot := s.now().UTC()
	now := func() time.Time { return requestSnapshot }
	projection, err := s.query.FindMeasurementPointDetail(ctx, userID, shopID, pointID, now)
	if err != nil {
		if errors.Is(err, persistence.ErrMeasurementPointNotFound) {
			return Detail{}, ErrMeasurementPointNotFound
		}
		return Detail{}, err
	}
	result := Detail{
		Point:         Point{ID: projection.Point.ID.String(), Name: projection.Point.Name, Shop: Shop{Code: projection.Point.Shop.Code, Name: projection.Point.Shop.Name}},
		CurrentPowerW: projection.CurrentPowerW, CurrentPowerSeenAt: projection.CurrentPowerSeenAt,
		Status: "unbound", AssignmentHistory: make([]Assignment, 0, len(projection.AssignmentHistory)),
	}
	if projection.CurrentDevice != nil {
		result.CurrentDevice = &Device{DisplayName: projection.CurrentDevice.Name, Mac: projection.CurrentDevice.MacAddress, IsOnline: projection.CurrentDevice.IsOnline, LastSeen: projection.CurrentDevice.LastSeen}
		if projection.CurrentDevice.IsOnline {
			result.Status = "online"
		} else {
			result.Status = "offline"
		}
	}
	for _, row := range projection.AssignmentHistory {
		result.AssignmentHistory = append(result.AssignmentHistory, Assignment{DisplayName: row.Name, Mac: row.MacAddress, ValidFrom: row.ValidFrom, ValidTo: row.ValidTo})
	}
	if projection.ScopedAdmin {
		deviceID := (*string)(nil)
		if projection.CurrentDevice != nil {
			value := strconv.FormatUint(uint64(projection.CurrentDevice.DeviceID), 10)
			deviceID = &value
		}
		result.TechnicalInfo = &TechnicalInfo{MeasurementPointID: projection.Point.ID.String(), DeviceID: deviceID}
	}
	if s.energy != nil {
		coverage, err := s.energy.GetAt(ctx, pointID, now)
		if err != nil {
			return Detail{}, err
		}
		result.Today = EnergyWindow{Kwh: coverage.Today.Kwh, CompleteThrough: coverage.Today.ThroughAt}
		result.Month = EnergyWindow{Kwh: coverage.Month.Kwh, CompleteThrough: coverage.Month.ThroughAt}
	}
	return result, nil
}

type GormQueryRunner struct{ service *Service }

func NewGormQueryRunner(db *gorm.DB) *GormQueryRunner {
	return &GormQueryRunner{service: New(persistence.NewMeasurementPointDetailQueryRepository(db), persistence.NewEnergyCoverageQueryRepository(db), nil)}
}

func NewQueryRunner(query Query, energy energycoverage.Query, now func() time.Time) *GormQueryRunner {
	return &GormQueryRunner{service: New(query, energy, now)}
}

func (r *GormQueryRunner) GetMeasurementPointDetail(ctx context.Context, userID, shopID uint, pointID uuid.UUID) (Detail, error) {
	if r == nil || r.service == nil {
		return Detail{}, ErrMeasurementPointNotFound
	}
	return r.service.Get(ctx, userID, shopID, pointID)
}

var _ interface {
	GetMeasurementPointDetail(context.Context, uint, uint, uuid.UUID) (Detail, error)
} = (*GormQueryRunner)(nil)
