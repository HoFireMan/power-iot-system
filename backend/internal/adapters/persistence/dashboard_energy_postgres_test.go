package persistence

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func insertDashboardEnergyReading(t *testing.T, db *gorm.DB, deviceID uint, pointID uuid.UUID, start, end time.Time, boot, sequence int64, energy float64, conflict bool, proven bool) {
	t.Helper()
	if proven {
		if err := db.Exec(`INSERT INTO telemetry_ingest_keys
			(id, device_id, boot_counter, sequence, canonical_coverage_digest, conflict_detected)
			VALUES (?, ?, ?, ?, ?, ?)`, uuid.New(), deviceID, boot, sequence, make([]byte, 32), conflict).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Exec(`INSERT INTO power_readings
		(time, recorded_at, received_at, measurement_point_id, device_id,
		 voltage, current, active_power, kwh_total, energy_delta_kwh,
		 protocol_version, boot_counter, sequence, coverage_version, interval_start, interval_end)
		VALUES (?, ?, ?, ?, ?, 110, 1, 110, 999, ?, 1, ?, ?, ?, ?, ?)`,
		start, start, start.Add(time.Minute), pointID, deviceID, energy, boot, sequence,
		coverageVersion(proven), startIf(proven, start), endIf(proven, end)).Error; err != nil {
		t.Fatal(err)
	}
}

func coverageVersion(proven bool) any {
	if !proven {
		return nil
	}
	return 1
}

func startIf(proven bool, value time.Time) any {
	if !proven {
		return nil
	}
	return value
}

func endIf(proven bool, value time.Time) any {
	if !proven {
		return nil
	}
	return value
}

func TestDashboardEnergyBestEffortAggregatesAuthoritativeIntervals(t *testing.T) {
	db := openPersistenceDB(t)
	fixture := newDashboardPowerFixture(t, db, 2)
	monthStart := time.Date(2026, 2, 28, 16, 0, 0, 0, time.UTC)
	todayStart := time.Date(2026, 3, 3, 16, 0, 0, 0, time.UTC)
	for _, deviceID := range fixture.deviceIDs {
		if err := db.Exec("UPDATE device_assignments SET valid_from = ? WHERE device_id = ?", monthStart, deviceID).Error; err != nil {
			t.Fatal(err)
		}
	}
	// The two valid intervals on either side of an absent interval both count.
	insertDashboardEnergyReading(t, db, fixture.deviceIDs[0], fixture.pointIDs[0], monthStart.Add(time.Hour), monthStart.Add(2*time.Hour), 1, 1, 1, false, true)
	insertDashboardEnergyReading(t, db, fixture.deviceIDs[0], fixture.pointIDs[0], monthStart.Add(3*time.Hour), monthStart.Add(4*time.Hour), 1, 2, 2, false, true)
	insertDashboardEnergyReading(t, db, fixture.deviceIDs[0], fixture.pointIDs[0], todayStart.Add(time.Hour), todayStart.Add(2*time.Hour), 1, 3, 3, false, true)
	insertDashboardEnergyReading(t, db, fixture.deviceIDs[0], fixture.pointIDs[0], todayStart.Add(3*time.Hour), todayStart.Add(4*time.Hour), 1, 4, 4, false, true)
	// A second MP contributes independently, including a real zero.
	insertDashboardEnergyReading(t, db, fixture.deviceIDs[1], fixture.pointIDs[1], todayStart.Add(2*time.Hour), todayStart.Add(3*time.Hour), 1, 1, 5, false, true)
	insertDashboardEnergyReading(t, db, fixture.deviceIDs[1], fixture.pointIDs[1], todayStart.Add(4*time.Hour), todayStart.Add(5*time.Hour), 1, 2, 0, false, true)
	// Conflicting, legacy-unproven, and unattributable rows are all skipped.
	insertDashboardEnergyReading(t, db, fixture.deviceIDs[0], fixture.pointIDs[0], todayStart.Add(5*time.Hour), todayStart.Add(6*time.Hour), 1, 5, 99, true, true)
	insertDashboardEnergyReading(t, db, fixture.deviceIDs[0], fixture.pointIDs[0], todayStart.Add(6*time.Hour), todayStart.Add(7*time.Hour), 1, 6, 88, false, false)
	insertDashboardEnergyReading(t, db, fixture.deviceIDs[1], fixture.pointIDs[0], todayStart.Add(7*time.Hour), todayStart.Add(8*time.Hour), 1, 7, 77, false, true)
	// Overlapping candidates are ambiguous and both are excluded rather than
	// choosing an arbitrary duplicate interval.
	insertDashboardEnergyReading(t, db, fixture.deviceIDs[0], fixture.pointIDs[0], todayStart.Add(8*time.Hour), todayStart.Add(10*time.Hour), 1, 8, 100, false, true)
	insertDashboardEnergyReading(t, db, fixture.deviceIDs[0], fixture.pointIDs[0], todayStart.Add(9*time.Hour), todayStart.Add(11*time.Hour), 1, 9, 200, false, true)
	// A direct malformed negative delta is not an authoritative valid interval.
	insertDashboardEnergyReading(t, db, fixture.deviceIDs[0], fixture.pointIDs[0], todayStart.Add(11*time.Hour), todayStart.Add(12*time.Hour), 1, 10, -5, false, true)

	projection, err := NewDashboardQueryRepository(db).FindDashboard(
		context.Background(), fixture.userID, fixture.shopID, func() time.Time { return fixture.now })
	if err != nil {
		t.Fatal(err)
	}
	if projection.DailyKwh == nil || *projection.DailyKwh != 12 {
		t.Fatalf("daily=%v, want 12 (valid before/after gap, second MP, and zero)", projection.DailyKwh)
	}
	if projection.MonthlyKwh == nil || *projection.MonthlyKwh != 15 {
		t.Fatalf("monthly=%v, want 15", projection.MonthlyKwh)
	}
}

func TestDashboardEnergyDeviceReplacementAndMidPeriodStart(t *testing.T) {
	db := openPersistenceDB(t)
	fixture := newDashboardPowerFixture(t, db, 1)
	monthStart := time.Date(2026, 2, 28, 16, 0, 0, 0, time.UTC)
	todayStart := time.Date(2026, 3, 3, 16, 0, 0, 0, time.UTC)
	boundary := todayStart.Add(2 * time.Hour)
	if err := db.Exec("UPDATE device_assignments SET valid_from = ?, valid_to = ? WHERE device_id = ?", monthStart, boundary, fixture.deviceIDs[0]).Error; err != nil {
		t.Fatal(err)
	}
	replacement := insertDashboardDevice(t, db, strings.ReplaceAll(uuid.NewString(), "-", "")[:12], fixture.shopID, "replacement", true)
	insertDashboardAssignment(t, db, replacement, fixture.pointIDs[0], boundary, nil)
	t.Cleanup(func() {
		db.Exec("DELETE FROM power_readings WHERE device_id = ?", replacement)
		db.Exec("DELETE FROM telemetry_ingest_keys WHERE device_id = ?", replacement)
		db.Exec("DELETE FROM device_assignments WHERE device_id = ?", replacement)
		db.Exec("DELETE FROM devices WHERE id = ?", replacement)
	})
	insertDashboardEnergyReading(t, db, fixture.deviceIDs[0], fixture.pointIDs[0], todayStart.Add(time.Hour), boundary, 1, 1, 2, false, true)
	insertDashboardEnergyReading(t, db, replacement, fixture.pointIDs[0], boundary, boundary.Add(time.Hour), 9, 1, 3, false, true)
	// This point begins producing data mid-period; no pre-start data is needed.
	insertDashboardEnergyReading(t, db, replacement, fixture.pointIDs[0], boundary.Add(time.Hour), boundary.Add(2*time.Hour), 9, 2, 4, false, true)

	projection, err := NewDashboardQueryRepository(db).FindDashboard(
		context.Background(), fixture.userID, fixture.shopID, func() time.Time { return fixture.now })
	if err != nil {
		t.Fatal(err)
	}
	if projection.DailyKwh == nil || *projection.DailyKwh != 9 || projection.MonthlyKwh == nil || *projection.MonthlyKwh != 9 {
		t.Fatalf("replacement/mid-period projection daily=%v month=%v", projection.DailyKwh, projection.MonthlyKwh)
	}
}

func TestDashboardEnergyNoValidIntervalsIsNull(t *testing.T) {
	db := openPersistenceDB(t)
	fixture := newDashboardPowerFixture(t, db, 1)
	projection, err := NewDashboardQueryRepository(db).FindDashboard(
		context.Background(), fixture.userID, fixture.shopID, func() time.Time { return fixture.now })
	if err != nil {
		t.Fatal(err)
	}
	if projection.DailyKwh != nil || projection.MonthlyKwh != nil {
		t.Fatalf("no-data projection daily=%v month=%v, want both nil", projection.DailyKwh, projection.MonthlyKwh)
	}
}
