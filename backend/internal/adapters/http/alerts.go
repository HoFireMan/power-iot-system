package httpadapter

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	applicationalerts "power-iot-backend/internal/application/alerts"
)

type AlertSettingsService interface {
	GetSettings(context.Context, uint, uint, string) (applicationalerts.Settings, error)
	UpdateSettings(context.Context, uint, uint, string, applicationalerts.SettingsUpdate) error
}
type AlertHistoryService interface {
	History(context.Context, uint, uint, string, int, string) (applicationalerts.HistoryPage, error)
}

type alertSettingsRequest struct {
	IsEnabled       bool    `json:"isEnabled"`
	QuietHoursStart string  `json:"quietHoursStart"`
	QuietHoursEnd   string  `json:"quietHoursEnd"`
	PowerThresholdW float64 `json:"powerThresholdW"`
}

func alertSettingsJSON(s applicationalerts.Settings) gin.H {
	return gin.H{"measurementPointId": s.MeasurementPointID, "isEnabled": s.IsEnabled, "quietHoursStart": s.QuietHoursStart, "quietHoursEnd": s.QuietHoursEnd, "powerThresholdW": s.PowerThresholdW, "updatedAt": s.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")}
}

func NewAlertSettingsHandler(service AlertSettingsService, write bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		identity, ok := IdentityFromGin(c)
		if !ok || service == nil {
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
		pointID := c.Param("measurementPointRef")
		if pointID == "" || strings.TrimSpace(pointID) != pointID {
			WritePublicError(c, ErrValidation)
			return
		}
		if write {
			var body alertSettingsRequest
			if !decodeStrict(c, &body) {
				WritePublicError(c, ErrValidation)
				return
			}
			if err := service.UpdateSettings(c.Request.Context(), userID, shopID, pointID, applicationalerts.SettingsUpdate{IsEnabled: body.IsEnabled, QuietHoursStart: body.QuietHoursStart, QuietHoursEnd: body.QuietHoursEnd, PowerThresholdW: body.PowerThresholdW}); err != nil {
				WritePublicError(c, err)
				return
			}
		}
		settings, err := service.GetSettings(c.Request.Context(), userID, shopID, pointID)
		if err != nil {
			WritePublicError(c, err)
			return
		}
		c.JSON(http.StatusOK, alertSettingsJSON(settings))
	}
}
func RegisterAlertSettingsRoutes(router gin.IRouter, authenticator AccessTokenAuthenticator, service AlertSettingsService) {
	if router == nil {
		return
	}
	path := "/api/v1/shops/:shopId/measurement-points/:measurementPointRef/alert-settings"
	router.GET(path, AuthenticationMiddleware(authenticator), NewAlertSettingsHandler(service, false))
	router.PUT(path, AuthenticationMiddleware(authenticator), NewAlertSettingsHandler(service, true))
}

type alertHistoryResponse struct {
	Items      []gin.H `json:"items"`
	NextCursor string  `json:"nextCursor,omitempty"`
}

func NewAlertHistoryHandler(service AlertHistoryService) gin.HandlerFunc {
	return func(c *gin.Context) {
		identity, ok := IdentityFromGin(c)
		if !ok || service == nil {
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
		limit := 50
		if raw := c.Query("limit"); raw != "" {
			parsed, e := strconv.Atoi(raw)
			if e != nil || parsed < 1 || parsed > 100 {
				WritePublicError(c, ErrValidation)
				return
			}
			limit = parsed
		}
		page, err := service.History(c.Request.Context(), userID, shopID, c.Query("measurementPointRef"), limit, c.Query("cursor"))
		if err != nil {
			if errors.Is(err, applicationalerts.ErrInvalidCursor) {
				WritePublicError(c, ErrValidation)
			} else {
				WritePublicError(c, err)
			}
			return
		}
		items := make([]gin.H, 0, len(page.Items))
		for _, item := range page.Items {
			device := gin.H{"deviceId": item.DeviceID, "name": item.DeviceName}
			if item.SerialNumber != nil {
				device["serialNumber"] = *item.SerialNumber
			}
			items = append(items, gin.H{"id": item.ID, "type": item.Type, "message": item.Message, "createdAt": item.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"), "measurementPoint": gin.H{"id": item.MeasurementPointID, "name": item.MeasurementPointName}, "device": device, "snapshot": gin.H{"voltage": item.Voltage, "current": item.Current, "power": item.Power}})
		}
		c.JSON(http.StatusOK, alertHistoryResponse{Items: items, NextCursor: page.NextCursor})
	}
}
func RegisterAlertHistoryRoute(router gin.IRouter, authenticator AccessTokenAuthenticator, service AlertHistoryService) {
	if router != nil {
		router.GET("/api/v1/shops/:shopId/alerts", AuthenticationMiddleware(authenticator), NewAlertHistoryHandler(service))
	}
}
