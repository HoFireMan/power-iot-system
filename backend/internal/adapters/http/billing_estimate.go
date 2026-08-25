package httpadapter

import (
	"context"
	"errors"
	"net/http"

	appbillingestimate "power-iot-backend/internal/application/billingestimate"
	corebillingenergy "power-iot-backend/internal/core/billingenergy"
	corebillingestimate "power-iot-backend/internal/core/billingestimate"

	"github.com/gin-gonic/gin"
)

type BillingEstimateQuery interface {
	Find(context.Context, uint, uint, string) (appbillingestimate.Estimate, error)
}

func NewBillingEstimateHandler(query BillingEstimateQuery) gin.HandlerFunc {
	return func(c *gin.Context) {
		identity, ok := IdentityFromGin(c)
		if !ok || query == nil {
			WritePublicError(c, ErrUnauthorized)
			return
		}
		userID, err := parseExternalID(identity.UserID)
		if err != nil {
			WritePublicError(c, ErrUnauthorized)
			return
		}
		shopID, err := parseExternalID(c.Param("shopId"))
		if err != nil {
			WritePublicError(c, ErrValidation)
			return
		}
		month := c.Query("month")
		if month == "" {
			WritePublicError(c, ErrValidation)
			return
		}
		result, err := query.Find(c.Request.Context(), userID, shopID, month)
		if err != nil {
			if errors.Is(err, corebillingestimate.ErrEstimateAccess) {
				WritePublicError(c, ErrForbidden)
				return
			}
			if errors.Is(err, corebillingenergy.ErrInvalidBillingMonth) || errors.Is(err, corebillingestimate.ErrInvalidCalculationInput) {
				WritePublicError(c, ErrValidation)
				return
			}
			WritePublicError(c, err)
			return
		}
		c.JSON(http.StatusOK, estimateResponse(result))
	}
}

type estimateResponseBody struct {
	Status   string                   `json:"status"`
	Month    string                   `json:"month"`
	Period   estimatePeriodResponse   `json:"period"`
	Shop     estimateShopResponse     `json:"shop"`
	Tariff   estimateTariffResponse   `json:"tariff"`
	RateSet  estimateRateSetResponse  `json:"rateSet"`
	Energy   estimateEnergyResponse   `json:"energy"`
	Tiers    []estimateTierResponse   `json:"tiers"`
	Charges  *estimateChargesResponse `json:"charges"`
	Warnings []string                 `json:"warnings"`
}

type estimatePeriodResponse struct {
	Start    *string `json:"start,omitempty"`
	End      *string `json:"end,omitempty"`
	Timezone string  `json:"timezone"`
}

type estimateShopResponse struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

type estimateTariffResponse struct {
	ElectricityTariff string  `json:"electricityTariff"`
	PlanCode          string  `json:"planCode"`
	UsageClass        *string `json:"usageClass,omitempty"`
	Season            string  `json:"season"`
}

type estimateRateSetResponse struct {
	Version     string `json:"version"`
	Currency    string `json:"currency"`
	IncludesTax bool   `json:"includesTax"`
}

type estimateEnergyResponse struct {
	UsageKwh                *string `json:"usageKwh"`
	ExpectedDurationSeconds int64   `json:"expectedDurationSeconds"`
	ObservedDurationSeconds int64   `json:"observedDurationSeconds"`
	Coverage                *string `json:"coverage"`
}

type estimateTierResponse struct {
	FromKwh    string  `json:"fromKwh"`
	ToKwh      *string `json:"toKwh"`
	UsageKwh   string  `json:"usageKwh"`
	RatePerKwh string  `json:"ratePerKwh"`
	Subtotal   string  `json:"subtotal"`
}

type estimateChargesResponse struct {
	EnergyCharge            string `json:"energyCharge"`
	MinimumMonthlyCharge    string `json:"minimumMonthlyCharge"`
	MinimumChargeAdjustment string `json:"minimumChargeAdjustment"`
	EstimatedTotal          string `json:"estimatedTotal"`
}

func estimateResponse(result appbillingestimate.Estimate) estimateResponseBody {
	var start, end *string
	if !result.Period.Start.IsZero() {
		value := result.Period.Start.UTC().Format("2006-01-02T15:04:05Z07:00")
		start = &value
	}
	if !result.Period.End.IsZero() {
		value := result.Period.End.UTC().Format("2006-01-02T15:04:05Z07:00")
		end = &value
	}
	tiers := make([]estimateTierResponse, 0, len(result.Tiers))
	for _, tier := range result.Tiers {
		tiers = append(tiers, estimateTierResponse{FromKwh: tier.FromKwh, ToKwh: tier.ToKwh, UsageKwh: tier.UsageKwh, RatePerKwh: tier.RatePerKwh, Subtotal: tier.Subtotal})
	}
	warnings := make([]string, 0, len(result.Warnings))
	for _, warning := range result.Warnings {
		warnings = append(warnings, string(warning))
	}
	var charges *estimateChargesResponse
	if result.Charges != nil {
		charges = &estimateChargesResponse{EnergyCharge: result.Charges.EnergyCharge, MinimumMonthlyCharge: result.Charges.MinimumMonthlyCharge, MinimumChargeAdjustment: result.Charges.MinimumChargeAdjustment, EstimatedTotal: result.Charges.EstimatedTotal}
	}
	return estimateResponseBody{
		Status: string(result.Status), Month: result.Month,
		Period:  estimatePeriodResponse{Start: start, End: end, Timezone: result.Period.Timezone},
		Shop:    estimateShopResponse{ID: result.Shop.ID, Code: result.Shop.Code, Name: result.Shop.Name},
		Tariff:  estimateTariffResponse{ElectricityTariff: result.Tariff.ElectricityTariff, PlanCode: result.Tariff.PlanCode, UsageClass: result.Tariff.UsageClass, Season: result.Tariff.Season},
		RateSet: estimateRateSetResponse{Version: result.RateSet.Version, Currency: result.RateSet.Currency, IncludesTax: result.RateSet.IncludesTax},
		Energy:  estimateEnergyResponse{UsageKwh: result.Energy.UsageKwh, ExpectedDurationSeconds: result.Energy.ExpectedDurationSeconds, ObservedDurationSeconds: result.Energy.ObservedDurationSeconds, Coverage: result.Energy.Coverage},
		Tiers:   tiers, Charges: charges, Warnings: warnings,
	}
}

func RegisterBillingEstimateRoute(router gin.IRouter, authenticator AccessTokenAuthenticator, query BillingEstimateQuery) {
	if router == nil {
		return
	}
	router.GET("/api/v1/shops/:shopId/billing/estimate", AuthenticationMiddleware(authenticator), NewBillingEstimateHandler(query))
}
