package iot

import (
	"testing"
	"time"

	"power-iot-backend/internal/core/domain"
)

func TestLegacyTelemetryAlertFailsClosedWithoutAssignment(t *testing.T) {
	db := openTelemetryIntegrationDB(t)
	fixture := newTelemetryFixture(t, db)
	if err := db.Create(&domain.DeviceAlertSetting{DeviceID: fixture.first.ID, NonUsageStartTime: "10:00", NonUsageEndTime: "11:00", IsEnabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	recorded := time.Date(2026, 8, 8, 10, 30, 0, 0, time.UTC)
	payload := MqttPayload{MacAddress: fixture.first.MacAddress, ProtocolVersion: 0, Timestamp: recorded.Unix(), Voltage: 110, Current: 1, Power: 110, KwhTotal: 1}
	if err := (&MqttService{db: db}).storeLegacyTelemetry(payload, recorded); err == nil {
		t.Fatal("legacy telemetry without an assignment unexpectedly created an alert")
	}
	var readings, alerts int64
	if err := db.Model(&domain.PowerReading{}).Where("device_id = ?", fixture.first.ID).Count(&readings).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&domain.AlertLog{}).Where("device_id = ?", fixture.first.ID).Count(&alerts).Error; err != nil {
		t.Fatal(err)
	}
	if readings != 0 || alerts != 0 {
		t.Fatalf("failed-closed legacy alert changed data: readings=%d alerts=%d", readings, alerts)
	}
}

func TestLegacyTelemetryLastSeenDoesNotRegress(t *testing.T) {
	db := openTelemetryIntegrationDB(t)
	fixture := newTelemetryFixture(t, db)
	service := &MqttService{db: db}
	newer := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	older := newer.Add(-time.Minute)
	payload := MqttPayload{MacAddress: fixture.first.MacAddress, ProtocolVersion: 0, Timestamp: newer.Unix(), Voltage: 110, Current: 1, Power: 110, KwhTotal: 1}
	if err := service.storeLegacyTelemetry(payload, newer); err != nil {
		t.Fatal(err)
	}
	payload.Timestamp = older.Add(-time.Hour).Unix()
	if err := service.storeLegacyTelemetry(payload, older); err != nil {
		t.Fatal(err)
	}
	var device domain.Device
	if err := db.First(&device, fixture.first.ID).Error; err != nil {
		t.Fatal(err)
	}
	if device.LastSeen == nil || !device.LastSeen.Equal(newer) {
		t.Fatalf("legacy last_seen regressed: %v", device.LastSeen)
	}
}
