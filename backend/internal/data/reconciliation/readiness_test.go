package reconciliation

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"power-iot-backend/internal/data/private_migrations"
)

func cleanV5ReadinessRequest() ReadinessRequest {
	facts := FactSet{
		SchemaVersion: SchemaVersion,
		AsOf:          time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC),
	}
	_, digest, err := CanonicalSourceFacts(facts)
	if err != nil {
		panic(err)
	}
	request := ReadinessRequest{
		Target:         ReadinessForCutover,
		ProtectedState: migrations.ProtectedStateCleanV5,
		Facts:          facts,
		Reconciliation: ReconciliationEvidence{
			Outcome:               ExecutionCommittedAndVerified,
			Committed:             true,
			PostCommitVerified:    true,
			PlanID:                "plan-id",
			PlanDigest:            "plan-digest",
			SourceFactsDigest:     hex.EncodeToString(digest),
			MappingBasisDigest:    mappingBasisDigestForTest(facts),
			PostCommitFactsDigest: hex.EncodeToString(digest),
			PostCommitFactsAsOf:   facts.AsOf,
			PredicateIdentity:     D007PredicateIdentity,
			PredicateVersion:      D007PredicateVersion,
		},
	}
	request.Reconciliation = sealReconciliationEvidence(request.Reconciliation)
	return request
}

func mappingBasisDigestForTest(facts FactSet) string {
	digest, err := MappingSourceFactsDigest(facts)
	if err != nil {
		panic(err)
	}
	return hex.EncodeToString(digest)
}

func bindReadinessEvidence(request *ReadinessRequest) {
	_, digest, err := CanonicalSourceFacts(request.Facts)
	if err != nil {
		panic(err)
	}
	request.Reconciliation.SourceFactsDigest = hex.EncodeToString(digest)
	request.Reconciliation.MappingBasisDigest = mappingBasisDigestForTest(request.Facts)
	request.Reconciliation.PostCommitFactsDigest = hex.EncodeToString(digest)
	request.Reconciliation.PostCommitFactsAsOf = request.Facts.AsOf
	request.Reconciliation.PredicateIdentity = D007PredicateIdentity
	request.Reconciliation.PredicateVersion = D007PredicateVersion
	request.Reconciliation = sealReconciliationEvidence(request.Reconciliation)
}

func TestD007LiveCapabilityIsOpaqueBoundAndExactlyOnce(t *testing.T) {
	request := cleanV5ReadinessRequest()
	request.Facts.AsOf = time.Now().UTC()
	bindReadinessEvidence(&request)
	decision := EvaluateReadiness(request)
	if !decision.Ready {
		t.Fatalf("fixture readiness failed: %+v", decision)
	}
	var target [32]byte
	target[0] = 9
	binding := D007Binding{AttemptID: uuid.New(), TargetFingerprint: target, Generation: 4}
	// The legacy freshness argument is deliberately non-authoritative.
	capability, err := IssueLiveD007Capability(decision, request.Facts, binding, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got := capability.String(); got != "LIVE_D007_CAPABILITY[opaque]" {
		t.Fatalf("capability string=%q", got)
	}
	var factsDigest [32]byte
	_, digestBytes, _ := CanonicalSourceFacts(request.Facts)
	copy(factsDigest[:], digestBytes)
	expected := D007CapabilityBinding{D007Binding: binding, FactsDigest: factsDigest, ProofDigest: d007ProofDigest(decision, factsDigest), FreshUntil: d007FreshUntil(request.Facts), PredicateVersion: D007PredicateVersion}
	evidence, err := ConsumeLiveD007Capability(capability, expected, request.Facts.AsOf.Add(time.Minute))
	if err != nil || evidence.Kind != "TERMINAL_D007_VALIDATION_CONSUMPTION_EVIDENCE" || evidence.Status != "CONSUMED" {
		t.Fatalf("evidence=%+v err=%v", evidence, err)
	}
	// A copied presentation shares D2's one-shot state and cannot be reused.
	copyCapability := capability
	if _, err := ConsumeLiveD007Capability(copyCapability, expected, request.Facts.AsOf.Add(time.Minute)); !errors.Is(err, ErrD007CapabilityReused) {
		t.Fatalf("copied capability err=%v, want reuse rejection", err)
	}
	if _, err := ConsumeLiveD007Capability(LiveD007Capability{}, expected, request.Facts.AsOf.Add(time.Minute)); !errors.Is(err, ErrD007CapabilityInvalid) {
		t.Fatalf("forged capability err=%v, want invalid", err)
	}
}

func TestD007IssuanceRegistryRejectsDuplicateAndConcurrentIssue(t *testing.T) {
	request := cleanV5ReadinessRequest()
	request.Facts.AsOf = time.Now().UTC()
	bindReadinessEvidence(&request)
	decision := EvaluateReadiness(request)
	issuer := NewD007CapabilityIssuer()
	var target [32]byte
	target[0] = 0x44
	binding := D007Binding{AttemptID: uuid.New(), TargetFingerprint: target, Generation: 1}
	const attempts = 16
	results := make(chan error, attempts)
	for index := 0; index < attempts; index++ {
		go func() {
			_, err := issuer.issue(decision, request.Facts, binding)
			results <- err
		}()
	}
	successes := 0
	duplicates := 0
	for index := 0; index < attempts; index++ {
		switch err := <-results; {
		case err == nil:
			successes++
		case errors.Is(err, ErrD007CapabilityIssued):
			duplicates++
		default:
			t.Fatalf("concurrent issue err=%v", err)
		}
	}
	if successes != 1 || duplicates != attempts-1 {
		t.Fatalf("successes=%d duplicates=%d", successes, duplicates)
	}
}

func TestD007FailedValidationTerminalizesCapability(t *testing.T) {
	request := cleanV5ReadinessRequest()
	request.Facts.AsOf = time.Now().UTC()
	bindReadinessEvidence(&request)
	decision := EvaluateReadiness(request)
	var target [32]byte
	target[0] = 0x45
	binding := D007Binding{AttemptID: uuid.New(), TargetFingerprint: target, Generation: 1}
	issuer := NewD007CapabilityIssuer()
	capability, err := issuer.issue(decision, request.Facts, binding)
	if err != nil {
		t.Fatal(err)
	}
	_, digestBytes, _ := CanonicalSourceFacts(request.Facts)
	var digest [32]byte
	copy(digest[:], digestBytes)
	expected := D007CapabilityBinding{D007Binding: binding, FactsDigest: digest, ProofDigest: d007ProofDigest(decision, digest), FreshUntil: d007FreshUntil(request.Facts), PredicateVersion: D007PredicateVersion}
	mismatch := expected
	mismatch.Generation++
	if _, err := ConsumeLiveD007Capability(capability, mismatch, time.Time{}); !errors.Is(err, ErrD007CapabilityInvalid) {
		t.Fatalf("first mismatch err=%v", err)
	}
	if _, err := ConsumeLiveD007Capability(capability, expected, time.Time{}); !errors.Is(err, ErrD007CapabilityReused) {
		t.Fatalf("retry after mismatch err=%v", err)
	}

	expired, err := issuer.issue(decision, request.Facts, D007Binding{AttemptID: uuid.New(), TargetFingerprint: target, Generation: 2})
	if err != nil {
		t.Fatal(err)
	}
	expiredExpected := expected
	expiredExpected.D007Binding.AttemptID = expired.state.binding.AttemptID
	expiredExpected.D007Binding.Generation = 2
	expiredExpected.FreshUntil = time.Now().UTC().Add(-time.Second)
	expired.state.binding.FreshUntil = expiredExpected.FreshUntil
	if _, err := ConsumeLiveD007Capability(expired, expiredExpected, time.Time{}); !errors.Is(err, ErrD007CapabilityExpired) {
		t.Fatalf("first expired err=%v", err)
	}
	if _, err := ConsumeLiveD007Capability(expired, expiredExpected, time.Time{}); !errors.Is(err, ErrD007CapabilityReused) {
		t.Fatalf("retry after expiry err=%v", err)
	}
}

func TestD007ConsumptionIsExactlyOnceUnderConcurrency(t *testing.T) {
	request := cleanV5ReadinessRequest()
	request.Facts.AsOf = time.Now().UTC()
	bindReadinessEvidence(&request)
	decision := EvaluateReadiness(request)
	issuer := NewD007CapabilityIssuer()
	var target [32]byte
	target[0] = 0x46
	binding := D007Binding{AttemptID: uuid.New(), TargetFingerprint: target, Generation: 1}
	capability, err := issuer.issue(decision, request.Facts, binding)
	if err != nil {
		t.Fatal(err)
	}
	_, digestBytes, _ := CanonicalSourceFacts(request.Facts)
	var digest [32]byte
	copy(digest[:], digestBytes)
	expected := D007CapabilityBinding{D007Binding: binding, FactsDigest: digest, ProofDigest: d007ProofDigest(decision, digest), FreshUntil: d007FreshUntil(request.Facts), PredicateVersion: D007PredicateVersion}
	const attempts = 16
	results := make(chan error, attempts)
	for index := 0; index < attempts; index++ {
		go func() {
			_, consumeErr := ConsumeLiveD007Capability(capability, expected, time.Time{})
			results <- consumeErr
		}()
	}
	successes := 0
	reused := 0
	for index := 0; index < attempts; index++ {
		switch err := <-results; {
		case err == nil:
			successes++
		case errors.Is(err, ErrD007CapabilityReused):
			reused++
		default:
			t.Fatalf("concurrent consume err=%v", err)
		}
	}
	if successes != 1 || reused != attempts-1 {
		t.Fatalf("successes=%d reused=%d", successes, reused)
	}
}

func TestD007IssueRejectsDecisionAndFactsSnapshotMismatch(t *testing.T) {
	request := cleanV5ReadinessRequest()
	request.Facts.AsOf = time.Now().UTC()
	bindReadinessEvidence(&request)
	decision := EvaluateReadiness(request)
	if !decision.Ready {
		t.Fatalf("fixture readiness failed: %+v", decision)
	}
	var target [32]byte
	target[0] = 8
	binding := D007Binding{AttemptID: uuid.New(), TargetFingerprint: target, Generation: 1}
	cases := []struct {
		name   string
		mutate func(*FactSet)
	}{
		{name: "different as-of", mutate: func(facts *FactSet) { facts.AsOf = facts.AsOf.Add(time.Second) }},
		{name: "dirty facts", mutate: func(facts *FactSet) { facts.SchemaVersion = "v5-dirty" }},
		{name: "fabricated row", mutate: func(facts *FactSet) { facts.Clients = []ClientFact{{ID: 1}} }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			facts := request.Facts
			test.mutate(&facts)
			if _, err := IssueLiveD007Capability(decision, facts, binding, time.Time{}); !errors.Is(err, ErrD007CapabilityInvalid) {
				t.Fatalf("err=%v, want snapshot mismatch rejection", err)
			}
		})
	}
}

func TestD007IssueRejectsMutatedOrReplacedReadinessChecks(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ReadinessDecision)
	}{
		{
			name: "mutated check",
			mutate: func(decision *ReadinessDecision) {
				decision.Checks[0].Reason = "forged reason"
			},
		},
		{
			name: "replaced checks",
			mutate: func(decision *ReadinessDecision) {
				decision.Checks = []ReadinessCheck{{Name: "forged-check", Passed: true}}
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			request := cleanV5ReadinessRequest()
			request.Facts.AsOf = time.Now().UTC()
			bindReadinessEvidence(&request)
			decision := EvaluateReadiness(request)
			if !decision.Ready {
				t.Fatalf("fixture readiness failed: %+v", decision)
			}
			test.mutate(&decision)
			var target [32]byte
			target[0] = 0x47
			binding := D007Binding{AttemptID: uuid.New(), TargetFingerprint: target, Generation: 1}
			if _, err := IssueLiveD007Capability(decision, request.Facts, binding, time.Time{}); !errors.Is(err, ErrD007CapabilityInvalid) {
				t.Fatalf("err=%v, want mutated checks rejection", err)
			}
		})
	}
}

func TestD007LiveCapabilityRejectsStaleExpiredAndMismatchedValidation(t *testing.T) {
	request := cleanV5ReadinessRequest()
	request.Facts.AsOf = time.Now().UTC()
	bindReadinessEvidence(&request)
	decision := EvaluateReadiness(request)
	var target [32]byte
	target[0] = 7
	binding := D007Binding{AttemptID: uuid.New(), TargetFingerprint: target, Generation: 2}
	freshUntil := request.Facts.AsOf.Add(time.Hour)
	capability, err := IssueLiveD007Capability(decision, request.Facts, binding, freshUntil)
	if err != nil {
		t.Fatal(err)
	}
	_, digestBytes, _ := CanonicalSourceFacts(request.Facts)
	var digest [32]byte
	copy(digest[:], digestBytes)
	expected := D007CapabilityBinding{D007Binding: binding, FactsDigest: digest, ProofDigest: d007ProofDigest(decision, digest), FreshUntil: d007FreshUntil(request.Facts), PredicateVersion: D007PredicateVersion}
	mismatch := expected
	mismatch.Generation++
	if _, err := ConsumeLiveD007Capability(capability, mismatch, request.Facts.AsOf.Add(time.Minute)); !errors.Is(err, ErrD007CapabilityInvalid) {
		t.Fatalf("mismatch err=%v, want invalid", err)
	}
	staleFacts := request.Facts
	staleFacts.AsOf = staleFacts.AsOf.Add(2 * time.Hour)
	staleExpected := expected
	staleExpected.FactsDigest[0] ^= 1
	if _, err := ConsumeLiveD007Capability(capability, staleExpected, request.Facts.AsOf.Add(time.Minute)); !errors.Is(err, ErrD007CapabilityReused) {
		t.Fatalf("stale facts err=%v, want terminal reuse rejection", err)
	}
	// The mismatched validation already terminalized the capability; it cannot
	// be retried with the originally correct tuple.
	if _, err := ConsumeLiveD007Capability(capability, expected, time.Unix(0, 0)); !errors.Is(err, ErrD007CapabilityReused) {
		t.Fatalf("retry err=%v, want terminal reuse rejection", err)
	}
}

func TestEvaluateReadinessRejectsFabricatedOrMismatchedPostCommitEvidence(t *testing.T) {
	request := cleanV5ReadinessRequest()
	cases := []func(*ReconciliationEvidence){
		func(e *ReconciliationEvidence) { e.PostCommitFactsDigest = strings.Repeat("0", 64) },
		func(e *ReconciliationEvidence) { e.PostCommitFactsAsOf = e.PostCommitFactsAsOf.Add(time.Second) },
		func(e *ReconciliationEvidence) { e.Outcome = ExecutionCommittedPostVerifyFailed },
	}
	for index, mutate := range cases {
		t.Run(fmt.Sprintf("case-%d", index), func(t *testing.T) {
			candidate := request
			mutate(&candidate.Reconciliation)
			if decision := EvaluateReadiness(candidate); decision.Ready {
				t.Fatalf("fabricated evidence passed readiness: %+v", decision)
			}
		})
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
	bindReadinessEvidence(&request)
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
	bindReadinessEvidence(&request)
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
