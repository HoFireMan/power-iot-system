package reconciliation

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"power-iot-backend/internal/data/private_migrations"
	"power-iot-backend/internal/data/reconciliation/sourceowner"
)

func protectedAdmissionFixture(t *testing.T) (FactSet, ProtectedAdmissionContext, time.Time) {
	t.Helper()
	facts := testFacts()
	// Production admission uses the owner clock for freshness; keep this
	// in-memory fixture current without changing shared classification fixtures.
	facts.AsOf = time.Now().UTC()
	owner := uint(10)
	facts.Devices[0].InventoryOwnerClientID = &owner
	now := facts.AsOf
	request := ProtectedAdmissionContext{
		Route:            ProtectedRouteCanonical,
		OperationID:      uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"),
		AttemptID:        uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"),
		TargetID:         1,
		Generation:       1,
		ObservedAt:       now,
		FreshUntil:       now.Add(time.Minute),
		CallerAuthorized: false,
	}
	request.Source = trustedSnapshotForTest(t, facts, request)
	request.Eligibility = testActiveD1Eligibility(request, now)
	return facts, request, now
}

func testActiveD1Eligibility(request ProtectedAdmissionContext, now time.Time) ProtectedD1Eligibility {
	// D1 issuance/provenance evidence is deliberately distinct from the fresh
	// source-owner classification evidence supplied in request.Source.
	targetFingerprint := []byte("01234567890123456789012345678901")
	issuanceEvidenceDigest := []byte(strings.Repeat("a", 32))
	identity := migrations.D1LLeaseIdentity{
		LeaseID: uuid.MustParse("cccccccc-cccc-cccc-cccc-cccccccccccc"), OperationID: request.OperationID,
		AttemptID: request.AttemptID, Generation: int64(request.Generation),
		TargetFingerprint: append([]byte(nil), targetFingerprint...), EvidenceDigest: append([]byte(nil), issuanceEvidenceDigest...),
	}
	inspection := migrations.D1LLeaseInspection{
		Identity: migrations.D1LLeaseIdentity{
			LeaseID: identity.LeaseID, OperationID: identity.OperationID, AttemptID: identity.AttemptID, Generation: identity.Generation,
			TargetFingerprint: append([]byte(nil), identity.TargetFingerprint...), EvidenceDigest: append([]byte(nil), identity.EvidenceDigest...),
		},
		TargetFingerprint: append([]byte(nil), targetFingerprint...), EvidenceDigest: append([]byte(nil), issuanceEvidenceDigest...),
		Status: "ACTIVE", IssuedAt: now.Add(-time.Second), ExpiresAt: now.Add(time.Minute), ActivatedAt: now.Add(-time.Millisecond),
	}
	return ProtectedD1Eligibility{identity: identity, inspect: func(context.Context, migrations.D1LLeaseIdentity) (migrations.D1LLeaseInspection, error) {
		return inspection, nil
	}}
}

func TestCanonicalAdmissionAllowsFreshClassificationEvidenceDistinctFromD1IssuanceEvidence(t *testing.T) {
	_, request, now := protectedAdmissionFixture(t)
	currentClassificationDigest := request.Source.Digest()
	if string(currentClassificationDigest[:]) == string(request.Eligibility.identity.EvidenceDigest) {
		t.Fatal("fixture must keep current classification evidence distinct from D1 issuance evidence")
	}

	result := NewCanonicalProtectedAdmission().Admit(request, now)
	if result.Status != AdmissionAllowed || result.Classification != ProtectedClassificationOwnedPlaced {
		t.Fatalf("result=%+v", result)
	}
}

func TestCanonicalAdmissionAllowsLaterFreshClassificationEvidence(t *testing.T) {
	facts, request, now := protectedAdmissionFixture(t)
	// The D1 fixture was issued before this observation. A later source-owner
	// observation changes the current digest while preserving every D1 binding.
	facts.AsOf = now.Add(-100 * time.Millisecond)
	request.ObservedAt = facts.AsOf
	request.Source = trustedSnapshotForTest(t, facts, request)
	request.Eligibility = testActiveD1Eligibility(request, now)

	currentClassificationDigest := request.Source.Digest()
	if string(currentClassificationDigest[:]) == string(request.Eligibility.identity.EvidenceDigest) {
		t.Fatal("later source evidence unexpectedly equals issuance evidence")
	}
	result := NewCanonicalProtectedAdmission().Admit(request, now)
	if result.Status != AdmissionAllowed || result.Classification != ProtectedClassificationOwnedPlaced {
		t.Fatalf("result=%+v", result)
	}
}

func TestZeroValuedCanonicalAdmissionDefensivelyInitializesAttemptLedger(t *testing.T) {
	_, request, now := protectedAdmissionFixture(t)
	var service CanonicalProtectedAdmission

	result := service.Admit(request, now)
	if result.Status != AdmissionAllowed || result.Classification != ProtectedClassificationOwnedPlaced {
		t.Fatalf("zero-valued service result=%+v", result)
	}
	if replay := service.Admit(request, now); replay.Status != AdmissionDenied || replay.Classification != ProtectedClassificationInvalid {
		t.Fatalf("zero-valued service replay=%+v", replay)
	}
}

func TestMissingD1EligibilityCannotAuthorize(t *testing.T) {
	_, request, now := protectedAdmissionFixture(t)
	request.Eligibility = ProtectedD1Eligibility{}
	result := NewCanonicalProtectedAdmission().Admit(request, now)
	if result.Status != AdmissionDenied || result.Classification != ProtectedClassificationInvalid {
		t.Fatalf("result=%+v", result)
	}
}

func TestActiveD1EligibilityIsOwnerBoundAndCallerGenerationDoesNotAuthorize(t *testing.T) {
	_, request, now := protectedAdmissionFixture(t)
	request.Generation = 99
	result := NewCanonicalProtectedAdmission().Admit(request, now)
	if result.Status != AdmissionAllowed || result.Generation != 1 {
		t.Fatalf("result=%+v", result)
	}
}

func TestD1EligibilityRejectsStatusAndDigestSubstitution(t *testing.T) {
	_, request, now := protectedAdmissionFixture(t)
	for _, status := range []string{"ISSUED", "EXPIRED", "REVOKED", "QUARANTINED", "CONSUMED", "UNKNOWN"} {
		candidate := request
		base := request.Eligibility
		candidate.Eligibility.inspect = func(context.Context, migrations.D1LLeaseIdentity) (migrations.D1LLeaseInspection, error) {
			inspection, _ := base.inspectOwner(context.Background())
			inspection.Status = status
			return inspection, nil
		}
		result := NewCanonicalProtectedAdmission().Admit(candidate, now)
		if result.Status != AdmissionDenied {
			t.Fatalf("status=%s result=%+v", status, result)
		}
	}

	candidate := request
	base := request.Eligibility
	candidate.Eligibility.inspect = func(context.Context, migrations.D1LLeaseIdentity) (migrations.D1LLeaseInspection, error) {
		inspection, _ := base.inspectOwner(context.Background())
		inspection.EvidenceDigest[0] ^= 1
		inspection.Identity.EvidenceDigest[0] ^= 1
		return inspection, nil
	}
	result := NewCanonicalProtectedAdmission().Admit(candidate, now)
	if result.Status != AdmissionDenied {
		t.Fatalf("digest substitution result=%+v", result)
	}
}

func TestD1EligibilityRejectsOwnerBindingSubstitution(t *testing.T) {
	_, request, now := protectedAdmissionFixture(t)
	cases := []struct {
		name string
		edit func(*migrations.D1LLeaseInspection)
	}{
		{name: "wrong lease", edit: func(inspection *migrations.D1LLeaseInspection) {
			inspection.Identity.LeaseID = uuid.MustParse("dddddddd-dddd-dddd-dddd-dddddddddddd")
		}},
		{name: "wrong operation", edit: func(inspection *migrations.D1LLeaseInspection) {
			inspection.Identity.OperationID = uuid.MustParse("eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee")
		}},
		{name: "wrong attempt", edit: func(inspection *migrations.D1LLeaseInspection) {
			inspection.Identity.AttemptID = uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")
		}},
		{name: "wrong generation", edit: func(inspection *migrations.D1LLeaseInspection) {
			inspection.Identity.Generation++
		}},
		{name: "wrong target fingerprint", edit: func(inspection *migrations.D1LLeaseInspection) {
			inspection.TargetFingerprint[0] ^= 1
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidate := request
			base := request.Eligibility
			candidate.Eligibility.inspect = func(context.Context, migrations.D1LLeaseIdentity) (migrations.D1LLeaseInspection, error) {
				inspection, err := base.inspectOwner(context.Background())
				if err != nil {
					return migrations.D1LLeaseInspection{}, err
				}
				tc.edit(&inspection)
				return inspection, nil
			}
			result := NewCanonicalProtectedAdmission().Admit(candidate, now)
			if result.Status != AdmissionDenied || result.Classification != ProtectedClassificationInvalid {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}

func TestUpstreamConsumedEvidenceIsNotConsumedAgainByPR1(t *testing.T) {
	_, request, now := protectedAdmissionFixture(t)
	if err := request.Source.UseForInvocation(sourceowner.NewInvocationBinding(request.OperationID, request.AttemptID), now); err != nil {
		t.Fatal(err)
	}
	result := NewCanonicalProtectedAdmission().Admit(request, now)
	if result.Status != AdmissionAllowed {
		t.Fatalf("result=%+v", result)
	}
}

func TestCallerConstructedFactSetCannotAuthorize(t *testing.T) {
	facts, request, now := protectedAdmissionFixture(t)
	owner := uint(10)
	facts.Devices[0].InventoryOwnerClientID = &owner
	// The raw FactSet is intentionally not placed in an admission context. A
	// zero TrustedSourceSnapshot has no source provenance and cannot authorize.
	request.Source = TrustedSourceSnapshot{}

	result := NewCanonicalProtectedAdmission().Admit(request, now)
	if result.Status != AdmissionDenied || result.Classification != ProtectedClassificationInvalid {
		t.Fatalf("result=%+v", result)
	}
}

func TestCallerBooleanCannotCompensateForMissingSourceEvidence(t *testing.T) {
	_, request, now := protectedAdmissionFixture(t)
	request.Source = TrustedSourceSnapshot{}
	request.CallerAuthorized = true

	result := NewCanonicalProtectedAdmission().Admit(request, now)
	if result.Status != AdmissionDenied {
		t.Fatalf("result=%+v", result)
	}
}

func TestConsumerCannotConstructTrustedOwnershipEvidence(t *testing.T) {
	_, request, now := protectedAdmissionFixture(t)
	request.Source = TrustedSourceSnapshot{}

	result := NewCanonicalProtectedAdmission().Admit(request, now)
	if result.Status != AdmissionDenied || result.Classification != ProtectedClassificationInvalid {
		t.Fatalf("result=%+v", result)
	}
}

func TestConsumerCannotConstructTrustedPlacementEvidence(t *testing.T) {
	_, request, now := protectedAdmissionFixture(t)
	request.Source = TrustedSourceSnapshot{}

	result := NewCanonicalProtectedAdmission().Admit(request, now)
	if result.Status != AdmissionDenied || result.Classification != ProtectedClassificationInvalid {
		t.Fatalf("result=%+v", result)
	}
}

func TestStaleSourceSnapshotCannotBecomeFreshByChangingContextTimes(t *testing.T) {
	facts, request, now := protectedAdmissionFixture(t)
	stale := facts
	stale.AsOf = now.Add(-time.Hour)
	request.Source = trustedSnapshotForTest(t, stale)
	request.ObservedAt = now
	request.FreshUntil = now.Add(time.Hour)

	result := NewCanonicalProtectedAdmission().Admit(request, now)
	if result.Status != AdmissionDenied {
		t.Fatalf("result=%+v", result)
	}
}

func TestConsumerCannotMutateSourceEvidenceFacts(t *testing.T) {
	_, request, now := protectedAdmissionFixture(t)
	facts := request.Source.Facts()
	facts.Devices[0].InventoryOwnerClientID = nil

	if request.Source.Facts().Devices[0].InventoryOwnerClientID == nil {
		t.Fatal("source evidence exposed mutable facts")
	}
	result := NewCanonicalProtectedAdmission().Admit(request, now)
	if result.Status != AdmissionAllowed || result.Classification != ProtectedClassificationOwnedPlaced {
		t.Fatalf("result=%+v", result)
	}
}

func TestCanonicalAdmissionRejectsInvalidContext(t *testing.T) {
	_, request, now := protectedAdmissionFixture(t)
	service := NewCanonicalProtectedAdmission()

	cases := []struct {
		name string
		edit func(*ProtectedAdmissionContext)
	}{
		{"missing operation", func(r *ProtectedAdmissionContext) { r.OperationID = uuid.Nil }},
		{"missing attempt", func(r *ProtectedAdmissionContext) { r.AttemptID = uuid.Nil }},
		{"missing target", func(r *ProtectedAdmissionContext) { r.TargetID = 0 }},
		{"missing generation", func(r *ProtectedAdmissionContext) { r.Generation = 0 }},
		{"missing source evidence", func(r *ProtectedAdmissionContext) { r.Source = TrustedSourceSnapshot{} }},

		{"unknown route", func(r *ProtectedAdmissionContext) { r.Route = ProtectedRoute("unknown") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidate := request
			tc.edit(&candidate)
			result := service.Admit(candidate, now)
			if result.Status != AdmissionDenied {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}

func TestCanonicalAdmissionRejectsStaleSourceOwnerEvidence(t *testing.T) {
	facts, request, now := protectedAdmissionFixture(t)
	facts.AsOf = now.Add(-time.Hour)
	request.ObservedAt = facts.AsOf
	request.FreshUntil = now.Add(time.Hour)
	request.Source = trustedSnapshotForTest(t, facts, request)

	result := NewCanonicalProtectedAdmission().Admit(request, now)
	if result.Status != AdmissionDenied {
		t.Fatalf("result=%+v", result)
	}
}

func TestCallerFreshUntilCannotOverrideOwnerFreshness(t *testing.T) {
	_, request, now := protectedAdmissionFixture(t)
	request.FreshUntil = now.Add(-time.Hour)

	result := NewCanonicalProtectedAdmission().Admit(request, now)
	if result.Status != AdmissionAllowed || result.Classification != ProtectedClassificationOwnedPlaced {
		t.Fatalf("result=%+v", result)
	}
}

func TestCanonicalAdmissionRejectsMalformedSemanticFacts(t *testing.T) {
	facts, request, now := protectedAdmissionFixture(t)
	facts.DeviceAssignments[0].ValidTo = timePtr(facts.DeviceAssignments[0].ValidFrom)
	request.Source = trustedSnapshotForTest(t, facts)

	result := NewCanonicalProtectedAdmission().Admit(request, now)
	if result.Status != AdmissionDenied || result.Classification != ProtectedClassificationInvalid {
		t.Fatalf("result=%+v", result)
	}
}

func TestCanonicalAdmissionRejectsAttemptBindingMismatch(t *testing.T) {
	_, request, now := protectedAdmissionFixture(t)
	service := NewCanonicalProtectedAdmission()
	if result := service.Admit(request, now); result.Status != AdmissionAllowed {
		t.Fatalf("initial result=%+v", result)
	}

	mismatch := request
	mismatch.Generation = 2
	result := service.Admit(mismatch, now)
	if result.Status != AdmissionDenied {
		t.Fatalf("result=%+v", result)
	}
}

func TestCanonicalAdmissionRejectsTargetBindingMismatch(t *testing.T) {
	facts, request, now := protectedAdmissionFixture(t)
	owner := uint(10)
	facts.Devices = append(facts.Devices, DeviceFact{ID: 4, InventoryOwnerClientID: &owner})
	request.Source = trustedSnapshotForTest(t, facts)
	request.Eligibility = testActiveD1Eligibility(request, now)
	service := NewCanonicalProtectedAdmission()
	if result := service.Admit(request, now); result.Status != AdmissionAllowed {
		t.Fatalf("initial result=%+v", result)
	}

	mismatch := request
	mismatch.TargetID = 4
	result := service.Admit(mismatch, now)
	if result.Status != AdmissionDenied {
		t.Fatalf("result=%+v", result)
	}
}

func TestDiagnosticAndLegacyRoutesCannotAuthorize(t *testing.T) {
	_, request, now := protectedAdmissionFixture(t)
	service := NewCanonicalProtectedAdmission()
	for _, route := range []ProtectedRoute{ProtectedRouteDiagnostic, ProtectedRouteLegacy} {
		candidate := request
		candidate.Route = route
		candidate.CallerAuthorized = true
		result := service.Admit(candidate, now)
		if result.Status != AdmissionDenied {
			t.Fatalf("route=%s result=%+v", route, result)
		}
	}
}

func TestCanonicalAdmissionPreservesOwnedUnplacedAndRecordsWaiting(t *testing.T) {
	facts, request, now := protectedAdmissionFixture(t)
	facts.DeviceAssignments = nil
	request.Source = trustedSnapshotForTest(t, facts)
	request.Eligibility = testActiveD1Eligibility(request, now)
	service := NewCanonicalProtectedAdmission()

	result := service.Admit(request, now)
	if result.Status != AdmissionWaitingForMapping || result.Classification != ProtectedClassificationOwnedUnplaced {
		t.Fatalf("result=%+v", result)
	}

	placed := facts
	request.Source = trustedSnapshotForTest(t, placed)
	request.Eligibility = testActiveD1Eligibility(request, now)
	result = service.Admit(request, now)
	if result.Status != AdmissionDenied || !strings.Contains(result.Reason, "WAITING_FOR_MAPPING") {
		t.Fatalf("waiting replay result=%+v", result)
	}
}

func TestDeviceShopIDOnlyCannotEstablishOwnership(t *testing.T) {
	facts, request, now := protectedAdmissionFixture(t)
	facts.Devices[0].InventoryOwnerClientID = nil
	facts.Devices[0].ShopID = 100
	facts.DeviceAssignments = nil
	request.Source = trustedSnapshotForTest(t, facts)
	request.Eligibility = testActiveD1Eligibility(request, now)

	result := NewCanonicalProtectedAdmission().Admit(request, now)
	if result.Status != AdmissionDenied || result.Classification != ProtectedClassificationUnowned {
		t.Fatalf("result=%+v", result)
	}
	if !strings.Contains(result.Reason, "ShopID") {
		t.Fatalf("reason=%q", result.Reason)
	}
}

func TestSemanticDeniedAttemptIsTerminalAcrossFactChanges(t *testing.T) {
	cases := []struct {
		name  string
		facts func(FactSet) FactSet
	}{
		{"unowned", func(f FactSet) FactSet {
			f.Devices[0].InventoryOwnerClientID = nil
			f.Devices[0].ShopID = 100
			f.DeviceAssignments = nil
			return f
		}},
		{"conflicting", func(f FactSet) FactSet { owner := uint(20); f.Devices[0].InventoryOwnerClientID = &owner; return f }},
		{"invalid", func(f FactSet) FactSet {
			f.DeviceAssignments[0].ValidTo = timePtr(f.DeviceAssignments[0].ValidFrom)
			return f
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			facts, request, now := protectedAdmissionFixture(t)
			facts = tc.facts(facts)
			request.Source = trustedSnapshotForTest(t, facts)
			request.Eligibility = testActiveD1Eligibility(request, now)
			service := NewCanonicalProtectedAdmission()
			first := service.Admit(request, now)
			if first.Status != AdmissionDenied {
				t.Fatalf("first=%+v", first)
			}

			freshFacts := testFacts()
			freshFacts.AsOf = now
			request.Source = trustedSnapshotForTest(t, freshFacts)
			request.Eligibility = testActiveD1Eligibility(request, now)
			second := service.Admit(request, now)
			if second.Status != AdmissionDenied || !strings.Contains(second.Reason, "terminal") {
				t.Fatalf("second=%+v", second)
			}
		})
	}
}

func TestWaitingBindingMismatchFailsClosed(t *testing.T) {
	facts, request, now := protectedAdmissionFixture(t)
	facts.DeviceAssignments = nil
	request.Source = trustedSnapshotForTest(t, facts)
	request.Eligibility = testActiveD1Eligibility(request, now)
	service := NewCanonicalProtectedAdmission()
	if result := service.Admit(request, now); result.Status != AdmissionWaitingForMapping {
		t.Fatalf("initial=%+v", result)
	}

	mismatch := request
	mismatch.TargetID = 2
	if result := service.Admit(mismatch, now); result.Status != AdmissionDenied || !strings.Contains(result.Reason, "binding mismatch") {
		t.Fatalf("target mismatch=%+v", result)
	}
	mismatch = request
	mismatch.Generation = 2
	if result := service.Admit(mismatch, now); result.Status != AdmissionDenied || !strings.Contains(result.Reason, "binding mismatch") {
		t.Fatalf("generation mismatch=%+v", result)
	}
}

func TestTerminalSameAttemptReplayIsRejected(t *testing.T) {
	_, request, now := protectedAdmissionFixture(t)
	service := NewCanonicalProtectedAdmission()
	if result := service.Admit(request, now); result.Status != AdmissionAllowed {
		t.Fatalf("initial result=%+v", result)
	}
	if err := service.RecordTerminal(request); err != nil {
		t.Fatal(err)
	}

	result := service.Admit(request, now)
	if result.Status != AdmissionDenied || !strings.Contains(result.Reason, "terminal") {
		t.Fatalf("result=%+v", result)
	}
}

func TestSourceEvidenceCannotCrossAdmissionInvocations(t *testing.T) {
	_, request, now := protectedAdmissionFixture(t)
	service := NewCanonicalProtectedAdmission()
	if result := service.Admit(request, now); result.Status != AdmissionAllowed {
		t.Fatalf("initial result=%+v", result)
	}
	other := request
	other.AttemptID = uuid.MustParse("cccccccc-cccc-cccc-cccc-cccccccccccc")
	if result := service.Admit(other, now); result.Status != AdmissionDenied {
		t.Fatalf("reused evidence result=%+v", result)
	}
}

func TestConcurrentDuplicateEntryFailsClosed(t *testing.T) {
	_, request, now := protectedAdmissionFixture(t)
	service := NewCanonicalProtectedAdmission()
	results := make(chan ProtectedAdmissionResult, 2)
	var group sync.WaitGroup
	group.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer group.Done()
			results <- service.Admit(request, now)
		}()
	}
	group.Wait()
	close(results)
	allowed := 0
	denied := 0
	for result := range results {
		if result.Status == AdmissionAllowed {
			allowed++
		}
		if result.Status == AdmissionDenied {
			denied++
		}
	}
	if allowed != 1 || denied != 1 {
		t.Fatalf("allowed=%d denied=%d", allowed, denied)
	}
}

func TestFutureCrossClientPlacementBlocksAdmission(t *testing.T) {
	facts, request, now := protectedAdmissionFixture(t)
	facts.DeviceAssignments = append(facts.DeviceAssignments, DeviceAssignmentFact{
		ID:                 uuid.MustParse("10000000-0000-0000-0000-000000000003"),
		DeviceID:           1,
		MeasurementPointID: uuid.MustParse("00000000-0000-0000-0000-000000000002"),
		ValidFrom:          facts.AsOf.Add(time.Hour),
	})
	request.Source = trustedSnapshotForTest(t, facts)
	request.Eligibility = testActiveD1Eligibility(request, now)

	result := NewCanonicalProtectedAdmission().Admit(request, now)
	if result.Status != AdmissionDenied || result.Classification != ProtectedClassificationConflicting {
		t.Fatalf("result=%+v", result)
	}
}

func TestFutureSameClientPlacementDoesNotBlockSolelyForFutureRow(t *testing.T) {
	facts, request, now := protectedAdmissionFixture(t)
	facts.DeviceAssignments = append(facts.DeviceAssignments, DeviceAssignmentFact{
		ID:                 uuid.MustParse("10000000-0000-0000-0000-000000000003"),
		DeviceID:           1,
		MeasurementPointID: uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		ValidFrom:          facts.AsOf.Add(time.Hour),
	})
	request.Source = trustedSnapshotForTest(t, facts)
	request.Eligibility = testActiveD1Eligibility(request, now)

	result := NewCanonicalProtectedAdmission().Admit(request, now)
	if result.Status != AdmissionAllowed || result.Classification != ProtectedClassificationOwnedPlaced {
		t.Fatalf("result=%+v", result)
	}
}

func TestFutureMalformedPlacementFailsClosed(t *testing.T) {
	facts, request, now := protectedAdmissionFixture(t)
	facts.Shops[0].ClientID = nil
	facts.DeviceAssignments = append(facts.DeviceAssignments, DeviceAssignmentFact{
		ID:                 uuid.MustParse("10000000-0000-0000-0000-000000000003"),
		DeviceID:           1,
		MeasurementPointID: uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		ValidFrom:          facts.AsOf.Add(time.Hour),
	})
	request.Source = trustedSnapshotForTest(t, facts)
	request.Eligibility = testActiveD1Eligibility(request, now)

	result := NewCanonicalProtectedAdmission().Admit(request, now)
	if result.Status != AdmissionDenied {
		t.Fatalf("result=%+v", result)
	}
}

func TestAdmissionResultContainsOnlySafeBoundData(t *testing.T) {
	_, request, now := protectedAdmissionFixture(t)
	result := NewCanonicalProtectedAdmission().Admit(request, now)
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, forbidden := range []string{"authority", "capability", "session", "transaction", "sql", "dsn", "handle", "pid", "fence", "lock"} {
		if strings.Contains(strings.ToLower(text), forbidden) {
			t.Fatalf("safe result contains %q: %s", forbidden, text)
		}
	}
}
