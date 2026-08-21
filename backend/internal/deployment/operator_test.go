package deployment

import (
	"context"
	"reflect"
	"testing"
)

type workflowStep struct {
	steps *[]string
	name  string
	err   error
}

func (s workflowStep) run(context.Context) error {
	*s.steps = append(*s.steps, s.name)
	return s.err
}

type workflowInspector struct {
	steps *[]string
	obs   DrainObservation
}

func (i workflowInspector) Inspect(context.Context) (DrainObservation, error) {
	*i.steps = append(*i.steps, "inspect")
	return i.obs, nil
}

type workflowMQTT struct{ workflowStep }

func (m workflowMQTT) StopIngestion(ctx context.Context) error { return m.run(ctx) }

type workflowRestart struct{ workflowStep }

func (r workflowRestart) SuppressRestarts(ctx context.Context) error { return r.run(ctx) }

type workflowDirect struct{ workflowStep }

func (d workflowDirect) ControlDirectWriters(ctx context.Context) error { return d.run(ctx) }

func completeObservation() DrainObservation {
	return DrainObservation{
		HTTPWritesBlocked: true, MQTTIngestionBlocked: true, RestartsSuppressed: true,
		DirectSQLControlled: true, ProcessStateInspected: true, IngressStateInspected: true,
		BrokerStateInspected: true, DatabaseStateInspected: true,
	}
}

func TestDrainWorkflowSequencesControlsBeforeProtectedMigration(t *testing.T) {
	steps := []string{}
	workflow := DrainWorkflow{
		HTTP:    NewWriteGate(false),
		MQTT:    workflowMQTT{workflowStep{steps: &steps, name: "mqtt", err: nil}},
		Restart: workflowRestart{workflowStep{steps: &steps, name: "restart", err: nil}},
		Direct:  workflowDirect{workflowStep{steps: &steps, name: "direct", err: nil}},
		Inspect: workflowInspector{steps: &steps, obs: completeObservation()},
	}
	if err := workflow.Execute(context.Background(), func(context.Context) error {
		steps = append(steps, "migration")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"mqtt", "restart", "direct", "inspect", "migration"}
	if !reflect.DeepEqual(steps, want) {
		t.Fatalf("steps=%v, want %v", steps, want)
	}
	if !workflow.HTTP.Blocked() {
		t.Fatal("HTTP writes reopened before explicit cutover")
	}
}

func TestDrainWorkflowRefusesMigrationWhenQuiescenceIsIncomplete(t *testing.T) {
	called := false
	workflow := DrainWorkflow{
		HTTP:    NewWriteGate(false),
		MQTT:    workflowMQTT{workflowStep{steps: &[]string{}, name: "mqtt", err: nil}},
		Restart: workflowRestart{workflowStep{steps: &[]string{}, name: "restart", err: nil}},
		Direct:  workflowDirect{workflowStep{steps: &[]string{}, name: "direct", err: nil}},
		Inspect: workflowInspector{steps: &[]string{}, obs: func() DrainObservation { o := completeObservation(); o.UnknownWriters = 1; return o }()},
	}
	if err := workflow.Execute(context.Background(), func(context.Context) error { called = true; return nil }); err == nil {
		t.Fatal("incomplete quiescence accepted")
	}
	if called {
		t.Fatal("protected migration called before quiescence")
	}
}

func TestReopenGeneralWritesRequiresAllFrozenGates(t *testing.T) {
	gate := NewWriteGate(true)
	if err := ReopenGeneralWrites(gate, readyGates()); err != nil {
		t.Fatal(err)
	}
	if gate.Blocked() {
		t.Fatal("complete gates did not reopen writes")
	}
	gate.Block()
	gates := readyGates()
	gates.ControlledSmokePassed = false
	if err := ReopenGeneralWrites(gate, gates); err == nil {
		t.Fatal("smoke-only state reopened writes")
	}
	if !gate.Blocked() {
		t.Fatal("gate reopened after incomplete cutover")
	}
}
