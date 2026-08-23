package coverage

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestOptionalInt64DistinguishesAbsentNullAndValue(t *testing.T) {
	var absent OptionalInt64
	if absent.Present {
		t.Fatal("zero value is present")
	}
	var null OptionalInt64
	if err := json.Unmarshal([]byte("null"), &null); err != nil || !null.Present || !null.IsNull {
		t.Fatalf("null=%+v err=%v", null, err)
	}
	var value OptionalInt64
	if err := json.Unmarshal([]byte("0"), &value); err != nil || !value.Valid() || value.Value != 0 {
		t.Fatalf("value=%+v err=%v", value, err)
	}
}

func TestIntervalUsesSecondsTsAndMillisecondBounds(t *testing.T) {
	start := time.UnixMilli(1_000)
	end := time.UnixMilli(3_000)
	if err := (Interval{StartMilliseconds: 1_000, EndMilliseconds: 3_000, TimestampSeconds: 1}).Validate(60_000); err != nil {
		t.Fatal(err)
	}
	if err := (Interval{StartMilliseconds: 1_000, EndMilliseconds: 3_000, TimestampSeconds: 3}).Validate(60_000); err == nil {
		t.Fatal("timestamp at exclusive end accepted")
	}
	if !start.Before(end) {
		t.Fatal("fixture interval invalid")
	}
}

func TestBoundaryValidation(t *testing.T) {
	loc, err := time.LoadLocation(BusinessTimezone)
	if err != nil {
		t.Fatal(err)
	}
	boundary := time.Date(2026, 1, 3, 0, 0, 0, 0, loc)
	valid := Interval{StartMilliseconds: boundary.Add(-30 * time.Second).UnixMilli(), EndMilliseconds: boundary.UnixMilli(), TimestampSeconds: boundary.Add(-30 * time.Second).Unix()}
	if err := valid.Validate(60_000); err != nil {
		t.Fatal(err)
	}
	invalid := Interval{StartMilliseconds: boundary.Add(-30 * time.Second).UnixMilli(), EndMilliseconds: boundary.Add(15 * time.Second).UnixMilli(), TimestampSeconds: boundary.Unix()}
	if err := invalid.Validate(60_000); err != ErrCoverageBoundary {
		t.Fatalf("got %v, want boundary error", err)
	}
}

func TestDigestIsFixedAndEnergyUsesSixDecimals(t *testing.T) {
	input := DigestInput{DeviceID: 7, ProfileVersion: 1, BootCounter: 2, Sequence: 3, IntervalStartMs: 1_000, IntervalEndMs: 3_000, RecordedAt: time.UnixMilli(1_000), EnergyDeltaKwh: 1.2345674}
	a := Digest(input)
	input.EnergyDeltaKwh = 1.23456749
	b := Digest(input)
	if !bytes.Equal(a[:], b[:]) {
		t.Fatal("values equal at persisted precision have different digests")
	}
	input.EnergyDeltaKwh = 1.234568
	c := Digest(input)
	if bytes.Equal(a[:], c[:]) {
		t.Fatal("changed persisted energy has same digest")
	}
}

func TestEvaluateWatermarkAndZero(t *testing.T) {
	start := time.Unix(0, 0).UTC()
	result := Evaluate(start, start.Add(3*time.Second), []Evidence{
		{Start: start, End: start.Add(time.Second), BootCounter: 1, Sequence: 0, Attributable: true, EnergyKwh: 0},
		{Start: start.Add(time.Second), End: start.Add(2 * time.Second), BootCounter: 1, Sequence: 1, Attributable: true, EnergyKwh: 0},
	})
	if result.Kwh == nil || *result.Kwh != 0 || result.ThroughAt == nil || !result.ThroughAt.Equal(start.Add(2*time.Second)) {
		t.Fatalf("result=%+v", result)
	}
}

func TestEvaluateStopsAtGap(t *testing.T) {
	start := time.Unix(0, 0).UTC()
	result := Evaluate(start, start.Add(4*time.Second), []Evidence{
		{Start: start, End: start.Add(time.Second), BootCounter: 1, Sequence: 0, Attributable: true, EnergyKwh: 1},
		{Start: start.Add(2 * time.Second), End: start.Add(3 * time.Second), BootCounter: 1, Sequence: 2, Attributable: true, EnergyKwh: 1},
	})
	if result.Kwh == nil || *result.Kwh != 1 || result.ThroughAt == nil || !result.ThroughAt.Equal(start.Add(time.Second)) || result.State != Gap {
		t.Fatalf("result=%+v", result)
	}
}

func TestEvaluateSkipsHistoricalBarrierBeforePeriod(t *testing.T) {
	month := time.Unix(0, 0).UTC()
	day := month.Add(time.Second)
	result := Evaluate(day, day.Add(time.Second), []Evidence{
		{Start: month, Barrier: Unknown},
		{Start: day, End: day.Add(time.Second), DeviceID: 1, BootCounter: 1, Sequence: 0, Attributable: true, EnergyKwh: 1},
	})
	if result.Kwh == nil || *result.Kwh != 1 || result.State != Proven {
		t.Fatalf("result=%+v", result)
	}
}

func TestEvaluateBarrierRetreatRemovesEnergyBeyondWatermark(t *testing.T) {
	start := time.Unix(0, 0).UTC()
	result := Evaluate(start, start.Add(time.Hour), []Evidence{
		{Start: start, End: start.Add(30 * time.Minute), DeviceID: 1, BootCounter: 1, Sequence: 0, Attributable: true, EnergyKwh: 1},
		{Start: start.Add(30 * time.Minute), End: start.Add(time.Hour), DeviceID: 1, BootCounter: 1, Sequence: 1, Attributable: true, EnergyKwh: 2},
		{Start: start.Add(45 * time.Minute), Barrier: Unknown},
	})
	if result.Kwh == nil || *result.Kwh != 1 || result.ThroughAt == nil || !result.ThroughAt.Equal(start.Add(30*time.Minute)) {
		t.Fatalf("result=%+v", result)
	}
}

func TestEvaluateOverlapRetreatRemovesOverlappedEnergy(t *testing.T) {
	start := time.Unix(0, 0).UTC()
	result := Evaluate(start, start.Add(90*time.Minute), []Evidence{
		{Start: start, End: start.Add(30 * time.Minute), DeviceID: 1, BootCounter: 1, Sequence: 0, Attributable: true, EnergyKwh: 1},
		{Start: start.Add(30 * time.Minute), End: start.Add(time.Hour), DeviceID: 1, BootCounter: 1, Sequence: 1, Attributable: true, EnergyKwh: 2},
		{Start: start.Add(45 * time.Minute), End: start.Add(75 * time.Minute), DeviceID: 1, BootCounter: 1, Sequence: 2, Attributable: true, EnergyKwh: 3},
	})
	if result.Kwh == nil || *result.Kwh != 1 || result.ThroughAt == nil || !result.ThroughAt.Equal(start.Add(30*time.Minute)) {
		t.Fatalf("result=%+v", result)
	}
}

func TestEvaluateAllowsDeviceReplacementAtAdjacentBoundary(t *testing.T) {
	start := time.Unix(0, 0).UTC()
	result := Evaluate(start, start.Add(3*time.Second), []Evidence{
		{Start: start, End: start.Add(time.Second), DeviceID: 1, BootCounter: 4, Sequence: 8, Attributable: true, EnergyKwh: 1},
		{Start: start.Add(time.Second), End: start.Add(2 * time.Second), DeviceID: 2, BootCounter: 9, Sequence: 87, Attributable: true, EnergyKwh: 2},
		{Start: start.Add(2 * time.Second), End: start.Add(3 * time.Second), DeviceID: 2, BootCounter: 9, Sequence: 88, Attributable: true, EnergyKwh: 3},
	})

	if result.Kwh == nil || *result.Kwh != 6 || result.State != Proven {
		t.Fatalf("result=%+v", result)
	}
}

func TestEvaluateLegacyBarrierDoesNotBecomeZero(t *testing.T) {
	start := time.Unix(0, 0).UTC()
	result := Evaluate(start, start.Add(3*time.Second), []Evidence{
		{Start: start, End: start.Add(time.Second), BootCounter: 1, Sequence: 0, Attributable: true, EnergyKwh: 2},
		{Start: start.Add(time.Second), Barrier: Unknown},
	})
	if result.Kwh == nil || *result.Kwh != 2 || result.State != Unknown {
		t.Fatalf("result=%+v", result)
	}
}
