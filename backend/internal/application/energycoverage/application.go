// Package energycoverage exposes the internal, non-HTTP Today/Month energy
// capability for a MeasurementPoint.
package energycoverage

import (
	"context"
	"time"

	"github.com/google/uuid"
	"power-iot-backend/internal/adapters/persistence"
)

type Window struct {
	Kwh       *float64
	ThroughAt *time.Time
	State     string
}

type Result struct {
	Today    Window
	Month    Window
	Snapshot time.Time
}

type Query interface {
	FindMeasurementPointEnergy(context.Context, uuid.UUID, func() time.Time) (persistence.EnergyCoverageProjection, error)
}

type Service struct {
	query Query
	now   func() time.Time
}

func New(query Query, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{query: query, now: now}
}

func (s *Service) Get(ctx context.Context, pointID uuid.UUID) (Result, error) {
	if s == nil || s.query == nil || pointID == uuid.Nil {
		return Result{}, persistence.ErrDashboardNotFound
	}
	projection, err := s.query.FindMeasurementPointEnergy(ctx, pointID, s.now)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Today:    Window{Kwh: projection.Today.Kwh, ThroughAt: projection.Today.Watermark, State: string(projection.Today.State)},
		Month:    Window{Kwh: projection.Month.Kwh, ThroughAt: projection.Month.Watermark, State: string(projection.Month.State)},
		Snapshot: projection.Snapshot,
	}, nil
}
