package iot

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"power-iot-backend/internal/core/domain"
	"power-iot-backend/internal/data/migrations"
)

func openTelemetryIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set; telemetry integration test not run")
	}
	if err := migrations.Up(dsn); err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

type telemetryFixture struct {
	client domain.Client
	shop   domain.Shop
	point  domain.MeasurementPoint
	other  domain.MeasurementPoint
	first  domain.Device
	second domain.Device
}

func newTelemetryFixture(t *testing.T, db *gorm.DB) telemetryFixture {
	t.Helper()
	suffix := uuid.NewString()[:8]
	suffix = strings.ToUpper(suffix)
	fixture := telemetryFixture{
		client: domain.Client{Code: "ingest-client-" + suffix, Name: "Telemetry Ingest Client"},
		shop:   domain.Shop{Code: "ingest-shop-" + suffix, Name: "Telemetry Ingest Shop"},
		point:  domain.MeasurementPoint{ID: uuid.New(), Name: "MP-001"},
		other:  domain.MeasurementPoint{ID: uuid.New(), Name: "MP-002"},
		first:  domain.Device{MacAddress: "A1B2C3D4" + suffix[:4], Name: "sensor-a"},
		second: domain.Device{MacAddress: "B1C2D3E4" + suffix[:4], Name: "sensor-b"},
	}
	if err := db.Create(&fixture.client).Error; err != nil {
		t.Fatal(err)
	}
	fixture.shop.ClientID = fixture.client.ID
	if err := db.Create(&fixture.shop).Error; err != nil {
		t.Fatal(err)
	}
	fixture.point.ShopID = fixture.shop.ID
	fixture.other.ShopID = fixture.shop.ID
	fixture.first.ShopID = fixture.shop.ID
	fixture.second.ShopID = fixture.shop.ID
	if err := db.Create(&fixture.point).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&fixture.other).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&fixture.first).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&fixture.second).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec("DELETE FROM power_readings WHERE device_id IN (?, ?)", fixture.first.ID, fixture.second.ID)
		db.Exec("DELETE FROM alert_logs WHERE device_id IN (?, ?)", fixture.first.ID, fixture.second.ID)
		db.Exec("DELETE FROM telemetry_ingest_keys WHERE device_id IN (?, ?)", fixture.first.ID, fixture.second.ID)
		db.Exec("DELETE FROM device_assignments WHERE device_id IN (?, ?)", fixture.first.ID, fixture.second.ID)
		db.Unscoped().Delete(&domain.Device{}, fixture.first.ID)
		db.Unscoped().Delete(&domain.Device{}, fixture.second.ID)
		db.Delete(&domain.MeasurementPoint{}, fixture.point.ID)
		db.Delete(&domain.MeasurementPoint{}, fixture.other.ID)
		db.Delete(&domain.Shop{}, fixture.shop.ID)
		db.Delete(&domain.Client{}, fixture.client.ID)
	})
	return fixture
}

func addAssignment(t *testing.T, db *gorm.DB, deviceID uint, pointID uuid.UUID, from time.Time, to *time.Time) {
	t.Helper()
	assignment := domain.DeviceAssignment{ID: uuid.New(), DeviceID: deviceID, MeasurementPointID: pointID, ValidFrom: from, ValidTo: to}
	if err := db.Create(&assignment).Error; err != nil {
		t.Fatal(err)
	}
}

func testPayload(mac string, timestamp int64, boot, sequence int64) MqttPayload {
	return MqttPayload{
		MacAddress: mac, Voltage: 110, Current: 1.2, Power: 132, KwhTotal: 12.3,
		Timestamp: timestamp, ProtocolVersion: 1, BootID: "boot-1", BootCounter: boot,
		Sequence: sequence, PowerFactor: 0.98, EnergyDeltaKwh: 0.002, RSSI: -60,
		ValidSamples: 10, InvalidSamples: 1, FirmwareVersion: "test",
	}
}

func TestTelemetryIngestBlocksAtSharedFenceBeforeDeviceRowLock(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set; telemetry integration test not run")
	}
	db := openTelemetryIntegrationDB(t)
	fixture := newTelemetryFixture(t, db)
	recorded := time.Now().UTC().Add(-time.Minute)
	addAssignment(t, db, fixture.first.ID, fixture.point.ID, recorded.Add(-time.Hour), nil)
	fence, err := migrations.OpenExclusiveWriterFence(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	ingestor := NewTelemetryIngestor(db)
	beforeDevice := make(chan struct{})
	ingestor.beforeDeviceLock = func(*gorm.DB) error {
		close(beforeDevice)
		return nil
	}
	resultCh := make(chan struct {
		result IngestResult
		err    error
	}, 1)
	go func() {
		result, err := ingestor.IngestContext(context.Background(), testPayload(fixture.first.MacAddress, recorded.Unix(), 90, 1), recorded)
		resultCh <- struct {
			result IngestResult
			err    error
		}{result, err}
	}()
	select {
	case <-beforeDevice:
		t.Fatal("telemetry reached Device lock seam while exclusive fence was held")
	case <-time.After(150 * time.Millisecond):
	}
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case outcome := <-resultCh:
		if outcome.err != nil || outcome.result.Status != IngestStored {
			t.Fatalf("telemetry after release result=%+v err=%v", outcome.result, outcome.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("telemetry did not proceed after exclusive release")
	}
	select {
	case <-beforeDevice:
	default:
		t.Fatal("telemetry did not reach Device lock seam after admission")
	}
}

func TestTelemetryIngestStoresHistoricalAssignmentAndTimestamps(t *testing.T) {
	db := openTelemetryIntegrationDB(t)
	fixture := newTelemetryFixture(t, db)
	start := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	addAssignment(t, db, fixture.first.ID, fixture.point.ID, start, &end)

	receivedAt := start.Add(30 * time.Minute)
	result, err := NewTelemetryIngestor(db).Ingest(testPayload(fixture.first.MacAddress, start.Add(5*time.Minute).Unix(), 1, 1), receivedAt)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != IngestStored {
		t.Fatalf("want stored, got %s", result.Status)
	}

	var reading domain.PowerReading
	if err := db.Where("device_id = ?", fixture.first.ID).First(&reading).Error; err != nil {
		t.Fatal(err)
	}
	if reading.MeasurementPointID == nil || *reading.MeasurementPointID != fixture.point.ID {
		t.Fatalf("wrong measurement point: %v", reading.MeasurementPointID)
	}
	if !reading.RecordedAt.Equal(start.Add(5*time.Minute)) || !reading.ReceivedAt.Equal(receivedAt) {
		t.Fatalf("wrong timestamps: recorded=%v received=%v", reading.RecordedAt, reading.ReceivedAt)
	}
	if reading.ActivePower != 132 || reading.PowerFactor == nil || reading.EnergyDeltaKwh == nil || reading.RSSI == nil || reading.ValidSamples == nil || reading.InvalidSamples == nil {
		t.Fatalf("protocol fields were not persisted: %+v", reading)
	}
}

func TestTelemetryIngestUsesHalfOpenAssignmentsForReplacementAndRelocation(t *testing.T) {
	db := openTelemetryIntegrationDB(t)
	fixture := newTelemetryFixture(t, db)
	boundary := time.Date(2026, 8, 8, 11, 0, 0, 0, time.UTC)
	end := boundary
	addAssignment(t, db, fixture.first.ID, fixture.point.ID, boundary.Add(-time.Hour), &end)
	addAssignment(t, db, fixture.first.ID, fixture.other.ID, boundary, nil)
	addAssignment(t, db, fixture.second.ID, fixture.point.ID, boundary, nil)

	ingestor := NewTelemetryIngestor(db)
	cases := []struct {
		name     string
		device   domain.Device
		recorded time.Time
		pointID  uuid.UUID
		sequence int64
	}{
		{"relocation history", fixture.first, boundary.Add(-30 * time.Minute), fixture.point.ID, 1},
		{"relocation boundary", fixture.first, boundary, fixture.other.ID, 2},
		{"replacement same point", fixture.second, boundary.Add(time.Minute), fixture.point.ID, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := ingestor.Ingest(testPayload(tc.device.MacAddress, tc.recorded.Unix(), 1, tc.sequence), tc.recorded.Add(10*time.Minute))
			if err != nil || result.Status != IngestStored {
				t.Fatalf("ingest result=%+v err=%v", result, err)
			}
			var reading domain.PowerReading
			if err := db.Where("device_id = ? AND sequence = ?", tc.device.ID, tc.sequence).First(&reading).Error; err != nil {
				t.Fatal(err)
			}
			if reading.MeasurementPointID == nil || *reading.MeasurementPointID != tc.pointID {
				t.Fatalf("want point %s, got %v", tc.pointID, reading.MeasurementPointID)
			}
		})
	}
}

func TestTelemetryIngestDeduplicatesByDeviceKeyAndPreservesLastSeen(t *testing.T) {
	db := openTelemetryIntegrationDB(t)
	fixture := newTelemetryFixture(t, db)
	recorded := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	addAssignment(t, db, fixture.first.ID, fixture.point.ID, recorded.Add(-time.Hour), nil)
	addAssignment(t, db, fixture.second.ID, fixture.other.ID, recorded.Add(-time.Hour), nil)
	ingestor := NewTelemetryIngestor(db)

	firstReceived := recorded.Add(10 * time.Minute)
	payload := testPayload(fixture.first.MacAddress, recorded.Unix(), 1, 1)
	first, err := ingestor.Ingest(payload, firstReceived)
	if err != nil || first.Status != IngestStored {
		t.Fatalf("first result=%+v err=%v", first, err)
	}
	duplicate, err := ingestor.Ingest(payload, firstReceived.Add(-time.Minute))
	if err != nil || duplicate.Status != IngestDuplicate {
		t.Fatalf("duplicate result=%+v err=%v", duplicate, err)
	}
	other, err := ingestor.Ingest(testPayload(fixture.second.MacAddress, recorded.Unix(), 1, 1), firstReceived)
	if err != nil || other.Status != IngestStored {
		t.Fatalf("other device result=%+v err=%v", other, err)
	}
	var keyCount, readingCount int64
	db.Model(&domain.TelemetryIngestKey{}).Where("device_id = ?", fixture.first.ID).Count(&keyCount)
	db.Model(&domain.PowerReading{}).Where("device_id = ?", fixture.first.ID).Count(&readingCount)
	if keyCount != 1 || readingCount != 1 {
		t.Fatalf("duplicate created extra rows: keys=%d readings=%d", keyCount, readingCount)
	}
	var device domain.Device
	if err := db.First(&device, fixture.first.ID).Error; err != nil {
		t.Fatal(err)
	}
	if device.LastSeen == nil || !device.LastSeen.Equal(firstReceived) {
		t.Fatalf("last_seen regressed or was not set: %v", device.LastSeen)
	}
}

func TestTelemetryIngestConcurrentDuplicateHasOneWinner(t *testing.T) {
	db := openTelemetryIntegrationDB(t)
	fixture := newTelemetryFixture(t, db)
	recorded := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	addAssignment(t, db, fixture.first.ID, fixture.point.ID, recorded.Add(-time.Hour), nil)
	ingestor := NewTelemetryIngestor(db)
	payload := testPayload(fixture.first.MacAddress, recorded.Unix(), 2, 7)
	results := make(chan IngestResultStatus, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := ingestor.Ingest(payload, recorded.Add(10*time.Minute))
			if err != nil {
				t.Errorf("concurrent ingest failed: %v", err)
				return
			}
			results <- result.Status
		}()
	}
	wg.Wait()
	close(results)
	statuses := make([]IngestResultStatus, 0, 2)
	for status := range results {
		statuses = append(statuses, status)
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i] < statuses[j] })
	if len(statuses) != 2 || statuses[0] != IngestDuplicate || statuses[1] != IngestStored {
		t.Fatalf("want one stored and one duplicate, got %v", statuses)
	}
	var keys, readings int64
	db.Model(&domain.TelemetryIngestKey{}).Where("device_id = ?", fixture.first.ID).Count(&keys)
	db.Model(&domain.PowerReading{}).Where("device_id = ?", fixture.first.ID).Count(&readings)
	if keys != 1 || readings != 1 {
		t.Fatalf("concurrent ingest duplicated rows: keys=%d readings=%d", keys, readings)
	}
}

func TestTelemetryIngestInvalidTimestampUsesIngressFallback(t *testing.T) {
	db := openTelemetryIntegrationDB(t)
	fixture := newTelemetryFixture(t, db)
	receivedAt := time.Date(2026, 8, 8, 10, 15, 0, 0, time.UTC)
	addAssignment(t, db, fixture.first.ID, fixture.point.ID, receivedAt.Add(-time.Hour), nil)

	result, err := NewTelemetryIngestor(db).Ingest(testPayload(fixture.first.MacAddress, 1, 3, 1), receivedAt)
	if err != nil || result.Status != IngestStored {
		t.Fatalf("fallback result=%+v err=%v", result, err)
	}
	var reading domain.PowerReading
	if err := db.Where("device_id = ?", fixture.first.ID).First(&reading).Error; err != nil {
		t.Fatal(err)
	}
	if !reading.RecordedAt.Equal(receivedAt) || !reading.ReceivedAt.Equal(receivedAt) {
		t.Fatalf("fallback did not use ingress server time: recorded=%v received=%v", reading.RecordedAt, reading.ReceivedAt)
	}
}

func TestTelemetryIngestRejectsUnknownIdentityWithoutPersistence(t *testing.T) {
	db := openTelemetryIntegrationDB(t)
	fixture := newTelemetryFixture(t, db)
	recorded := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	ingestor := NewTelemetryIngestor(db)

	unknownDevice, err := ingestor.Ingest(testPayload("FFEEDDCCBBAA", recorded.Unix(), 1, 1), recorded.Add(time.Minute))
	if err != nil || unknownDevice.Status != IngestUnknownDevice {
		t.Fatalf("unknown device result=%+v err=%v", unknownDevice, err)
	}
	unknownAssignment, err := ingestor.Ingest(testPayload(fixture.first.MacAddress, recorded.Unix(), 1, 2), recorded.Add(time.Minute))
	if err != nil || unknownAssignment.Status != IngestUnknownAssignment {
		t.Fatalf("unknown assignment result=%+v err=%v", unknownAssignment, err)
	}
	var keys, readings int64
	db.Model(&domain.TelemetryIngestKey{}).Where("device_id = ?", fixture.first.ID).Count(&keys)
	db.Model(&domain.PowerReading{}).Where("device_id = ?", fixture.first.ID).Count(&readings)
	if keys != 0 || readings != 0 {
		t.Fatalf("unknown identities persisted data: keys=%d readings=%d", keys, readings)
	}
}

func TestTelemetryIngestRollbackRemovesKeyAndReading(t *testing.T) {
	db := openTelemetryIntegrationDB(t)
	fixture := newTelemetryFixture(t, db)
	recorded := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	addAssignment(t, db, fixture.first.ID, fixture.point.ID, recorded.Add(-time.Hour), nil)
	ingestor := NewTelemetryIngestor(db)
	ingestor.afterKeyClaim = func() error { return errors.New("forced telemetry transaction failure") }

	result, err := ingestor.Ingest(testPayload(fixture.first.MacAddress, recorded.Unix(), 1, 99), recorded.Add(time.Minute))
	if err == nil || result.Status != IngestFailed {
		t.Fatalf("want failed ingest, result=%+v err=%v", result, err)
	}
	var keys, readings int64
	db.Model(&domain.TelemetryIngestKey{}).Where("device_id = ?", fixture.first.ID).Count(&keys)
	db.Model(&domain.PowerReading{}).Where("device_id = ?", fixture.first.ID).Count(&readings)
	if keys != 0 || readings != 0 {
		t.Fatalf("rollback left partial data: keys=%d readings=%d", keys, readings)
	}
}
