package dashboard

import (
	"context"
	"testing"
	"time"

	"power-iot-backend/internal/adapters/persistence"
)

type dashboardQueryStub struct {
	projection persistence.DashboardProjection
	err        error
	now        time.Time
}

func (s *dashboardQueryStub) FindDashboard(_ context.Context, _, _ uint, now func() time.Time) (persistence.DashboardProjection, error) {
	s.now = now()
	s.projection.Snapshot = s.now
	return s.projection, s.err
}

func TestServiceUsesOneSnapshotForGeneratedAtAndAssignments(t *testing.T) {
	now := time.Date(2026, 2, 3, 4, 5, 6, 123, time.FixedZone("offset", 8*60*60))
	power := 123.45
	query := &dashboardQueryStub{projection: persistence.DashboardProjection{
		Shop:          persistence.DashboardShopProjection{ID: 12, Code: "S12", Name: "Shop"},
		Devices:       []persistence.DashboardDeviceProjection{{ID: 5, Name: "Device", IsOnline: true}},
		CurrentPowerW: &power,
		DailyKwh:      floatPtr(4.5),
		MonthlyKwh:    floatPtr(18),
	}}
	result, err := New(query, func() time.Time { return now }).GetDashboard(context.Background(), 8, 12)
	if err != nil {
		t.Fatal(err)
	}
	if !result.GeneratedAt.Equal(now.UTC()) || !query.now.Equal(now.UTC()) {
		t.Fatalf("snapshot/generated=%s/%s", query.now, result.GeneratedAt)
	}
	if len(result.Devices) != 1 || result.Devices[0].ID != "5" {
		t.Fatalf("devices=%v", result.Devices)
	}
	if result.CurrentPowerW == nil || *result.CurrentPowerW != power {
		t.Fatalf("current power=%v, want %v", result.CurrentPowerW, power)
	}
	if result.DailyKwh == nil || *result.DailyKwh != 4.5 || result.MonthlyKwh == nil || *result.MonthlyKwh != 18 {
		t.Fatalf("energy daily/monthly=%v/%v", result.DailyKwh, result.MonthlyKwh)
	}
	if result.DailyKg != nil || result.MonthlyKg != nil {
		t.Fatal("carbon values must remain nil")
	}
}

func TestServiceDerivesCarbonOnlyFromEnergyAndMatchingFactor(t *testing.T) {
	query := &dashboardQueryStub{projection: persistence.DashboardProjection{
		Shop:     persistence.DashboardShopProjection{ID: 1},
		DailyKwh: floatPtr(9.7), MonthlyKwh: floatPtr(10),
		CarbonFactorKgPerKwh: floatPtr(0.466),
	}}
	result, err := New(query, time.Now).GetDashboard(context.Background(), 2, 1)
	if err != nil || result.DailyKg == nil || *result.DailyKg != 4.5202 || result.MonthlyKg == nil || *result.MonthlyKg != 4.66 {
		t.Fatalf("carbon=%v/%v err=%v", result.DailyKg, result.MonthlyKg, err)
	}
	query.projection.CarbonFactorKgPerKwh = nil
	result, err = New(query, time.Now).GetDashboard(context.Background(), 2, 1)
	if err != nil || result.DailyKg != nil || result.MonthlyKg != nil {
		t.Fatalf("missing factor must fail closed: %+v err=%v", result, err)
	}
}

func TestServicePreservesZeroCarbonWhenEnergyIsValid(t *testing.T) {
	query := &dashboardQueryStub{projection: persistence.DashboardProjection{
		Shop:     persistence.DashboardShopProjection{ID: 1},
		DailyKwh: floatPtr(0), MonthlyKwh: floatPtr(0),
		CarbonFactorKgPerKwh: floatPtr(0.471),
	}}
	result, err := New(query, time.Now).GetDashboard(context.Background(), 2, 1)
	if err != nil || result.DailyKg == nil || *result.DailyKg != 0 || result.MonthlyKg == nil || *result.MonthlyKg != 0 {
		t.Fatalf("zero carbon=%v/%v err=%v", result.DailyKg, result.MonthlyKg, err)
	}
}

func TestServiceSortsDevicesDeterministically(t *testing.T) {
	query := &dashboardQueryStub{projection: persistence.DashboardProjection{
		Shop:    persistence.DashboardShopProjection{ID: 1},
		Devices: []persistence.DashboardDeviceProjection{{ID: 9}, {ID: 2}},
	}}
	result, err := New(query, time.Now).GetDashboard(context.Background(), 2, 1)
	if err != nil || len(result.Devices) != 2 || result.Devices[0].ID != "2" || result.Devices[1].ID != "9" {
		t.Fatalf("result=%+v err=%v", result.Devices, err)
	}
}

func TestServiceFailsClosedOnDuplicateDeviceProjection(t *testing.T) {
	query := &dashboardQueryStub{projection: persistence.DashboardProjection{
		Shop:    persistence.DashboardShopProjection{ID: 1},
		Devices: []persistence.DashboardDeviceProjection{{ID: 7}, {ID: 7}},
	}}
	if _, err := New(query, func() time.Time { return time.Unix(1, 0) }).GetDashboard(context.Background(), 2, 1); err != ErrAmbiguousProjection {
		t.Fatalf("error=%v", err)
	}
}

func floatPtr(value float64) *float64 { return &value }

func TestServiceMapsUnauthorizedDashboardToShopNotFound(t *testing.T) {
	query := &dashboardQueryStub{err: persistence.ErrDashboardNotFound}
	if _, err := New(query, time.Now).GetDashboard(context.Background(), 2, 1); err != ErrShopNotFound {
		t.Fatalf("error=%v", err)
	}
}
