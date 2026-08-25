package persistence

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	appbillingenergy "power-iot-backend/internal/application/billingenergy"
	core "power-iot-backend/internal/core/billingenergy"
)

func insertBillingRelation(t *testing.T, db *gorm.DB, userID, shopID uint) {
	t.Helper()
	if err := db.Exec("INSERT INTO user_shop_relations (user_id, shop_id) VALUES (?, ?)", userID, shopID).Error; err != nil {
		t.Fatal(err)
	}
}

func insertBillingAssignment(t *testing.T, db *gorm.DB, deviceID uint, pointID uuid.UUID, from time.Time, to *time.Time) {
	t.Helper()
	if err := db.Exec(`INSERT INTO device_assignments (device_id, measurement_point_id, valid_from, valid_to) VALUES (?, ?, ?, ?)`, deviceID, pointID, from, to).Error; err != nil {
		t.Fatal(err)
	}
}

func insertBillingReading(t *testing.T, db *gorm.DB, pointID uuid.UUID, deviceID uint, start, end time.Time, boot, sequence int64, energy string, conflict bool) {
	t.Helper()
	if err := db.Exec(`
INSERT INTO power_readings
(time, recorded_at, received_at, measurement_point_id, device_id, energy_delta_kwh,
 protocol_version, coverage_version, interval_start, interval_end, boot_counter, sequence)
VALUES (?, ?, ?, ?, ?, ?::numeric, 1, 1, ?, ?, ?, ?)`, start, start, start.Add(24*time.Hour), pointID, deviceID, energy, start, end, boot, sequence).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`
INSERT INTO telemetry_ingest_keys
(device_id, boot_counter, sequence, canonical_coverage_digest, conflict_detected)
VALUES (?, ?, ?, ?, ?)`, deviceID, boot, sequence, make([]byte, 32), conflict).Error; err != nil {
		t.Fatal(err)
	}
}

func cleanupBillingFixture(t *testing.T, db *gorm.DB, fixture persistenceFixture) {
	t.Helper()
	db.Exec("DELETE FROM user_shop_relations WHERE user_id = ?", fixture.actorID)
	db.Exec("DELETE FROM power_readings WHERE device_id IN (?, ?)", fixture.deviceID, fixture.otherDevice)
	db.Exec("DELETE FROM telemetry_ingest_keys WHERE device_id IN (?, ?)", fixture.deviceID, fixture.otherDevice)
	db.Exec("DELETE FROM device_assignments WHERE device_id IN (?, ?)", fixture.deviceID, fixture.otherDevice)
	db.Exec("DELETE FROM measurement_points WHERE shop_id = ?", fixture.shopID)
	db.Exec("DELETE FROM devices WHERE id IN (?, ?)", fixture.deviceID, fixture.otherDevice)
	db.Exec("DELETE FROM users WHERE id = ?", fixture.actorID)
	db.Exec("DELETE FROM shops WHERE id = ?", fixture.shopID)
}

func TestBillingEnergyHistoricalExactEnergyCoverageAndZero(t *testing.T) {
	db := openPersistenceDB(t)
	fixture := newPersistenceFixture(t, db)
	t.Cleanup(func() { cleanupBillingFixture(t, db, fixture) })
	insertBillingRelation(t, db, fixture.actorID, fixture.shopID)
	loc := mustBusinessLocation()
	monthStart := time.Date(2026, 8, 1, 0, 0, 0, 0, loc).UTC()
	monthEnd := monthStart.AddDate(0, 1, 0)
	insertBillingAssignment(t, db, fixture.deviceID, fixture.pointID, monthStart.Add(-time.Hour), nil)
	insertBillingReading(t, db, fixture.pointID, fixture.deviceID, monthStart, monthStart.Add(time.Hour), 1, 0, "1.234567", false)
	insertBillingReading(t, db, fixture.pointID, fixture.deviceID, monthStart.Add(time.Hour), monthStart.Add(2*time.Hour), 1, 1, "0.000001", false)
	insertBillingReading(t, db, fixture.pointID, fixture.deviceID, monthStart.Add(2*time.Hour), monthStart.Add(3*time.Hour), 1, 2, "0", false)

	now := time.Date(2026, 9, 15, 1, 0, 0, 0, time.UTC)
	repository := NewBillingEnergyQueryRepository(db)
	service := appbillingenergy.New(repository, func() time.Time { return now })
	facts, err := service.Find(context.Background(), fixture.actorID, fixture.shopID, "2026-08")
	if err != nil {
		t.Fatal(err)
	}
	if facts.UsageMicros == nil || *facts.UsageMicros != 1_234_568 || facts.ExpectedDuration != monthEnd.Sub(monthStart) || facts.ObservedDuration != 3*time.Hour {
		t.Fatalf("facts=%+v", facts)
	}
	if facts.Coverage == nil || facts.Coverage.Cmp(new(big.Rat).SetFrac(big.NewInt(3*time.Hour.Nanoseconds()), big.NewInt(monthEnd.Sub(monthStart).Nanoseconds()))) != 0 {
		t.Fatalf("coverage=%v", facts.Coverage)
	}
	if len(facts.Points) != 2 {
		t.Fatalf("points=%d", len(facts.Points))
	}

	noDataFixture := newPersistenceFixture(t, db)
	t.Cleanup(func() { cleanupBillingFixture(t, db, noDataFixture) })
	insertBillingRelation(t, db, noDataFixture.actorID, noDataFixture.shopID)
	insertBillingAssignment(t, db, noDataFixture.deviceID, noDataFixture.pointID, monthStart, nil)
	noData, err := service.Find(context.Background(), noDataFixture.actorID, noDataFixture.shopID, "2026-08")
	if err != nil {
		t.Fatal(err)
	}
	if noData.UsageMicros != nil || noData.ObservedDuration != 0 || noData.Coverage == nil || noData.Coverage.Sign() != 0 {
		t.Fatalf("no data=%+v", noData)
	}
}

func TestBillingEnergyBestEffortGapReplacementAndBoundaries(t *testing.T) {
	db := openPersistenceDB(t)
	fixture := newPersistenceFixture(t, db)
	t.Cleanup(func() { cleanupBillingFixture(t, db, fixture) })
	insertBillingRelation(t, db, fixture.actorID, fixture.shopID)
	loc := mustBusinessLocation()
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, loc).UTC()
	end := start.AddDate(0, 1, 0)
	boundary := start.Add(2 * time.Hour)
	insertBillingAssignment(t, db, fixture.deviceID, fixture.pointID, start, &boundary)
	insertBillingAssignment(t, db, fixture.otherDevice, fixture.pointID, boundary, nil)
	insertBillingReading(t, db, fixture.pointID, fixture.deviceID, start, start.Add(time.Hour), 1, 0, "1", false)
	insertBillingReading(t, db, fixture.pointID, fixture.otherDevice, boundary, boundary.Add(time.Hour), 9, 0, "2", false)
	insertBillingReading(t, db, fixture.pointID, fixture.otherDevice, boundary.Add(2*time.Hour), boundary.Add(3*time.Hour), 9, 2, "4", false)

	// This interval ends at the historical period start and must not be used.
	insertBillingReading(t, db, fixture.pointID, fixture.deviceID, start.Add(-time.Hour), start, 1, 99, "100", false)
	// This interval straddles the replacement boundary and must not be split.
	insertBillingReading(t, db, fixture.pointID, fixture.deviceID, boundary.Add(-time.Minute), boundary.Add(time.Minute), 1, 98, "100", false)

	now := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	service := appbillingenergy.New(NewBillingEnergyQueryRepository(db), func() time.Time { return now })
	facts, err := service.Find(context.Background(), fixture.actorID, fixture.shopID, "2026-08")
	if err != nil {
		t.Fatal(err)
	}
	if facts.UsageMicros == nil || *facts.UsageMicros != 7_000_000 || facts.ObservedDuration != 3*time.Hour {
		t.Fatalf("replacement facts=%+v", facts)
	}
	if facts.ExpectedDuration != end.Sub(start) {
		t.Fatalf("replacement denominator=%s", facts.ExpectedDuration)
	}
	if !containsBillingWarning(facts.Warnings, core.WarningUnattributableEvidence) {
		t.Fatalf("boundary warning=%v", facts.Warnings)
	}
}

func insertBillingReadingRow(t *testing.T, db *gorm.DB, pointID uuid.UUID, deviceID uint, start, end time.Time, boot, sequence int64, energy string) {
	t.Helper()
	if err := db.Exec(`
INSERT INTO power_readings
(time, recorded_at, received_at, measurement_point_id, device_id, energy_delta_kwh,
 protocol_version, coverage_version, interval_start, interval_end, boot_counter, sequence)
VALUES (?, ?, ?, ?, ?, ?::numeric, 1, 1, ?, ?, ?, ?)`, start, start, start.Add(time.Hour), pointID, deviceID, energy, start, end, boot, sequence).Error; err != nil {
		t.Fatal(err)
	}
}

func TestBillingEnergyCurrentCutoffAndFullIntervalContainment(t *testing.T) {
	db := openPersistenceDB(t)
	fixture := newPersistenceFixture(t, db)
	t.Cleanup(func() { cleanupBillingFixture(t, db, fixture) })
	insertBillingRelation(t, db, fixture.actorID, fixture.shopID)
	loc := mustBusinessLocation()
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, loc).UTC()
	insertBillingAssignment(t, db, fixture.deviceID, fixture.pointID, start, nil)
	now := time.Date(2026, 8, 10, 8, 0, 0, 0, time.UTC)
	cutoff := now
	insertBillingReading(t, db, fixture.pointID, fixture.deviceID, cutoff.Add(-time.Hour), cutoff, 1, 0, "1", false)
	insertBillingReading(t, db, fixture.pointID, fixture.deviceID, cutoff.Add(-30*time.Minute), cutoff.Add(30*time.Minute), 1, 1, "10", false)
	insertBillingReading(t, db, fixture.pointID, fixture.deviceID, cutoff, cutoff.Add(time.Hour), 1, 2, "20", false)
	service := appbillingenergy.New(NewBillingEnergyQueryRepository(db), func() time.Time { return now })
	facts, err := service.Find(context.Background(), fixture.actorID, fixture.shopID, "2026-08")
	if err != nil {
		t.Fatal(err)
	}
	if facts.Cutoff != cutoff || facts.UsageMicros == nil || *facts.UsageMicros != 1_000_000 || facts.ObservedDuration != time.Hour {
		t.Fatalf("cutoff facts=%+v", facts)
	}
	if facts.ExpectedDuration != cutoff.Sub(start) {
		t.Fatalf("cutoff expected=%s want=%s", facts.ExpectedDuration, cutoff.Sub(start))
	}
}

func TestBillingEnergyDeduplicatesConflictsLegacyAndOverlaps(t *testing.T) {
	db := openPersistenceDB(t)
	fixture := newPersistenceFixture(t, db)
	t.Cleanup(func() { cleanupBillingFixture(t, db, fixture) })
	insertBillingRelation(t, db, fixture.actorID, fixture.shopID)
	loc := mustBusinessLocation()
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, loc).UTC()
	insertBillingAssignment(t, db, fixture.deviceID, fixture.pointID, start, nil)
	insertBillingReadingRow(t, db, fixture.pointID, fixture.deviceID, start, start.Add(time.Hour), 1, 0, "1")
	insertBillingReadingRow(t, db, fixture.pointID, fixture.deviceID, start, start.Add(time.Hour), 1, 0, "1")
	if err := db.Exec(`INSERT INTO telemetry_ingest_keys (device_id, boot_counter, sequence, canonical_coverage_digest) VALUES (?, ?, ?, ?)`, fixture.deviceID, 1, 0, make([]byte, 32)).Error; err != nil {
		t.Fatal(err)
	}
	insertBillingReadingRow(t, db, fixture.pointID, fixture.deviceID, start.Add(time.Hour), start.Add(2*time.Hour), 1, 1, "2")
	insertBillingReadingRow(t, db, fixture.pointID, fixture.deviceID, start.Add(time.Hour), start.Add(2*time.Hour), 1, 1, "3")
	if err := db.Exec(`INSERT INTO telemetry_ingest_keys (device_id, boot_counter, sequence, canonical_coverage_digest) VALUES (?, ?, ?, ?)`, fixture.deviceID, 1, 1, make([]byte, 32)).Error; err != nil {
		t.Fatal(err)
	}
	insertBillingReadingRow(t, db, fixture.pointID, fixture.deviceID, start.Add(2*time.Hour), start.Add(4*time.Hour), 1, 2, "4")
	insertBillingReadingRow(t, db, fixture.pointID, fixture.deviceID, start.Add(3*time.Hour), start.Add(5*time.Hour), 1, 3, "5")
	for _, sequence := range []int64{2, 3} {
		if err := db.Exec(`INSERT INTO telemetry_ingest_keys (device_id, boot_counter, sequence, canonical_coverage_digest) VALUES (?, ?, ?, ?)`, fixture.deviceID, 1, sequence, make([]byte, 32)).Error; err != nil {
			t.Fatal(err)
		}
	}
	insertReading(t, db, fixture.deviceID, fixture.pointID, start.Add(5*time.Hour))
	service := appbillingenergy.New(NewBillingEnergyQueryRepository(db), func() time.Time {
		return time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	})
	facts, err := service.Find(context.Background(), fixture.actorID, fixture.shopID, "2026-08")
	if err != nil {
		t.Fatal(err)
	}
	if facts.UsageMicros == nil || *facts.UsageMicros != 1_000_000 || facts.ObservedDuration != time.Hour {
		t.Fatalf("evidence facts=%+v", facts)
	}
	for _, warning := range []core.WarningCode{core.WarningConflictingTelemetry, core.WarningOverlappingEvidenceExcluded, core.WarningLegacyEvidence} {
		if !containsBillingWarning(facts.Warnings, warning) {
			t.Fatalf("warnings=%v missing %s", facts.Warnings, warning)
		}
	}
}

func TestBillingEnergyWeightedMultiplePointsAndNoExpectedWindow(t *testing.T) {
	db := openPersistenceDB(t)
	fixture := newPersistenceFixture(t, db)
	t.Cleanup(func() { cleanupBillingFixture(t, db, fixture) })
	insertBillingRelation(t, db, fixture.actorID, fixture.shopID)
	loc := mustBusinessLocation()
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, loc).UTC()
	pointBStart := start.AddDate(0, 0, 15)
	pointBEnd := start.AddDate(0, 0, 20)
	insertBillingAssignment(t, db, fixture.deviceID, fixture.pointID, start, nil)
	insertBillingAssignment(t, db, fixture.otherDevice, fixture.otherPointID, pointBStart, &pointBEnd)
	insertBillingReading(t, db, fixture.pointID, fixture.deviceID, start, start.Add(2*time.Hour), 1, 0, "0", false)
	insertBillingReading(t, db, fixture.otherPointID, fixture.otherDevice, pointBStart, pointBStart.Add(time.Hour), 1, 0, "2", false)
	service := appbillingenergy.New(NewBillingEnergyQueryRepository(db), func() time.Time {
		return time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	})
	facts, err := service.Find(context.Background(), fixture.actorID, fixture.shopID, "2026-08")
	if err != nil {
		t.Fatal(err)
	}
	wantExpected := start.AddDate(0, 1, 0).Sub(start) + pointBEnd.Sub(pointBStart)
	if facts.UsageMicros == nil || *facts.UsageMicros != 2_000_000 || facts.ExpectedDuration != wantExpected || facts.ObservedDuration != 3*time.Hour {
		t.Fatalf("multi point facts=%+v", facts)
	}
	if facts.Coverage == nil || facts.Coverage.Cmp(new(big.Rat).SetFrac(big.NewInt(3*time.Hour.Nanoseconds()), big.NewInt(wantExpected.Nanoseconds()))) != 0 {
		t.Fatalf("multi point coverage=%v", facts.Coverage)
	}
	if len(facts.Points) != 2 || facts.Points[0].UsageMicros == nil || facts.Points[1].UsageMicros == nil {
		t.Fatalf("point facts=%+v", facts.Points)
	}

	noWindow := newPersistenceFixture(t, db)
	t.Cleanup(func() { cleanupBillingFixture(t, db, noWindow) })
	insertBillingRelation(t, db, noWindow.actorID, noWindow.shopID)
	noWindowFacts, err := service.Find(context.Background(), noWindow.actorID, noWindow.shopID, "2026-08")
	if err != nil {
		t.Fatal(err)
	}
	if noWindowFacts.ExpectedDuration != 0 || noWindowFacts.Coverage != nil || !containsBillingWarning(noWindowFacts.Warnings, core.WarningNoExpectedMonitoringWindow) {
		t.Fatalf("no window=%+v", noWindowFacts)
	}
}

func TestBillingEnergyRepeatableReadExcludesRowsInsertedAfterSnapshot(t *testing.T) {
	db := openPersistenceDB(t)
	fixture := newPersistenceFixture(t, db)
	t.Cleanup(func() { cleanupBillingFixture(t, db, fixture) })
	insertBillingRelation(t, db, fixture.actorID, fixture.shopID)
	loc := mustBusinessLocation()
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, loc).UTC()
	insertBillingAssignment(t, db, fixture.deviceID, fixture.pointID, start, nil)
	inserted := false
	now := func() time.Time {
		if !inserted {
			inserted = true
			insertBillingReading(t, db, fixture.pointID, fixture.deviceID, start, start.Add(time.Hour), 1, 0, "9", false)
		}
		return time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	}
	service := appbillingenergy.New(NewBillingEnergyQueryRepository(db), now)
	facts, err := service.Find(context.Background(), fixture.actorID, fixture.shopID, "2026-08")
	if err != nil {
		t.Fatal(err)
	}
	if facts.UsageMicros != nil || facts.ObservedDuration != 0 {
		t.Fatalf("post-snapshot row became visible: %+v", facts)
	}
}

func TestBillingEnergyDeviceShopFieldDoesNotAuthorizeOrExcludeHistory(t *testing.T) {
	db := openPersistenceDB(t)
	fixture := newPersistenceFixture(t, db)
	other := newPersistenceFixture(t, db)
	t.Cleanup(func() { cleanupBillingFixture(t, db, fixture) })
	t.Cleanup(func() { cleanupBillingFixture(t, db, other) })
	insertBillingRelation(t, db, fixture.actorID, fixture.shopID)
	loc := mustBusinessLocation()
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, loc).UTC()
	if err := db.Exec("UPDATE devices SET shop_id = ? WHERE id = ?", other.shopID, fixture.deviceID).Error; err != nil {
		t.Fatal(err)
	}
	insertBillingAssignment(t, db, fixture.deviceID, fixture.pointID, start, nil)
	insertBillingReading(t, db, fixture.pointID, fixture.deviceID, start, start.Add(time.Hour), 1, 0, "3", false)
	service := appbillingenergy.New(NewBillingEnergyQueryRepository(db), func() time.Time {
		return time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	})
	facts, err := service.Find(context.Background(), fixture.actorID, fixture.shopID, "2026-08")
	if err != nil || facts.UsageMicros == nil || *facts.UsageMicros != 3_000_000 {
		t.Fatalf("device shop authority err=%v facts=%+v", err, facts)
	}
}

func containsBillingWarning(values []core.WarningCode, want core.WarningCode) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestBillingEnergyAuthorizationIsolationAndFutureMonth(t *testing.T) {
	db := openPersistenceDB(t)
	fixture := newPersistenceFixture(t, db)
	t.Cleanup(func() { cleanupBillingFixture(t, db, fixture) })
	other := newPersistenceFixture(t, db)
	t.Cleanup(func() { cleanupBillingFixture(t, db, other) })
	insertBillingRelation(t, db, fixture.actorID, fixture.shopID)
	insertBillingRelation(t, db, other.actorID, other.shopID)
	service := appbillingenergy.New(NewBillingEnergyQueryRepository(db), func() time.Time {
		return time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	})
	if _, err := service.Find(context.Background(), other.actorID, fixture.shopID, "2026-07"); !errors.Is(err, ErrBillingEnergyAccess) {
		t.Fatalf("cross-shop err=%v", err)
	}
	if _, err := service.Find(context.Background(), fixture.actorID, fixture.shopID, "2026-09"); !errors.Is(err, core.ErrFutureBillingMonth) {
		t.Fatalf("future err=%v", err)
	}
	if _, err := service.Find(context.Background(), other.actorID, fixture.shopID, "2026-07"); err == nil {
		t.Fatal("unrelated user was authorized")
	}
	if err := db.Exec("UPDATE shops SET is_active = FALSE WHERE id = ?", fixture.shopID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.Find(context.Background(), fixture.actorID, fixture.shopID, "2026-07"); !errors.Is(err, ErrBillingEnergyAccess) {
		t.Fatalf("inactive shop err=%v", err)
	}
}
