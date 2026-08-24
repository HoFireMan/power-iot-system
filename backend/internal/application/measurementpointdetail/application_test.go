package measurementpointdetail

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"power-iot-backend/internal/adapters/persistence"
	"power-iot-backend/internal/core/coverage"
)

type detailQueryStub struct {
	projection persistence.MeasurementPointDetailProjection
	seen       bool
}

func (q *detailQueryStub) FindMeasurementPointDetail(_ context.Context, _, _ uint, _ uuid.UUID, now func() time.Time) (persistence.MeasurementPointDetailProjection, error) {
	q.seen = !now().IsZero()
	return q.projection, nil
}

type energyStub struct {
	projection persistence.EnergyCoverageProjection
	observed   time.Time
}

func (q *energyStub) FindMeasurementPointEnergy(_ context.Context, _ uuid.UUID, now func() time.Time) (persistence.EnergyCoverageProjection, error) {
	q.observed = now()
	return q.projection, nil
}

func TestServiceComposesStatusAdminHistoryAndB02Windows(t *testing.T) {
	pointID := uuid.New()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	zero := 0.0
	seen := now.Add(-time.Minute)
	query := &detailQueryStub{projection: persistence.MeasurementPointDetailProjection{
		Point:             persistence.MeasurementPointProjection{ID: pointID, Shop: persistence.DashboardShopProjection{Code: "S7", Name: "Shop"}, Name: "Main"},
		CurrentDevice:     &persistence.MeasurementPointAssignmentProjection{DeviceID: 9, Name: "meter", MacAddress: "AABBCCDDEEFF", IsOnline: true, LastSeen: &seen},
		AssignmentHistory: []persistence.MeasurementPointAssignmentProjection{{Name: "meter", MacAddress: "AABBCCDDEEFF", ValidFrom: now.Add(-time.Hour)}},
		CurrentPowerW:     &zero, CurrentPowerSeenAt: &seen, ScopedAdmin: true,
	}}
	energy := &energyStub{projection: persistence.EnergyCoverageProjection{Today: persistence.EnergyCoverageWindow{Kwh: &zero, Watermark: &seen, State: coverage.Proven}}}
	nowCalls := 0
	result, err := New(query, energy, func() time.Time {
		nowCalls++
		return now
	}).Get(context.Background(), 42, 7, pointID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "online" || result.CurrentDevice == nil || result.CurrentPowerW == nil || *result.CurrentPowerW != 0 || result.Today.Kwh == nil || result.TechnicalInfo == nil || len(result.AssignmentHistory) != 1 || !query.seen || nowCalls != 1 || !energy.observed.Equal(now) {
		t.Fatalf("result=%+v", result)
	}
}

func TestServiceUnboundIsValid(t *testing.T) {
	pointID := uuid.New()
	query := &detailQueryStub{projection: persistence.MeasurementPointDetailProjection{Point: persistence.MeasurementPointProjection{ID: pointID}}}
	result, err := New(query, &energyStub{}, time.Now).Get(context.Background(), 42, 7, pointID)
	if err != nil || result.Status != "unbound" || result.CurrentDevice != nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
