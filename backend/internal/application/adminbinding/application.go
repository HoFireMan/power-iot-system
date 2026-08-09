// Package adminbinding is the application seam for Admin Device Binding
// command validation and transition planning. It performs no database writes,
// transaction management, locking, or effective-time sampling; those concerns
// belong to Milestone 3B-3.
package adminbinding

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"power-iot-backend/internal/core/domain"
)

// Lookup is the narrow state capability needed by command planning. A lookup
// returns (nil, nil) when a requested row does not exist. A concrete PostgreSQL
// adapter can map its own not-found result to that contract without exposing a
// generic repository or GORM to commands.
type Lookup interface {
	FindShop(ctx context.Context, id uint) (*domain.Shop, error)
	FindMeasurementPoint(ctx context.Context, id uuid.UUID) (*domain.MeasurementPoint, error)

	FindDeviceByID(ctx context.Context, id uint) (*domain.Device, error)
	FindDeviceBySerial(ctx context.Context, serial string) (*domain.Device, error)
	FindDeviceByMAC(ctx context.Context, mac string) (*domain.Device, error)

	FindAssignment(ctx context.Context, id uuid.UUID) (*domain.DeviceAssignment, error)
	FindActiveAssignmentByDevice(ctx context.Context, id uint) (*domain.DeviceAssignment, error)
	FindActiveAssignmentByMeasurementPoint(ctx context.Context, id uuid.UUID) (*domain.DeviceAssignment, error)
}

// Application owns the meaningful Admin Binding planning behavior behind one
// application interface. It deliberately has no five-service pass-through
// hierarchy and no persistence dependency.
type Application struct {
	lookup Lookup
}

func New(lookup Lookup) *Application { return &Application{lookup: lookup} }

func (a *Application) CreateMeasurementPoint(ctx context.Context, cmd domain.CreateMeasurementPointCommand) (domain.CreateMeasurementPointPlan, error) {
	actor := cloneActorContext(cmd.Actor)
	if err := authorize(actor, domain.ActionCreateMeasurementPoint, nil, nil); err != nil {
		return domain.CreateMeasurementPointPlan{}, err
	}
	if err := validateRequestIdentity(cmd.RequestIdentity); err != nil {
		return domain.CreateMeasurementPointPlan{}, err
	}
	if cmd.ShopID == 0 || !validName(cmd.Name) {
		return domain.CreateMeasurementPointPlan{}, domain.NewDomainError(domain.ErrInvalidRequest, "shop and non-empty name within 100 characters are required")
	}
	shop, err := a.findShop(ctx, cmd.ShopID)
	if err != nil {
		return domain.CreateMeasurementPointPlan{}, err
	}
	if err := authorize(actor, domain.ActionCreateMeasurementPoint, []uint{shop.ID}, nil); err != nil {
		return domain.CreateMeasurementPointPlan{}, err
	}

	pointID := uuid.New()
	return domain.CreateMeasurementPointPlan{
		Action:             domain.ActionCreateMeasurementPoint,
		RequestIdentity:    cmd.RequestIdentity,
		Actor:              cloneActorContext(actor),
		ShopID:             shop.ID,
		Name:               cmd.Name,
		MeasurementPointID: pointID,
		Audit: domain.AuditIntent{
			Action:             domain.ActionCreateMeasurementPoint,
			RequestIdentity:    cmd.RequestIdentity,
			ActorID:            actor.ActorID,
			ScopeKey:           actor.ScopeKey,
			ScopeSnapshot:      cloneScopeSnapshot(actor.Scope),
			ShopID:             uintPtr(shop.ID),
			MeasurementPointID: uuidPtr(pointID),
		},
	}, nil
}

func (a *Application) BindDevice(ctx context.Context, cmd domain.BindDeviceCommand) (domain.AssignmentTransitionPlan, error) {
	actor := cloneActorContext(cmd.Actor)
	if err := authorize(actor, domain.ActionBind, nil, nil); err != nil {
		return domain.AssignmentTransitionPlan{}, err
	}
	if err := validateRequestIdentity(cmd.RequestIdentity); err != nil {
		return domain.AssignmentTransitionPlan{}, err
	}
	device, err := a.resolveDeviceRef(ctx, cmd.DeviceRef)
	if err != nil {
		return domain.AssignmentTransitionPlan{}, err
	}
	point, err := a.findMeasurementPoint(ctx, cmd.MeasurementPointID)
	if err != nil {
		return domain.AssignmentTransitionPlan{}, err
	}
	if err := ensureEligibleDevice(device); err != nil {
		return domain.AssignmentTransitionPlan{}, err
	}
	if err := authorize(actor, domain.ActionBind, []uint{point.ShopID}, []uint{device.ID}); err != nil {
		return domain.AssignmentTransitionPlan{}, err
	}
	activeDevice, err := a.findActiveDeviceAssignment(ctx, device.ID)
	if err != nil {
		return domain.AssignmentTransitionPlan{}, err
	}
	if activeDevice != nil {
		return domain.AssignmentTransitionPlan{}, domain.NewDomainError(domain.ErrDeviceAlreadyAssigned, "device already has an active assignment")
	}
	activePoint, err := a.findActivePointAssignment(ctx, point.ID)
	if err != nil {
		return domain.AssignmentTransitionPlan{}, err
	}
	if activePoint != nil {
		return domain.AssignmentTransitionPlan{}, domain.NewDomainError(domain.ErrMeasurementPointOccupied, "measurement point already has an active assignment")
	}

	return assignmentPlan(domain.ActionBind, cmd.RequestIdentity, actor, cmd.Reason, point.ShopID, device, nil, nil, nil, &point.ID, &point.ID), nil
}

func (a *Application) ReplaceDevice(ctx context.Context, cmd domain.ReplaceDeviceCommand) (domain.AssignmentTransitionPlan, error) {
	actor := cloneActorContext(cmd.Actor)
	if err := authorize(actor, domain.ActionReplace, nil, nil); err != nil {
		return domain.AssignmentTransitionPlan{}, err
	}
	if err := validateRequestIdentity(cmd.RequestIdentity); err != nil {
		return domain.AssignmentTransitionPlan{}, err
	}
	assignment, err := a.findCurrentAssignment(ctx, cmd.CurrentAssignmentID)
	if err != nil {
		return domain.AssignmentTransitionPlan{}, err
	}
	point, err := a.findMeasurementPoint(ctx, assignment.MeasurementPointID)
	if err != nil {
		return domain.AssignmentTransitionPlan{}, err
	}
	currentDevice, err := a.findDeviceByID(ctx, assignment.DeviceID)
	if err != nil {
		return domain.AssignmentTransitionPlan{}, err
	}
	replacement, err := a.resolveDeviceRef(ctx, cmd.ReplacementDeviceRef)
	if err != nil {
		return domain.AssignmentTransitionPlan{}, err
	}
	if err := ensureEligibleDevice(replacement); err != nil {
		return domain.AssignmentTransitionPlan{}, err
	}
	if replacement.ID == currentDevice.ID {
		return domain.AssignmentTransitionPlan{}, domain.NewDomainError(domain.ErrInvalidStateTransition, "replacement device must be a different physical device")
	}
	if err := authorize(actor, domain.ActionReplace, []uint{point.ShopID}, []uint{currentDevice.ID, replacement.ID}); err != nil {
		return domain.AssignmentTransitionPlan{}, err
	}
	activeReplacement, err := a.findActiveDeviceAssignment(ctx, replacement.ID)
	if err != nil {
		return domain.AssignmentTransitionPlan{}, err
	}
	if activeReplacement != nil {
		return domain.AssignmentTransitionPlan{}, domain.NewDomainError(domain.ErrDeviceAlreadyAssigned, "replacement device already has an active assignment")
	}

	return assignmentPlan(domain.ActionReplace, cmd.RequestIdentity, actor, cmd.Reason, point.ShopID, currentDevice, replacement, &assignment.ID, &point.ID, &point.ID, &point.ID), nil
}

func (a *Application) RelocateDevice(ctx context.Context, cmd domain.RelocateDeviceCommand) (domain.AssignmentTransitionPlan, error) {
	actor := cloneActorContext(cmd.Actor)
	if err := authorize(actor, domain.ActionRelocate, nil, nil); err != nil {
		return domain.AssignmentTransitionPlan{}, err
	}
	if err := validateRequestIdentity(cmd.RequestIdentity); err != nil {
		return domain.AssignmentTransitionPlan{}, err
	}
	assignment, err := a.findCurrentAssignment(ctx, cmd.CurrentAssignmentID)
	if err != nil {
		return domain.AssignmentTransitionPlan{}, err
	}
	source, err := a.findMeasurementPoint(ctx, assignment.MeasurementPointID)
	if err != nil {
		return domain.AssignmentTransitionPlan{}, err
	}
	target, err := a.findMeasurementPoint(ctx, cmd.TargetMeasurementPointID)
	if err != nil {
		return domain.AssignmentTransitionPlan{}, err
	}
	if source.ID == target.ID {
		return domain.AssignmentTransitionPlan{}, domain.NewDomainError(domain.ErrInvalidStateTransition, "relocation target must differ from source")
	}
	device, err := a.findDeviceByID(ctx, assignment.DeviceID)
	if err != nil {
		return domain.AssignmentTransitionPlan{}, err
	}
	if err := authorize(actor, domain.ActionRelocate, []uint{source.ShopID, target.ShopID}, []uint{device.ID}); err != nil {
		return domain.AssignmentTransitionPlan{}, err
	}
	activeTarget, err := a.findActivePointAssignment(ctx, target.ID)
	if err != nil {
		return domain.AssignmentTransitionPlan{}, err
	}
	if activeTarget != nil {
		return domain.AssignmentTransitionPlan{}, domain.NewDomainError(domain.ErrMeasurementPointOccupied, "relocation target already has an active assignment")
	}

	return assignmentPlan(domain.ActionRelocate, cmd.RequestIdentity, actor, cmd.Reason, target.ShopID, device, nil, &assignment.ID, &source.ID, &target.ID, &target.ID), nil
}

func (a *Application) UnbindDevice(ctx context.Context, cmd domain.UnbindDeviceCommand) (domain.AssignmentTransitionPlan, error) {
	actor := cloneActorContext(cmd.Actor)
	if err := authorize(actor, domain.ActionUnbind, nil, nil); err != nil {
		return domain.AssignmentTransitionPlan{}, err
	}
	if err := validateRequestIdentity(cmd.RequestIdentity); err != nil {
		return domain.AssignmentTransitionPlan{}, err
	}
	assignment, err := a.findCurrentAssignment(ctx, cmd.CurrentAssignmentID)
	if err != nil {
		return domain.AssignmentTransitionPlan{}, err
	}
	point, err := a.findMeasurementPoint(ctx, assignment.MeasurementPointID)
	if err != nil {
		return domain.AssignmentTransitionPlan{}, err
	}
	device, err := a.findDeviceByID(ctx, assignment.DeviceID)
	if err != nil {
		return domain.AssignmentTransitionPlan{}, err
	}
	if err := authorize(actor, domain.ActionUnbind, []uint{point.ShopID}, []uint{device.ID}); err != nil {
		return domain.AssignmentTransitionPlan{}, err
	}

	return assignmentPlan(domain.ActionUnbind, cmd.RequestIdentity, actor, cmd.Reason, point.ShopID, device, nil, &assignment.ID, &point.ID, nil, nil), nil
}

func (a *Application) resolveDeviceRef(ctx context.Context, ref domain.DeviceRef) (*domain.Device, error) {
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	if a == nil || a.lookup == nil {
		return nil, domain.NewDomainError(domain.ErrPersistenceFailure, "device lookup is unavailable")
	}
	var candidates []*domain.Device
	if ref.DeviceID != nil {
		device, err := a.lookup.FindDeviceByID(ctx, *ref.DeviceID)
		if err != nil {
			return nil, persistenceError(err)
		}
		if device == nil {
			return nil, domain.NewDomainError(domain.ErrDeviceNotFound, "device ID was not found")
		}
		candidates = append(candidates, device)
	}
	if ref.SerialNumber != nil {
		device, err := a.lookup.FindDeviceBySerial(ctx, *ref.SerialNumber)
		if err != nil {
			return nil, persistenceError(err)
		}
		if device == nil {
			return nil, domain.NewDomainError(domain.ErrDeviceNotFound, "serial number was not found")
		}
		candidates = append(candidates, device)
	}
	if ref.MAC != nil {
		device, err := a.lookup.FindDeviceByMAC(ctx, *ref.MAC)
		if err != nil {
			return nil, persistenceError(err)
		}
		if device == nil {
			return nil, domain.NewDomainError(domain.ErrDeviceNotFound, "MAC was not found")
		}
		candidates = append(candidates, device)
	}
	resolved := candidates[0]
	for _, candidate := range candidates[1:] {
		if candidate.ID != resolved.ID {
			return nil, domain.NewDomainError(domain.ErrIdentifiersInconsistent, "device identifiers resolve to different devices")
		}
	}
	return resolved, nil
}

func (a *Application) findShop(ctx context.Context, id uint) (*domain.Shop, error) {
	if a == nil || a.lookup == nil {
		return nil, domain.NewDomainError(domain.ErrPersistenceFailure, "shop lookup is unavailable")
	}
	shop, err := a.lookup.FindShop(ctx, id)
	if err != nil {
		return nil, persistenceError(err)
	}
	if shop == nil {
		return nil, domain.NewDomainError(domain.ErrShopNotFound, "shop was not found")
	}
	return shop, nil
}

func (a *Application) findMeasurementPoint(ctx context.Context, id uuid.UUID) (*domain.MeasurementPoint, error) {
	if id == uuid.Nil {
		return nil, domain.NewDomainError(domain.ErrInvalidRequest, "measurement point ID is required")
	}
	if a == nil || a.lookup == nil {
		return nil, domain.NewDomainError(domain.ErrPersistenceFailure, "measurement point lookup is unavailable")
	}
	point, err := a.lookup.FindMeasurementPoint(ctx, id)
	if err != nil {
		return nil, persistenceError(err)
	}
	if point == nil {
		return nil, domain.NewDomainError(domain.ErrMeasurementPointNotFound, "measurement point was not found")
	}
	return point, nil
}

func (a *Application) findDeviceByID(ctx context.Context, id uint) (*domain.Device, error) {
	if id == 0 {
		return nil, domain.NewDomainError(domain.ErrDeviceNotFound, "device ID was not found")
	}
	if a == nil || a.lookup == nil {
		return nil, domain.NewDomainError(domain.ErrPersistenceFailure, "device lookup is unavailable")
	}
	device, err := a.lookup.FindDeviceByID(ctx, id)
	if err != nil {
		return nil, persistenceError(err)
	}
	if device == nil {
		return nil, domain.NewDomainError(domain.ErrDeviceNotFound, "device was not found")
	}
	return device, nil
}

func (a *Application) findCurrentAssignment(ctx context.Context, id uuid.UUID) (*domain.DeviceAssignment, error) {
	if id == uuid.Nil {
		return nil, domain.NewDomainError(domain.ErrInvalidRequest, "current assignment ID is required")
	}
	if a == nil || a.lookup == nil {
		return nil, domain.NewDomainError(domain.ErrPersistenceFailure, "assignment lookup is unavailable")
	}
	assignment, err := a.lookup.FindAssignment(ctx, id)
	if err != nil {
		return nil, persistenceError(err)
	}
	if assignment == nil {
		return nil, domain.NewDomainError(domain.ErrAssignmentNotFound, "assignment was not found")
	}
	if assignment.ID != id || assignment.ValidTo != nil {
		return nil, domain.NewDomainError(domain.ErrAssignmentNotCurrent, "assignment is not current")
	}
	return assignment, nil
}

func (a *Application) findActiveDeviceAssignment(ctx context.Context, id uint) (*domain.DeviceAssignment, error) {
	if a == nil || a.lookup == nil {
		return nil, domain.NewDomainError(domain.ErrPersistenceFailure, "assignment lookup is unavailable")
	}
	assignment, err := a.lookup.FindActiveAssignmentByDevice(ctx, id)
	if err != nil {
		return nil, persistenceError(err)
	}
	return assignment, nil
}

func (a *Application) findActivePointAssignment(ctx context.Context, id uuid.UUID) (*domain.DeviceAssignment, error) {
	if a == nil || a.lookup == nil {
		return nil, domain.NewDomainError(domain.ErrPersistenceFailure, "assignment lookup is unavailable")
	}
	assignment, err := a.lookup.FindActiveAssignmentByMeasurementPoint(ctx, id)
	if err != nil {
		return nil, persistenceError(err)
	}
	return assignment, nil
}

func ensureEligibleDevice(device *domain.Device) error {
	if device == nil || device.ID == 0 {
		return domain.NewDomainError(domain.ErrDeviceNotFound, "device was not found")
	}
	if !isCanonicalMAC(device.MacAddress) {
		return domain.NewDomainError(domain.ErrDeviceNotEligible, "registered device MAC is not canonical")
	}
	if device.SerialNumber == nil || strings.TrimSpace(*device.SerialNumber) == "" {
		return domain.NewDomainError(domain.ErrDeviceNotEligible, "device serial number is absent")
	}
	// No retirement/disabled field exists in the current canonical Device model.
	// Eligibility therefore cannot evaluate a lifecycle state in 3B-2.
	return nil
}

func cloneScopeSnapshot(scope domain.ScopeSnapshot) domain.ScopeSnapshot {
	return domain.ScopeSnapshot{
		TenantKey:      scope.TenantKey,
		ShopIDs:        cloneUintSlice(scope.ShopIDs),
		DeviceIDs:      cloneUintSlice(scope.DeviceIDs),
		AllowedActions: cloneBindingActionSlice(scope.AllowedActions),
	}
}

func cloneActorContext(actor domain.ActorContext) domain.ActorContext {
	actor.Scope = cloneScopeSnapshot(actor.Scope)
	return actor
}

func cloneUintSlice(values []uint) []uint {
	if values == nil {
		return nil
	}
	return append([]uint(nil), values...)
}

func cloneBindingActionSlice(values []domain.BindingAction) []domain.BindingAction {
	if values == nil {
		return nil
	}
	return append([]domain.BindingAction(nil), values...)
}

func authorize(actor domain.ActorContext, action domain.BindingAction, shopIDs, deviceIDs []uint) error {
	if actor.ActorID == 0 || strings.TrimSpace(actor.ScopeKey) == "" {
		return domain.NewDomainError(domain.ErrAuthenticationRequired, "verified actor context is required")
	}
	if strings.TrimSpace(actor.Scope.TenantKey) == "" {
		return domain.NewDomainError(domain.ErrTenantScopeDenied, "tenant scope is required")
	}
	if !actor.HasAction(action) {
		return domain.NewDomainError(domain.ErrOperationForbidden, "actor is not authorized for this operation")
	}
	for _, shopID := range shopIDs {
		if !actor.HasShop(shopID) {
			return domain.NewDomainError(domain.ErrSiteScopeDenied, "actor lacks Shop scope")
		}
	}
	for _, deviceID := range deviceIDs {
		if !actor.HasDevice(deviceID) {
			return domain.NewDomainError(domain.ErrDeviceScopeDenied, "actor lacks explicit Device scope")
		}
	}
	return nil
}

func assignmentPlan(action domain.BindingAction, requestIdentity string, actor domain.ActorContext, reason string, shopID uint, device, replacement *domain.Device, currentAssignmentID, sourcePointID, targetPointID, auditPointID *uuid.UUID) domain.AssignmentTransitionPlan {
	frozenActor := cloneActorContext(actor)
	plan := domain.AssignmentTransitionPlan{
		Action:              action,
		RequestIdentity:     requestIdentity,
		Actor:               cloneActorContext(frozenActor),
		CurrentAssignmentID: currentAssignmentID,
		DeviceID:            device.ID,
		Reason:              reason,
	}
	if sourcePointID != nil {
		plan.SourceMeasurementPointID = uuidPtr(*sourcePointID)
	}
	if targetPointID != nil {
		plan.TargetMeasurementPointID = uuidPtr(*targetPointID)
	}
	if replacement != nil {
		plan.ReplacementDeviceID = uintPtr(replacement.ID)
		plan.Audit.DeviceID = uintPtr(replacement.ID)
		plan.Audit.DeviceSerialNumber = serialOf(replacement)
		plan.Audit.DeviceMAC = replacement.MacAddress
	} else {
		plan.Audit.DeviceID = uintPtr(device.ID)
		plan.Audit.DeviceSerialNumber = serialOf(device)
		plan.Audit.DeviceMAC = device.MacAddress
	}
	plan.Audit.Action = action
	plan.Audit.RequestIdentity = requestIdentity
	plan.Audit.ShopID = uintPtr(shopID)
	plan.Audit.ActorID = actor.ActorID
	plan.Audit.ScopeKey = actor.ScopeKey
	plan.Audit.ScopeSnapshot = cloneScopeSnapshot(frozenActor.Scope)
	plan.Audit.Reason = reason
	plan.Audit.OldAssignmentID = cloneUUID(currentAssignmentID)
	plan.Audit.OldMeasurementPointID = cloneUUID(sourcePointID)
	plan.Audit.NewMeasurementPointID = cloneUUID(auditPointID)
	return plan
}

func serialOf(device *domain.Device) string {
	if device == nil || device.SerialNumber == nil {
		return ""
	}
	return *device.SerialNumber
}

func persistenceError(err error) error {
	if err == nil {
		return nil
	}
	return &domain.DomainError{Code: domain.ErrPersistenceFailure, Message: "binding state lookup failed", Cause: err}
}

func uintPtr(value uint) *uint           { return &value }
func uuidPtr(value uuid.UUID) *uuid.UUID { return &value }

func cloneUUID(value *uuid.UUID) *uuid.UUID {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func validateRequestIdentity(identity string) error {
	if strings.TrimSpace(identity) == "" {
		return domain.NewDomainError(domain.ErrInvalidRequest, "request identity is required")
	}
	return nil
}

func validName(name string) bool {
	return strings.TrimSpace(name) != "" && len([]rune(name)) <= 100
}

func isCanonicalMAC(value string) bool {
	if len(value) != 12 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'A' || char > 'F') {
			return false
		}
	}
	return true
}
