package sourceowner

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
)

func evidenceFixture() (FactSet, InvocationBinding, time.Time) {
	now := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	owner := uint(10)
	return FactSet{
			SchemaVersion: SchemaVersion,
			AsOf:          now,
			Clients:       []ClientFact{{ID: owner}},
			Devices:       []DeviceFact{{ID: 1, InventoryOwnerClientID: &owner}},
		}, InvocationBinding{
			OperationID: uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"),
			AttemptID:   uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"),
		}, now
}

func TestOwnerCreatesCanonicalOpaqueEvidence(t *testing.T) {
	facts, binding, now := evidenceFixture()
	evidence, err := newEvidence(facts, binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := evidence.ValidateForInvocation(binding, now); err != nil {
		t.Fatal(err)
	}
	if evidence.ObservedAt() != now || evidence.FreshUntil().IsZero() {
		t.Fatalf("evidence timing=%v/%v", evidence.ObservedAt(), evidence.FreshUntil())
	}
	if evidence.Digest() == [32]byte{} {
		t.Fatal("owner evidence has no digest")
	}
}

func TestZeroEvidenceFailsClosed(t *testing.T) {
	_, binding, now := evidenceFixture()
	if err := (Evidence{}).ValidateForInvocation(binding, now); err == nil {
		t.Fatal("zero evidence was accepted")
	}
}

func TestMutatedOwnerFactsFailDigestValidation(t *testing.T) {
	facts, binding, now := evidenceFixture()
	evidence, err := newEvidence(facts, binding)
	if err != nil {
		t.Fatal(err)
	}
	evidence.state.facts.Devices[0].InventoryOwnerClientID = nil
	if err := evidence.ValidateForInvocation(binding, now); err == nil {
		t.Fatal("mutated evidence was accepted")
	}
}

func TestEvidenceIsInvocationBound(t *testing.T) {
	facts, binding, now := evidenceFixture()
	evidence, err := newEvidence(facts, binding)
	if err != nil {
		t.Fatal(err)
	}
	other := binding
	other.AttemptID = uuid.MustParse("cccccccc-cccc-cccc-cccc-cccccccccccc")
	if err := evidence.ValidateForInvocation(other, now); err == nil {
		t.Fatal("evidence crossed invocation binding")
	}
}

func TestOwnerClockRejectsExpiredEvidenceWithoutBackdating(t *testing.T) {
	facts, binding, _ := evidenceFixture()
	evidence, err := newEvidence(facts, binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := evidence.UseForOwnerInvocation(binding); err == nil {
		t.Fatal("expired evidence accepted by owner clock")
	}
}

func TestEvidenceCannotBeReusedAcrossAdmissions(t *testing.T) {
	facts, binding, now := evidenceFixture()
	evidence, err := newEvidence(facts, binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := evidence.UseForInvocation(binding, now); err != nil {
		t.Fatal(err)
	}
	if err := evidence.UseForInvocation(binding, now); err == nil {
		t.Fatal("evidence was reused")
	}
}

func TestEvidenceFactsAreDefensive(t *testing.T) {
	facts, binding, now := evidenceFixture()
	evidence, err := newEvidence(facts, binding)
	if err != nil {
		t.Fatal(err)
	}
	copyOfFacts := evidence.Facts()
	copyOfFacts.Devices[0].InventoryOwnerClientID = nil
	if evidence.Facts().Devices[0].InventoryOwnerClientID == nil {
		t.Fatal("evidence exposed mutable facts")
	}
	if err := evidence.ValidateForInvocation(binding, now); err != nil {
		t.Fatal(err)
	}
}

func TestTrustedCollectionSurfaceDoesNotAcceptCallerInputs(t *testing.T) {
	ownerType := reflect.TypeOf(&PostgresSourceOwner{})
	method, ok := ownerType.MethodByName("CollectTrustedV5")
	if !ok {
		t.Fatal("source owner request method is missing")
	}
	if method.Type.NumIn() != 3 {
		t.Fatalf("trusted request inputs=%d, want receiver/context/binding", method.Type.NumIn())
	}
	if method.Type.In(1) != reflect.TypeOf((*context.Context)(nil)).Elem() {
		t.Fatalf("trusted request context=%v", method.Type.In(1))
	}
	if method.Type.In(2) != reflect.TypeOf(InvocationBinding{}) {
		t.Fatalf("trusted request binding=%v", method.Type.In(2))
	}
	if _, exposed := reflect.TypeOf(&PostgresFactCollector{}).MethodByName("CollectTrustedV5" + "Pinned"); exposed {
		t.Fatal("raw collector exposes trusted pinned mint method")
	}
}
