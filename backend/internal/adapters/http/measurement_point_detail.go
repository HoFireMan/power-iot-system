package httpadapter

import (
	"context"
	"net/http"
	"time"

	applicationmeasurementpointdetail "power-iot-backend/internal/application/measurementpointdetail"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type MeasurementPointDetailQuery interface {
	GetMeasurementPointDetail(context.Context, uint, uint, uuid.UUID) (applicationmeasurementpointdetail.Detail, error)
}

type MeasurementPointDetailHandlerConfig struct{ Query MeasurementPointDetailQuery }

func NewMeasurementPointDetailHandler(config MeasurementPointDetailHandlerConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		identity, ok := IdentityFromGin(c)
		if !ok || config.Query == nil {
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
		pointID, err := uuid.Parse(c.Param("measurementPointRef"))
		if err != nil || pointID == uuid.Nil {
			WritePublicError(c, ErrValidation)
			return
		}
		result, err := config.Query.GetMeasurementPointDetail(c.Request.Context(), userID, shopID, pointID)
		if err != nil {
			WritePublicError(c, err)
			return
		}
		response := measurementPointDetailResponse{
			Shop:              measurementPointDetailShopResponse{Code: result.Point.Shop.Code, Name: result.Point.Shop.Name},
			MeasurementPoint:  measurementPointResponse{Name: result.Point.Name, Status: result.Status},
			CurrentPower:      currentPowerResponse{Watts: result.CurrentPowerW, LastUpdatedAt: result.CurrentPowerSeenAt},
			TodayEnergy:       energyResponse{Kwh: result.Today.Kwh, CompleteThrough: result.Today.CompleteThrough},
			MonthEnergy:       energyResponse{Kwh: result.Month.Kwh, CompleteThrough: result.Month.CompleteThrough},
			CurrentDevice:     detailCurrentDevice(result.CurrentDevice),
			AssignmentHistory: detailAssignmentHistory(result.AssignmentHistory),
		}
		if result.TechnicalInfo != nil {
			response.TechnicalInfo = &technicalInfoResponse{MeasurementPointID: result.TechnicalInfo.MeasurementPointID, DeviceID: result.TechnicalInfo.DeviceID}
		}
		c.JSON(http.StatusOK, response)
	}
}

type measurementPointDetailShopResponse struct {
	Code string `json:"code"`
	Name string `json:"name"`
}
type measurementPointResponse struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}
type currentPowerResponse struct {
	Watts         *float64   `json:"watts"`
	LastUpdatedAt *time.Time `json:"lastUpdatedAt"`
}
type energyResponse struct {
	Kwh             *float64   `json:"kwh"`
	CompleteThrough *time.Time `json:"completeThrough"`
}
type detailCurrentDeviceResponse struct {
	DisplayName string     `json:"displayName"`
	Mac         string     `json:"mac"`
	LastSeen    *time.Time `json:"lastSeen"`
}
type detailAssignmentResponse struct {
	DisplayName string     `json:"displayName"`
	Mac         string     `json:"mac"`
	ValidFrom   time.Time  `json:"validFrom"`
	ValidTo     *time.Time `json:"validTo"`
}
type technicalInfoResponse struct {
	MeasurementPointID string  `json:"measurementPointId"`
	DeviceID           *string `json:"deviceId"`
}
type measurementPointDetailResponse struct {
	Shop              measurementPointDetailShopResponse `json:"shop"`
	MeasurementPoint  measurementPointResponse           `json:"measurementPoint"`
	CurrentPower      currentPowerResponse               `json:"currentPower"`
	TodayEnergy       energyResponse                     `json:"todayEnergy"`
	MonthEnergy       energyResponse                     `json:"monthEnergy"`
	CurrentDevice     *detailCurrentDeviceResponse       `json:"currentDevice"`
	AssignmentHistory []detailAssignmentResponse         `json:"assignmentHistory"`
	TechnicalInfo     *technicalInfoResponse             `json:"technicalInfo,omitempty"`
}

func detailCurrentDevice(device *applicationmeasurementpointdetail.Device) *detailCurrentDeviceResponse {
	if device == nil {
		return nil
	}
	return &detailCurrentDeviceResponse{DisplayName: device.DisplayName, Mac: device.Mac, LastSeen: device.LastSeen}
}
func detailAssignmentHistory(rows []applicationmeasurementpointdetail.Assignment) []detailAssignmentResponse {
	out := make([]detailAssignmentResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, detailAssignmentResponse{DisplayName: row.DisplayName, Mac: row.Mac, ValidFrom: row.ValidFrom, ValidTo: row.ValidTo})
	}
	return out
}

func RegisterMeasurementPointDetailRoute(router gin.IRouter, authenticator AccessTokenAuthenticator, query MeasurementPointDetailQuery) {
	if router != nil {
		router.GET("/api/v1/shops/:shopId/measurement-points/:measurementPointRef", AuthenticationMiddleware(authenticator), NewMeasurementPointDetailHandler(MeasurementPointDetailHandlerConfig{Query: query}))
	}
}
