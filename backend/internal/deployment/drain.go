package deployment

import (
	"errors"
	"fmt"
)

var ErrDrainNotProven = errors.New("writer quiescence was not proven")

// DrainObservation is an evidence-only snapshot assembled by the operator.
// It is intentionally not a coordinator and does not mutate processes, the
// broker, ingress, or PostgreSQL.
type DrainObservation struct {
	HTTPWritesBlocked      bool
	MQTTIngestionBlocked   bool
	RestartsSuppressed     bool
	DirectSQLControlled    bool
	InFlightWrites         int
	OldWriters             int
	UnknownWriters         int
	ProcessStateInspected  bool
	IngressStateInspected  bool
	BrokerStateInspected   bool
	DatabaseStateInspected bool
}

func (o DrainObservation) ProveQuiescence() error {
	if !o.HTTPWritesBlocked || !o.MQTTIngestionBlocked || !o.RestartsSuppressed || !o.DirectSQLControlled {
		return fmt.Errorf("%w: ingress/restart/direct-writer control incomplete", ErrDrainNotProven)
	}
	if o.InFlightWrites != 0 || o.OldWriters != 0 || o.UnknownWriters != 0 {
		return fmt.Errorf("%w: in_flight=%d old=%d unknown=%d", ErrDrainNotProven, o.InFlightWrites, o.OldWriters, o.UnknownWriters)
	}
	if !o.ProcessStateInspected || !o.IngressStateInspected || !o.BrokerStateInspected || !o.DatabaseStateInspected {
		return fmt.Errorf("%w: process/ingress/broker/database inspection incomplete", ErrDrainNotProven)
	}
	return nil
}
