package billing

import (
	"context"
	"errors"
	"testing"
	"time"

	"power-iot-backend/internal/adapters/persistence"
	corebilling "power-iot-backend/internal/core/billing"
)

type billingRepositoryStub struct {
	configuration persistence.BillingConfigurationProjection
	plan          string
	effective     time.Time
}

func (s *billingRepositoryStub) FindBillingConfiguration(context.Context, uint, uint, time.Time) (persistence.BillingConfigurationProjection, error) {
	return s.configuration, nil
}
func (s *billingRepositoryStub) SetBillingPlan(_ context.Context, _ uint, _ uint, plan string, effective time.Time) error {
	s.plan, s.effective = plan, effective
	return nil
}

func TestServiceDerivesEffectiveMonthAndRejectsIncompatiblePlan(t *testing.T) {
	tariff := "LIGHTING_NONCOMMERCIAL"
	repository := &billingRepositoryStub{configuration: persistence.BillingConfigurationProjection{ShopID: 7, ElectricityTariff: &tariff, Supported: true}}
	now := func() time.Time { return time.Date(2026, 8, 15, 4, 0, 0, 0, time.UTC) }
	service := New(repository, now)
	if err := service.SetConfiguration(context.Background(), 2, 7, corebilling.PlanNoncommercialResidentialNonTOU); err != nil {
		t.Fatal(err)
	}
	if repository.plan != corebilling.PlanNoncommercialResidentialNonTOU || !repository.effective.Equal(now()) {
		t.Fatalf("assignment=%s/%s", repository.plan, repository.effective)
	}
	repository.configuration.Current = &persistence.BillingAssignmentProjection{PlanCode: repository.plan, ValidFrom: repository.effective}
	if err := service.SetConfiguration(context.Background(), 2, 7, corebilling.PlanNoncommercialNonresidentialNonTOU); err != nil {
		t.Fatal(err)
	}
	if !repository.effective.Equal(now()) {
		t.Fatalf("as-of=%s", repository.effective)
	}
	if err := service.SetConfiguration(context.Background(), 2, 7, corebilling.PlanCommercialNonTOU); !errors.Is(err, corebilling.ErrBillingTariffMismatch) {
		t.Fatalf("mismatch=%v", err)
	}
}
