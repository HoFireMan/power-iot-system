package main

import (
	"testing"
	"time"

	"power-iot-device-simulator/internal/simulator"
)

func TestParseRecordedAt(t *testing.T) {
	recordedAt, err := parseRecordedAt("2026-08-08T04:00:01+08:00")
	if err != nil {
		t.Fatal(err)
	}
	if recordedAt == nil || !recordedAt.Equal(time.Date(2026, 8, 7, 20, 0, 1, 0, time.UTC)) {
		t.Fatalf("unexpected recorded_at: %v", recordedAt)
	}
	if _, err := parseRecordedAt("not-a-timestamp"); err == nil {
		t.Fatal("accepted invalid RFC3339 timestamp")
	}
}

func TestNextTelemetryUsesConfiguredRecordedAt(t *testing.T) {
	recordedAt := time.Unix(1786021200, 0).UTC()
	generator, err := simulator.NewGenerator(simulator.DefaultMAC, "test-fw", 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	device := &deviceSimulator{
		config:    config{Interval: 5 * time.Second, StartSequence: 10, RecordedAt: &recordedAt},
		generator: generator,
	}
	first := device.nextTelemetry(time.Now())
	second := device.nextTelemetry(time.Now().Add(time.Hour))
	if first.Timestamp != recordedAt.Unix() || second.Timestamp != recordedAt.Add(5*time.Second).Unix() {
		t.Fatalf("unexpected deterministic timestamps: %d, %d", first.Timestamp, second.Timestamp)
	}
}

func TestOfflineQueuePreservesConfiguredRecordedAt(t *testing.T) {
	recordedAt := time.Unix(1786021200, 0).UTC()
	generator, err := simulator.NewGenerator(simulator.DefaultMAC, "test-fw", 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	device := &deviceSimulator{
		config:    config{Interval: 5 * time.Second, ReplayCount: 2, RecordedAt: &recordedAt},
		generator: generator,
	}
	device.prepareOfflineQueue()
	if len(device.offlineQueue) != 2 {
		t.Fatalf("unexpected queue length: %d", len(device.offlineQueue))
	}
	if device.offlineQueue[0].Timestamp != recordedAt.Unix() || device.offlineQueue[1].Timestamp != recordedAt.Add(5*time.Second).Unix() {
		t.Fatalf("offline queue changed timestamps: %+v", device.offlineQueue)
	}
}
