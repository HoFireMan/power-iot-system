package httpadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"power-iot-backend/internal/adapters/persistence"
	applicationadmin "power-iot-backend/internal/application/adminbinding"
	"power-iot-backend/internal/core/domain"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// AdminBindingHandlerConfig is the composition seam for the bounded HTTP adapter.
type AdminBindingHandlerConfig struct {
	Executor *applicationadmin.Executor
	DB       *gorm.DB
	Overview AdminBindingOverviewQuery
}
type AdminBindingOverviewQuery interface {
	FindAdminBindingOverview(context.Context, uint, uint) (persistence.AdminBindingOverview, error)
}

// AdminBindingOverviewSessionQuery is the HTTP-safe overview capability. The
// optional seam preserves the legacy narrow query for non-HTTP callers while
// preventing an HTTP request from bypassing session revalidation.
type AdminBindingOverviewSessionQuery interface {
	FindAdminBindingOverviewForSession(context.Context, uint, uint, uuid.UUID) (persistence.AdminBindingOverview, error)
}

type createMPRequest struct {
	ShopID uint   `json:"shopId"`
	Name   string `json:"name"`
}
type deviceRefRequest struct {
	DeviceID     *uint   `json:"deviceId"`
	SerialNumber *string `json:"serialNumber"`
	MAC          *string `json:"mac"`
}
type bindRequest struct {
	DeviceRef          deviceRefRequest `json:"deviceRef"`
	MeasurementPointID string           `json:"measurementPointId"`
	Reason             string           `json:"reason"`
}
type replaceRequest struct {
	ReplacementDeviceRef deviceRefRequest `json:"replacementDeviceRef"`
	Reason               string           `json:"reason"`
}
type relocateRequest struct {
	TargetMeasurementPointID string `json:"targetMeasurementPointId"`
	Reason                   string `json:"reason"`
}
type reasonRequest struct {
	Reason string `json:"reason"`
}

func NewAdminBindingHandler(config AdminBindingHandlerConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		if config.Executor == nil {
			WritePublicError(c, domain.NewDomainError(domain.ErrPersistenceFailure, "executor unavailable"))
			return
		}
		_, ok := adminIdentity(c)
		if !ok {
			WritePublicError(c, ErrUnauthorized)
			return
		}
		// Establish the admin capability before any mutation-specific lookup so
		// non-admin callers cannot distinguish valid from invalid resources.
		if _, err := requireAdminCapability(c, config.DB); err != nil {
			WritePublicError(c, err)
			return
		}
		switch domain.BindingAction(c.GetString("admin_binding_action")) {
		case domain.ActionCreateMeasurementPoint:
			createMPHandler(config)(c)
		case domain.ActionBind:
			bindHandler(config)(c)
		case domain.ActionReplace:
			replaceHandler(config)(c)
		case domain.ActionRelocate:
			relocateHandler(config)(c)
		case domain.ActionUnbind:
			unbindHandler(config)(c)
		default:
			WritePublicError(c, ErrValidation)
		}
	}
}

func RegisterAdminBindingRoutes(router gin.IRouter, authenticator AccessTokenAuthenticator, config AdminBindingHandlerConfig) {
	if router == nil {
		return
	}
	protected := func(action domain.BindingAction, handler gin.HandlerFunc) gin.HandlerFunc {
		return func(c *gin.Context) { c.Set("admin_binding_action", string(action)); handler(c) }
	}
	handler := NewAdminBindingHandler(config)
	router.POST("/api/v1/admin/measurement-points", AuthenticationMiddleware(authenticator), protected(domain.ActionCreateMeasurementPoint, handler))
	router.POST("/api/v1/admin/device-bindings", AuthenticationMiddleware(authenticator), protected(domain.ActionBind, handler))
	router.POST("/api/v1/admin/device-bindings/:assignmentId/replace", AuthenticationMiddleware(authenticator), protected(domain.ActionReplace, handler))
	router.POST("/api/v1/admin/device-bindings/:assignmentId/relocate", AuthenticationMiddleware(authenticator), protected(domain.ActionRelocate, handler))
	router.POST("/api/v1/admin/device-bindings/:assignmentId/unbind", AuthenticationMiddleware(authenticator), protected(domain.ActionUnbind, handler))
	query := config.Overview
	if query == nil && config.DB != nil {
		query = persistence.NewAdminBindingOverviewRepository(config.DB)
	}
	router.GET("/api/v1/admin/device-bindings", AuthenticationMiddleware(authenticator), overviewHandler(query, config.DB))
}

func createMPHandler(config AdminBindingHandlerConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req createMPRequest
		if !requireIdempotency(c) || !decodeStrict(c, &req) || req.ShopID == 0 || strings.TrimSpace(req.Name) == "" {
			WritePublicError(c, ErrValidation)
			return
		}
		actor, err := actorFor(c, config.DB, domain.ActionCreateMeasurementPoint, []uint{req.ShopID}, nil)
		if err != nil {
			WritePublicError(c, err)
			return
		}
		result, err := config.Executor.CreateMeasurementPoint(c, domain.CreateMeasurementPointCommand{ShopID: req.ShopID, Name: strings.TrimSpace(req.Name), RequestIdentity: idempotencyKey(c), Actor: actor})
		writeResult(c, result, err)
	}
}
func bindHandler(config AdminBindingHandlerConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req bindRequest
		if !requireIdempotency(c) || !decodeStrict(c, &req) || !validUUID(req.MeasurementPointID) {
			WritePublicError(c, ErrValidation)
			return
		}
		pointID, _ := uuid.Parse(req.MeasurementPointID)
		ref := toDomainRef(req.DeviceRef)
		deviceIDs, err := resolveHTTPDeviceRef(config.DB, c, ref)
		if err != nil {
			WritePublicError(c, err)
			return
		}
		shopID, err := lookupBindShop(config.DB, c, pointID)
		if err != nil {
			WritePublicError(c, err)
			return
		}
		actor, err := actorFor(c, config.DB, domain.ActionBind, []uint{shopID}, deviceIDs)
		if err != nil {
			WritePublicError(c, err)
			return
		}
		result, err := config.Executor.BindDevice(c, domain.BindDeviceCommand{DeviceRef: ref, MeasurementPointID: pointID, Reason: req.Reason, RequestIdentity: idempotencyKey(c), Actor: actor})
		writeResult(c, result, err)
	}
}
func replaceHandler(config AdminBindingHandlerConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		assignment, ok := parsePathUUID(c)
		if !ok || !requireIdempotency(c) {
			WritePublicError(c, ErrValidation)
			return
		}
		var req replaceRequest
		if !decodeStrict(c, &req) {
			WritePublicError(c, ErrValidation)
			return
		}
		ref := toDomainRef(req.ReplacementDeviceRef)
		deviceIDs, err := resolveHTTPDeviceRef(config.DB, c, ref)
		if err != nil {
			WritePublicError(c, err)
			return
		}
		shop, currentDevice, err := lookupAssignmentTargets(config.DB, c, assignment)
		if err != nil {
			WritePublicError(c, err)
			return
		}
		deviceIDs = append([]uint{currentDevice}, deviceIDs...)
		actor, err := actorFor(c, config.DB, domain.ActionReplace, []uint{shop}, deviceIDs)
		if err != nil {
			WritePublicError(c, err)
			return
		}
		result, err := config.Executor.ReplaceDevice(c, domain.ReplaceDeviceCommand{CurrentAssignmentID: assignment, ReplacementDeviceRef: ref, Reason: req.Reason, RequestIdentity: idempotencyKey(c), Actor: actor})
		writeResult(c, result, err)
	}
}
func relocateHandler(config AdminBindingHandlerConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		assignment, ok := parsePathUUID(c)
		if !ok || !requireIdempotency(c) {
			WritePublicError(c, ErrValidation)
			return
		}
		var req relocateRequest
		if !decodeStrict(c, &req) || !validUUID(req.TargetMeasurementPointID) {
			WritePublicError(c, ErrValidation)
			return
		}
		target, _ := uuid.Parse(req.TargetMeasurementPointID)
		shops, device, err := lookupRelocateTargets(config.DB, c, assignment, target)
		if err != nil {
			WritePublicError(c, err)
			return
		}
		actor, err := actorFor(c, config.DB, domain.ActionRelocate, shops, []uint{device})
		if err != nil {
			WritePublicError(c, err)
			return
		}
		result, err := config.Executor.RelocateDevice(c, domain.RelocateDeviceCommand{CurrentAssignmentID: assignment, TargetMeasurementPointID: target, Reason: req.Reason, RequestIdentity: idempotencyKey(c), Actor: actor})
		writeResult(c, result, err)
	}
}
func unbindHandler(config AdminBindingHandlerConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		assignment, ok := parsePathUUID(c)
		if !ok || !requireIdempotency(c) {
			WritePublicError(c, ErrValidation)
			return
		}
		var req reasonRequest
		if !decodeStrict(c, &req) {
			WritePublicError(c, ErrValidation)
			return
		}
		shop, device, err := lookupUnbindTargets(config.DB, c, assignment)
		if err != nil {
			WritePublicError(c, err)
			return
		}
		actor, err := actorFor(c, config.DB, domain.ActionUnbind, []uint{shop}, []uint{device})
		if err != nil {
			WritePublicError(c, err)
			return
		}
		result, err := config.Executor.UnbindDevice(c, domain.UnbindDeviceCommand{CurrentAssignmentID: assignment, Reason: req.Reason, RequestIdentity: idempotencyKey(c), Actor: actor})
		writeResult(c, result, err)
	}
}

func overviewHandler(query AdminBindingOverviewQuery, db ...*gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		identity, ok := IdentityFromGin(c)
		if !ok {
			WritePublicError(c, ErrUnauthorized)
			return
		}
		user, ok := adminIdentity(c)
		if !ok {
			WritePublicError(c, ErrUnauthorized)
			return
		}
		// Production callers provide the DB so the admin capability is
		// established before parsing or querying the requested Shop. The
		// no-DB form remains available for narrow unit-test query stubs.
		if len(db) > 0 && db[0] != nil {
			if _, err := requireAdminCapability(c, db[0]); err != nil {
				WritePublicError(c, err)
				return
			}
		}
		sessionID, err := uuid.Parse(identity.SessionID)
		if err != nil || sessionID == uuid.Nil {
			// AuthenticationMiddleware normally rejects this, but keep the
			// handler fail closed if it is called through another seam.
			WritePublicError(c, ErrUnauthorized)
			return
		}
		raw := c.Query("shopId")
		id64, e := strconv.ParseUint(raw, 10, 64)
		if e != nil || id64 == 0 || uint64(uint(id64)) != id64 {
			WritePublicError(c, ErrValidation)
			return
		}
		if query == nil {
			WritePublicError(c, domain.NewDomainError(domain.ErrPersistenceFailure, "overview unavailable"))
			return
		}
		sessionQuery, ok := query.(AdminBindingOverviewSessionQuery)
		if !ok {
			// The legacy query seam is deliberately not safe for HTTP: it has
			// no authenticated session and cannot close the logout race.
			WritePublicError(c, domain.NewDomainError(domain.ErrPersistenceFailure, "overview authorization unavailable"))
			return
		}
		view, err := sessionQuery.FindAdminBindingOverviewForSession(c, user, uint(id64), sessionID)
		if err != nil {
			WritePublicError(c, err)
			return
		}
		c.JSON(http.StatusOK, overviewJSON(view))
	}
}
func overviewJSON(v persistence.AdminBindingOverview) gin.H {
	points := make([]gin.H, 0, len(v.MeasurementPoints))
	for _, p := range v.MeasurementPoints {
		points = append(points, gin.H{"id": p.ID.String(), "shopId": strconv.FormatUint(uint64(p.ShopID), 10), "name": p.Name})
	}
	devices := make([]gin.H, 0, len(v.Devices))
	for _, d := range v.Devices {
		devices = append(devices, gin.H{"id": strconv.FormatUint(uint64(d.ID), 10), "name": d.Name, "serialNumber": d.SerialNumber, "macAddress": d.MACAddress, "status": d.Status})
	}
	assign := func(items []persistence.AdminAssignmentProjection) []gin.H {
		out := make([]gin.H, 0, len(items))
		for _, a := range items {
			x := gin.H{"id": a.ID.String(), "deviceId": strconv.FormatUint(uint64(a.DeviceID), 10), "measurementPointId": a.MeasurementPointID.String(), "validFrom": a.ValidFrom.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")}
			if a.ValidTo != nil {
				x["validTo"] = a.ValidTo.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
			} else {
				x["validTo"] = nil
			}
			out = append(out, x)
		}
		return out
	}
	return gin.H{"measurementPoints": points, "devices": devices, "activeAssignments": assign(v.ActiveAssignments), "assignmentHistory": assign(v.AssignmentHistory)}
}

type adminBindingResultResponse struct {
	OperationID           string     `json:"operationId"`
	Action                string     `json:"action"`
	MeasurementPointID    *string    `json:"measurementPointId,omitempty"`
	DeviceID              *string    `json:"deviceId,omitempty"`
	ReplacementDeviceID   *string    `json:"replacementDeviceId,omitempty"`
	OldMeasurementPointID *string    `json:"oldMeasurementPointId,omitempty"`
	NewMeasurementPointID *string    `json:"newMeasurementPointId,omitempty"`
	OldAssignmentID       *string    `json:"oldAssignmentId,omitempty"`
	NewAssignmentID       *string    `json:"newAssignmentId,omitempty"`
	EffectiveAt           *time.Time `json:"effectiveAt,omitempty"`
}

func resultResponse(r domain.AdminBindingResult) adminBindingResultResponse {
	strUUID := func(v *uuid.UUID) *string {
		if v == nil {
			return nil
		}
		s := v.String()
		return &s
	}
	strUint := func(v *uint) *string {
		if v == nil {
			return nil
		}
		s := strconv.FormatUint(uint64(*v), 10)
		return &s
	}
	return adminBindingResultResponse{OperationID: r.OperationID.String(), Action: string(r.Action), MeasurementPointID: strUUID(r.MeasurementPointID), DeviceID: strUint(r.DeviceID), ReplacementDeviceID: strUint(r.ReplacementDeviceID), OldMeasurementPointID: strUUID(r.OldMeasurementPointID), NewMeasurementPointID: strUUID(r.NewMeasurementPointID), OldAssignmentID: strUUID(r.OldAssignmentID), NewAssignmentID: strUUID(r.NewAssignmentID), EffectiveAt: r.EffectiveAt}
}
func writeResult(c *gin.Context, result domain.AdminBindingResult, err error) {
	if err != nil {
		WritePublicError(c, err)
		return
	}
	c.JSON(http.StatusOK, resultResponse(result))
}

const maxAdminBindingRequestBody = 64 * 1024

func decodeStrict(c *gin.Context, v interface{}) bool {
	// DTOs are intentionally small. Read one byte past the limit so oversized
	// bodies are rejected without retaining or exposing their contents.
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, maxAdminBindingRequestBody+1))
	if err != nil || len(raw) > maxAdminBindingRequestBody || len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(v) != nil {
		return false
	}
	var extra interface{}
	return dec.Decode(&extra) == io.EOF
}
func idempotencyKey(c *gin.Context) string { return strings.TrimSpace(c.GetHeader("Idempotency-Key")) }
func requireIdempotency(c *gin.Context) bool {
	key := idempotencyKey(c)
	return key != "" && len(key) <= 255
}
func validUUID(s string) bool {
	// Transport UUIDs are canonical input, not user-facing text. Reject
	// surrounding whitespace so validation and the subsequent parse address
	// exactly the same value.
	if s != strings.TrimSpace(s) {
		return false
	}
	u, e := uuid.Parse(s)
	return e == nil && u != uuid.Nil
}
func parsePathUUID(c *gin.Context) (uuid.UUID, bool) {
	raw := c.Param("assignmentId")
	if raw != strings.TrimSpace(raw) {
		return uuid.Nil, false
	}
	u, e := uuid.Parse(raw)
	return u, e == nil && u != uuid.Nil
}
func toDomainRef(r deviceRefRequest) domain.DeviceRef {
	return domain.DeviceRef{DeviceID: r.DeviceID, SerialNumber: r.SerialNumber, MAC: r.MAC}
}
func adminIdentity(c *gin.Context) (uint, bool) {
	i, ok := IdentityFromGin(c)
	if !ok {
		return 0, false
	}
	n, e := strconv.ParseUint(i.UserID, 10, 64)
	return uint(n), e == nil && n > 0 && uint64(uint(n)) == n
}

func requireAdminCapability(c *gin.Context, db *gorm.DB) (uint, error) {
	user, ok := adminIdentity(c)
	if !ok {
		return 0, domain.NewDomainError(domain.ErrAuthenticationRequired, "actor required")
	}
	if db == nil {
		return 0, domain.NewDomainError(domain.ErrPersistenceFailure, "authorization unavailable")
	}
	var row struct {
		IsAdmin bool
	}
	query := db.WithContext(c).Raw("SELECT is_admin FROM users WHERE id=? AND auth_enabled=TRUE", user).Scan(&row)
	if query.Error != nil {
		return 0, domain.NewDomainError(domain.ErrPersistenceFailure, "authorization unavailable")
	}
	if query.RowsAffected != 1 || !row.IsAdmin {
		return 0, domain.NewDomainError(domain.ErrOperationForbidden, "admin access required")
	}
	return user, nil
}

func actorFor(c *gin.Context, db *gorm.DB, action domain.BindingAction, shops, devices []uint) (domain.ActorContext, error) {
	identity, identityOK := IdentityFromGin(c)
	user, ok := adminIdentity(c)
	if !identityOK || !ok {
		return domain.ActorContext{}, domain.NewDomainError(domain.ErrAuthenticationRequired, "actor required")
	}
	sessionID, err := uuid.Parse(identity.SessionID)
	if err != nil || sessionID == uuid.Nil {
		return domain.ActorContext{}, domain.NewDomainError(domain.ErrAuthenticationRequired, "actor required")
	}
	if db == nil {
		return domain.ActorContext{}, domain.NewDomainError(domain.ErrPersistenceFailure, "authorization unavailable")
	}
	var isAdmin bool
	if err := db.WithContext(c).Raw("SELECT is_admin FROM users WHERE id=? AND auth_enabled=TRUE", user).Scan(&isAdmin).Error; err != nil {
		return domain.ActorContext{}, domain.NewDomainError(domain.ErrPersistenceFailure, "authorization unavailable")
	}
	if !isAdmin {
		return domain.ActorContext{}, domain.NewDomainError(domain.ErrOperationForbidden, "admin access required")
	}
	if len(shops) == 0 {
		return domain.ActorContext{}, domain.NewDomainError(domain.ErrSiteScopeDenied, "authoritative Shop scope is required")
	}
	var client uint
	for _, shop := range shops {
		if shop == 0 {
			return domain.ActorContext{}, domain.NewDomainError(domain.ErrSiteScopeDenied, "authoritative Shop scope is required")
		}
		var row struct{ ClientID uint }
		if err := db.WithContext(c).Raw("SELECT s.client_id FROM shops s JOIN user_shop_relations r ON r.shop_id=s.id AND r.user_id=? WHERE s.id=? AND s.is_active=TRUE", user, shop).Scan(&row).Error; err != nil {
			return domain.ActorContext{}, domain.NewDomainError(domain.ErrPersistenceFailure, "authorization unavailable")
		}
		if row.ClientID == 0 {
			return domain.ActorContext{}, domain.NewDomainError(domain.ErrSiteScopeDenied, "Shop is outside the authorized scope")
		}
		if client == 0 {
			client = row.ClientID
		} else if client != row.ClientID {
			return domain.ActorContext{}, domain.NewDomainError(domain.ErrTenantScopeDenied, "cross-client scope denied")
		}
	}
	for _, device := range devices {
		if device == 0 {
			return domain.ActorContext{}, domain.NewDomainError(domain.ErrDeviceScopeDenied, "authoritative inventory scope is required")
		}
		var owned bool
		if err := db.WithContext(c).Raw("SELECT EXISTS (SELECT 1 FROM devices WHERE id=? AND inventory_owner_client_id=? AND inventory_owner_client_id IS NOT NULL)", device, client).Scan(&owned).Error; err != nil {
			return domain.ActorContext{}, domain.NewDomainError(domain.ErrPersistenceFailure, "authorization unavailable")
		}
		if !owned {
			return domain.ActorContext{}, domain.NewDomainError(domain.ErrDeviceScopeDenied, "Device inventory authority is unavailable")
		}
	}
	tenant := "client:" + strconv.FormatUint(uint64(client), 10)
	return domain.ActorContext{ActorID: user, SessionID: sessionID, ScopeKey: "admin-binding:" + tenant, Scope: domain.ScopeSnapshot{TenantKey: tenant, ShopIDs: append([]uint(nil), shops...), DeviceIDs: append([]uint(nil), devices...), AllowedActions: []domain.BindingAction{action}}}, nil
}

// resolveHTTPDeviceRef resolves every supplied identifier before authorization
// scope construction. It deliberately retains distinct existing IDs so the
// application can return identifiers_inconsistent after actorFor has verified
// that every ID belongs to the authorized inventory. Missing IDs are still
// collapsed to not-found, and actorFor collapses out-of-scope IDs likewise.
func resolveHTTPDeviceRef(db *gorm.DB, c *gin.Context, ref domain.DeviceRef) ([]uint, error) {
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	if db == nil {
		return nil, domain.NewDomainError(domain.ErrPersistenceFailure, "device lookup unavailable")
	}
	ids := make([]uint, 0, 3)
	find := func(query string, arg interface{}) error {
		var d domain.Device
		if err := db.WithContext(c).Where(query, arg).First(&d).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.NewDomainError(domain.ErrDeviceNotFound, "device was not found")
			}
			return domain.NewDomainError(domain.ErrPersistenceFailure, "device lookup unavailable")
		}
		ids = append(ids, d.ID)
		return nil
	}
	if ref.DeviceID != nil {
		if err := find("id = ?", *ref.DeviceID); err != nil {
			return nil, err
		}
	}
	if ref.SerialNumber != nil {
		if err := find("serial_number = ?", strings.TrimSpace(*ref.SerialNumber)); err != nil {
			return nil, err
		}
	}
	if ref.MAC != nil {
		if err := find("mac_address = ?", *ref.MAC); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

func lookupBindShop(db *gorm.DB, c *gin.Context, pid uuid.UUID) (uint, error) {
	if db == nil {
		return 0, domain.NewDomainError(domain.ErrPersistenceFailure, "target lookup unavailable")
	}
	var point struct{ ShopID uint }
	if err := db.WithContext(c).Raw("SELECT shop_id FROM measurement_points WHERE id=?", pid).Scan(&point).Error; err != nil {
		return 0, domain.NewDomainError(domain.ErrPersistenceFailure, "target lookup unavailable")
	}
	if point.ShopID == 0 {
		return 0, domain.NewDomainError(domain.ErrMeasurementPointNotFound, "measurement point was not found")
	}
	return point.ShopID, nil
}

func lookupAssignmentTargets(db *gorm.DB, c *gin.Context, id uuid.UUID) (uint, uint, error) {
	if db == nil {
		return 0, 0, domain.NewDomainError(domain.ErrPersistenceFailure, "target lookup unavailable")
	}
	var a domain.DeviceAssignment
	if err := db.WithContext(c).First(&a, "id=?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, 0, domain.NewDomainError(domain.ErrAssignmentNotFound, "assignment was not found")
		}
		return 0, 0, domain.NewDomainError(domain.ErrPersistenceFailure, "target lookup unavailable")
	}
	shop, err := lookupBindShop(db, c, a.MeasurementPointID)
	if err != nil {
		return 0, 0, err
	}
	if a.DeviceID == 0 {
		return 0, 0, domain.NewDomainError(domain.ErrDeviceNotFound, "device was not found")
	}
	return shop, a.DeviceID, nil
}

func lookupRelocateTargets(db *gorm.DB, c *gin.Context, id, target uuid.UUID) ([]uint, uint, error) {
	shop, device, err := lookupAssignmentTargets(db, c, id)
	if err != nil {
		return nil, 0, err
	}
	targetShop, err := lookupBindShop(db, c, target)
	if err != nil {
		return nil, 0, err
	}
	return []uint{shop, targetShop}, device, nil
}

func lookupUnbindTargets(db *gorm.DB, c *gin.Context, id uuid.UUID) (uint, uint, error) {
	return lookupAssignmentTargets(db, c, id)
}
