package iot

import (
	"testing"
	"time"

	"power-iot-backend/internal/core/domain"
)

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
