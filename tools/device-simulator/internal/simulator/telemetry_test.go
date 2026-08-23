package simulator

import (
	"encoding/json"
	"errors"
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

func TestGeneratorCoverageUsesExplicitIntervalAndActualDuration(t *testing.T) {
	start := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	end := start.Add(1500 * time.Millisecond)
	generator, err := NewGenerator(DefaultMAC, "test", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	telemetry := generator.NextCoverage(start, end)
	if telemetry.CoverageVersion == nil || *telemetry.CoverageVersion != 1 || telemetry.IntervalStartTS == nil || telemetry.IntervalEndTS == nil {
		t.Fatalf("coverage fields missing: %+v", telemetry)
	}
	instant := time.Unix(telemetry.Timestamp, 0)
	if telemetry.Sequence != 0 || instant.Before(start) || !instant.Before(end) {
		t.Fatalf("invalid coverage timestamp/sequence: %+v", telemetry)
	}
	want := telemetry.Power * end.Sub(start).Hours() / 1000
	if telemetry.EnergyDeltaKwh != want {
		t.Fatalf("delta=%v want=%v", telemetry.EnergyDeltaKwh, want)
	}
}

func TestGeneratorCoverageUnsynchronizedClockFailsClosed(t *testing.T) {
	generator, err := NewGenerator(DefaultMAC, "test", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	telemetry, err := generator.NextCoverageCheckedWithClock(start, start.Add(time.Second), false)
	if err != ErrCoverageClockUnsynchronized {
		t.Fatalf("err=%v", err)
	}
	if telemetry.CoverageVersion != nil || generator.Sequence != 0 {
		t.Fatalf("unsynchronized clock emitted or advanced generator: %+v seq=%d", telemetry, generator.Sequence)
	}
}

func TestGeneratorCoverageRejectsNonRepresentableInterval(t *testing.T) {
	start := time.Date(2026, 8, 24, 23, 59, 59, 500000000, time.UTC)
	end := start.Add(500 * time.Millisecond)
	generator, err := NewGenerator(DefaultMAC, "test", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := generator.NextCoverageChecked(start, end); err == nil {
		t.Fatal("accepted sub-second coverage interval")
	}
	if telemetry := generator.NextCoverage(start, end); telemetry.CoverageVersion != nil || telemetry.IntervalStartTS != nil || telemetry.IntervalEndTS != nil {
		t.Fatalf("invalid interval was emitted: %+v", telemetry)
	}
}

func TestGeneratorCoverageTimestampIsRepresentable(t *testing.T) {
	cases := []struct {
		name  string
		start time.Time
		end   time.Time
	}{
		{
			name:  "fractional start with one second available",
			start: time.Date(2026, 8, 24, 23, 59, 59, 500000000, time.UTC),
			end:   time.Date(2026, 8, 25, 0, 0, 1, 0, time.UTC),
		},
		{
			name:  "exact whole-second midnight",
			start: time.Date(2026, 8, 24, 23, 59, 59, 0, time.UTC),
			end:   time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			generator, err := NewGenerator(DefaultMAC, "test", 1, 0)
			if err != nil {
				t.Fatal(err)
			}
			telemetry, err := generator.NextCoverageChecked(tc.start, tc.end)
			if err != nil {
				t.Fatal(err)
			}
			selected := time.Unix(telemetry.Timestamp, 0)
			if selected.Before(tc.start) || !selected.Before(tc.end) {
				t.Fatalf("ts=%v outside [%v,%v)", selected, tc.start, tc.end)
			}
			if tc.end.Sub(tc.start) < time.Second {
				t.Fatalf("test interval is below minimum: %v", tc.end.Sub(tc.start))
			}
		})
	}
}

func TestCoverageIntervalEndRejectsSubMinimumMidnightTail(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Taipei")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 24, 23, 59, 59, 500000000, loc)
	if _, err := CoverageIntervalEnd(start, time.Second); err == nil {
		t.Fatal("accepted sub-minimum midnight tail")
	}
}

func TestCoverageIntervalEndFailsClosedOnTimezoneError(t *testing.T) {
	start := time.Date(2026, 8, 24, 23, 59, 55, 0, time.UTC)
	_, err := CoverageIntervalEndWithLoader(start, 10*time.Second, func(string) (*time.Location, error) {
		return nil, errors.New("timezone unavailable")
	})
	if err == nil {
		t.Fatal("timezone failure did not fail closed")
	}
}

func TestCoverageIntervalEndShortensAtMidnightAndRemainsContiguous(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Taipei")
	if err != nil {
		t.Fatal(err)
	}
	firstStart := time.Date(2026, 8, 24, 23, 59, 55, 0, loc)
	firstEnd, err := CoverageIntervalEnd(firstStart, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	secondEnd, err := CoverageIntervalEnd(firstEnd, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	midnight := time.Date(2026, 8, 25, 0, 0, 0, 0, loc)
	if !firstEnd.Equal(midnight) || !secondEnd.Equal(midnight.Add(10*time.Second)) {
		t.Fatalf("unexpected adjacent intervals: [%v,%v), [%v,%v)", firstStart, firstEnd, firstEnd, secondEnd)
	}
	if secondEnd.Sub(firstEnd) < MinCoverageInterval {
		t.Fatalf("second interval below minimum: %v", secondEnd.Sub(firstEnd))
	}
}

func TestCoverageIntervalEndAdjustsPreviousIntervalForOneSecondTail(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Taipei")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 24, 23, 59, 54, 500000000, loc)
	firstEnd, err := CoverageIntervalEnd(start, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	secondEnd, err := CoverageIntervalEnd(firstEnd, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	midnight := time.Date(2026, 8, 25, 0, 0, 0, 0, loc)
	if !firstEnd.Equal(midnight.Add(-MinCoverageInterval)) || !secondEnd.Equal(midnight) {
		t.Fatalf("interval chain did not close at midnight: [%v,%v), [%v,%v)", start, firstEnd, firstEnd, secondEnd)
	}
}

func TestCoverageIntervalEndAcceptsExactWholeSecondMidnight(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Taipei")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 24, 23, 59, 59, 0, loc)
	end, err := CoverageIntervalEnd(start, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !end.Equal(time.Date(2026, 8, 25, 0, 0, 0, 0, loc)) {
		t.Fatalf("unexpected end: %v", end)
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
