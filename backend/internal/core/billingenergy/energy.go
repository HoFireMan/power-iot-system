// Package billingenergy contains the persistence-independent Billing V1 energy
// and monitoring-completeness facts. It deliberately does not know about rates,
// money, HTTP, or ORM models.
package billingenergy

import (
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"
)

const BusinessTimezone = "Asia/Taipei"

var (
	ErrInvalidBillingMonth = errors.New("invalid billing month")
	ErrFutureBillingMonth  = errors.New("future billing month")
)

type WarningCode string

const (
	WarningPartialMonitoringData       WarningCode = "PARTIAL_MONITORING_DATA"
	WarningNoExpectedMonitoringWindow  WarningCode = "NO_EXPECTED_MONITORING_WINDOW"
	WarningConflictingTelemetry        WarningCode = "CONFLICTING_TELEMETRY_EXCLUDED"
	WarningAmbiguousAssignment         WarningCode = "AMBIGUOUS_ASSIGNMENT_EXCLUDED"
	WarningLegacyEvidence              WarningCode = "LEGACY_EVIDENCE_EXCLUDED"
	WarningUnattributableEvidence      WarningCode = "UNATTRIBUTABLE_EVIDENCE_EXCLUDED"
	WarningOverlappingEvidenceExcluded WarningCode = "OVERLAPPING_EVIDENCE_EXCLUDED"
)

type BillingMonth struct {
	year  int
	month time.Month
}

func ParseBillingMonth(value string) (BillingMonth, error) {
	if len(value) != 7 || value[4] != '-' || strings.Trim(value, "0123456789-") != "" {
		return BillingMonth{}, ErrInvalidBillingMonth
	}
	year, yearErr := strconv.Atoi(value[:4])
	monthValue, monthErr := strconv.Atoi(value[5:])
	if yearErr != nil || monthErr != nil || year < 1 || monthValue < 1 || monthValue > 12 {
		return BillingMonth{}, ErrInvalidBillingMonth
	}
	return BillingMonth{year: year, month: time.Month(monthValue)}, nil
}

func (m BillingMonth) String() string {
	if m.year == 0 || m.month == 0 {
		return ""
	}
	return fmt.Sprintf("%04d-%02d", m.year, m.month)
}

func (m BillingMonth) Period(snapshot time.Time) (Period, error) {
	if m.year == 0 || m.month < time.January || m.month > time.December {
		return Period{}, ErrInvalidBillingMonth
	}
	loc, err := time.LoadLocation(BusinessTimezone)
	if err != nil {
		return Period{}, err
	}
	if snapshot.IsZero() {
		return Period{}, errors.New("billing snapshot is required")
	}
	snapshot = snapshot.UTC()
	localSnapshot := snapshot.In(loc)
	requestedIndex := m.year*12 + int(m.month)
	currentIndex := localSnapshot.Year()*12 + int(localSnapshot.Month())
	if requestedIndex > currentIndex {
		return Period{}, ErrFutureBillingMonth
	}
	startLocal := time.Date(m.year, m.month, 1, 0, 0, 0, 0, loc)
	endLocal := startLocal.AddDate(0, 1, 0)
	period := Period{Month: m, Start: startLocal.UTC(), End: endLocal.UTC(), Cutoff: endLocal.UTC()}
	if requestedIndex == currentIndex {
		period.Current = true
		if snapshot.Before(period.Cutoff) {
			period.Cutoff = snapshot
		}
	}
	return period, nil
}

type Period struct {
	Month   BillingMonth
	Start   time.Time
	End     time.Time
	Cutoff  time.Time
	Current bool
}

func (p Period) Valid() bool {
	return !p.Start.IsZero() && p.Start.Before(p.End) && !p.Cutoff.Before(p.Start) && !p.Cutoff.After(p.End)
}

type Interval struct {
	Start time.Time
	End   time.Time
}

func (i Interval) valid() bool { return i.Start.Before(i.End) }

func UnionDuration(intervals []Interval, lower, upper time.Time) time.Duration {
	if !lower.Before(upper) {
		return 0
	}
	clipped := make([]Interval, 0, len(intervals))
	for _, interval := range intervals {
		start, end := interval.Start, interval.End
		if start.Before(lower) {
			start = lower
		}
		if end.After(upper) {
			end = upper
		}
		if start.Before(end) {
			clipped = append(clipped, Interval{Start: start, End: end})
		}
	}
	return unionSortedDuration(clipped)
}

func unionSortedDuration(intervals []Interval) time.Duration {
	if len(intervals) == 0 {
		return 0
	}
	sort.Slice(intervals, func(i, j int) bool { return intervals[i].Start.Before(intervals[j].Start) })
	start, end := intervals[0].Start, intervals[0].End
	var total time.Duration
	for _, interval := range intervals[1:] {
		if !interval.Start.After(end) {
			if interval.End.After(end) {
				end = interval.End
			}
			continue
		}
		total += end.Sub(start)
		start, end = interval.Start, interval.End
	}
	return total + end.Sub(start)
}

type ObservedInterval struct {
	Interval
	EnergyMicros int64
}

type ObservedFacts struct {
	EnergyMicros *int64
	Duration     time.Duration
}

func EvaluateObserved(input []ObservedInterval, lower, upper time.Time) (ObservedFacts, []WarningCode) {
	if !lower.Before(upper) {
		return ObservedFacts{}, nil
	}
	unique := make(map[string]ObservedInterval, len(input))
	for _, item := range input {
		start, end := item.Start, item.End
		if start.Before(lower) {
			start = lower
		}
		if end.After(upper) {
			end = upper
		}
		if !start.Before(end) || item.EnergyMicros < 0 {
			continue
		}
		item.Start, item.End = start, end
		key := start.UTC().Format(time.RFC3339Nano) + "|" + end.UTC().Format(time.RFC3339Nano) + "|" + strconv.FormatInt(item.EnergyMicros, 10)
		unique[key] = item
	}
	items := make([]ObservedInterval, 0, len(unique))
	for _, item := range unique {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Start.Equal(items[j].Start) {
			return items[i].End.Before(items[j].End)
		}
		return items[i].Start.Before(items[j].Start)
	})
	accepted := make([]ObservedInterval, 0, len(items))
	warnings := make([]WarningCode, 0)
	for index := 0; index < len(items); {
		groupEnd := index + 1
		maxEnd := items[index].End
		for groupEnd < len(items) && items[groupEnd].Start.Before(maxEnd) {
			if items[groupEnd].End.After(maxEnd) {
				maxEnd = items[groupEnd].End
			}
			groupEnd++
		}
		if groupEnd-index > 1 {
			warnings = append(warnings, WarningOverlappingEvidenceExcluded)
		} else {
			accepted = append(accepted, items[index])
		}
		index = groupEnd
	}
	if len(accepted) == 0 {
		return ObservedFacts{}, warnings
	}
	var total int64
	intervals := make([]Interval, 0, len(accepted))
	for _, item := range accepted {
		total += item.EnergyMicros
		intervals = append(intervals, item.Interval)
	}
	duration := unionSortedDuration(intervals)
	return ObservedFacts{EnergyMicros: &total, Duration: duration}, warnings
}

type PointFacts struct {
	MeasurementPointID string
	ExpectedDuration   time.Duration
	ObservedDuration   time.Duration
	Coverage           *big.Rat
	UsageMicros        *int64
	Warnings           []WarningCode
}

type Facts struct {
	ShopID           uint
	Month            BillingMonth
	PeriodStart      time.Time
	PeriodEnd        time.Time
	Cutoff           time.Time
	Snapshot         time.Time
	Points           []PointFacts
	UsageMicros      *int64
	ExpectedDuration time.Duration
	ObservedDuration time.Duration
	Coverage         *big.Rat
	Warnings         []WarningCode
}

func Aggregate(shopID uint, month string, points []PointFacts) Facts {
	parsed, _ := ParseBillingMonth(month)
	result := Facts{ShopID: shopID, Month: parsed, Points: append([]PointFacts(nil), points...)}
	warningSet := make(map[WarningCode]struct{})
	var usage int64
	usagePresent := false
	for index := range result.Points {
		point := &result.Points[index]
		if point.ExpectedDuration > 0 {
			point.Coverage = new(big.Rat).SetFrac(big.NewInt(int64(point.ObservedDuration)), big.NewInt(int64(point.ExpectedDuration)))
		}
		result.ExpectedDuration += point.ExpectedDuration
		result.ObservedDuration += point.ObservedDuration
		if point.UsageMicros != nil {
			usage += *point.UsageMicros
			usagePresent = true
		}
		for _, warning := range point.Warnings {
			warningSet[warning] = struct{}{}
		}
	}
	if usagePresent {
		result.UsageMicros = &usage
	}
	if result.ExpectedDuration > 0 {
		result.Coverage = new(big.Rat).SetFrac(big.NewInt(int64(result.ObservedDuration)), big.NewInt(int64(result.ExpectedDuration)))
		if result.ObservedDuration < result.ExpectedDuration {
			warningSet[WarningPartialMonitoringData] = struct{}{}
		}
	} else {
		warningSet[WarningNoExpectedMonitoringWindow] = struct{}{}
	}
	result.Warnings = make([]WarningCode, 0, len(warningSet))
	for warning := range warningSet {
		result.Warnings = append(result.Warnings, warning)
	}
	sort.Slice(result.Warnings, func(i, j int) bool { return result.Warnings[i] < result.Warnings[j] })
	return result
}

func mustLocation() *time.Location {
	location, err := time.LoadLocation(BusinessTimezone)
	if err != nil {
		panic(err)
	}
	return location
}
