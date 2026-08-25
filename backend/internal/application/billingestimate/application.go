// Package billingestimate orchestrates historical catalog/configuration facts,
// exact Billing Energy, and the deep V1 calculator behind one read seam.
package billingestimate

import (
	"context"
	"errors"
	"math/big"
	"strconv"
	"time"

	"power-iot-backend/internal/adapters/persistence"
	corebillingenergy "power-iot-backend/internal/core/billingenergy"
	coreestimate "power-iot-backend/internal/core/billingestimate"
)

type Repository interface {
	FindBillingEstimate(context.Context, uint, uint, corebillingenergy.BillingMonth, func() time.Time) (persistence.BillingEstimateProjection, error)
}

type Service struct {
	repository Repository
	now        func() time.Time
}

func New(repository Repository, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{repository: repository, now: now}
}

type Shop struct {
	ID   string
	Code string
	Name string
}

type Tariff struct {
	ElectricityTariff string
	PlanCode          string
	UsageClass        *string
	Season            string
}

type RateSet struct {
	Version     string
	Currency    string
	IncludesTax bool
}

type Energy struct {
	UsageKwh                *string
	ExpectedDurationSeconds int64
	ObservedDurationSeconds int64
	Coverage                *string
}

type Tier struct {
	FromKwh    string
	ToKwh      *string
	UsageKwh   string
	RatePerKwh string
	Subtotal   string
}

type Charges struct {
	EnergyCharge            string
	MinimumMonthlyCharge    string
	MinimumChargeAdjustment string
	EstimatedTotal          string
}

type Estimate struct {
	Status   coreestimate.Status
	Month    string
	Period   Period
	Shop     Shop
	Tariff   Tariff
	RateSet  RateSet
	Energy   Energy
	Tiers    []Tier
	Charges  *Charges
	Warnings []coreestimate.WarningCode
}

type Period struct {
	Start    time.Time
	End      time.Time
	Timezone string
}

func (s *Service) Find(ctx context.Context, userID, shopID uint, monthValue string) (Estimate, error) {
	if s == nil || s.repository == nil || userID == 0 || shopID == 0 {
		return Estimate{}, coreestimate.ErrEstimateAccess
	}
	month, err := corebillingenergy.ParseBillingMonth(monthValue)
	if err != nil {
		return Estimate{}, err
	}
	projection, err := s.repository.FindBillingEstimate(ctx, userID, shopID, month, s.now)
	if err != nil {
		if isPublicOutcome(err) {
			return outcome(monthValue, err), nil
		}
		return Estimate{}, err
	}
	return calculate(monthValue, projection)
}

func calculate(monthValue string, projection persistence.BillingEstimateProjection) (Estimate, error) {
	usage, err := usageRat(projection.Energy.UsageMicros)
	if err != nil {
		return Estimate{}, err
	}
	result := Estimate{
		Status:   coreestimate.StatusNoData,
		Month:    monthValue,
		Period:   Period{Start: projection.Energy.PeriodStart, End: projection.Energy.PeriodEnd, Timezone: corebillingenergy.BusinessTimezone},
		Shop:     Shop{ID: strconv.FormatUint(uint64(projection.ShopID), 10), Code: projection.ShopCode, Name: projection.ShopName},
		Tariff:   Tariff{ElectricityTariff: projection.ElectricityTariff, PlanCode: projection.PlanCode, UsageClass: projection.UsageClass, Season: projection.Season},
		RateSet:  RateSet{Version: projection.RateSetVersion, Currency: projection.Currency, IncludesTax: projection.IncludesTax},
		Energy:   Energy{ExpectedDurationSeconds: int64(projection.Energy.ExpectedDuration / time.Second), ObservedDurationSeconds: int64(projection.Energy.ObservedDuration / time.Second)},
		Warnings: warningsFromEnergy(projection.Energy.Warnings),
	}
	if projection.Energy.Coverage != nil {
		coverage := formatRatio(projection.Energy.Coverage)
		result.Energy.Coverage = &coverage
	}
	result.Warnings = appendWarning(result.Warnings, coreestimate.WarningBottomDegreeNotModeled)
	if usage == nil {
		return result, nil
	}
	usageText := coreestimate.FormatDecimal(usage)
	result.Energy.UsageKwh = &usageText
	calculatorTiers := make([]coreestimate.Tier, 0, len(projection.Tiers))
	for _, tier := range projection.Tiers {
		lower, parseErr := parseRat(tier.LowerKwh)
		if parseErr != nil {
			return Estimate{}, parseErr
		}
		var upper *big.Rat
		if tier.UpperKwh != nil {
			upper, parseErr = parseRat(*tier.UpperKwh)
			if parseErr != nil {
				return Estimate{}, parseErr
			}
		}
		rate, parseErr := parseRat(tier.RatePerKwh)
		if parseErr != nil {
			return Estimate{}, parseErr
		}
		calculatorTiers = append(calculatorTiers, coreestimate.Tier{LowerKwh: lower, UpperKwh: upper, RatePerKwh: rate})
	}
	minimum, err := parseRat(projection.MinimumMonthlyCharge)
	if err != nil {
		return Estimate{}, err
	}
	calculation, err := (coreestimate.ProgressiveNonTOUCalculator{}).Calculate(usage, calculatorTiers, minimum)
	if err != nil {
		return Estimate{}, err
	}
	result.Tiers = make([]Tier, 0, len(calculation.Tiers))
	for _, tier := range calculation.Tiers {
		to := (*string)(nil)
		if tier.ToKwh != nil {
			value := coreestimate.FormatDecimal(tier.ToKwh)
			to = &value
		}
		result.Tiers = append(result.Tiers, Tier{FromKwh: coreestimate.FormatDecimal(tier.FromKwh), ToKwh: to, UsageKwh: coreestimate.FormatDecimal(tier.UsageKwh), RatePerKwh: coreestimate.FormatDecimal(tier.RatePerKwh), Subtotal: coreestimate.FormatDecimal(tier.Subtotal)})
	}
	result.Charges = &Charges{EnergyCharge: coreestimate.FormatTenths(calculation.EnergyCharge), MinimumMonthlyCharge: coreestimate.FormatTenths(calculation.MinimumMonthlyCharge), MinimumChargeAdjustment: coreestimate.FormatTenths(calculation.MinimumChargeAdjustment), EstimatedTotal: coreestimate.FormatDecimal(calculation.EstimatedTotal)}
	if projection.Energy.Coverage != nil && projection.Energy.ExpectedDuration > 0 && projection.Energy.ObservedDuration == projection.Energy.ExpectedDuration {
		result.Status = coreestimate.StatusComplete
	} else {
		result.Status = coreestimate.StatusPartial
		result.Warnings = appendWarning(result.Warnings, coreestimate.WarningPartialMonitoringData)
	}
	return result, nil
}

func outcome(month string, err error) Estimate {
	status := coreestimate.StatusUnsupportedPeriod
	switch {
	case errors.Is(err, coreestimate.ErrConfigurationRequired):
		status = coreestimate.StatusConfigurationRequired
	case errors.Is(err, coreestimate.ErrUnsupportedTariff):
		status = coreestimate.StatusUnsupportedTariff
	case errors.Is(err, coreestimate.ErrRateNotFound):
		status = coreestimate.StatusRateNotFound
	}
	return Estimate{Status: status, Month: month, Period: Period{Timezone: corebillingenergy.BusinessTimezone}}
}

func isPublicOutcome(err error) bool {
	return errors.Is(err, coreestimate.ErrConfigurationRequired) || errors.Is(err, coreestimate.ErrUnsupportedTariff) || errors.Is(err, coreestimate.ErrUnsupportedPeriod) || errors.Is(err, coreestimate.ErrRateNotFound)
}

func usageRat(micros *int64) (*big.Rat, error) {
	if micros == nil {
		return nil, nil
	}
	if *micros < 0 {
		return nil, coreestimate.ErrCatalogInvariant
	}
	return new(big.Rat).SetFrac(big.NewInt(*micros), big.NewInt(1_000_000)), nil
}

func parseRat(value string) (*big.Rat, error) {
	rat, ok := new(big.Rat).SetString(value)
	if !ok || rat.Sign() < 0 {
		return nil, coreestimate.ErrCatalogInvariant
	}
	return rat, nil
}

func formatRatio(value *big.Rat) string { return coreestimate.FormatDecimal(value) }

func warningsFromEnergy(values []corebillingenergy.WarningCode) []coreestimate.WarningCode {
	warnings := make([]coreestimate.WarningCode, 0, len(values))
	for _, value := range values {
		switch value {
		case corebillingenergy.WarningPartialMonitoringData:
			warnings = appendWarning(warnings, coreestimate.WarningPartialMonitoringData)
		case corebillingenergy.WarningConflictingTelemetry:
			warnings = appendWarning(warnings, coreestimate.WarningConflictingTelemetry)
		case corebillingenergy.WarningAmbiguousAssignment:
			warnings = appendWarning(warnings, coreestimate.WarningAmbiguousAssignment)
		case corebillingenergy.WarningLegacyEvidence:
			warnings = appendWarning(warnings, coreestimate.WarningLegacyEvidence)
		case corebillingenergy.WarningUnattributableEvidence:
			warnings = appendWarning(warnings, coreestimate.WarningUnattributableEvidence)
		case corebillingenergy.WarningOverlappingEvidenceExcluded:
			warnings = appendWarning(warnings, coreestimate.WarningOverlappingEvidenceExcluded)
		}
	}
	return warnings
}

func appendWarning(values []coreestimate.WarningCode, value coreestimate.WarningCode) []coreestimate.WarningCode {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
