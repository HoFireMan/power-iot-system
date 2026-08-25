package billing

import "errors"

const (
	PlanCommercialNonTOU                  = "LIGHTING_COMMERCIAL_NON_TOU"
	PlanNoncommercialResidentialNonTOU    = "LIGHTING_NONCOMMERCIAL_RESIDENTIAL_NON_TOU"
	PlanNoncommercialNonresidentialNonTOU = "LIGHTING_NONCOMMERCIAL_NONRESIDENTIAL_NON_TOU"

	BillingMethodNonTOU         = "NON_TOU"
	CalculatorProgressiveNonTOU = "PROGRESSIVE_NON_TOU"
	UsageClassResidential       = "RESIDENTIAL"
	UsageClassNonResidential    = "NON_RESIDENTIAL_NONCOMMERCIAL"
	SeasonSummer                = "SUMMER"
	SeasonNonSummer             = "NON_SUMMER"
	RateSetDraft                = "DRAFT"
	RateSetAuthoritative        = "AUTHORITATIVE"
	RateSetRetired              = "RETIRED"
)

var (
	ErrUnsupportedBillingPlan       = errors.New("unsupported billing plan")
	ErrBillingTariffMismatch        = errors.New("billing plan does not match shop tariff")
	ErrBillingConfigurationRequired = errors.New("billing configuration required")
	ErrBillingHistoryConflict       = errors.New("billing history conflicts with tariff change")
)

func IsSupportedPlan(plan string) bool {
	switch plan {
	case PlanCommercialNonTOU, PlanNoncommercialResidentialNonTOU, PlanNoncommercialNonresidentialNonTOU:
		return true
	default:
		return false
	}
}

func PlanTariff(plan string) string {
	switch plan {
	case PlanCommercialNonTOU:
		return "LIGHTING_COMMERCIAL"
	case PlanNoncommercialResidentialNonTOU, PlanNoncommercialNonresidentialNonTOU:
		return "LIGHTING_NONCOMMERCIAL"
	default:
		return ""
	}
}

func PlanUsageClass(plan string) *string {
	switch plan {
	case PlanNoncommercialResidentialNonTOU:
		value := UsageClassResidential
		return &value
	case PlanNoncommercialNonresidentialNonTOU:
		value := UsageClassNonResidential
		return &value
	default:
		return nil
	}
}

func CompatiblePlan(tariff, plan string) bool {
	return IsSupportedPlan(plan) && PlanTariff(plan) == tariff
}

func SupportedPlansForTariff(tariff string) []string {
	switch tariff {
	case "LIGHTING_COMMERCIAL":
		return []string{PlanCommercialNonTOU}
	case "LIGHTING_NONCOMMERCIAL":
		return []string{PlanNoncommercialResidentialNonTOU, PlanNoncommercialNonresidentialNonTOU}
	default:
		return nil
	}
}
