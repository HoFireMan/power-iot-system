package domain

import "errors"

// DeviceLifecycle is the authoritative lifecycle state for an inventory
// Device. It is deliberately independent from online/telemetry presence.
type DeviceLifecycle string

const (
	DeviceLifecycleActive   DeviceLifecycle = "ACTIVE"
	DeviceLifecycleDisabled DeviceLifecycle = "DISABLED"
	DeviceLifecycleRetired  DeviceLifecycle = "RETIRED"
)

// Lifecycle commands use the existing scoped Admin Binding operation ledger,
// but are intentionally not Admin Binding audit actions.
const (
	ActionDisableDevice BindingAction = "disable_device"
	ActionEnableDevice  BindingAction = "enable_device"
	ActionRetireDevice  BindingAction = "retire_device"
)

var ErrInvalidDeviceLifecycleTransition = errors.New("invalid device lifecycle transition")

func (s DeviceLifecycle) Valid() bool {
	return s == DeviceLifecycleActive || s == DeviceLifecycleDisabled || s == DeviceLifecycleRetired
}

// CanTransition describes the closed V1 state machine. RETIRED is terminal.
func (s DeviceLifecycle) CanTransition(target DeviceLifecycle) bool {
	switch s {
	case DeviceLifecycleActive:
		return target == DeviceLifecycleDisabled || target == DeviceLifecycleRetired
	case DeviceLifecycleDisabled:
		return target == DeviceLifecycleActive || target == DeviceLifecycleRetired
	default:
		return false
	}
}

func ValidateDeviceLifecycleTransition(from, to DeviceLifecycle) error {
	if !from.Valid() || !to.Valid() || !from.CanTransition(to) {
		return ErrInvalidDeviceLifecycleTransition
	}
	return nil
}
