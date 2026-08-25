package httpadapter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	applicationbilling "power-iot-backend/internal/application/billing"
	corebilling "power-iot-backend/internal/core/billing"

	"github.com/gin-gonic/gin"
)

type BillingConfigurationQuery interface {
	GetConfiguration(context.Context, uint, uint) (applicationbilling.Configuration, error)
}

type BillingConfigurationMutation interface {
	SetConfiguration(context.Context, uint, uint, string) error
}

func NewBillingConfigurationHandler(query BillingConfigurationQuery) gin.HandlerFunc {
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
		result, err := query.GetConfiguration(c.Request.Context(), userID, shopID)
		if err != nil {
			WritePublicError(c, err)
			return
		}
		plans := make([]billingPlanResponse, 0, len(result.Plans))
		for _, plan := range result.Plans {
			plans = append(plans, billingPlanResponse{Code: plan.Code, UsageClass: plan.UsageClass})
		}
		c.JSON(http.StatusOK, billingConfigurationResponse{
			Shop:      billingShopResponse{ID: result.ShopID, ElectricityTariff: result.ElectricityTariff},
			Supported: result.Supported, Plans: plans,
			CurrentAssignment:   billingAssignmentResponse(result.Current),
			ScheduledAssignment: billingAssignmentResponse(result.Scheduled),
		})
	}
}

func NewBillingConfigurationMutationHandler(mutation BillingConfigurationMutation) gin.HandlerFunc {
	return func(c *gin.Context) {
		identity, ok := IdentityFromGin(c)
		if !ok || mutation == nil {
			WritePublicError(c, ErrUnauthorized)
			return
		}
		actorID, err := parseExternalID(identity.UserID)
		if err != nil {
			WritePublicError(c, ErrUnauthorized)
			return
		}
		shopID, err := parseExternalID(c.Param("shopId"))
		if err != nil {
			WritePublicError(c, ErrValidation)
			return
		}
		var body struct {
			PlanCode string `json:"planCode"`
		}
		decoder := json.NewDecoder(c.Request.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil || body.PlanCode == "" || !corebilling.IsSupportedPlan(body.PlanCode) {
			WritePublicError(c, ErrValidation)
			return
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			WritePublicError(c, ErrValidation)
			return
		}
		if err := mutation.SetConfiguration(c.Request.Context(), actorID, shopID, body.PlanCode); err != nil {
			WritePublicError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	}
}

type billingConfigurationResponse struct {
	Shop                billingShopResponse            `json:"shop"`
	Supported           bool                           `json:"supported"`
	Plans               []billingPlanResponse          `json:"compatiblePlans"`
	CurrentAssignment   *billingAssignmentResponseBody `json:"currentAssignment,omitempty"`
	ScheduledAssignment *billingAssignmentResponseBody `json:"scheduledAssignment,omitempty"`
}
type billingShopResponse struct {
	ID                string  `json:"id"`
	ElectricityTariff *string `json:"electricityTariff"`
}
type billingPlanResponse struct {
	Code       string  `json:"planCode"`
	UsageClass *string `json:"usageClass,omitempty"`
}
type billingAssignmentResponseBody struct {
	PlanCode  string  `json:"planCode"`
	ValidFrom string  `json:"validFrom"`
	ValidTo   *string `json:"validTo,omitempty"`
}

func billingAssignmentResponse(value *applicationbilling.Assignment) *billingAssignmentResponseBody {
	if value == nil {
		return nil
	}
	from := value.ValidFrom.Format("2006-01-02")
	out := &billingAssignmentResponseBody{PlanCode: value.PlanCode, ValidFrom: from}
	if value.ValidTo != nil {
		to := value.ValidTo.Format("2006-01-02")
		out.ValidTo = &to
	}
	return out
}

func RegisterBillingConfigurationRoutes(router gin.IRouter, authenticator AccessTokenAuthenticator, query BillingConfigurationQuery, mutation BillingConfigurationMutation) {
	if router == nil {
		return
	}
	path := "/api/v1/shops/:shopId/billing/configuration"
	router.GET(path, AuthenticationMiddleware(authenticator), NewBillingConfigurationHandler(query))
	router.PUT(path, AuthenticationMiddleware(authenticator), NewBillingConfigurationMutationHandler(mutation))
}
