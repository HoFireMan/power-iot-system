package httpadapter

import (
	"context"
	"net/http"
	"strconv"

	applicationlifecycle "power-iot-backend/internal/application/devicelifecycle"
	"power-iot-backend/internal/core/domain"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// DeviceLifecycleService is the HTTP capability for the three explicit V1
// lifecycle commands. It is separate from Admin Binding so lifecycle changes
// cannot accidentally append to the five-action audit history.
type DeviceLifecycleService interface {
	Disable(context.Context, applicationlifecycle.Command) (applicationlifecycle.Result, error)
	Enable(context.Context, applicationlifecycle.Command) (applicationlifecycle.Result, error)
	Retire(context.Context, applicationlifecycle.Command) (applicationlifecycle.Result, error)
}

func RegisterDeviceLifecycleRoutes(router gin.IRouter, authenticator AccessTokenAuthenticator, service DeviceLifecycleService, db *gorm.DB) {
	if router == nil || service == nil {
		return
	}
	protected := func(action domain.BindingAction, fn func(context.Context, applicationlifecycle.Command) (applicationlifecycle.Result, error)) gin.HandlerFunc {
		return func(c *gin.Context) {
			userID, ok := adminIdentity(c)
			if !ok {
				WritePublicError(c, ErrUnauthorized)
				return
			}
			if _, err := requireAdminCapability(c, db); err != nil {
				WritePublicError(c, err)
				return
			}
			deviceID, ok := lifecycleDeviceID(c)
			if !ok || !requireIdempotency(c) {
				WritePublicError(c, ErrValidation)
				return
			}
			var body reasonRequest
			if !decodeStrict(c, &body) {
				WritePublicError(c, ErrValidation)
				return
			}
			shopID, err := lifecycleShopID(c, db, deviceID, userID)
			if err != nil {
				WritePublicError(c, err)
				return
			}
			actor, err := actorFor(c, db, action, []uint{shopID}, []uint{deviceID})
			if err != nil {
				WritePublicError(c, err)
				return
			}
			result, err := fn(c.Request.Context(), applicationlifecycle.Command{DeviceID: deviceID, Reason: body.Reason, RequestIdentity: idempotencyKey(c), Actor: actor})
			if err != nil {
				WritePublicError(c, err)
				return
			}
			c.JSON(http.StatusOK, gin.H{"operationId": result.OperationID.String(), "action": string(result.Action), "deviceId": strconv.FormatUint(uint64(result.DeviceID), 10), "lifecycleStatus": string(result.LifecycleStatus)})
		}
	}
	if authenticator == nil {
		return
	}
	// Keep exactly one explicit route per supported lifecycle command.
	router.POST("/api/v1/admin/devices/:deviceId/disable", AuthenticationMiddleware(authenticator), protected(domain.ActionDisableDevice, func(ctx context.Context, cmd applicationlifecycle.Command) (applicationlifecycle.Result, error) {
		return service.Disable(ctx, cmd)
	}))
	router.POST("/api/v1/admin/devices/:deviceId/enable", AuthenticationMiddleware(authenticator), protected(domain.ActionEnableDevice, func(ctx context.Context, cmd applicationlifecycle.Command) (applicationlifecycle.Result, error) {
		return service.Enable(ctx, cmd)
	}))
	router.POST("/api/v1/admin/devices/:deviceId/retire", AuthenticationMiddleware(authenticator), protected(domain.ActionRetireDevice, func(ctx context.Context, cmd applicationlifecycle.Command) (applicationlifecycle.Result, error) {
		return service.Retire(ctx, cmd)
	}))
}

func lifecycleDeviceID(c *gin.Context) (uint, bool) {
	raw := c.Param("deviceId")
	id, err := strconv.ParseUint(raw, 10, 64)
	return uint(id), err == nil && id > 0 && uint64(uint(id)) == id && raw == strconv.FormatUint(id, 10)
}

func lifecycleShopID(c *gin.Context, db *gorm.DB, deviceID, userID uint) (uint, error) {
	if db == nil {
		return 0, domain.NewDomainError(domain.ErrPersistenceFailure, "authorization unavailable")
	}
	var shopID uint
	query := db.WithContext(c).Raw(`SELECT p.shop_id FROM device_assignments a JOIN measurement_points p ON p.id = a.measurement_point_id WHERE a.device_id = ? AND a.valid_to IS NULL LIMIT 1`, deviceID).Scan(&shopID)
	if query.Error != nil {
		return 0, domain.NewDomainError(domain.ErrPersistenceFailure, "device scope unavailable")
	}
	if query.RowsAffected == 1 && shopID != 0 {
		return shopID, nil
	}
	// Unassigned inventory has no Device->MP->Shop edge. Select an active Shop
	// through the authoritative Device owner Client and the authenticated user's
	// live membership. Device.ShopID is deliberately not consulted.
	query = db.WithContext(c).Raw(`SELECT s.id
		FROM devices d
		JOIN clients client ON client.id = d.inventory_owner_client_id
		JOIN shops s ON s.client_id = client.id AND s.is_active = TRUE
		JOIN user_shop_relations relation ON relation.shop_id = s.id AND relation.user_id = ?
		WHERE d.id = ?
		ORDER BY s.id LIMIT 1`, userID, deviceID).Scan(&shopID)
	if query.Error != nil {
		return 0, domain.NewDomainError(domain.ErrPersistenceFailure, "device scope unavailable")
	}
	if query.RowsAffected == 1 && shopID != 0 {
		return shopID, nil
	}
	// A caller may supply a Shop scope hint when no authorized owner Shop can be
	// selected automatically; actorFor still proves the relation and owner.
	raw := c.Query("shopId")
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 || uint64(uint(id)) != id || raw != strconv.FormatUint(id, 10) {
		return 0, domain.NewDomainError(domain.ErrSiteScopeDenied, "an authorized Shop scope is required for unassigned inventory")
	}
	return uint(id), nil
}
