package persistence

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	corebilling "power-iot-backend/internal/core/billing"
	corebillingenergy "power-iot-backend/internal/core/billingenergy"
	coreestimate "power-iot-backend/internal/core/billingestimate"
)

func insertEstimatePlanAssignment(t *testing.T, db *gorm.DB, shopID uint, planCode string, from, to string) {
	t.Helper()
	if err := db.Exec(`INSERT INTO shop_billing_assignments (shop_id, tariff_plan_id, valid_from, valid_to)
SELECT ?, id, ?::date, NULLIF(?::text, '')::date FROM electricity_tariff_plans WHERE plan_code = ?`, shopID, from, to, planCode).Error; err != nil {
		t.Fatal(err)
	}
}

func cleanupEstimateFixture(t *testing.T, db *gorm.DB, fixture persistenceFixture) {
	t.Helper()
	db.Exec("DELETE FROM shop_billing_assignments WHERE shop_id = ?", fixture.shopID)
	cleanupBillingFixture(t, db, fixture)
}

func findEstimateProjection(t *testing.T, repository *BillingEstimateQueryRepository, userID, shopID uint, month string, now time.Time) (BillingEstimateProjection, error) {
	t.Helper()
	parsed, err := corebillingenergy.ParseBillingMonth(month)
	if err != nil {
		return BillingEstimateProjection{}, err
	}
	return repository.FindBillingEstimate(context.Background(), userID, shopID, parsed, func() time.Time { return now })
}

func TestBillingEstimatePostgresCompletePartialZeroAndNoData(t *testing.T) {
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
	end := time.Date(2026, 9, 1, 0, 0, 0, 0, loc).UTC()
	insertBillingAssignment(t, db, fixture.deviceID, fixture.pointID, start, nil)
	insertBillingReading(t, db, fixture.pointID, fixture.deviceID, start, end, 1, 0, "500", false)
	repository := NewBillingEstimateQueryRepository(db)
	complete, err := findEstimateProjection(t, repository, fixture.actorID, fixture.shopID, "2026-08", time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if complete.Energy.UsageMicros == nil || *complete.Energy.UsageMicros != 500_000_000 || complete.Energy.ExpectedDuration <= 0 || complete.Energy.ObservedDuration != complete.Energy.ExpectedDuration || complete.Season != coreestimate.SeasonSummer || len(complete.Tiers) != 5 {
		t.Fatalf("complete=%+v", complete)
	}

	if err := db.Exec("DELETE FROM power_readings WHERE measurement_point_id = ?", fixture.pointID).Error; err != nil {
		t.Fatal(err)
	}
	noData, err := findEstimateProjection(t, repository, fixture.actorID, fixture.shopID, "2026-08", time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC))
	if err != nil || noData.Energy.UsageMicros != nil {
		t.Fatalf("no data=%+v err=%v", noData, err)
	}

	insertBillingReading(t, db, fixture.pointID, fixture.deviceID, start, start.Add(time.Hour), 1, 1, "0", false)
	zero, err := findEstimateProjection(t, repository, fixture.actorID, fixture.shopID, "2026-08", time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC))
	if err != nil || zero.Energy.UsageMicros == nil || *zero.Energy.UsageMicros != 0 || zero.Energy.ObservedDuration != time.Hour {
		t.Fatalf("zero=%+v err=%v", zero, err)
	}
}

func TestBillingEstimatePostgresConfigurationTariffRateAndHistoricalPlanOutcomes(t *testing.T) {
	db := openPersistenceDB(t)
	fixture := newPersistenceFixture(t, db)
	t.Cleanup(func() { cleanupEstimateFixture(t, db, fixture) })
	insertBillingRelation(t, db, fixture.actorID, fixture.shopID)
	repository := NewBillingEstimateQueryRepository(db)
	now := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	if err := db.Exec("UPDATE shops SET electricity_tariff = 'LIGHTING_COMMERCIAL' WHERE id = ?", fixture.shopID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := findEstimateProjection(t, repository, fixture.actorID, fixture.shopID, "2026-08", now); !errors.Is(err, coreestimate.ErrConfigurationRequired) {
		t.Fatalf("missing configuration err=%v", err)
	}
	insertEstimatePlanAssignment(t, db, fixture.shopID, corebilling.PlanCommercialNonTOU, "2026-08-15", "")
	if _, err := findEstimateProjection(t, repository, fixture.actorID, fixture.shopID, "2026-08", now); !errors.Is(err, coreestimate.ErrUnsupportedPeriod) {
		t.Fatalf("partial plan err=%v", err)
	}
	if err := db.Exec("DELETE FROM shop_billing_assignments WHERE shop_id = ?", fixture.shopID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("UPDATE shops SET electricity_tariff = 'LOW_VOLTAGE' WHERE id = ?", fixture.shopID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := findEstimateProjection(t, repository, fixture.actorID, fixture.shopID, "2026-08", now); !errors.Is(err, coreestimate.ErrUnsupportedTariff) {
		t.Fatalf("unsupported tariff err=%v", err)
	}
	if err := db.Exec("UPDATE shops SET electricity_tariff = 'LIGHTING_COMMERCIAL' WHERE id = ?", fixture.shopID).Error; err != nil {
		t.Fatal(err)
	}
	insertEstimatePlanAssignment(t, db, fixture.shopID, corebilling.PlanCommercialNonTOU, "2025-09-01", "2025-10-01")
	if _, err := findEstimateProjection(t, repository, fixture.actorID, fixture.shopID, "2025-09", now); !errors.Is(err, coreestimate.ErrRateNotFound) {
		t.Fatalf("before rate err=%v", err)
	}
}

func TestBillingEstimatePostgresRateBoundaryAndNoncommercialPlans(t *testing.T) {
	db := openPersistenceDB(t)
	fixture := newPersistenceFixture(t, db)
	t.Cleanup(func() { cleanupEstimateFixture(t, db, fixture) })
	insertBillingRelation(t, db, fixture.actorID, fixture.shopID)
	if err := db.Exec("UPDATE shops SET electricity_tariff = 'LIGHTING_NONCOMMERCIAL' WHERE id = ?", fixture.shopID).Error; err != nil {
		t.Fatal(err)
	}
	insertEstimatePlanAssignment(t, db, fixture.shopID, corebilling.PlanNoncommercialResidentialNonTOU, "2026-08-01", "2026-09-01")
	repository := NewBillingEstimateQueryRepository(db)
	now := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	residential, err := findEstimateProjection(t, repository, fixture.actorID, fixture.shopID, "2026-08", now)
	if err != nil || residential.PlanCode != corebilling.PlanNoncommercialResidentialNonTOU || residential.Season != coreestimate.SeasonSummer {
		t.Fatalf("residential=%+v err=%v", residential, err)
	}
	if err := db.Exec("DELETE FROM shop_billing_assignments WHERE shop_id = ?", fixture.shopID).Error; err != nil {
		t.Fatal(err)
	}
	insertEstimatePlanAssignment(t, db, fixture.shopID, corebilling.PlanNoncommercialNonresidentialNonTOU, "2026-08-01", "2026-09-01")
	nonresidential, err := findEstimateProjection(t, repository, fixture.actorID, fixture.shopID, "2026-08", now)
	if err != nil || nonresidential.PlanCode != corebilling.PlanNoncommercialNonresidentialNonTOU {
		t.Fatalf("nonresidential=%+v err=%v", nonresidential, err)
	}

	if err := db.Exec(`UPDATE electricity_rate_sets SET effective_to = DATE '2026-08-15' WHERE version_code = 'TAIPOWER_2025_10_01'`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO electricity_rate_sets
(provider, version_code, effective_from, source_organization, source_document, approval_reference, currency, includes_tax, status)
VALUES ('TAIPOWER', 'TAIPOWER_TEST_2026_08_15', DATE '2026-08-15', 'test', 'test', 'test', 'TWD', true, 'AUTHORITATIVE')`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := findEstimateProjection(t, repository, fixture.actorID, fixture.shopID, "2026-08", now); !errors.Is(err, coreestimate.ErrUnsupportedPeriod) {
		t.Fatalf("crossed rate err=%v", err)
	}
}

func TestBillingEstimatePostgresAuthorizationAndFutureMonth(t *testing.T) {
	db := openPersistenceDB(t)
	fixture := newPersistenceFixture(t, db)
	other := newPersistenceFixture(t, db)
	t.Cleanup(func() { cleanupEstimateFixture(t, db, fixture) })
	t.Cleanup(func() { cleanupEstimateFixture(t, db, other) })
	if err := db.Exec("UPDATE shops SET electricity_tariff = 'LIGHTING_COMMERCIAL' WHERE id IN (?, ?)", fixture.shopID, other.shopID).Error; err != nil {
		t.Fatal(err)
	}
	insertEstimatePlanAssignment(t, db, fixture.shopID, corebilling.PlanCommercialNonTOU, "2026-08-01", "2026-09-01")
	insertBillingRelation(t, db, fixture.actorID, fixture.shopID)
	insertBillingRelation(t, db, other.actorID, other.shopID)
	repository := NewBillingEstimateQueryRepository(db)
	now := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	if _, err := findEstimateProjection(t, repository, other.actorID, fixture.shopID, "2026-08", now); !errors.Is(err, coreestimate.ErrEstimateAccess) {
		t.Fatalf("cross-shop err=%v", err)
	}
	if _, err := findEstimateProjection(t, repository, fixture.actorID, fixture.shopID, "2026-09", now); !errors.Is(err, coreestimate.ErrUnsupportedPeriod) {
		t.Fatalf("future err=%v", err)
	}
	if err := db.Exec("UPDATE shops SET is_active = FALSE WHERE id = ?", fixture.shopID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := findEstimateProjection(t, repository, fixture.actorID, fixture.shopID, "2026-07", now); !errors.Is(err, coreestimate.ErrEstimateAccess) {
		t.Fatalf("inactive err=%v", err)
	}
}
