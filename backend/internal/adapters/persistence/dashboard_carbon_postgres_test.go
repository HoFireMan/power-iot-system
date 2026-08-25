package persistence

import (
	"context"
	"testing"
	"time"
)

func TestDashboardCarbonReadTimeFactorAndNullSemantics(t *testing.T) {
	db := openPersistenceDB(t)
	tests := []struct {
		name   string
		tariff *string
		factor *float64
	}{
		{name: "tariff unset", tariff: nil, factor: nil},
		{name: "lighting commercial", tariff: stringPtr("LIGHTING_COMMERCIAL"), factor: floatPtr(0.466)},
		{name: "low voltage", tariff: stringPtr("LOW_VOLTAGE"), factor: floatPtr(0.466)},
		{name: "high voltage", tariff: stringPtr("HIGH_VOLTAGE"), factor: floatPtr(0.466)},
		{name: "extra high voltage", tariff: stringPtr("EXTRA_HIGH_VOLTAGE"), factor: floatPtr(0.466)},
		{name: "lighting noncommercial", tariff: stringPtr("LIGHTING_NONCOMMERCIAL"), factor: floatPtr(0.471)},
		{name: "package lighting", tariff: stringPtr("PACKAGE_LIGHTING"), factor: floatPtr(0.471)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newDashboardPowerFixture(t, db, 1)
			if err := db.Exec("UPDATE shops SET electricity_tariff = ? WHERE id = ?", tc.tariff, fixture.shopID).Error; err != nil {
				t.Fatal(err)
			}
			monthStart := time.Date(2026, 2, 28, 16, 0, 0, 0, time.UTC)
			todayStart := time.Date(2026, 3, 3, 16, 0, 0, 0, time.UTC)
			if err := db.Exec("UPDATE device_assignments SET valid_from = ? WHERE device_id = ?", monthStart, fixture.deviceIDs[0]).Error; err != nil {
				t.Fatal(err)
			}
			insertDashboardEnergyReading(t, db, fixture.deviceIDs[0], fixture.pointIDs[0], monthStart.Add(time.Hour), monthStart.Add(2*time.Hour), 1, 1, 1.3, false, true)
			insertDashboardEnergyReading(t, db, fixture.deviceIDs[0], fixture.pointIDs[0], todayStart.Add(time.Hour), todayStart.Add(2*time.Hour), 1, 2, 9.7, false, true)
			projection, err := NewDashboardQueryRepository(db).FindDashboard(context.Background(), fixture.userID, fixture.shopID, func() time.Time { return fixture.now })
			if err != nil {
				t.Fatal(err)
			}
			if projection.DailyKwh == nil || projection.MonthlyKwh == nil || *projection.DailyKwh != 9.7 || *projection.MonthlyKwh != 11 {
				t.Fatalf("energy daily/monthly=%v/%v", projection.DailyKwh, projection.MonthlyKwh)
			}
			if tc.factor == nil {
				if projection.CarbonFactorKgPerKwh != nil {
					t.Fatalf("unset tariff factor=%v, want nil", projection.CarbonFactorKgPerKwh)
				}
				return
			}
			if projection.CarbonFactorKgPerKwh == nil || *projection.CarbonFactorKgPerKwh != *tc.factor {
				t.Fatalf("factor=%v, want %v", projection.CarbonFactorKgPerKwh, *tc.factor)
			}
			if projection.DailyKwh == nil || projection.MonthlyKwh == nil {
				t.Fatalf("carbon inputs unexpectedly null")
			}
		})
	}
}

func TestDashboardCarbonZeroMissingFactorAndLegacyConfig(t *testing.T) {
	db := openPersistenceDB(t)
	fixture := newDashboardPowerFixture(t, db, 1)
	if err := db.Exec("UPDATE shops SET electricity_tariff = 'LOW_VOLTAGE' WHERE id = ?", fixture.shopID).Error; err != nil {
		t.Fatal(err)
	}
	monthStart := time.Date(2026, 2, 28, 16, 0, 0, 0, time.UTC)
	todayStart := time.Date(2026, 3, 3, 16, 0, 0, 0, time.UTC)
	if err := db.Exec("UPDATE device_assignments SET valid_from = ? WHERE device_id = ?", monthStart, fixture.deviceIDs[0]).Error; err != nil {
		t.Fatal(err)
	}
	insertDashboardEnergyReading(t, db, fixture.deviceIDs[0], fixture.pointIDs[0], todayStart.Add(time.Hour), todayStart.Add(2*time.Hour), 2, 1, 0, false, true)
	if err := db.Exec("UPDATE system_configs SET value = '0.474' WHERE key = 'carbon_factor'").Error; err != nil {
		t.Fatal(err)
	}
	projection, err := NewDashboardQueryRepository(db).FindDashboard(context.Background(), fixture.userID, fixture.shopID, func() time.Time { return fixture.now })
	if err != nil {
		t.Fatal(err)
	}
	if projection.DailyKwh == nil || *projection.DailyKwh != 0 || projection.CarbonFactorKgPerKwh == nil || *projection.CarbonFactorKgPerKwh != 0.466 {
		t.Fatalf("zero energy/factor=%v/%v", projection.DailyKwh, projection.CarbonFactorKgPerKwh)
	}
	if err := db.Exec(`DELETE FROM carbon_factor_rates WHERE set_id=(SELECT id FROM carbon_factor_sets WHERE is_active) AND tariff_code='LOW_VOLTAGE'`).Error; err != nil {
		t.Fatal(err)
	}
	projection, err = NewDashboardQueryRepository(db).FindDashboard(context.Background(), fixture.userID, fixture.shopID, func() time.Time { return fixture.now })
	if err != nil {
		t.Fatal(err)
	}
	if projection.CarbonFactorKgPerKwh != nil {
		t.Fatalf("missing factor=%v, want nil", projection.CarbonFactorKgPerKwh)
	}
	if err := db.Exec(`INSERT INTO carbon_factor_rates (set_id, tariff_code, factor_kgco2e_per_kwh)
		SELECT id, 'LOW_VOLTAGE', 0.466 FROM carbon_factor_sets WHERE is_active`).Error; err != nil {
		t.Fatal(err)
	}
}

func stringPtr(value string) *string { return &value }
