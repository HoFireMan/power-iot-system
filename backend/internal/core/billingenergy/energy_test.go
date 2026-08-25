package billingenergy

import (
	"math/big"
	"testing"
	"time"
)

func TestParseBillingMonthAndBuildPeriodAtTaipeiBoundary(t *testing.T) {
	month, err := ParseBillingMonth("2026-08")
	if err != nil {
		t.Fatal(err)
	}
	loc := mustLocation()
	period, err := month.Period(time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 8, 1, 0, 0, 0, 0, loc).UTC(); !period.Start.Equal(want) {
		t.Fatalf("start=%s want=%s", period.Start, want)
	}
	if want := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC); !period.Cutoff.Equal(want) {
		t.Fatalf("cutoff=%s want=%s", period.Cutoff, want)
	}
	if !period.Current {
		t.Fatal("August should be current for August snapshot")
	}

	for _, value := range []string{"2026-8", "08/2026", "2026-08-01", "2026-13", "x-08"} {
		if _, err := ParseBillingMonth(value); err == nil {
			t.Fatalf("accepted invalid month %q", value)
		}
	}
}

func TestBillingMonthHistoricalFutureAndLeapYear(t *testing.T) {
	loc := mustLocation()
	historical, err := ParseBillingMonth("2026-07")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	period, err := historical.Period(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if period.Current || !period.Cutoff.Equal(period.End) {
		t.Fatalf("historical period=%+v", period)
	}

	future, err := ParseBillingMonth("2026-09")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := future.Period(snapshot); err != ErrFutureBillingMonth {
		t.Fatalf("future error=%v", err)
	}

	leap, err := ParseBillingMonth("2028-02")
	if err != nil {
		t.Fatal(err)
	}
	leapPeriod, err := leap.Period(time.Date(2028, 3, 1, 1, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if got := leapPeriod.End.Sub(leapPeriod.Start); got != 29*24*time.Hour {
		t.Fatalf("leap duration=%s", got)
	}
	dec, err := ParseBillingMonth("2026-12")
	if err != nil {
		t.Fatal(err)
	}
	decPeriod, err := dec.Period(time.Date(2027, 1, 1, 0, 0, 0, 0, loc))
	if err != nil {
		t.Fatal(err)
	}
	if !decPeriod.End.Equal(time.Date(2027, 1, 1, 0, 0, 0, 0, loc).UTC()) || decPeriod.Current {
		t.Fatalf("December rollover=%+v", decPeriod)
	}
}

func TestUnionClipsAssignmentsAndObservedIntervals(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(10 * time.Hour)
	assigned := UnionDuration([]Interval{
		{Start: start.Add(-time.Hour), End: start.Add(3 * time.Hour)},
		{Start: start.Add(3 * time.Hour), End: start.Add(7 * time.Hour)},
		{Start: start.Add(6 * time.Hour), End: end.Add(time.Hour)},
	}, start, end)
	if assigned != 10*time.Hour {
		t.Fatalf("assigned=%s", assigned)
	}
	observed := []ObservedInterval{
		{Interval: Interval{Start: start, End: start.Add(time.Hour)}, EnergyMicros: 1_000_001},
		{Interval: Interval{Start: start.Add(2 * time.Hour), End: start.Add(3 * time.Hour)}, EnergyMicros: 0},
	}
	facts, warnings := EvaluateObserved(observed, start, end)
	if facts.Duration != 2*time.Hour || facts.EnergyMicros == nil || *facts.EnergyMicros != 1_000_001 {
		t.Fatalf("observed=%+v", facts)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings=%v", warnings)
	}
}

func TestEvaluateObservedExcludesOverlapsAndPreservesNoData(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(4 * time.Hour)
	facts, warnings := EvaluateObserved([]ObservedInterval{
		{Interval: Interval{Start: start, End: start.Add(2 * time.Hour)}, EnergyMicros: 2_000_000},
		{Interval: Interval{Start: start.Add(time.Hour), End: start.Add(3 * time.Hour)}, EnergyMicros: 3_000_000},
	}, start, end)
	if facts.Duration != 0 || facts.EnergyMicros != nil {
		t.Fatalf("overlap facts=%+v", facts)
	}
	if !containsWarning(warnings, WarningOverlappingEvidenceExcluded) {
		t.Fatalf("overlap warnings=%v", warnings)
	}

	zero, warnings := EvaluateObserved([]ObservedInterval{{Interval: Interval{Start: start, End: end}, EnergyMicros: 0}}, start, end)
	if zero.EnergyMicros == nil || *zero.EnergyMicros != 0 || zero.Duration != 4*time.Hour || len(warnings) != 0 {
		t.Fatalf("zero=%+v warnings=%v", zero, warnings)
	}
}

func TestAggregatePointsUsesDurationWeightedExactRatioAndNullNoData(t *testing.T) {
	points := []PointFacts{
		{MeasurementPointID: "A", ExpectedDuration: 100 * time.Hour, ObservedDuration: 90 * time.Hour, UsageMicros: ptrInt64(0)},
		{MeasurementPointID: "B", ExpectedDuration: 50 * time.Hour, ObservedDuration: 25 * time.Hour, UsageMicros: ptrInt64(1_250_000)},
	}
	result := Aggregate(7, "2026-08", points)
	if result.UsageMicros == nil || *result.UsageMicros != 1_250_000 || result.ExpectedDuration != 150*time.Hour || result.ObservedDuration != 115*time.Hour {
		t.Fatalf("aggregate=%+v", result)
	}
	if result.Coverage == nil || result.Coverage.Cmp(big.NewRat(23, 30)) != 0 || result.Points[0].Coverage.Cmp(big.NewRat(9, 10)) != 0 || result.Points[1].Coverage.Cmp(big.NewRat(1, 2)) != 0 {
		t.Fatalf("coverage=%v points=%+v", result.Coverage, result.Points)
	}

	noData := Aggregate(7, "2026-08", []PointFacts{{MeasurementPointID: "A", ExpectedDuration: time.Hour}})
	if noData.UsageMicros != nil || noData.ObservedDuration != 0 || noData.Coverage == nil || noData.Coverage.Sign() != 0 {
		t.Fatalf("no data=%+v", noData)
	}
}

func ptrInt64(value int64) *int64 { return &value }

func containsWarning(values []WarningCode, want WarningCode) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
