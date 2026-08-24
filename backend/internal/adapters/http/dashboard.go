package httpadapter

import (
	"context"
	"net/http"
	"strconv"
	"time"

	applicationdashboard "power-iot-backend/internal/application/dashboard"

	"github.com/gin-gonic/gin"
)

type DashboardQuery interface {
	GetDashboard(context.Context, uint, uint) (applicationdashboard.Dashboard, error)
}

type DashboardHandlerConfig struct{ Query DashboardQuery }

func NewDashboardHandler(config DashboardHandlerConfig) gin.HandlerFunc {
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
		result, err := config.Query.GetDashboard(c.Request.Context(), userID, shopID)
		if err != nil {
			WritePublicError(c, err)
			return
		}
		devices := make([]dashboardDeviceResponse, 0, len(result.Devices))
		for _, device := range result.Devices {
			devices = append(devices, dashboardDeviceResponse{MeasurementPointRef: device.MeasurementPointRef, Name: device.Name, IsOnline: device.IsOnline, LastSeen: device.LastSeen})
		}
		c.JSON(http.StatusOK, dashboardResponse{
			Shop:    dashboardShopResponse{ID: result.Shop.ID, Code: result.Shop.Code, Name: result.Shop.Name},
			Devices: devices, CurrentPowerW: result.CurrentPowerW, DailyKwh: result.DailyKwh,
			MonthlyKwh: result.MonthlyKwh, DailyKg: result.DailyKg, MonthlyKg: result.MonthlyKg,
			GeneratedAt: result.GeneratedAt,
		})
	}
}

type dashboardShopResponse struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

type dashboardDeviceResponse struct {
	MeasurementPointRef string     `json:"measurementPointRef"`
	Name                string     `json:"name"`
	IsOnline            bool       `json:"isOnline"`
	LastSeen            *time.Time `json:"lastSeen"`
}

type dashboardResponse struct {
	Shop          dashboardShopResponse     `json:"shop"`
	Devices       []dashboardDeviceResponse `json:"devices"`
	CurrentPowerW *float64                  `json:"currentPowerW"`
	DailyKwh      *float64                  `json:"dailyKwh"`
	MonthlyKwh    *float64                  `json:"monthlyKwh"`
	DailyKg       *float64                  `json:"dailyKg"`
	MonthlyKg     *float64                  `json:"monthlyKg"`
	GeneratedAt   time.Time                 `json:"generatedAt"`
}

func parseExternalID(value string) (uint, error) {
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || parsed == 0 || uint64(uint(parsed)) != parsed {
		return 0, ErrValidation
	}
	return uint(parsed), nil
}

func RegisterDashboardRoute(router gin.IRouter, authenticator AccessTokenAuthenticator, query DashboardQuery) {
	if router != nil {
		router.GET("/api/v1/shops/:shopId/dashboard", AuthenticationMiddleware(authenticator), NewDashboardHandler(DashboardHandlerConfig{Query: query}))
	}
}
