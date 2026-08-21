package deployment

import (
	"errors"
	"testing"
)

func readyLayers() ReadinessLayers {
	return ReadinessLayers{ProcessAlive: true, DependenciesReachable: true, SchemaReady: true, WritePathReady: true, ProtectedControlReady: true, ApplicationReady: true, BackendMQTTReady: true}
}

func readyGates() GeneralWriteGates {
	return GeneralWriteGates{ExactCleanV6Proven: true, Readiness: readyLayers(), ControlledSmokePassed: true, OldWritersAbsent: true, DatabaseSessionsClear: true, RestartConverged: true, OperatorEvidenceReady: true}
}

func TestControlledSmokeDoesNotMeanGeneralWriteReopen(t *testing.T) {
	if err := ValidateControlledSmoke(SmokeRequest{AuthorizedBoundedAction: true, ExactCleanV6Proven: true, GeneralIngressBlocked: true}); err != nil {
		t.Fatalf("controlled smoke rejected: %v", err)
	}
	gates := readyGates()
	gates.ControlledSmokePassed = false
	if reopened, err := GeneralWritesMayReopen(gates); reopened || !errors.Is(err, ErrGeneralWriteGateNotPassed) {
		t.Fatalf("general writes reopened from smoke-only state: reopened=%t err=%v", reopened, err)
	}
}

func TestGeneralWritesRequireEveryGate(t *testing.T) {
	if reopened, err := GeneralWritesMayReopen(readyGates()); !reopened || err != nil {
		t.Fatalf("ready gates rejected: reopened=%t err=%v", reopened, err)
	}
	for name, mutate := range map[string]func(*GeneralWriteGates){
		"v6":          func(g *GeneralWriteGates) { g.ExactCleanV6Proven = false },
		"readiness":   func(g *GeneralWriteGates) { g.Readiness.ApplicationReady = false },
		"smoke":       func(g *GeneralWriteGates) { g.ControlledSmokePassed = false },
		"old writers": func(g *GeneralWriteGates) { g.OldWritersAbsent = false },
		"sessions":    func(g *GeneralWriteGates) { g.DatabaseSessionsClear = false },
		"restart":     func(g *GeneralWriteGates) { g.RestartConverged = false },
		"evidence":    func(g *GeneralWriteGates) { g.OperatorEvidenceReady = false },
	} {
		t.Run(name, func(t *testing.T) {
			gates := readyGates()
			mutate(&gates)
			if reopened, err := GeneralWritesMayReopen(gates); reopened || !errors.Is(err, ErrGeneralWriteGateNotPassed) {
				t.Fatalf("incomplete gate reopened writes: reopened=%t err=%v", reopened, err)
			}
		})
	}
}
