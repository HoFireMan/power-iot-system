// Package billingestimate contains the exact Billing V1 calculation rules.
// It has no persistence, HTTP, or presentation dependencies.
package billingestimate

import (
	"errors"
	"math/big"
	"strings"
)

var (
	ErrInvalidCalculationInput = errors.New("invalid billing calculation input")
	ErrInvalidTierSchedule     = errors.New("invalid billing tier schedule")
)

type Tier struct {
	LowerKwh   *big.Rat
	UpperKwh   *big.Rat
	RatePerKwh *big.Rat
}

type TierAllocation struct {
	FromKwh    *big.Rat
	ToKwh      *big.Rat
	UsageKwh   *big.Rat
	RatePerKwh *big.Rat
	Subtotal   *big.Rat
}

type Calculation struct {
	Tiers                   []TierAllocation
	RawEnergyCharge         *big.Rat
	EnergyCharge            *big.Rat
	MinimumMonthlyCharge    *big.Rat
	MinimumChargeAdjustment *big.Rat
	PreFinalTotal           *big.Rat
	EstimatedTotal          *big.Rat
}

type ProgressiveNonTOUCalculator struct{}

func (ProgressiveNonTOUCalculator) Calculate(usageKwh *big.Rat, tiers []Tier, minimumMonthlyCharge *big.Rat) (Calculation, error) {
	if usageKwh == nil || usageKwh.Sign() < 0 || minimumMonthlyCharge == nil || minimumMonthlyCharge.Sign() < 0 {
		return Calculation{}, ErrInvalidCalculationInput
	}
	if err := validateTiers(tiers); err != nil {
		return Calculation{}, err
	}
	result := Calculation{
		Tiers:                   make([]TierAllocation, 0, len(tiers)),
		RawEnergyCharge:         new(big.Rat),
		MinimumMonthlyCharge:    new(big.Rat).Set(minimumMonthlyCharge),
		MinimumChargeAdjustment: new(big.Rat),
	}
	remaining := new(big.Rat).Set(usageKwh)
	for _, tier := range tiers {
		if remaining.Sign() == 0 {
			break
		}
		if tier.UpperKwh != nil && remaining.Cmp(new(big.Rat).Sub(tier.UpperKwh, tier.LowerKwh)) > 0 {
			remainingInTier := new(big.Rat).Sub(tier.UpperKwh, tier.LowerKwh)
			allocation := new(big.Rat).Set(remainingInTier)
			result.Tiers = append(result.Tiers, allocationForTier(tier, allocation))
			result.RawEnergyCharge.Add(result.RawEnergyCharge, new(big.Rat).Mul(allocation, tier.RatePerKwh))
			remaining.Sub(remaining, allocation)
			continue
		}
		allocation := new(big.Rat).Set(remaining)
		result.Tiers = append(result.Tiers, allocationForTier(tier, allocation))
		result.RawEnergyCharge.Add(result.RawEnergyCharge, new(big.Rat).Mul(allocation, tier.RatePerKwh))
		remaining.SetInt64(0)
	}
	if remaining.Sign() > 0 {
		return Calculation{}, ErrInvalidTierSchedule
	}
	result.EnergyCharge = TruncateToTenth(result.RawEnergyCharge)
	if result.EnergyCharge.Cmp(result.MinimumMonthlyCharge) < 0 {
		result.MinimumChargeAdjustment.Sub(result.MinimumMonthlyCharge, result.EnergyCharge)
	}
	result.PreFinalTotal = new(big.Rat).Add(result.EnergyCharge, result.MinimumChargeAdjustment)
	result.EstimatedTotal = RoundToWhole(result.PreFinalTotal)
	return result, nil
}

func allocationForTier(tier Tier, usage *big.Rat) TierAllocation {
	to := (*big.Rat)(nil)
	if tier.UpperKwh != nil {
		to = new(big.Rat).Set(tier.UpperKwh)
	}
	return TierAllocation{
		FromKwh:    new(big.Rat).Set(tier.LowerKwh),
		ToKwh:      to,
		UsageKwh:   new(big.Rat).Set(usage),
		RatePerKwh: new(big.Rat).Set(tier.RatePerKwh),
		Subtotal:   new(big.Rat).Mul(usage, tier.RatePerKwh),
	}
}

func validateTiers(tiers []Tier) error {
	if len(tiers) == 0 || tiers[0].LowerKwh == nil || tiers[0].LowerKwh.Sign() != 0 {
		return ErrInvalidTierSchedule
	}
	for index, tier := range tiers {
		if tier.LowerKwh == nil || tier.RatePerKwh == nil || tier.LowerKwh.Sign() < 0 || tier.RatePerKwh.Sign() < 0 {
			return ErrInvalidTierSchedule
		}
		if index > 0 && tier.LowerKwh.Cmp(tiers[index-1].UpperKwh) != 0 {
			return ErrInvalidTierSchedule
		}
		if tier.UpperKwh != nil && tier.UpperKwh.Cmp(tier.LowerKwh) <= 0 {
			return ErrInvalidTierSchedule
		}
		if index < len(tiers)-1 && tier.UpperKwh == nil {
			return ErrInvalidTierSchedule
		}
	}
	return nil
}

func TruncateToTenth(value *big.Rat) *big.Rat {
	if value == nil {
		return nil
	}
	if value.Sign() < 0 {
		return new(big.Rat).Neg(TruncateToTenth(new(big.Rat).Neg(value)))
	}
	scaled := new(big.Int).Mul(value.Num(), big.NewInt(10))
	quotient := new(big.Int).Quo(scaled, value.Denom())
	return new(big.Rat).SetFrac(quotient, big.NewInt(10))
}

func RoundToWhole(value *big.Rat) *big.Rat {
	if value == nil {
		return nil
	}
	if value.Sign() < 0 {
		return new(big.Rat).Neg(RoundToWhole(new(big.Rat).Neg(value)))
	}
	quotient, remainder := new(big.Int).QuoRem(value.Num(), value.Denom(), new(big.Int))
	if new(big.Int).Lsh(new(big.Int).Set(remainder), 1).Cmp(value.Denom()) >= 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	return new(big.Rat).SetInt(quotient)
}

// FormatDecimal renders the finite decimal values used by the catalog without
// introducing binary floating point. It trims only insignificant trailing
// zeroes and returns a stable decimal string.
func FormatTenths(value *big.Rat) string {
	if value == nil {
		return ""
	}
	return TruncateToTenth(value).FloatString(1)
}

func FormatDecimal(value *big.Rat) string {
	if value == nil {
		return ""
	}
	if value.Sign() == 0 {
		return "0"
	}
	negative := value.Sign() < 0
	numerator := new(big.Int).Abs(value.Num())
	denominator := new(big.Int).Set(value.Denom())
	integer, remainder := new(big.Int).QuoRem(numerator, denominator, new(big.Int))
	if remainder.Sign() == 0 {
		if negative {
			return "-" + integer.String()
		}
		return integer.String()
	}
	factorTwo, factorFive := 0, 0
	for new(big.Int).Mod(denominator, big.NewInt(2)).Sign() == 0 {
		denominator.Quo(denominator, big.NewInt(2))
		factorTwo++
	}
	for new(big.Int).Mod(denominator, big.NewInt(5)).Sign() == 0 {
		denominator.Quo(denominator, big.NewInt(5))
		factorFive++
	}
	if denominator.Cmp(big.NewInt(1)) != 0 {
		return value.FloatString(12)
	}
	scale := factorTwo
	if factorFive > scale {
		scale = factorFive
	}
	multiplier := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil)
	scaled := new(big.Int).Quo(new(big.Int).Mul(numerator, multiplier), value.Denom())
	digits := scaled.String()
	if len(digits) <= scale {
		digits = strings.Repeat("0", scale-len(digits)+1) + digits
	}
	point := len(digits) - scale
	fraction := strings.TrimRight(digits[point:], "0")
	result := digits[:point]
	if fraction != "" {
		result += "." + fraction
	}
	if negative {
		return "-" + result
	}
	return result
}
