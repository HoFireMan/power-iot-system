package historicalreport

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	corebillingenergy "power-iot-backend/internal/core/billingenergy"
)

type billingEnergyRepositoryStub struct {
	facts corebillingenergy.Facts
	err   error
}

func (s billingEnergyRepositoryStub) FindBillingEnergy(context.Context, uint, uint, corebillingenergy.BillingMonth, func() time.Time) (corebillingenergy.Facts, error) {
	return s.facts, s.err
}

func TestServiceProjectsMonthlyFactsAndStatuses(t *testing.T) {
	expected := 2 * time.Hour
	observed := time.Hour
	usage := int64(1_250_000)
	facts := corebillingenergy.Facts{
		ShopID:           7,
		PeriodStart:      time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		PeriodEnd:        time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		Cutoff:           time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		Snapshot:         time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC),
		UsageMicros:      &usage,
		ExpectedDuration: expected,
		ObservedDuration: observed,
		Coverage:         new(big.Rat).SetFrac64(1, 2),
		Warnings:         []corebillingenergy.WarningCode{corebillingenergy.WarningPartialMonitoringData},
		Points: []corebillingenergy.PointFacts{{
			MeasurementPointID: "mp-1",
			UsageMicros:        &usage,
			ExpectedDuration:   expected,
			ObservedDuration:   observed,
			Coverage:           new(big.Rat).SetFrac64(1, 2),
			Warnings:           []corebillingenergy.WarningCode{corebillingenergy.WarningPartialMonitoringData},
		}},
	}

	result, err := New(billingEnergyRepositoryStub{facts: facts}, func() time.Time { return facts.Snapshot }).Find(context.Background(), 42, 7, "2026-08")
	if err != nil {
		t.Fatal(err)
	}
	if result.Month != "2026-08" || result.Timezone != corebillingenergy.BusinessTimezone {
		t.Fatalf("identity=%+v", result)
	}
	if result.Summary.Status != StatusPartial || result.Summary.UsageKwh == nil || *result.Summary.UsageKwh != "1.25" || result.Summary.Coverage == nil || *result.Summary.Coverage != "0.5" {
		t.Fatalf("summary=%+v", result.Summary)
	}
	if len(result.MeasurementPoints) != 1 || result.MeasurementPoints[0].Status != StatusPartial || result.MeasurementPoints[0].UsageKwh == nil || *result.MeasurementPoints[0].UsageKwh != "1.25" {
		t.Fatalf("points=%+v", result.MeasurementPoints)
	}
	if len(result.Warnings) != 1 || result.Warnings[0] != string(corebillingenergy.WarningPartialMonitoringData) {
		t.Fatalf("warnings=%v", result.Warnings)
	}
}

func TestStatusDistinguishesNoDataAndValidZero(t *testing.T) {
	cases := []struct {
		name     string
		usage    *int64
		expected time.Duration
		observed time.Duration
		want     Status
	}{
		{name: "no expected window", expected: 0, want: StatusNoExpectedWindow},
		{name: "no accepted data", expected: time.Hour, want: StatusNoData},
		{name: "valid zero complete", usage: ptrInt64(0), expected: time.Hour, observed: time.Hour, want: StatusComplete},
		{name: "valid zero partial", usage: ptrInt64(0), expected: time.Hour, observed: 30 * time.Minute, want: StatusPartial},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			facts := corebillingenergy.Facts{PeriodStart: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), PeriodEnd: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Cutoff: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Snapshot: time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC), UsageMicros: tc.usage, ExpectedDuration: tc.expected, ObservedDuration: tc.observed}
			result, err := New(billingEnergyRepositoryStub{facts: facts}, nil).Find(context.Background(), 42, 7, "2026-08")
			if err != nil {
				t.Fatal(err)
			}
			if result.Summary.Status != tc.want {
				t.Fatalf("status=%s want=%s", result.Summary.Status, tc.want)
			}
			if tc.name == "valid zero complete" && (result.Summary.UsageKwh == nil || *result.Summary.UsageKwh != "0") {
				t.Fatalf("zero usage=%v", result.Summary.UsageKwh)
			}
			if tc.name == "no accepted data" && result.Summary.UsageKwh != nil {
				t.Fatalf("no-data usage=%v", result.Summary.UsageKwh)
			}
		})
	}
}

func TestServicePropagatesMonthValidationFromBillingEnergy(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "invalid month", err: corebillingenergy.ErrInvalidBillingMonth},
		{name: "future month", err: corebillingenergy.ErrFutureBillingMonth},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := New(billingEnergyRepositoryStub{err: test.err}, nil).Find(context.Background(), 42, 7, "2026-08")
			if !errors.Is(err, test.err) || result.Month != "" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func ptrInt64(value int64) *int64 { return &value }
