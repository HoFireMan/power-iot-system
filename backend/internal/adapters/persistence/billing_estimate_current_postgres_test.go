package persistence

import (
	"testing"
	"time"

	corebilling "power-iot-backend/internal/core/billing"
	corebillingenergy "power-iot-backend/internal/core/billingenergy"
)

func TestBillingEstimatePostgresCurrentMonthUsesRequestCutoff(t *testing.T) {
	db := openPersistenceDB(t)
	fixture := newPersistenceFixture(t, db)
	t.Cleanup(func() { cleanupEstimateFixture(t, db, fixture) })
	if err := db.Exec("UPDATE shops SET electricity_tariff = 'LIGHTING_COMMERCIAL' WHERE id = ?", fixture.shopID).Error; err != nil {
		t.Fatal(err)
	}
	insertBillingRelation(t, db, fixture.actorID, fixture.shopID)
	insertEstimatePlanAssignment(t, db, fixture.shopID, corebilling.PlanCommercialNonTOU, "2026-08-01", "2026-09-01")
	loc := mustBusinessLocation()
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, loc).UTC()
	now := time.Date(2026, 8, 10, 8, 0, 0, 0, time.UTC)
	insertBillingAssignment(t, db, fixture.deviceID, fixture.pointID, start, nil)
	insertBillingReading(t, db, fixture.pointID, fixture.deviceID, now.Add(-time.Hour), now, 1, 0, "4", false)
	month, err := corebillingenergy.ParseBillingMonth("2026-08")
	if err != nil {
		t.Fatal(err)
	}
	projection, err := NewBillingEstimateQueryRepository(db).FindBillingEstimate(nil, fixture.actorID, fixture.shopID, month, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if !projection.Energy.Cutoff.Equal(now) || projection.Energy.UsageMicros == nil || *projection.Energy.UsageMicros != 4_000_000 || projection.Energy.ObservedDuration != time.Hour {
		t.Fatalf("current=%+v", projection.Energy)
	}
}
