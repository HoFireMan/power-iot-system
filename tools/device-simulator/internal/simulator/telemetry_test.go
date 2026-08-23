package simulator

import (
	"encoding/json"
	"testing"
	"time"
)

func TestGeneratorProducesProtocolV1Telemetry(t *testing.T) {
	generator, err := NewGenerator("aa:bb:cc:dd:ee:ff", "test-fw", 7, 123)
	if err != nil {
		t.Fatal(err)
	}
	telemetry := generator.Next(time.Unix(1786021200, 0), 5*time.Second)
	if !telemetry.IsPhysicallyConsistent() {
		t.Fatalf("inconsistent telemetry: %+v", telemetry)
	}
	if telemetry.MAC != "AABBCCDDEEFF" || telemetry.ProtocolVersion != 1 || telemetry.Sequence != 123 || telemetry.BootCounter != 7 {
		t.Fatalf("unexpected identity: %+v", telemetry)
	}
	body, err := json.Marshal(telemetry)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"mac"`, `"v"`, `"c"`, `"p"`, `"kwh"`, `"ts"`, `"protocol_version"`, `"boot_id"`, `"boot_counter"`, `"seq"`, `"pf"`, `"energy_delta_kwh"`, `"rssi"`, `"valid_samples"`, `"invalid_samples"`, `"fw"`} {
		if !contains(string(body), field) {
			t.Errorf("missing field %s in %s", field, body)
		}
	}
}

func TestGeneratorEmitsExplicitZeroEnergyDelta(t *testing.T) {
	generator, err := NewGenerator(DefaultMAC, "test-fw", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	telemetry := generator.Next(time.Unix(1786021200, 0), 0)
	if telemetry.ProtocolVersion != 1 || telemetry.EnergyDeltaKwh != 0 {
		t.Fatalf("unexpected zero-delta telemetry: %+v", telemetry)
	}
	body, err := json.Marshal(telemetry)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(body), `"energy_delta_kwh":0`) {
		t.Fatalf("zero energy delta was omitted: %s", body)
	}
}

func TestGeneratorSequenceAndKwhIncrease(t *testing.T) {
	generator, err := NewGenerator(DefaultMAC, "test-fw", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	first := generator.Next(time.Now(), time.Second)
	second := generator.Next(time.Now().Add(time.Second), time.Second)
	if first.Sequence != 0 || second.Sequence != 1 {
		t.Fatalf("sequence did not increment: %d, %d", first.Sequence, second.Sequence)
	}
	if second.Kwh < first.Kwh {
		t.Fatalf("kWh decreased: %f -> %f", first.Kwh, second.Kwh)
	}
}

func TestDuplicateIdentityCanBeReusedByCaller(t *testing.T) {
	generator, err := NewGenerator(DefaultMAC, "test-fw", 2, 10)
	if err != nil {
		t.Fatal(err)
	}
	first := generator.Next(time.Now(), time.Second)
	duplicate := first
	if duplicate.BootCounter != first.BootCounter || duplicate.Sequence != first.Sequence {
		t.Fatalf("duplicate identity changed: %+v vs %+v", first, duplicate)
	}
	if generator.Sequence != 11 {
		t.Fatalf("generator should advance only once, got %d", generator.Sequence)
	}
}

func TestTelemetryIdentityPreservesReplay(t *testing.T) {
	generator, err := NewGenerator(DefaultMAC, "test-fw", 7, 123)
	if err != nil {
		t.Fatal(err)
	}
	first := generator.Next(time.Unix(1786021200, 0), time.Second)
	sequenceAfterFirst := generator.Sequence
	replay := first

	if replay.Identity() != first.Identity() {
		t.Fatalf("replay changed identity: first=%+v replay=%+v", first.Identity(), replay.Identity())
	}
	if generator.Sequence != sequenceAfterFirst {
		t.Fatalf("replay allocated a new sequence: got %d, want %d", generator.Sequence, sequenceAfterFirst)
	}
}

func TestTelemetryIdentitySeparatesBootCounters(t *testing.T) {
	firstGenerator, err := NewGenerator(DefaultMAC, "test-fw", 1, 42)
	if err != nil {
		t.Fatal(err)
	}
	secondGenerator, err := NewGenerator(DefaultMAC, "test-fw", 2, 42)
	if err != nil {
		t.Fatal(err)
	}
	first := firstGenerator.Next(time.Unix(1786021200, 0), time.Second)
	second := secondGenerator.Next(time.Unix(1786021200, 0), time.Second)

	if first.Sequence != second.Sequence {
		t.Fatalf("test setup did not reuse sequence: %d vs %d", first.Sequence, second.Sequence)
	}
	if first.Identity() == second.Identity() {
		t.Fatalf("different boot counters share identity: first=%+v second=%+v", first.Identity(), second.Identity())
	}
}

func contains(value, needle string) bool {
	for i := 0; i+len(needle) <= len(value); i++ {
		if value[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
