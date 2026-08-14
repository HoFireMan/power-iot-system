package reconciliation

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"power-iot-backend/internal/data/migrations"
)

func cleanV5ReadinessRequest() ReadinessRequest {
	return ReadinessRequest{
		Target:         ReadinessForCutover,
		ProtectedState: migrations.ProtectedStateCleanV5,
		Facts: FactSet{
			SchemaVersion: SchemaVersion,
			AsOf:          time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC),
		},
		Reconciliation: ReconciliationEvidence{
			Outcome:            ExecutionCommittedAndVerified,
			Committed:          true,
			PostCommitVerified: true,
			PlanID:             "plan-id",
			PlanDigest:         "plan-digest",
			SourceFactsDigest:  "facts-digest",
		},
	}
}

func TestEvaluateReadinessRequiresCleanProtectedStateAndCommittedA2(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ReadinessRequest)
	}{
		{
			name: "dirty v5",
			mutate: func(request *ReadinessRequest) {
				request.ProtectedState = migrations.ProtectedStateDirtyV5
			},
		},
		{
			name: "transition dirty v6",
			mutate: func(request *ReadinessRequest) {
				request.ProtectedState = migrations.ProtectedStateTransitionV6
			},
		},
		{
			name: "ambiguous metadata",
			mutate: func(request *ReadinessRequest) {
				request.ProtectedState = migrations.ProtectedStateAmbiguous
			},
		},
		{
			name: "future metadata",
			mutate: func(request *ReadinessRequest) {
				request.ProtectedState = migrations.ProtectedStateFuture
			},
		},
		{
			name: "missing a2 commit proof",
			mutate: func(request *ReadinessRequest) {
				request.Reconciliation = ReconciliationEvidence{}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := cleanV5ReadinessRequest()
			test.mutate(&request)
			decision := EvaluateReadiness(request)
			if decision.Ready {
				t.Fatalf("readiness unexpectedly passed: %+v", decision)
			}
			if len(decision.FailedPredicates()) == 0 {
				t.Fatalf("failed readiness had no failed predicate: %+v", decision)
			}
		})
	}
}

func TestEvaluateReadinessRejectsUnresolvedSemanticFactsAndMappings(t *testing.T) {
	request := cleanV5ReadinessRequest()
	request.Facts.Shops = []ShopFact{{ID: 1}}
	decision := EvaluateReadiness(request)
	if decision.Ready {
		t.Fatalf("readiness passed for unresolved Shop Client: %+v", decision)
	}
	failed := decision.FailedPredicates()
	if len(failed) != 2 || failed[0] != "semantic-data-predicates" || failed[1] != "deterministic-post-a2-plan" {
		t.Fatalf("failed predicates=%v, want semantic and deterministic-plan failures", failed)
	}
}

func TestEvaluateReadinessRejectsUncommittedAutoReconcilableFacts(t *testing.T) {
	request := cleanV5ReadinessRequest()
	pointID := migrationsTestUUID(1)
	request.Facts.Clients = []ClientFact{{ID: 1}}
	request.Facts.Shops = []ShopFact{{ID: 1, ClientID: uintPointer(1)}}
	request.Facts.Devices = []DeviceFact{{ID: 1}}
	request.Facts.MeasurementPoints = []MeasurementPointFact{{ID: pointID, ShopID: 1}}
	request.Facts.DeviceAssignments = []DeviceAssignmentFact{{
		ID:                 migrationsTestUUID(2),
		DeviceID:           1,
		MeasurementPointID: pointID,
		ValidFrom:          request.Facts.AsOf.Add(-time.Hour),
	}}
	decision := EvaluateReadiness(request)
	if decision.Ready {
		t.Fatalf("readiness passed while an auto-reconcilable owner remained: %+v", decision)
	}
}

func uintPointer(value uint) *uint { return &value }

func migrationsTestUUID(value byte) uuid.UUID {
	id := uuid.Nil
	id[15] = value
	return id
}

func TestEvaluateReadinessServingRequiresCleanV6AndFinalSemanticProof(t *testing.T) {
	request := cleanV5ReadinessRequest()
	request.Target = ReadinessForServing
	request.ProtectedState = migrations.ProtectedStateCleanV6
	if decision := EvaluateReadiness(request); decision.Ready {
		t.Fatal("v6 serving readiness passed without final semantic proof")
	}
	request.PostCutoverSemanticVerified = true
	decision := EvaluateReadiness(request)
	if !decision.Ready {
		t.Fatalf("clean v6 serving readiness failed: %+v", decision)
	}
	request.ProtectedState = migrations.ProtectedStateCleanV5
	if decision := EvaluateReadiness(request); decision.Ready {
		t.Fatal("v5 was admitted as clean v6 serving")
	}
}

func TestRequireReadinessPreservesFailClosedEvidenceCategories(t *testing.T) {
	request := cleanV5ReadinessRequest()
	request.Reconciliation = ReconciliationEvidence{}
	decision, err := RequireReadiness(request)
	if decision.Ready || !errors.Is(err, ErrReadinessNotMet) || !errors.Is(err, ErrReadinessEvidence) {
		t.Fatalf("decision=%+v err=%v, want readiness and evidence errors", decision, err)
	}
}

func TestEvaluateReadinessRejectsUnknownTarget(t *testing.T) {
	request := cleanV5ReadinessRequest()
	request.Target = ReadinessTarget("D4")
	decision := EvaluateReadiness(request)
	if decision.Ready || len(decision.FailedPredicates()) != 1 {
		t.Fatalf("unsupported target decision=%+v", decision)
	}
}

func TestLifecycleBlocksAbnormalProtectedStatesAndWriters(t *testing.T) {
	states := []migrations.ProtectedMigrationState{
		migrations.ProtectedStateDirtyV5,
		migrations.ProtectedStateTransitionV6,
		migrations.ProtectedStateAmbiguous,
		migrations.ProtectedStateBootstrap,
		migrations.ProtectedStateFuture,
	}
	for _, protectedState := range states {
		t.Run(string(protectedState), func(t *testing.T) {
			lifecycle := NewLifecycleFromProtectedState(protectedState)
			if lifecycle.State() != LifecycleBlocked || lifecycle.WriterAdmission() != WriterAdmissionDenied {
				t.Fatalf("protected state=%s lifecycle=%s admission=%s", protectedState, lifecycle.State(), lifecycle.WriterAdmission())
			}
			if err := lifecycle.BeginReadiness(); !errors.Is(err, ErrLifecycleBlocked) {
				t.Fatalf("BeginReadiness error=%v, want fail-closed lifecycle error", err)
			}
			if err := lifecycle.RequireWriterAdmission(WriterAdmissionProtectedA2, migrations.ProtectedWorkCapability{}); !errors.Is(err, ErrLifecycleWriterDenied) {
				t.Fatalf("writer admission error=%v, want denial", err)
			}
		})
	}
}

func TestLifecycleNeverStartsProtectedWriterWithoutLiveFence(t *testing.T) {
	lifecycle := NewLifecycleFromProtectedState(migrations.ProtectedStateCleanV5)
	if lifecycle.WriterAdmission() != WriterAdmissionDenied {
		t.Fatal("clean v5 lifecycle exposed a writer lane before protected admission")
	}
	if err := lifecycle.AdmitProtectedReconciliation(migrations.ProtectedWorkCapability{}, migrations.ExternalWriterAdmission{}); !errors.Is(err, ErrLifecycleWriterDenied) {
		t.Fatalf("zero capability error=%v, want writer denial", err)
	}
	if lifecycle.State() != LifecycleCleanV5 {
		t.Fatalf("failed admission changed lifecycle state to %s", lifecycle.State())
	}
	if err := lifecycle.RequireWriterAdmission(WriterAdmissionProtectedA2, migrations.ProtectedWorkCapability{}); !errors.Is(err, ErrLifecycleWriterDenied) {
		t.Fatalf("protected writer error=%v, want writer denial", err)
	}
}

func TestLifecycleCleanV6DoesNotBypassD1D6WriterAdmission(t *testing.T) {
	lifecycle := NewLifecycleFromProtectedState(migrations.ProtectedStateCleanV6)
	if lifecycle.WriterAdmission() != WriterAdmissionDenied {
		t.Fatalf("clean v6 admission=%s, want D2 denial", lifecycle.WriterAdmission())
	}
	if err := lifecycle.RequireWriterAdmission(WriterAdmissionProtectedA2, migrations.ProtectedWorkCapability{}); !errors.Is(err, ErrLifecycleWriterDenied) {
		t.Fatalf("clean v6 A2 lane error=%v, want denial", err)
	}
}

func TestLifecycleRequiresProtectedOwnershipBeforeReadiness(t *testing.T) {
	lifecycle := NewLifecycleFromProtectedState(migrations.ProtectedStateCleanV5)
	if err := lifecycle.AdmitProtectedReconciliation(migrations.ProtectedWorkCapability{}, migrations.ExternalWriterAdmission{}); !errors.Is(err, ErrLifecycleWriterDenied) {
		t.Fatalf("initial admission error=%v", err)
	}
	// A lifecycle that cannot establish protected ownership never reaches the
	// readiness state; this assertion protects the no-writer-before-fence seam.
	if lifecycle.State() != LifecycleCleanV5 {
		t.Fatalf("lifecycle state=%s after failed protected admission", lifecycle.State())
	}
}

func TestApplyReadinessRejectsCallerForgedSuccess(t *testing.T) {
	lifecycle := Lifecycle{state: LifecycleReadinessCheck}
	forged := ReadinessDecision{Target: ReadinessForCutover, Ready: true}
	if err := lifecycle.ApplyReadiness(forged); !errors.Is(err, ErrReadinessNotMet) {
		t.Fatalf("forged readiness error=%v, want readiness refusal", err)
	}
	if lifecycle.State() != LifecycleReadinessFailed {
		t.Fatalf("forged readiness changed state to %s, want failed", lifecycle.State())
	}
}
