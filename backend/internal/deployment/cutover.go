package deployment

import (
	"errors"
	"fmt"
)

type ReadinessLayers struct {
	ProcessAlive          bool
	DependenciesReachable bool
	SchemaReady           bool
	WritePathReady        bool
	ProtectedControlReady bool
	ApplicationReady      bool
	BackendMQTTReady      bool
}

func (r ReadinessLayers) Ready() bool {
	return r.ProcessAlive && r.DependenciesReachable && r.SchemaReady && r.WritePathReady && r.ProtectedControlReady && r.ApplicationReady && r.BackendMQTTReady
}

var (
	ErrReadinessIncomplete       = errors.New("layered readiness is incomplete")
	ErrSmokeNotAllowed           = errors.New("controlled smoke is not allowed")
	ErrGeneralWriteGateNotPassed = errors.New("general write reopen gates did not pass")
)

type SmokeRequest struct {
	AuthorizedBoundedAction bool
	ExactCleanV6Proven      bool
	GeneralIngressBlocked   bool
}

// ValidateControlledSmoke makes the acceptance exception explicit. A smoke
// write is never a write-reopen decision.
func ValidateControlledSmoke(request SmokeRequest) error {
	if !request.AuthorizedBoundedAction || !request.ExactCleanV6Proven || !request.GeneralIngressBlocked {
		return ErrSmokeNotAllowed
	}
	return nil
}

type GeneralWriteGates struct {
	ExactCleanV6Proven    bool
	Readiness             ReadinessLayers
	ControlledSmokePassed bool
	OldWritersAbsent      bool
	DatabaseSessionsClear bool
	RestartConverged      bool
	OperatorEvidenceReady bool
}

// GeneralWritesMayReopen is intentionally stricter than smoke validation.
// It returns true only after every frozen gate passes.
func GeneralWritesMayReopen(gates GeneralWriteGates) (bool, error) {
	if !gates.ExactCleanV6Proven {
		return false, fmt.Errorf("%w: clean V6 not proven", ErrGeneralWriteGateNotPassed)
	}
	if !gates.Readiness.Ready() {
		return false, fmt.Errorf("%w: readiness incomplete", ErrGeneralWriteGateNotPassed)
	}
	if !gates.ControlledSmokePassed || !gates.OldWritersAbsent || !gates.DatabaseSessionsClear || !gates.RestartConverged || !gates.OperatorEvidenceReady {
		return false, ErrGeneralWriteGateNotPassed
	}
	return true, nil
}
