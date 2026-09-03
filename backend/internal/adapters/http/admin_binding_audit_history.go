package httpadapter

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	applicationadminaudit "power-iot-backend/internal/application/adminbindingaudit"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type AdminBindingAuditHistoryService interface {
	History(context.Context, uint, uint, string, string, string, int, string, uuid.UUID) (applicationadminaudit.HistoryPage, error)
}

// NewAdminBindingAuditHistoryHandler is a read-only route. Capability checks
// and the session lock are both repeated by the persistence transaction; the
// early admin check only avoids parsing resource filters for non-admin users.
func NewAdminBindingAuditHistoryHandler(service AdminBindingAuditHistoryService, db *gorm.DB) gin.HandlerFunc {
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
		if db != nil {
			if _, err := requireAdminCapability(c, db); err != nil {
				WritePublicError(c, err)
				return
			}
		}
		shopID, err := parseExternalID(c.Param("shopId"))
		if err != nil {
			WritePublicError(c, ErrValidation)
			return
		}
		sessionID, err := uuid.Parse(identity.SessionID)
		if err != nil || sessionID == uuid.Nil {
			WritePublicError(c, ErrUnauthorized)
			return
		}
		limit := 50
		if raw := c.Query("limit"); raw != "" {
			limit, err = strconv.Atoi(raw)
			if err != nil || limit < 1 || limit > 100 {
				WritePublicError(c, ErrValidation)
				return
			}
		}
		page, err := service.History(c.Request.Context(), userID, shopID, c.Query("action"), c.Query("measurementPointId"), c.Query("deviceId"), limit, c.Query("cursor"), sessionID)
		if err != nil {
			WritePublicError(c, err)
			return
		}
		items := make([]gin.H, 0, len(page.Items))
		for _, item := range page.Items {
			items = append(items, auditHistoryJSON(item))
		}
		response := gin.H{"items": items}
		if page.NextCursor != "" {
			response["nextCursor"] = page.NextCursor
		}
		c.JSON(http.StatusOK, response)
	}
}

func auditHistoryJSON(item applicationadminaudit.Audit) gin.H {
	id := func(value *uuid.UUID) interface{} {
		if value == nil {
			return nil
		}
		return value.String()
	}
	currentName := func(name string) interface{} {
		if strings.TrimSpace(name) == "" {
			return nil
		}
		return name
	}
	point := func(pointID *uuid.UUID, name string) interface{} {
		if pointID == nil {
			return nil
		}
		return gin.H{"id": pointID.String(), "currentDisplayName": currentName(name)}
	}
	device := interface{}(nil)
	if item.DeviceID != nil {
		device = gin.H{
			"id":                 itemID(item.DeviceID),
			"serialNumber":       valueString(item.DeviceSerialNumber),
			"mac":                valueString(item.DeviceMAC),
			"currentDisplayName": currentName(item.DeviceName),
		}
	}
	itemJSON := gin.H{
		"id":          item.ID.String(),
		"operationId": item.OperationID.String(),
		"action":      item.Action,
		"occurredAt":  item.OccurredAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		"effectiveAt": nil,
		"reason":      valueString(item.Reason),
		"actor": gin.H{
			"id":                 itemID(&item.ActorID),
			"currentDisplayName": currentName(item.ActorName),
		},
		"measurementPoint":    point(item.MeasurementPointID, item.MeasurementPointName),
		"device":              device,
		"oldMeasurementPoint": point(item.OldMeasurementPointID, item.OldMeasurementPointName),
		"newMeasurementPoint": point(item.NewMeasurementPointID, item.NewMeasurementPointName),
		"oldAssignmentId":     id(item.OldAssignmentID),
		"newAssignmentId":     id(item.NewAssignmentID),
	}
	if item.EffectiveAt != nil {
		itemJSON["effectiveAt"] = item.EffectiveAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	}
	return itemJSON
}
func itemID(value *uint) string {
	if value == nil {
		return ""
	}
	return strconv.FormatUint(uint64(*value), 10)
}
func valueString(value *string) interface{} {
	if value == nil {
		return nil
	}
	return *value
}

// RegisterAdminBindingAuditHistoryRoute is separate from the legacy overview
// registration so existing composition tests can continue to inventory the
// original six routes independently.
func RegisterAdminBindingAuditHistoryRoute(router gin.IRouter, authenticator AccessTokenAuthenticator, service AdminBindingAuditHistoryService, db *gorm.DB) {
	if router == nil {
		return
	}
	router.GET("/api/v1/shops/:shopId/admin/binding-audits", AuthenticationMiddleware(authenticator), NewAdminBindingAuditHistoryHandler(service, db))
}

var _ AdminBindingAuditHistoryService = (*applicationadminaudit.Service)(nil)
