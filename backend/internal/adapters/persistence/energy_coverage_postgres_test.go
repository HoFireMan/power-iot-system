package persistence

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"power-iot-backend/internal/core/coverage"
	"power-iot-backend/internal/core/domain"
)

func insertCoverageReading(t *testing.T, db *gorm.DB, point uuid.UUID, device uint, start, end time.Time, boot, sequence int64, energy float64) {
	t.Helper()
	version := int64(1)
	measurementPoint := point
	if err := db.Create(&domain.PowerReading{
		Time: start, RecordedAt: start, ReceivedAt: start.Add(time.Minute),
		MeasurementPointID: &measurementPoint, DeviceID: device,
		Voltage: 110, Current: 1, Power: 110, ActivePower: 110,
		EnergyDeltaKwh: &energy, ProtocolVersion: 1,
		BootCounter: &boot, Sequence: &sequence,
		CoverageVersion: &version, IntervalStart: &start, IntervalEnd: &end,
	}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestEnergyCoverageRequiresDigestProvenanceAndHonorsConflict(t *testing.T) {
	db := openPersistenceDB(t)
	fixture := newPersistenceFixture(t, db)
	start := time.Date(2026, 8, 23, 16, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	insertAssignment(t, db, fixture.deviceID, fixture.pointID, start.Add(-time.Hour), nil)
	insertCoverageReading(t, db, fixture.pointID, fixture.deviceID, start, end, 1, 1, 1)
	now := func() time.Time { return time.Date(2026, 8, 24, 12, 0, 0, 0, time.FixedZone("Asia/Taipei", 8*60*60)) }
	repo := NewEnergyCoverageQueryRepository(db)
	projection, err := repo.FindMeasurementPointEnergy(t.Context(), fixture.pointID, now)
	if err != nil {
		t.Fatal(err)
	}
	if projection.Today.State != coverage.Unknown || projection.Today.Kwh != nil {
		t.Fatalf("missing key became proven: %+v", projection.Today)
	}
	if err := db.Create(&domain.TelemetryIngestKey{ID: uuid.New(), DeviceID: fixture.deviceID, BootCounter: 1, Sequence: 1}).Error; err != nil {
		t.Fatal(err)
	}
	projection, err = repo.FindMeasurementPointEnergy(t.Context(), fixture.pointID, now)
	if err != nil {
		t.Fatal(err)
	}
	if projection.Today.State != coverage.Unknown || projection.Today.Kwh != nil {
		t.Fatalf("NULL digest became proven: %+v", projection.Today)
	}
	if err := db.Model(&domain.TelemetryIngestKey{}).Where("device_id=? AND boot_counter=1 AND sequence=1", fixture.deviceID).Updates(map[string]interface{}{"canonical_coverage_digest": make([]byte, 32)}).Error; err != nil {
		t.Fatal(err)
	}
	projection, err = repo.FindMeasurementPointEnergy(t.Context(), fixture.pointID, now)
	if err != nil {
		t.Fatal(err)
	}
	if projection.Today.State != coverage.Proven || projection.Today.Kwh == nil || *projection.Today.Kwh != 1 {
		t.Fatalf("valid provenance not proven: %+v", projection.Today)
	}
	if err := db.Model(&domain.TelemetryIngestKey{}).Where("device_id=? AND boot_counter=1 AND sequence=1", fixture.deviceID).Update("conflict_detected", true).Error; err != nil {
		t.Fatal(err)
	}
	projection, err = repo.FindMeasurementPointEnergy(t.Context(), fixture.pointID, now)
	if err != nil {
		t.Fatal(err)
	}
	if projection.Today.State != coverage.Ambiguous || projection.Today.Kwh != nil {
		t.Fatalf("conflict not ambiguous: %+v", projection.Today)
	}
}

func TestEnergyCoverageGapReplayReplacementWatermarksAndZero(t *testing.T) {
	db := openPersistenceDB(t)
	fixture := newPersistenceFixture(t, db)
	start := time.Date(2026, 8, 23, 16, 0, 0, 0, time.UTC)
	hour := time.Hour
	insertAssignment(t, db, fixture.deviceID, fixture.pointID, start.Add(-hour), nil)
	insertCoverageReading(t, db, fixture.pointID, fixture.deviceID, start, start.Add(hour), 1, 0, 1)
	insertCoverageReading(t, db, fixture.pointID, fixture.deviceID, start.Add(2*hour), start.Add(3*hour), 1, 2, 3)
	for _, sequence := range []int64{0, 2} {
		if err := db.Create(&domain.TelemetryIngestKey{ID: uuid.New(), DeviceID: fixture.deviceID, BootCounter: 1, Sequence: sequence, CanonicalCoverageDigest: make([]byte, 32)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	now := func() time.Time { return time.Date(2026, 8, 24, 12, 0, 0, 0, time.FixedZone("Asia/Taipei", 8*60*60)) }
	repo := NewEnergyCoverageQueryRepository(db)
	projection, err := repo.FindMeasurementPointEnergy(t.Context(), fixture.pointID, now)
	if err != nil {
		t.Fatal(err)
	}
	if projection.Today.State != coverage.Gap || projection.Today.Kwh == nil || *projection.Today.Kwh != 1 {
		t.Fatalf("gap=%+v", projection.Today)
	}
	if projection.Month.State != coverage.Gap || projection.Month.Kwh != nil {
		t.Fatalf("month watermark=%+v", projection.Month)
	}
	insertCoverageReading(t, db, fixture.pointID, fixture.deviceID, start.Add(hour), start.Add(2*hour), 1, 1, 2)
	if err := db.Create(&domain.TelemetryIngestKey{ID: uuid.New(), DeviceID: fixture.deviceID, BootCounter: 1, Sequence: 1, CanonicalCoverageDigest: make([]byte, 32)}).Error; err != nil {
		t.Fatal(err)
	}
	projection, err = repo.FindMeasurementPointEnergy(t.Context(), fixture.pointID, now)
	if err != nil {
		t.Fatal(err)
	}
	if projection.Today.State != coverage.Proven || projection.Today.Kwh == nil || *projection.Today.Kwh != 6 {
		t.Fatalf("replay=%+v", projection.Today)
	}

	replacementPoint := fixture.otherPointID
	boundary := start.Add(hour)
	var replacementDevice domain.Device
	replacementDevice.ShopID = fixture.shopID
	replacementDevice.MacAddress = "CC" + strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", ""))[:10]
	replacementDevice.Name = "replacement"
	if err := db.Create(&replacementDevice).Error; err != nil {
		t.Fatal(err)
	}
	insertAssignment(t, db, fixture.otherDevice, replacementPoint, start.Add(-hour), &boundary)
	insertAssignment(t, db, replacementDevice.ID, replacementPoint, boundary, nil)
	insertCoverageReading(t, db, replacementPoint, fixture.otherDevice, start, boundary, 4, 8, 1)
	insertCoverageReading(t, db, replacementPoint, replacementDevice.ID, boundary, boundary.Add(hour), 9, 87, 2)
	insertCoverageReading(t, db, replacementPoint, replacementDevice.ID, boundary.Add(hour), boundary.Add(2*hour), 9, 88, 3)
	for _, sequence := range []int64{8, 87, 88} {
		device := fixture.otherDevice
		boot := int64(4)
		if sequence != 8 {
			device, boot = replacementDevice.ID, 9
		}
		if err := db.Create(&domain.TelemetryIngestKey{ID: uuid.New(), DeviceID: device, BootCounter: boot, Sequence: sequence, CanonicalCoverageDigest: make([]byte, 32)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	projection, err = repo.FindMeasurementPointEnergy(t.Context(), replacementPoint, now)
	if err != nil {
		t.Fatal(err)
	}
	if projection.Today.State != coverage.Proven || projection.Today.Kwh == nil || *projection.Today.Kwh != 6 {
		t.Fatalf("replacement=%+v", projection.Today)
	}
}
