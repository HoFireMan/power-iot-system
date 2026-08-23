package energycoverage

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"power-iot-backend/internal/adapters/persistence"
	"power-iot-backend/internal/core/coverage"
)

type fakeQuery struct {
	projection persistence.EnergyCoverageProjection
	now        func() time.Time
}

func (f *fakeQuery) FindMeasurementPointEnergy(_ context.Context, _ uuid.UUID, now func() time.Time) (persistence.EnergyCoverageProjection, error) {
	f.now = now
	return f.projection, nil
}

func TestServicePreservesIndependentTodayMonthWatermarksAndZero(t *testing.T) {
	todayThrough := time.Date(2026, 8, 24, 1, 0, 0, 0, time.UTC)
	monthThrough := time.Date(2026, 8, 24, 2, 0, 0, 0, time.UTC)
	zero := 0.0
	query := &fakeQuery{projection: persistence.EnergyCoverageProjection{
		Today:    persistence.EnergyCoverageWindow{Kwh: &zero, Watermark: &todayThrough, State: coverage.Proven},
		Month:    persistence.EnergyCoverageWindow{Kwh: nil, Watermark: &monthThrough, State: coverage.Gap},
		Snapshot: time.Date(2026, 8, 24, 4, 0, 0, 0, time.UTC),
	}}
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.FixedZone("Asia/Taipei", 8*60*60))
	result, err := New(query, func() time.Time { return now }).Get(context.Background(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if result.Today.Kwh == nil || *result.Today.Kwh != 0 || result.Today.ThroughAt == nil || !result.Today.ThroughAt.Equal(todayThrough) {
		t.Fatalf("today=%+v", result.Today)
	}
	if result.Month.Kwh != nil || result.Month.ThroughAt == nil || !result.Month.ThroughAt.Equal(monthThrough) || result.Month.State != string(coverage.Gap) {
		t.Fatalf("month=%+v", result.Month)
	}
	if query.now == nil || !query.now().Equal(now) {
		t.Fatal("service did not pass its configured Asia/Taipei-capable clock")
	}
}
