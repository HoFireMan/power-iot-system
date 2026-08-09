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

func TestAckWaiterIdentityIsolation(t *testing.T) {
	device := &deviceSimulator{pending: make(map[ackKey][]chan simulator.Ack)}
	wanted := simulator.TelemetryIdentity{BootCounter: 7, Sequence: 11}
	waiter := device.registerAck(wanted)

	device.resolveAck(simulator.Ack{BootCounter: 8, Sequence: 11, Status: "stored"})
	assertNoAck(t, waiter, "different boot counter")
	device.resolveAck(simulator.Ack{BootCounter: 7, Sequence: 12, Status: "stored"})
	assertNoAck(t, waiter, "different sequence")
	device.resolveAck(simulator.Ack{BootCounter: 99, Sequence: 99, Status: "stored"})
	assertNoAck(t, waiter, "unknown identity")

	device.resolveAck(simulator.Ack{BootCounter: 7, Sequence: 11, Status: "stored"})
	select {
	case ack := <-waiter:
		if ack.BootCounter != wanted.BootCounter || ack.Sequence != wanted.Sequence {
			t.Fatalf("resolved wrong waiter identity: %+v", ack)
		}
	case <-time.After(time.Second):
		t.Fatal("matching ACK did not resolve waiter")
	}
}

func assertNoAck(t *testing.T, waiter <-chan simulator.Ack, reason string) {
	t.Helper()
	select {
	case ack := <-waiter:
		t.Fatalf("ACK for %s resolved waiter: %+v", reason, ack)
	default:
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
