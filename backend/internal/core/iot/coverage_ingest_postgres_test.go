package iot

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"power-iot-backend/internal/core/coverage"
	"power-iot-backend/internal/core/domain"
)

func coverageTestPayload(mac string, start, end time.Time, boot, sequence int64, energy float64, timestamp time.Time) MqttPayload {
	payload := testPayload(mac, timestamp.Unix(), boot, sequence)
	payload.EnergyDeltaKwh = &energy
	payload.CoverageVersion = coverage.OptionalInt64{Present: true, Value: 1}
	payload.IntervalStartTS = coverage.OptionalInt64{Present: true, Value: start.UnixMilli()}
	payload.IntervalEndTS = coverage.OptionalInt64{Present: true, Value: end.UnixMilli()}
	return payload
}

func coverageTestIngestor(db *gorm.DB) *TelemetryIngestor {
	ingestor := NewTelemetryIngestor(db)
	ingestor.coverageMaxInterval = func(*gorm.DB) (int64, error) { return int64(24 * time.Hour / time.Millisecond), nil }
	return ingestor
}

func TestCoverageIngestClaimDuplicateConflictAndNullDigest(t *testing.T) {
	db := openTelemetryIntegrationDB(t)
	fixture := newTelemetryFixture(t, db)
	start := time.Date(2026, 8, 24, 1, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	addAssignment(t, db, fixture.first.ID, fixture.point.ID, start.Add(-time.Hour), nil)
	ingestor := coverageTestIngestor(db)
	payload := coverageTestPayload(fixture.first.MacAddress, start, end, 1, 1, 0.001, start)
	result, err := ingestor.Ingest(payload, start.Add(time.Minute))
	if err != nil || result.Status != IngestStored {
		t.Fatalf("first claim=%+v err=%v", result, err)
	}
	result, err = ingestor.Ingest(payload, start.Add(2*time.Minute))
	if err != nil || result.Status != IngestDuplicate {
		t.Fatalf("exact duplicate=%+v err=%v", result, err)
	}
	conflicting := coverageTestPayload(fixture.first.MacAddress, start, end, 1, 1, 0.002, start)
	result, err = ingestor.Ingest(conflicting, start.Add(3*time.Minute))
	if err != nil || result.Status != IngestConflict {
		t.Fatalf("conflict=%+v err=%v", result, err)
	}
	var key domain.TelemetryIngestKey
	if err := db.Where("device_id=? AND boot_counter=1 AND sequence=1", fixture.first.ID).First(&key).Error; err != nil {
		t.Fatal(err)
	}
	if !key.ConflictDetected {
		t.Fatal("conflict flag was not sticky")
	}
	var readings int64
	db.Model(&domain.PowerReading{}).Where("device_id=? AND boot_counter=1 AND sequence=1", fixture.first.ID).Count(&readings)
	if readings != 1 {
		t.Fatalf("reading count=%d", readings)
	}

	if err := db.Create(&domain.TelemetryIngestKey{ID: uuid.New(), DeviceID: fixture.first.ID, BootCounter: 1, Sequence: 2, ReceivedAt: start}).Error; err != nil {
		t.Fatal(err)
	}
	nullCollision := coverageTestPayload(fixture.first.MacAddress, start, end, 1, 2, 0.001, start)
	result, err = ingestor.Ingest(nullCollision, start.Add(4*time.Minute))
	if err != nil || result.Status != IngestConflict {
		t.Fatalf("NULL digest collision=%+v err=%v", result, err)
	}
}

func TestCoverageIngestFullIntervalBoundariesAndAlertEventTime(t *testing.T) {
	db := openTelemetryIntegrationDB(t)
	fixture := newTelemetryFixture(t, db)
	start := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Hour)
	addAssignment(t, db, fixture.first.ID, fixture.point.ID, start, &end)
	addMPAlertSetting(t, db, fixture.point.ID, "17:00", "19:00", 10)
	ingestor := coverageTestIngestor(db)
	payload := coverageTestPayload(fixture.first.MacAddress, start, end, 1, 10, 0.001, start.Add(90*time.Minute))
	result, err := ingestor.Ingest(payload, start.Add(2*time.Hour))
	if err != nil || result.Status != IngestStored {
		t.Fatalf("full boundary=%+v err=%v", result, err)
	}
	var alerts domain.AlertLog
	if err := db.Where("device_id=? AND created_at=?", fixture.first.ID, start).First(&alerts).Error; err != nil {
		t.Fatal(err)
	}
	if alerts.MeasurementPointID == nil || *alerts.MeasurementPointID != fixture.point.ID || alerts.LegacyUnresolved {
		t.Fatalf("alert identity=%v unresolved=%t", alerts.MeasurementPointID, alerts.LegacyUnresolved)
	}

	straddle := coverageTestPayload(fixture.first.MacAddress, end.Add(-30*time.Minute), end.Add(30*time.Minute), 1, 11, 0.001, end)
	result, err = ingestor.Ingest(straddle, end.Add(time.Hour))
	if err != nil || result.Status != IngestUnknownAssignment {
		t.Fatalf("straddle=%+v err=%v", result, err)
	}
}

func TestCoverageIngestConcurrentExactAndConflictClaims(t *testing.T) {
	db := openTelemetryIntegrationDB(t)
	fixture := newTelemetryFixture(t, db)
	start := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	addAssignment(t, db, fixture.first.ID, fixture.point.ID, start.Add(-time.Hour), nil)
	ingestor := coverageTestIngestor(db)
	runRace := func(payloads []MqttPayload) []IngestResult {
		results := make([]IngestResult, len(payloads))
		errs := make([]error, len(payloads))
		var wg sync.WaitGroup
		for i, payload := range payloads {
			wg.Add(1)
			go func(i int, payload MqttPayload) {
				defer wg.Done()
				results[i], errs[i] = ingestor.Ingest(payload, start.Add(time.Duration(i+1)*time.Minute))
			}(i, payload)
		}
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		return results
	}
	exact := coverageTestPayload(fixture.first.MacAddress, start, end, 1, 20, 0.001, start)
	results := runRace([]MqttPayload{exact, exact})
	stored, duplicate := 0, 0
	for _, result := range results {
		if result.Status == IngestStored {
			stored++
		}
		if result.Status == IngestDuplicate {
			duplicate++
		}
	}
	if stored != 1 || duplicate != 1 {
		t.Fatalf("exact race=%+v", results)
	}
	results = runRace([]MqttPayload{
		coverageTestPayload(fixture.first.MacAddress, start, end, 1, 21, 0.002, start),
		coverageTestPayload(fixture.first.MacAddress, start, end, 1, 21, 0.003, start),
	})
	conflicts := 0
	for _, result := range results {
		if result.Status == IngestConflict {
			conflicts++
		}
	}
	if conflicts != 1 {
		t.Fatalf("conflict race=%+v", results)
	}
}
