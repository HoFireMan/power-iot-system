package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// BindingAction is the stable business action persisted in the Admin Binding
// audit/operation contract.
type BindingAction string

const (
	ActionCreateMeasurementPoint BindingAction = "create_measurement_point"
	ActionBind                   BindingAction = "bind"
	ActionReplace                BindingAction = "replace"
	ActionRelocate               BindingAction = "relocate"
	ActionUnbind                 BindingAction = "unbind"
)

// ScopeSnapshot contains authorization facts established by a future auth
// boundary. DeviceIDs are explicit inventory scope; they must not be inferred
// from Device.ShopID or from a supplied DeviceRef.
type ScopeSnapshot struct {
	TenantKey      string          `json:"tenant_key"`
	ShopIDs        []uint          `json:"shop_ids"`
	DeviceIDs      []uint          `json:"device_ids"`
	AllowedActions []BindingAction `json:"allowed_actions"`
}

// ActorContext is a verified actor boundary, not an HTTP/JWT representation.
// ScopeKey is the stable persistence/idempotency scope identity. ScopeSnapshot
// is typed deliberately so callers cannot smuggle an arbitrary authorization
// map into command behavior.
type ActorContext struct {
	ActorID uint
	// SessionID is populated by the HTTP adapter from the authenticated
	// session. Direct application/domain callers may leave it nil; those
	// trusted test actors retain their existing boundary.
	SessionID uuid.UUID
	ScopeKey  string
	Scope     ScopeSnapshot
}

// DeviceRef is the only identity input accepted by binding commands. The
// identifiers are pointers so an explicitly supplied empty value can receive a
// specific validation error instead of being treated as omitted.
type DeviceRef struct {
	DeviceID     *uint
	SerialNumber *string
	MAC          *string
}

// CreateMeasurementPointCommand requests a plan for a new logical point. The
// UUID is deliberately absent: the application generates it and 3B-3 persists
// it atomically with the audit and operation ledger.
type CreateMeasurementPointCommand struct {
	ShopID          uint
	Name            string
	RequestIdentity string
	Actor           ActorContext
}

// BindDeviceCommand requests a new active assignment at an existing MP.
type BindDeviceCommand struct {
	DeviceRef          DeviceRef
	MeasurementPointID uuid.UUID
	Reason             string
	RequestIdentity    string
	Actor              ActorContext
}

// ReplaceDeviceCommand requests replacement at the MP of the current
// assignment. A target MP is intentionally absent: replacement never relocates.
type ReplaceDeviceCommand struct {
	CurrentAssignmentID  uuid.UUID
	ReplacementDeviceRef DeviceRef
	Reason               string
	RequestIdentity      string
	Actor                ActorContext
}

// RelocateDeviceCommand requests a move from the current assignment's MP to a
// different existing MP.
type RelocateDeviceCommand struct {
	CurrentAssignmentID      uuid.UUID
	TargetMeasurementPointID uuid.UUID
	Reason                   string
	RequestIdentity          string
	Actor                    ActorContext
}

// UnbindDeviceCommand requests closure of the current assignment. It does not
// delete or retire either the Device or MeasurementPoint.
type UnbindDeviceCommand struct {
	CurrentAssignmentID uuid.UUID
	Reason              string
	RequestIdentity     string
	Actor               ActorContext
}

// AuditIntent carries the audit facts a future transaction executor needs. It
// intentionally has no effective timestamp; 3B-3 must fill that only after
// acquiring all required locks and sampling the database clock.
type AuditIntent struct {
	Action          BindingAction
	RequestIdentity string
	ActorID         uint
	ScopeKey        string
	ScopeSnapshot   ScopeSnapshot
	// ClientID is derived from Shop -> Client relational facts, never from
	// request values or the verified actor snapshot.
	ClientID              *uint
	Reason                string
	DeviceID              *uint
	DeviceSerialNumber    string
	DeviceMAC             string
	ShopID                *uint
	MeasurementPointID    *uuid.UUID
	OldMeasurementPointID *uuid.UUID
	NewMeasurementPointID *uuid.UUID
	OldAssignmentID       *uuid.UUID
	NewAssignmentID       *uuid.UUID
}

// CreateMeasurementPointPlan is an application result, not a persisted row.
type CreateMeasurementPointPlan struct {
	Action             BindingAction
	RequestIdentity    string
	Actor              ActorContext
	ShopID             uint
	Name               string
	MeasurementPointID uuid.UUID
	Audit              AuditIntent
}

// AssignmentTransitionPlan is the handoff from 3B-2 validation to 3B-3
// transaction execution. It contains transition intent only: no effective T,
// ValidFrom, ValidTo, lock result, or persistence outcome is represented.
type AssignmentTransitionPlan struct {
	Action                   BindingAction
	RequestIdentity          string
	Actor                    ActorContext
	CurrentAssignmentID      *uuid.UUID
	DeviceID                 uint
	ReplacementDeviceID      *uint
	SourceMeasurementPointID *uuid.UUID
	TargetMeasurementPointID *uuid.UUID
	Reason                   string
	Audit                    AuditIntent
}

// AdminBindingResult is the committed semantic result stored in the operation
// ledger. It contains immutable identifiers and the transition boundary, so a
// replay never has to infer success from mutable current state.
type AdminBindingResult struct {
	OperationID           uuid.UUID     `json:"operation_id"`
	Action                BindingAction `json:"action"`
	MeasurementPointID    *uuid.UUID    `json:"measurement_point_id,omitempty"`
	DeviceID              *uint         `json:"device_id,omitempty"`
	ReplacementDeviceID   *uint         `json:"replacement_device_id,omitempty"`
	OldMeasurementPointID *uuid.UUID    `json:"old_measurement_point_id,omitempty"`
	NewMeasurementPointID *uuid.UUID    `json:"new_measurement_point_id,omitempty"`
	OldAssignmentID       *uuid.UUID    `json:"old_assignment_id,omitempty"`
	NewAssignmentID       *uuid.UUID    `json:"new_assignment_id,omitempty"`
	EffectiveAt           *time.Time    `json:"effective_at,omitempty"`
}

// Stable Admin Binding error codes. They are domain/application outcomes, not
// HTTP statuses; transport mapping remains outside this package.
type ErrorCode string

const (
	ErrDeviceNotFound           ErrorCode = "device_not_found"
	ErrShopNotFound             ErrorCode = "shop_not_found"
	ErrMeasurementPointNotFound ErrorCode = "measurement_point_not_found"
	ErrAssignmentNotFound       ErrorCode = "assignment_not_found"
	ErrDeviceAlreadyAssigned    ErrorCode = "device_already_assigned"
	ErrMeasurementPointOccupied ErrorCode = "measurement_point_occupied"
	ErrOverlappingAssignment    ErrorCode = "overlapping_assignment"
	ErrConcurrentTransition     ErrorCode = "concurrent_transition_conflict"
	ErrSerialConflict           ErrorCode = "serial_conflict"
	ErrAssignmentNotCurrent     ErrorCode = "assignment_not_current"
	ErrDeviceRetired            ErrorCode = "device_retired"
	ErrDeviceLifecycleDisabled  ErrorCode = "device_disabled"
	ErrIdempotencyKeyReused     ErrorCode = "idempotency_key_reused"
	ErrAssignmentTimeConflict   ErrorCode = "assignment_time_conflict"
	ErrAuthenticationRequired   ErrorCode = "authentication_required"
	ErrTenantScopeDenied        ErrorCode = "tenant_scope_denied"
	ErrSiteScopeDenied          ErrorCode = "site_scope_denied"
	ErrDeviceScopeDenied        ErrorCode = "device_scope_denied"
	ErrOperationForbidden       ErrorCode = "operation_forbidden"
	ErrMalformedMAC             ErrorCode = "malformed_mac"
	ErrInvalidSerial            ErrorCode = "invalid_serial"
	ErrIdentifiersInconsistent  ErrorCode = "identifiers_inconsistent"
	ErrInvalidStateTransition   ErrorCode = "invalid_state_transition"
	ErrInvalidEffectiveTime     ErrorCode = "invalid_effective_time"
	ErrHistoricalCorrection     ErrorCode = "historical_correction_required"
	ErrDeviceNotEligible        ErrorCode = "device_not_eligible"
	ErrInvalidRequest           ErrorCode = "invalid_request"
	ErrPersistenceFailure       ErrorCode = "persistence_failure"
)

// DomainError is the stable machine-readable business error. Callers should
// branch on Code and treat Message as informational.
type DomainError struct {
	Code    ErrorCode
	Message string
	Cause   error
}

func (e *DomainError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Message == "" {
		return string(e.Code)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *DomainError) Unwrap() error { return e.Cause }

func NewDomainError(code ErrorCode, message string) *DomainError {
	return &DomainError{Code: code, Message: message}
}

// CodeOf extracts a stable business code without exposing transport concerns.
func CodeOf(err error) ErrorCode {
	var domainErr *DomainError
	if errors.As(err, &domainErr) {
		return domainErr.Code
	}
	return ""
}

func validateRequestIdentity(identity string) error {
	if strings.TrimSpace(identity) == "" {
		return NewDomainError(ErrInvalidRequest, "request identity is required")
	}
	return nil
}

func (ref DeviceRef) Validate() error {
	if ref.DeviceID == nil && ref.SerialNumber == nil && ref.MAC == nil {
		return NewDomainError(ErrInvalidRequest, "at least one device identifier is required")
	}
	if ref.DeviceID != nil && *ref.DeviceID == 0 {
		return NewDomainError(ErrInvalidRequest, "device ID must be non-zero")
	}
	if ref.SerialNumber != nil {
		trimmed := strings.TrimSpace(*ref.SerialNumber)
		if trimmed == "" {
			return NewDomainError(ErrInvalidSerial, "serial number must be non-empty")
		}
		// Reject rather than silently changing the lookup key. Authorization
		// and execution must always address the same serial value.
		if trimmed != *ref.SerialNumber {
			return NewDomainError(ErrInvalidSerial, "serial number must not contain surrounding whitespace")
		}
	}
	if ref.MAC != nil && !isCanonicalMAC(*ref.MAC) {
		return NewDomainError(ErrMalformedMAC, "MAC must be uppercase 12-hex without separators")
	}
	return nil
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

func (actor ActorContext) HasAction(action BindingAction) bool {
	for _, allowed := range actor.Scope.AllowedActions {
		if allowed == action {
			return true
		}
	}
	return false
}

func (actor ActorContext) HasShop(shopID uint) bool {
	for _, allowed := range actor.Scope.ShopIDs {
		if allowed == shopID {
			return true
		}
	}
	return false
}

func (actor ActorContext) HasDevice(deviceID uint) bool {
	for _, allowed := range actor.Scope.DeviceIDs {
		if allowed == deviceID {
			return true
		}
	}
	return false
}

func validName(name string) bool {
	return strings.TrimSpace(name) != "" && utf8.RuneCountInString(name) <= 100
}
