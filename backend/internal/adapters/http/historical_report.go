package httpadapter

import (
	"context"
	"errors"
	"net/http"
	"time"

	"power-iot-backend/internal/adapters/persistence"
	apphistoricalreport "power-iot-backend/internal/application/historicalreport"
	corebillingenergy "power-iot-backend/internal/core/billingenergy"

	"github.com/gin-gonic/gin"
)

type HistoricalReportQuery interface {
	Find(context.Context, uint, uint, string) (apphistoricalreport.Report, error)
}

func NewHistoricalReportHandler(query HistoricalReportQuery) gin.HandlerFunc {
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
			switch {
			case errors.Is(err, corebillingenergy.ErrInvalidBillingMonth), errors.Is(err, corebillingenergy.ErrFutureBillingMonth):
				WritePublicError(c, ErrValidation)
			case errors.Is(err, persistence.ErrBillingEnergyAccess):
				WritePublicError(c, ErrForbidden)
			default:
				WritePublicError(c, err)
			}
			return
		}
		c.JSON(http.StatusOK, newHistoricalReportResponse(result))
	}
}

type historicalReportResponse struct {
	Month             string                          `json:"month"`
	Timezone          string                          `json:"timezone"`
	Period            historicalReportPeriodResponse  `json:"period"`
	Summary           historicalReportFactsResponse   `json:"summary"`
	MeasurementPoints []historicalReportPointResponse `json:"measurementPoints"`
	Warnings          []string                        `json:"warnings"`
}

type historicalReportPeriodResponse struct {
	Start    string `json:"start"`
	End      string `json:"end"`
	Cutoff   string `json:"cutoff"`
	Snapshot string `json:"snapshot"`
}

type historicalReportFactsResponse struct {
	Status                  string  `json:"status"`
	UsageKwh                *string `json:"usageKwh"`
	ExpectedDurationSeconds int64   `json:"expectedDurationSeconds"`
	ObservedDurationSeconds int64   `json:"observedDurationSeconds"`
	Coverage                *string `json:"coverage"`
}

type historicalReportPointResponse struct {
	MeasurementPointID      string   `json:"measurementPointId"`
	Status                  string   `json:"status"`
	UsageKwh                *string  `json:"usageKwh"`
	ExpectedDurationSeconds int64    `json:"expectedDurationSeconds"`
	ObservedDurationSeconds int64    `json:"observedDurationSeconds"`
	Coverage                *string  `json:"coverage"`
	Warnings                []string `json:"warnings"`
}

func newHistoricalReportResponse(result apphistoricalreport.Report) historicalReportResponse {
	points := make([]historicalReportPointResponse, 0, len(result.MeasurementPoints))
	for _, point := range result.MeasurementPoints {
		points = append(points, historicalReportPointResponse{
			MeasurementPointID: point.MeasurementPointID, Status: string(point.Status), UsageKwh: point.UsageKwh,
			ExpectedDurationSeconds: point.ExpectedDurationSeconds, ObservedDurationSeconds: point.ObservedDurationSeconds,
			Coverage: point.Coverage, Warnings: point.Warnings,
		})
	}
	return historicalReportResponse{
		Month: result.Month, Timezone: result.Timezone,
		Period:            historicalReportPeriodResponse{Start: reportTimestamp(result.Period.Start), End: reportTimestamp(result.Period.End), Cutoff: reportTimestamp(result.Period.Cutoff), Snapshot: reportTimestamp(result.Period.Snapshot)},
		Summary:           historicalReportFactsResponse{Status: string(result.Summary.Status), UsageKwh: result.Summary.UsageKwh, ExpectedDurationSeconds: result.Summary.ExpectedDurationSeconds, ObservedDurationSeconds: result.Summary.ObservedDurationSeconds, Coverage: result.Summary.Coverage},
		MeasurementPoints: points, Warnings: result.Warnings,
	}
}

func reportTimestamp(value time.Time) string {
	return value.UTC().Format(time.RFC3339)
}

func RegisterHistoricalReportRoute(router gin.IRouter, authenticator AccessTokenAuthenticator, query HistoricalReportQuery) {
	if router != nil {
		router.GET("/api/v1/shops/:shopId/reports/energy", AuthenticationMiddleware(authenticator), NewHistoricalReportHandler(query))
	}
}
