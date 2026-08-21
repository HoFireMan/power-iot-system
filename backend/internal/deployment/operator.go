package deployment

import (
	"context"
	"errors"
	"fmt"
)

var (
	ErrDrainWorkflowIncomplete = errors.New("operational drain workflow is incomplete")
	ErrMigrationNotEntered     = errors.New("protected migration entry was not supplied")
)

// IngressController blocks the external HTTP ingress before backend drain.
type IngressController interface {
	BlockHTTPWrites(context.Context) error
}

// IngestionController is the narrow runtime seam implemented by the backend's
// MQTT service. It is intentionally an interface so the operator workflow can
// be rehearsed without a distributed coordinator.
type IngestionController interface {
	StopIngestion(context.Context) error
}

// RestartController disables automatic resurrection of old writers before
// quiescence is inspected.
type RestartController interface {
	SuppressRestarts(context.Context) error
}

// DirectWriterController controls known direct/legacy SQL writers. Unknown
// writers must be reported by Inspect and cause ProveQuiescence to fail.
type DirectWriterController interface {
	ControlDirectWriters(context.Context) error
}

// RuntimeInspector collects fresh process, ingress, broker, and database facts.
type RuntimeInspector interface {
	Inspect(context.Context) (DrainObservation, error)
}

// ProtectedMigration is the already-authorized D5 entry. The workflow never
// chooses a migration, runs generic Up, or performs SQL itself.
type ProtectedMigration func(context.Context) error

// DrainWorkflow is a single-process operator seam. It sequences local/external
// controls and only calls protected migration after fresh quiescence proof.
type DrainWorkflow struct {
	HTTP    *WriteGate
	Ingress IngressController
	MQTT    IngestionController
	Restart RestartController
	Direct  DirectWriterController
	Inspect RuntimeInspector
}

func (w DrainWorkflow) Execute(ctx context.Context, migrate ProtectedMigration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if w.HTTP == nil || w.MQTT == nil || w.Restart == nil || w.Direct == nil || w.Inspect == nil {
		return ErrDrainWorkflowIncomplete
	}
	if migrate == nil {
		return ErrMigrationNotEntered
	}
	w.HTTP.Block()
	if w.Ingress != nil {
		if err := w.Ingress.BlockHTTPWrites(ctx); err != nil {
			return fmt.Errorf("block HTTP ingress: %w", err)
		}
	}
	if err := w.MQTT.StopIngestion(ctx); err != nil {
		return fmt.Errorf("stop MQTT ingestion: %w", err)
	}
	if err := w.Restart.SuppressRestarts(ctx); err != nil {
		return fmt.Errorf("suppress old-writer restarts: %w", err)
	}
	if err := w.Direct.ControlDirectWriters(ctx); err != nil {
		return fmt.Errorf("control direct writers: %w", err)
	}
	observation, err := w.Inspect.Inspect(ctx)
	if err != nil {
		return fmt.Errorf("inspect drain state: %w", err)
	}
	if err := observation.ProveQuiescence(); err != nil {
		return err
	}
	if err := migrate(ctx); err != nil {
		return fmt.Errorf("protected migration: %w", err)
	}
	return nil
}

// ReopenGeneralWrites is the only workflow-level write reopening seam. It
// cannot reopen from controlled smoke alone and keeps the local HTTP gate
// blocked until every frozen gate is proven.
func ReopenGeneralWrites(gate *WriteGate, gates GeneralWriteGates) error {
	if gate == nil {
		return ErrGeneralWriteGateNotPassed
	}
	if reopened, err := GeneralWritesMayReopen(gates); !reopened {
		return err
	}
	gate.Reopen()
	return nil
}
