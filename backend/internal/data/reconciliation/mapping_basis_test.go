package reconciliation

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestMappingBasisIgnoresRawAsOfWhenSemanticStateIsUnchanged(t *testing.T) {
	first := testFacts()
	second := first
	second.AsOf = first.AsOf.Add(10 * time.Minute)
	firstDigest, err := MappingSourceFactsDigest(first)
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := MappingSourceFactsDigest(second)
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(firstDigest) != hex.EncodeToString(secondDigest) {
		t.Fatalf("raw AsOf changed mapping basis: %x != %x", firstDigest, secondDigest)
	}
	if first.AsOf.Equal(second.AsOf) {
		t.Fatal("test did not change raw AsOf")
	}
}

func TestMappingBasisDetectsFutureToActiveTransition(t *testing.T) {
	first := testFacts()
	future := DeviceAssignmentFact{ID: stableTestAssignmentID(3), DeviceID: 3, MeasurementPointID: first.MeasurementPoints[0].ID, ValidFrom: first.AsOf.Add(time.Hour)}
	first.DeviceAssignments = append(first.DeviceAssignments, future)
	firstDigest, err := MappingSourceFactsDigest(first)
	if err != nil {
		t.Fatal(err)
	}
	second := first
	second.AsOf = first.AsOf.Add(2 * time.Hour)
	secondDigest, err := MappingSourceFactsDigest(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstDigest) == string(secondDigest) {
		t.Fatal("future-to-active transition retained mapping basis")
	}
	artifact := &MappingArtifact{SchemaVersion: MappingSchema, Version: 5, SourceFactsDigest: hex.EncodeToString(firstDigest), Mappings: []MappingEntry{{Category: MappingDevice, DeviceID: 3, ClientID: 10}}}
	if _, err := BuildPlan(second, artifact); err == nil {
		t.Fatal("future-to-active artifact was accepted by fresh planning")
	}
}

func TestMappingBasisDetectsActiveToHistoricalTransition(t *testing.T) {
	first := testFacts()
	first.DeviceAssignments[0].ValidTo = timePtr(first.AsOf.Add(time.Hour))
	firstDigest, err := MappingSourceFactsDigest(first)
	if err != nil {
		t.Fatal(err)
	}
	second := first
	second.AsOf = first.AsOf.Add(2 * time.Hour)
	secondDigest, err := MappingSourceFactsDigest(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstDigest) == string(secondDigest) {
		t.Fatal("active-to-historical transition retained mapping basis")
	}
}

func TestMappingBasisDetectsSourceAndExpectedCurrentChanges(t *testing.T) {
	first := testFacts()
	firstDigest, err := MappingSourceFactsDigest(first)
	if err != nil {
		t.Fatal(err)
	}
	changedRow := first
	changedRow.Shops[0].ClientID = cloneUint(changedRow.Shops[1].ClientID)
	changedDigest, err := MappingSourceFactsDigest(changedRow)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstDigest) == string(changedDigest) {
		t.Fatal("source row change retained mapping basis")
	}
	owner := uint(10)
	withOwner := first
	withOwner.Devices[0].InventoryOwnerClientID = &owner
	ownerDigest, err := MappingSourceFactsDigest(withOwner)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstDigest) == string(ownerDigest) {
		t.Fatal("expected-current change retained mapping basis")
	}
}

func TestMappingBasisIsDeterministicAcrossOrdering(t *testing.T) {
	first := testFacts()
	second := testFacts()
	second.Clients[0], second.Clients[1] = second.Clients[1], second.Clients[0]
	second.Devices[0], second.Devices[2] = second.Devices[2], second.Devices[0]
	firstDigest, err := MappingSourceFactsDigest(first)
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := MappingSourceFactsDigest(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstDigest) != string(secondDigest) {
		t.Fatalf("ordering changed mapping basis: %x != %x", firstDigest, secondDigest)
	}
}

func stableTestAssignmentID(n byte) uuid.UUID {
	var id uuid.UUID
	id[0] = 0x20
	id[15] = n
	return id
}
