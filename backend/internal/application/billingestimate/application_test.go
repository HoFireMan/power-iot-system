package billingestimate

import (
	"context"
	"math/big"
	"testing"
	"time"

	"power-iot-backend/internal/adapters/persistence"
	corebillingenergy "power-iot-backend/internal/core/billingenergy"
	coreestimate "power-iot-backend/internal/core/billingestimate"
)

type estimateRepositoryStub struct {
	projection persistence.BillingEstimateProjection
	err        error
}

func (s estimateRepositoryStub) FindBillingEstimate(context.Context, uint, uint, corebillingenergy.BillingMonth, func() time.Time) (persistence.BillingEstimateProjection, error) {
	return s.projection, s.err
}

func estimateProjection(usage *int64, observed, expected time.Duration) persistence.BillingEstimateProjection {
	return persistence.BillingEstimateProjection{
		ShopID: 7, ShopCode: "SHOP-7", ShopName: "Test Shop", ElectricityTariff: "LIGHTING_COMMERCIAL",
		PlanCode: "LIGHTING_COMMERCIAL_NON_TOU", RateSetVersion: "TAIPOWER_2025_10_01", Currency: "TWD", IncludesTax: true,
		Season: coreestimate.SeasonSummer, MinimumMonthlyCharge: "100.000000",
		Tiers: []persistence.BillingRateTierProjection{
			{LowerKwh: "0", UpperKwh: ptrString("330"), RatePerKwh: "2.71"},
			{LowerKwh: "330", UpperKwh: nil, RatePerKwh: "3.76"},
		},
		Energy: corebillingenergy.Facts{PeriodStart: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), PeriodEnd: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), ExpectedDuration: expected, ObservedDuration: observed, UsageMicros: usage, Coverage: new(big.Rat).SetFrac(big.NewInt(int64(observed)), big.NewInt(int64(expected)))},
	}
}

func ptrString(value string) *string { return &value }

func TestServiceDistinguishesNoDataZeroPartialAndComplete(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC) }
	for _, test := range []struct {
		name   string
		usage  *int64
		seen   time.Duration
		status coreestimate.Status
		amount *string
	}{
		{name: "no data", usage: nil, seen: 0, status: coreestimate.StatusNoData},
		{name: "zero", usage: ptrInt64(0), seen: time.Hour, status: coreestimate.StatusPartial, amount: ptrString("100")},
		{name: "partial", usage: ptrInt64(1_000_000), seen: time.Hour, status: coreestimate.StatusPartial, amount: ptrString("100")},
		{name: "complete", usage: ptrInt64(1_000_000), seen: 2 * time.Hour, status: coreestimate.StatusComplete, amount: ptrString("100")},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := New(estimateRepositoryStub{projection: estimateProjection(test.usage, test.seen, 2*time.Hour)}, now)
			result, err := service.Find(context.Background(), 1, 7, "2026-08")
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != test.status {
				t.Fatalf("status=%s want=%s", result.Status, test.status)
			}
			if test.amount == nil && result.Charges != nil {
				t.Fatalf("no-data charges=%+v", result.Charges)
			}
			if test.amount != nil && result.Charges.EstimatedTotal != *test.amount {
				t.Fatalf("amount=%s want=%s", result.Charges.EstimatedTotal, *test.amount)
			}
		})
	}
}

func TestServiceReturnsStableDomainOutcomesWithoutAmounts(t *testing.T) {
	for _, test := range []struct {
		err    error
		status coreestimate.Status
	}{
		{err: coreestimate.ErrConfigurationRequired, status: coreestimate.StatusConfigurationRequired},
		{err: coreestimate.ErrUnsupportedTariff, status: coreestimate.StatusUnsupportedTariff},
		{err: coreestimate.ErrUnsupportedPeriod, status: coreestimate.StatusUnsupportedPeriod},
		{err: coreestimate.ErrRateNotFound, status: coreestimate.StatusRateNotFound},
	} {
		result, err := New(estimateRepositoryStub{err: test.err}, time.Now).Find(context.Background(), 1, 7, "2026-08")
		if err != nil || result.Status != test.status || result.Charges != nil {
			t.Fatalf("err=%v result=%+v", err, result)
		}
	}
}

func TestServiceRejectsMalformedMonthBeforeRepository(t *testing.T) {
	result, err := New(estimateRepositoryStub{}, time.Now).Find(context.Background(), 1, 7, "2026-8")
	if err == nil || result.Status != "" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func ptrInt64(value int64) *int64 { return &value }
