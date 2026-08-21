package reconciliation

import (
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func mappingProgressionFixture(t *testing.T) (FactSet, MappingProgressionRequest) {
	t.Helper()
	facts := testFacts()
	facts.Devices = facts.Devices[:1]
	facts.Devices[0].InventoryOwnerClientID = ptrUint(10)
	facts.DeviceAssignments = facts.DeviceAssignments[:1]
	facts.Shops = facts.Shops[:1]
	facts.Users = []UserFact{{ID: 7}}
	opID := uuid.MustParse("20000000-0000-0000-0000-000000000101")
	auditID := uuid.MustParse("30000000-0000-0000-0000-000000000101")
	pointID := facts.MeasurementPoints[0].ID
	assignmentID := facts.DeviceAssignments[0].ID
	facts.AdminOperations = []AdminOperationFact{{OperationID: opID, Operation: "binding", ActorID: 7}}
	facts.AdminAudits = []AdminAuditFact{{ID: auditID, OperationID: opID, Action: "bind", ActorID: 7, ShopID: ptrUint(100), NewMeasurementPointID: &pointID, NewAssignmentID: &assignmentID, DeviceID: ptrUint(1), DeviceSerialNumber: ptrString("SERIAL-1"), DeviceMAC: ptrString("AABBCCDDEEFF")}}
	raw, sourceDigest, err := CanonicalSourceFacts(facts)
	if err != nil || len(raw) == 0 {
		t.Fatalf("source digest: %v", err)
	}
	basisDigest, err := MappingSourceFactsDigest(facts)
	if err != nil {
		t.Fatal(err)
	}
	artifact := &MappingArtifact{SchemaVersion: MappingSchema, Version: 5, SourceFactsDigest: hex.EncodeToString(basisDigest), Mappings: []MappingEntry{{Category: MappingAdminProvenance, OperationID: opID, ClientID: 10}}}
	request := MappingProgressionRequest{
		Artifact: artifact, SourceFactsDigest: hex.EncodeToString(sourceDigest), MappingBasisDigest: hex.EncodeToString(basisDigest),
		TargetID: 1, ClientID: 10, OperationID: opID, AttemptID: uuid.New(), Generation: 1,
		ExpectedCurrent: []MappingExpectedCurrentBinding{{Category: MappingAdminProvenance, OperationID: opID, Present: true}},
	}
	return facts, request
}

func TestAdmitMappingRequiredExactBindingAndFreshReclassification(t *testing.T) {
	facts, request := mappingProgressionFixture(t)
	result, err := AdmitMappingRequired(facts, request)
	if err != nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if result.Status != MappingProgressionAdmitted || result.FreshClassification != ProtectedClassificationOwnedPlaced {
		t.Fatalf("result=%+v", result)
	}
}

func TestAdmitMappingRequiredRejectsStaleAndChangedFacts(t *testing.T) {
	facts, request := mappingProgressionFixture(t)
	request.SourceFactsDigest = hex.EncodeToString(make([]byte, 32))
	if _, err := AdmitMappingRequired(facts, request); !errors.Is(err, ErrMappingProgressionRejected) {
		t.Fatalf("stale digest err=%v", err)
	}
	_, request = mappingProgressionFixture(t)
	facts.Shops[0].ID = 101
	if _, err := AdmitMappingRequired(facts, request); !errors.Is(err, ErrMappingProgressionRejected) {
		t.Fatalf("changed facts err=%v", err)
	}
}

func TestAdmitMappingRequiredRejectsIncompleteAndShopIDOnlyAuthority(t *testing.T) {
	facts, request := mappingProgressionFixture(t)
	request.ExpectedCurrent = nil
	if _, err := AdmitMappingRequired(facts, request); !errors.Is(err, ErrMappingProgressionRejected) {
		t.Fatalf("incomplete expected-current err=%v", err)
	}
	facts, request = mappingProgressionFixture(t)
	facts.Devices[0].InventoryOwnerClientID = nil
	facts.Devices[0].ShopID = 100
	facts.DeviceAssignments = nil
	request.Artifact.Mappings = []MappingEntry{{Category: MappingDevice, DeviceID: 1, ClientID: 10}}
	request.ExpectedCurrent = []MappingExpectedCurrentBinding{{Category: MappingDevice, DeviceID: 1, Present: true}}
	if _, err := AdmitMappingRequired(facts, request); !errors.Is(err, ErrMappingProgressionRejected) {
		t.Fatalf("ShopID-only err=%v", err)
	}
}

func TestAdmitMappingRequiredNilArtifactWaitsOnlyForStructuralMapping(t *testing.T) {
	facts, request := mappingProgressionFixture(t)
	facts.Devices[0].InventoryOwnerClientID = nil
	facts.DeviceAssignments = nil
	request.Artifact = nil
	result, err := AdmitMappingRequired(facts, request)
	if err != nil || result.Status != MappingProgressionWaiting || result.Classification != ProtectedClassificationUnowned {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestAdmitMappingRequiredNilArtifactFailsClosedOutsideStructuralMapping(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*FactSet)
	}{
		{"invalid", func(f *FactSet) { f.DeviceAssignments[0].ValidTo = timePtr(f.DeviceAssignments[0].ValidFrom) }},
		{"ambiguous", func(f *FactSet) {
			f.DeviceAssignments = append(f.DeviceAssignments, DeviceAssignmentFact{ID: uuid.MustParse("10000000-0000-0000-0000-000000000003"), DeviceID: 1, MeasurementPointID: f.MeasurementPoints[1].ID, ValidFrom: f.AsOf.Add(-time.Hour)})
		}},
		{"conflicting", func(f *FactSet) { f.Devices[0].InventoryOwnerClientID = ptrUint(20) }},
		{"shop-id-only", func(f *FactSet) {
			f.Devices[0].InventoryOwnerClientID = nil
			f.Devices[0].ShopID = 100
			f.DeviceAssignments = nil
		}},
		{"active-null-shop-authority", func(f *FactSet) {
			f.Devices[0].InventoryOwnerClientID = nil
			f.Shops[0].ClientID = nil
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			facts, request := mappingProgressionFixture(t)
			tc.mutate(&facts)
			request.Artifact = nil
			result, err := AdmitMappingRequired(facts, request)
			if !errors.Is(err, ErrMappingProgressionRejected) || result.Status != MappingProgressionDenied {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if result.Status == MappingProgressionWaiting {
				t.Fatalf("unsafe wait for %s: %+v", tc.name, result)
			}
		})
	}
}

func refreshMappingProgressionDigests(t *testing.T, facts FactSet, request *MappingProgressionRequest) {
	t.Helper()
	_, source, err := CanonicalSourceFacts(facts)
	if err != nil {
		t.Fatal(err)
	}
	basis, err := MappingSourceFactsDigest(facts)
	if err != nil {
		t.Fatal(err)
	}
	request.SourceFactsDigest = hex.EncodeToString(source)
	request.MappingBasisDigest = hex.EncodeToString(basis)
	request.Artifact.SourceFactsDigest = request.MappingBasisDigest
}

func TestAdmitMappingRequiredRejectsStaleExpectedCurrentValues(t *testing.T) {
	facts, request := mappingProgressionFixture(t)
	expected := uint(10)
	request.Artifact.Mappings[0].ExpectedCurrentClientID = &expected
	request.ExpectedCurrent[0].ClientID = &expected
	if _, err := AdmitMappingRequired(facts, request); !errors.Is(err, ErrMappingProgressionRejected) {
		t.Fatalf("stale non-NULL expected-current err=%v", err)
	}

	facts, request = mappingProgressionFixture(t)
	actual := uint(20)
	facts.AdminOperations[0].ClientID = &actual
	refreshMappingProgressionDigests(t, facts, &request)
	if _, err := AdmitMappingRequired(facts, request); !errors.Is(err, ErrMappingProgressionRejected) {
		t.Fatalf("stale NULL expected-current err=%v", err)
	}
}

func TestAdmitMappingRequiredOwnedUnplacedWithoutArtifactFailsClosed(t *testing.T) {
	facts := testFacts()
	facts.Devices = facts.Devices[:1]
	owner := uint(10)
	facts.Devices[0].InventoryOwnerClientID = &owner
	facts.DeviceAssignments = nil
	result, err := AdmitMappingRequired(facts, MappingProgressionRequest{TargetID: 1})
	if !errors.Is(err, ErrMappingProgressionRejected) || result.Status != MappingProgressionDenied || result.Classification != ProtectedClassificationOwnedUnplaced {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if result.Status == MappingProgressionWaiting {
		t.Fatal("OWNED_UNPLACED without an artifact must not wait for mapping")
	}
}
