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
	GetSettings(context.Context, uint, string) (applicationalerts.Settings, error)
	UpdateSettings(context.Context, uint, string, applicationalerts.SettingsUpdate) error
}
type AlertHistoryService interface {
	History(context.Context, uint, uint, int, string) (applicationalerts.HistoryPage, error)
}

type alertSettingsRequest struct {
	DailyLimitKwh     *float64 `json:"dailyLimitKwh"`
	MonthlyLimitKwh   *float64 `json:"monthlyLimitKwh"`
	NonUsageStartTime string   `json:"nonUsageStartTime"`
	NonUsageEndTime   string   `json:"nonUsageEndTime"`
	IsEnabled         bool     `json:"isEnabled"`
}

func alertSettingsJSON(s applicationalerts.Settings) gin.H {
	return gin.H{"measurementPointId": s.MeasurementPointID, "dailyLimitKwh": s.DailyLimitKwh, "monthlyLimitKwh": s.MonthlyLimitKwh, "nonUsageStartTime": s.NonUsageStartTime, "nonUsageEndTime": s.NonUsageEndTime, "isEnabled": s.IsEnabled, "updatedAt": s.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")}
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
		pointID := c.Param("measurementPointId")
		if pointID == "" || strings.TrimSpace(pointID) != pointID {
			WritePublicError(c, ErrValidation)
			return
		}
		if !write {
			settings, err := service.GetSettings(c.Request.Context(), userID, pointID)
			if err != nil {
				WritePublicError(c, err)
				return
			}
			c.JSON(http.StatusOK, alertSettingsJSON(settings))
			return
		}
		var body alertSettingsRequest
		if !decodeStrict(c, &body) {
			WritePublicError(c, ErrValidation)
			return
		}
		if err := service.UpdateSettings(c.Request.Context(), userID, pointID, applicationalerts.SettingsUpdate{DailyLimitKwh: body.DailyLimitKwh, MonthlyLimitKwh: body.MonthlyLimitKwh, NonUsageStartTime: body.NonUsageStartTime, NonUsageEndTime: body.NonUsageEndTime, IsEnabled: body.IsEnabled}); err != nil {
			WritePublicError(c, err)
			return
		}
		settings, err := service.GetSettings(c.Request.Context(), userID, pointID)
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
	path := "/api/v1/admin/measurement-points/:measurementPointId/alert-settings"
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
		limit := 20
		if raw := c.Query("limit"); raw != "" {
			parsed, e := strconv.Atoi(raw)
			if e != nil || parsed < 1 || parsed > 100 {
				WritePublicError(c, ErrValidation)
				return
			}
			limit = parsed
		}
		cursor := c.Query("cursor")
		page, err := service.History(c.Request.Context(), userID, shopID, limit, cursor)
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
			items = append(items, gin.H{"id": item.ID, "measurementPointId": item.MeasurementPointID, "measurementPointName": item.MeasurementPointName, "type": item.Type, "message": item.Message, "voltage": item.Voltage, "current": item.Current, "power": item.Power, "isRead": item.IsRead, "recordedAt": item.RecordedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")})
		}
		c.JSON(http.StatusOK, alertHistoryResponse{Items: items, NextCursor: page.NextCursor})
	}
}
func RegisterAlertHistoryRoute(router gin.IRouter, authenticator AccessTokenAuthenticator, service AlertHistoryService) {
	if router == nil {
		return
	}
	router.GET("/api/v1/shops/:shopId/alerts", AuthenticationMiddleware(authenticator), NewAlertHistoryHandler(service))
}
