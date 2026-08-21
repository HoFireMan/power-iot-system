package deployment

import (
	"errors"
	"testing"
)

func validDrainObservation() DrainObservation {
	return DrainObservation{
		HTTPWritesBlocked: true, MQTTIngestionBlocked: true, RestartsSuppressed: true,
		DirectSQLControlled: true, ProcessStateInspected: true, IngressStateInspected: true,
		BrokerStateInspected: true, DatabaseStateInspected: true,
	}
}

func TestDrainObservationRequiresAllWriterEvidence(t *testing.T) {
	if err := validDrainObservation().ProveQuiescence(); err != nil {
		t.Fatalf("valid drain rejected: %v", err)
	}
	for name, mutate := range map[string]func(*DrainObservation){
		"http":                func(o *DrainObservation) { o.HTTPWritesBlocked = false },
		"mqtt":                func(o *DrainObservation) { o.MQTTIngestionBlocked = false },
		"restart":             func(o *DrainObservation) { o.RestartsSuppressed = false },
		"direct sql":          func(o *DrainObservation) { o.DirectSQLControlled = false },
		"in flight":           func(o *DrainObservation) { o.InFlightWrites = 1 },
		"old writer":          func(o *DrainObservation) { o.OldWriters = 1 },
		"unknown writer":      func(o *DrainObservation) { o.UnknownWriters = 1 },
		"process inspection":  func(o *DrainObservation) { o.ProcessStateInspected = false },
		"database inspection": func(o *DrainObservation) { o.DatabaseStateInspected = false },
	} {
		t.Run(name, func(t *testing.T) {
			observation := validDrainObservation()
			mutate(&observation)
			if !errors.Is(observation.ProveQuiescence(), ErrDrainNotProven) {
				t.Fatal("incomplete drain evidence was accepted")
			}
		})
	}
}
