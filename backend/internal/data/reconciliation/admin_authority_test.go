package reconciliation

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestAdminProvenanceClassificationDerivesActionAuthority(t *testing.T) {
	f := testFacts()
	f.Users = []UserFact{{ID: 7}}
	opID := uuid.MustParse("20000000-0000-0000-0000-000000000001")
	auditID := uuid.MustParse("30000000-0000-0000-0000-000000000001")
	client := uint(10)
	f.AdminOperations = []AdminOperationFact{{OperationID: opID, Operation: "binding", ActorID: 7, ClientID: &client}}
	newAssignmentID := f.DeviceAssignments[0].ID
	targetPointID := f.MeasurementPoints[0].ID
	f.AdminAudits = []AdminAuditFact{{ID: auditID, OperationID: opID, Action: "bind", ActorID: 7, ShopID: ptrUint(100), NewMeasurementPointID: &targetPointID, NewAssignmentID: &newAssignmentID, DeviceID: ptrUint(1), DeviceSerialNumber: ptrString("SERIAL-1"), DeviceMAC: ptrString("AABBCCDDEEFF"), ClientID: &client}}
	classes, err := ClassifyFacts(f)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range classes {
		if c.Kind == FactKindAdminProvenance && c.Classification != AlreadyConsistent {
			t.Fatalf("classification=%+v", c)
		}
	}
	wrong := uint(20)
	f.AdminAudits[0].ClientID = &wrong
	classes, err = ClassifyFacts(f)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range classes {
		if c.Kind == FactKindAdminProvenance && c.Classification == AlreadyConsistent {
			t.Fatalf("accepted contradictory provenance=%+v", c)
		}
	}
}

func ptrUint(v uint) *uint       { return &v }
func ptrString(v string) *string { return &v }

func TestAdminProvenanceMappingCannotOverrideKnownAuthority(t *testing.T) {
	f := testFacts()
	f.Users = []UserFact{{ID: 7}}
	opID := uuid.MustParse("20000000-0000-0000-0000-000000000003")
	auditID := uuid.MustParse("30000000-0000-0000-0000-000000000003")
	f.AdminOperations = []AdminOperationFact{{OperationID: opID, Operation: "binding", ActorID: 7}}
	newAssignmentID := f.DeviceAssignments[0].ID
	targetPointID := f.MeasurementPoints[0].ID
	f.AdminAudits = []AdminAuditFact{{ID: auditID, OperationID: opID, Action: "bind", ActorID: 7, ShopID: ptrUint(100), NewMeasurementPointID: &targetPointID, NewAssignmentID: &newAssignmentID, DeviceID: ptrUint(1), DeviceSerialNumber: ptrString("SERIAL-1"), DeviceMAC: ptrString("AABBCCDDEEFF")}}
	_, digest, err := CanonicalSourceFacts(f)
	if err != nil {
		t.Fatal(err)
	}
	artifact := &MappingArtifact{SchemaVersion: MappingSchema, Version: 5, SourceFactsDigest: hex.EncodeToString(digest), Mappings: []MappingEntry{{Category: MappingAdminProvenance, OperationID: opID, ClientID: 20}}}
	if _, err := BuildPlan(f, artifact); err == nil {
		t.Fatal("admin mapping overrode known relational authority")
	}
}

func TestAdminMappingRejectsBlockingSiblingRow(t *testing.T) {
	f := testFacts()
	f.Users = []UserFact{{ID: 7}}
	opID := uuid.MustParse("20000000-0000-0000-0000-000000000013")
	auditID := uuid.MustParse("30000000-0000-0000-0000-000000000013")
	client := uint(10)
	wrong := uint(20)
	f.AdminOperations = []AdminOperationFact{{OperationID: opID, Operation: "binding", ActorID: 7, ClientID: &client}}
	newAssignmentID := f.DeviceAssignments[0].ID
	targetPointID := f.MeasurementPoints[0].ID
	f.AdminAudits = []AdminAuditFact{{ID: auditID, OperationID: opID, Action: "bind", ActorID: 7, ShopID: ptrUint(100), NewMeasurementPointID: &targetPointID, NewAssignmentID: &newAssignmentID, DeviceID: ptrUint(1), DeviceSerialNumber: ptrString("SERIAL-1"), DeviceMAC: ptrString("AABBCCDDEEFF"), ClientID: &wrong}}
	_, digest, err := CanonicalSourceFacts(f)
	if err != nil {
		t.Fatal(err)
	}
	artifact := &MappingArtifact{SchemaVersion: MappingSchema, Version: 5, SourceFactsDigest: hex.EncodeToString(digest), Mappings: []MappingEntry{{Category: MappingAdminProvenance, OperationID: opID, ClientID: 10}}}
	if _, err := BuildPlan(f, artifact); err == nil {
		t.Fatal("mapping repaired an operation with a blocking audit sibling")
	}
}

func TestAdminMappingFillsOnlyNullSideOfMatchingOperationPair(t *testing.T) {
	f := testFacts()
	f.Users = []UserFact{{ID: 7}}
	opID := uuid.MustParse("20000000-0000-0000-0000-000000000012")
	auditID := uuid.MustParse("30000000-0000-0000-0000-000000000012")
	client := uint(10)
	f.AdminOperations = []AdminOperationFact{{OperationID: opID, Operation: "binding", ActorID: 7, ClientID: &client}}
	newAssignmentID := f.DeviceAssignments[0].ID
	targetPointID := f.MeasurementPoints[0].ID
	f.AdminAudits = []AdminAuditFact{{ID: auditID, OperationID: opID, Action: "bind", ActorID: 7, ShopID: ptrUint(100), NewMeasurementPointID: &targetPointID, NewAssignmentID: &newAssignmentID, DeviceID: ptrUint(1), DeviceSerialNumber: ptrString("SERIAL-1"), DeviceMAC: ptrString("AABBCCDDEEFF")}}
	_, digest, err := CanonicalSourceFacts(f)
	if err != nil {
		t.Fatal(err)
	}
	artifact := &MappingArtifact{SchemaVersion: MappingSchema, Version: 5, SourceFactsDigest: hex.EncodeToString(digest), Mappings: []MappingEntry{{Category: MappingAdminProvenance, OperationID: opID, ClientID: 10}}}
	plan, err := BuildPlan(f, artifact)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range plan.Items {
		if item.Kind != PlanItemAdmin || item.OperationID != opID {
			continue
		}
		if item.AuditID != auditID || !item.SetAdminClient || item.ExpectedCurrent.ClientID != nil || item.ExpectedAffectedCount != 1 {
			t.Fatalf("mapping did not produce one NULL-side CAS intent: %+v", item)
		}
		return
	}
	t.Fatalf("missing NULL-side audit intent: %+v", plan.Items)
}

func TestNullAdminProvenanceMappingProducesIntent(t *testing.T) {
	f := testFacts()
	f.Users = []UserFact{{ID: 7}}
	opID := uuid.MustParse("20000000-0000-0000-0000-000000000002")
	auditID := uuid.MustParse("30000000-0000-0000-0000-000000000002")
	f.AdminOperations = []AdminOperationFact{{OperationID: opID, Operation: "binding", ActorID: 7}}
	newAssignmentID := f.DeviceAssignments[0].ID
	targetPointID := f.MeasurementPoints[0].ID
	f.AdminAudits = []AdminAuditFact{{ID: auditID, OperationID: opID, Action: "bind", ActorID: 7, ShopID: ptrUint(100), NewMeasurementPointID: &targetPointID, NewAssignmentID: &newAssignmentID, DeviceID: ptrUint(1), DeviceSerialNumber: ptrString("SERIAL-1"), DeviceMAC: ptrString("AABBCCDDEEFF")}}
	_, digest, _ := CanonicalSourceFacts(f)
	artifact := &MappingArtifact{SchemaVersion: MappingSchema, Version: 5, SourceFactsDigest: hex.EncodeToString(digest), Mappings: []MappingEntry{{Category: MappingAdminProvenance, OperationID: opID, ClientID: 10}}}
	plan, err := BuildPlan(f, artifact)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range plan.Items {
		if item.Kind == PlanItemAdmin && item.OperationID == opID {
			found = item.SetAdminClient
		}
	}
	if !found {
		t.Fatalf("missing admin intent: %+v", plan.Items)
	}
}

func TestAdminAuditWriterShapesAreAccepted(t *testing.T) {
	point1 := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	point2 := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	point3 := uuid.MustParse("00000000-0000-0000-0000-000000000003")
	oldAssignmentID := uuid.MustParse("10000000-0000-0000-0000-000000000001")
	cases := []struct {
		name   string
		action string
		setup  func(*FactSet) AdminAuditFact
	}{
		{name: "create", action: "create_measurement_point", setup: func(f *FactSet) AdminAuditFact {
			opID, auditID := StableID("writer-shape-op", "create"), StableID("writer-shape-audit", "create")
			return AdminAuditFact{ID: auditID, OperationID: opID, Action: "create_measurement_point", ActorID: 7, ShopID: ptrUint(200), MeasurementPointID: &point2}
		}},
		{name: "bind", action: "bind", setup: func(f *FactSet) AdminAuditFact {
			assignmentID := uuid.MustParse("10000000-0000-0000-0000-000000000201")
			f.DeviceAssignments = append(f.DeviceAssignments, DeviceAssignmentFact{ID: assignmentID, DeviceID: 3, MeasurementPointID: point1, ValidFrom: f.AsOf.Add(-time.Minute)})
			opID, auditID := StableID("writer-shape-op", "bind"), StableID("writer-shape-audit", "bind")
			return AdminAuditFact{ID: auditID, OperationID: opID, Action: "bind", ActorID: 7, ShopID: ptrUint(100), DeviceID: ptrUint(3), DeviceSerialNumber: ptrString("SERIAL-3"), DeviceMAC: ptrString("FFEEDDCCBBAA"), NewMeasurementPointID: &point1, NewAssignmentID: &assignmentID}
		}},
		{name: "replace", action: "replace", setup: func(f *FactSet) AdminAuditFact {
			f.DeviceAssignments[0].ValidTo = timePtr(f.AsOf.Add(-time.Minute))
			assignmentID := uuid.MustParse("10000000-0000-0000-0000-000000000202")
			f.DeviceAssignments = append(f.DeviceAssignments, DeviceAssignmentFact{ID: assignmentID, DeviceID: 3, MeasurementPointID: point1, ValidFrom: f.AsOf.Add(-time.Minute)})
			opID, auditID := StableID("writer-shape-op", "replace"), StableID("writer-shape-audit", "replace")
			return AdminAuditFact{ID: auditID, OperationID: opID, Action: "replace", ActorID: 7, ShopID: ptrUint(100), DeviceID: ptrUint(3), DeviceSerialNumber: ptrString("SERIAL-3"), DeviceMAC: ptrString("FFEEDDCCBBAA"), OldMeasurementPointID: &point1, NewMeasurementPointID: &point1, OldAssignmentID: &oldAssignmentID, NewAssignmentID: &assignmentID}
		}},
		{name: "relocate", action: "relocate", setup: func(f *FactSet) AdminAuditFact {
			f.MeasurementPoints = append(f.MeasurementPoints, MeasurementPointFact{ID: point3, ShopID: 100})
			f.DeviceAssignments[0].ValidTo = timePtr(f.AsOf.Add(-time.Minute))
			assignmentID := uuid.MustParse("10000000-0000-0000-0000-000000000203")
			f.DeviceAssignments = append(f.DeviceAssignments, DeviceAssignmentFact{ID: assignmentID, DeviceID: 1, MeasurementPointID: point3, ValidFrom: f.AsOf.Add(-time.Minute)})
			opID, auditID := StableID("writer-shape-op", "relocate"), StableID("writer-shape-audit", "relocate")
			return AdminAuditFact{ID: auditID, OperationID: opID, Action: "relocate", ActorID: 7, ShopID: ptrUint(100), DeviceID: ptrUint(1), DeviceSerialNumber: ptrString("SERIAL-1"), DeviceMAC: ptrString("AABBCCDDEEFF"), OldMeasurementPointID: &point1, NewMeasurementPointID: &point3, OldAssignmentID: &oldAssignmentID, NewAssignmentID: &assignmentID}
		}},
		{name: "unbind", action: "unbind", setup: func(f *FactSet) AdminAuditFact {
			f.DeviceAssignments[0].ValidTo = timePtr(f.AsOf.Add(-time.Minute))
			opID, auditID := StableID("writer-shape-op", "unbind"), StableID("writer-shape-audit", "unbind")
			return AdminAuditFact{ID: auditID, OperationID: opID, Action: "unbind", ActorID: 7, ShopID: ptrUint(100), DeviceID: ptrUint(1), DeviceSerialNumber: ptrString("SERIAL-1"), DeviceMAC: ptrString("AABBCCDDEEFF"), OldMeasurementPointID: &point1, OldAssignmentID: &oldAssignmentID}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := testFacts()
			f.Users = []UserFact{{ID: 7}}
			audit := tc.setup(&f)
			f.AdminOperations = []AdminOperationFact{{OperationID: audit.OperationID, Operation: tc.action, ActorID: 7}}
			f.AdminAudits = []AdminAuditFact{audit}
			classes, err := ClassifyFacts(f)
			if err != nil {
				t.Fatal(err)
			}
			for _, class := range classes {
				if class.Kind == FactKindAdminProvenance && class.Classification == BlockingIntegrityError {
					t.Fatalf("writer shape was blocked: %+v", class)
				}
			}
		})
	}
}

func TestAdminAuthorityAllActionPathsUseRelationalEvidence(t *testing.T) {
	f := testFacts()
	point := f.MeasurementPoints[0].ID
	f.DeviceAssignments = append(f.DeviceAssignments, DeviceAssignmentFact{ID: uuid.MustParse("10000000-0000-0000-0000-000000000008"), DeviceID: 3, MeasurementPointID: point, ValidFrom: f.AsOf.Add(-time.Hour)})
	misleadingShop := uint(200)
	misleadingOwner := uint(20)
	f.Devices[0].ShopID, f.Devices[0].InventoryOwnerClientID = misleadingShop, &misleadingOwner
	cases := []AdminAuthorityRequest{
		{Action: AdminCreateMeasurementPoint, ShopID: 100},
		{Action: AdminBind, DeviceID: 1, TargetMeasurementPointID: point},
		{Action: AdminReplace, DeviceID: 1, ReplacementDeviceID: 3, TargetMeasurementPointID: point},
		{Action: AdminRelocate, DeviceID: 1, SourceMeasurementPointID: point, TargetMeasurementPointID: point},
		{Action: AdminUnbind, DeviceID: 1, SourceMeasurementPointID: point},
	}
	for _, req := range cases {
		req.AsOf = f.AsOf
		got, err := DeriveAdminAuthority(f, req)
		if err != nil || got.ClientID != 10 {
			t.Fatalf("action %s authority=%+v err=%v", req.Action, got, err)
		}
	}
}

func TestReplaceAuditCrossClientAndMismatchedReferencesBlock(t *testing.T) {
	f := testFacts()
	f.Users = []UserFact{{ID: 7}}
	point1, point2 := f.MeasurementPoints[0].ID, f.MeasurementPoints[1].ID
	newAssignmentID := uuid.MustParse("10000000-0000-0000-0000-000000000008")
	f.Devices = append(f.Devices, DeviceFact{ID: 4})
	f.DeviceAssignments = append(f.DeviceAssignments, DeviceAssignmentFact{ID: newAssignmentID, DeviceID: 4, MeasurementPointID: point2, ValidFrom: f.AsOf.Add(-time.Hour)})
	opID := uuid.MustParse("20000000-0000-0000-0000-000000000004")
	auditID := uuid.MustParse("30000000-0000-0000-0000-000000000004")
	oldAssignmentID := f.DeviceAssignments[0].ID
	f.AdminOperations = []AdminOperationFact{{OperationID: opID, Operation: "replace", ActorID: 7}}
	f.AdminAudits = []AdminAuditFact{{ID: auditID, OperationID: opID, Action: "replace", ActorID: 7, DeviceID: ptrUint(4), OldAssignmentID: &oldAssignmentID, NewAssignmentID: &newAssignmentID, OldMeasurementPointID: &point1, NewMeasurementPointID: &point2}}
	classes, err := ClassifyFacts(f)
	if err != nil {
		t.Fatal(err)
	}
	for _, class := range classes {
		if class.Kind == FactKindAdminProvenance && class.Classification != BlockingIntegrityError {
			t.Fatalf("cross-client replace was not blocked: %+v", class)
		}
	}
	// Direct replacement identity must agree with the new assignment evidence.
	f.AdminAudits[0].DeviceID = ptrUint(1)
	classes, err = ClassifyFacts(f)
	if err != nil {
		t.Fatal(err)
	}
	for _, class := range classes {
		if class.Kind == FactKindAdminProvenance && class.Classification != BlockingIntegrityError {
			t.Fatalf("mismatched replace references were not blocked: %+v", class)
		}
	}
}

func TestAdminAuditTypedReferenceMatrixBlocksMissingAndExtraEvidence(t *testing.T) {
	newPoint := uuid.MustParse("00000000-0000-0000-0000-000000000003")
	newAssignment := uuid.MustParse("10000000-0000-0000-0000-000000000013")
	oldAssignment := uuid.MustParse("10000000-0000-0000-0000-000000000001")
	newDevice := uint(3)
	cases := []struct {
		name   string
		action string
		mutate func(*AdminAuditFact, *FactSet)
	}{
		{name: "create extra device", action: "create_measurement_point", mutate: func(a *AdminAuditFact, f *FactSet) {
			a.ShopID = ptrUint(100)
			a.MeasurementPointID = &f.MeasurementPoints[0].ID
			a.DeviceID = ptrUint(1)
		}},
		{name: "bind extra old references", action: "bind", mutate: func(a *AdminAuditFact, f *FactSet) {
			a.DeviceID = ptrUint(1)
			a.NewMeasurementPointID = &f.MeasurementPoints[0].ID
			a.NewAssignmentID = &oldAssignment
			a.OldMeasurementPointID = &f.MeasurementPoints[0].ID
		}},
		{name: "replace missing new assignment", action: "replace", mutate: func(a *AdminAuditFact, f *FactSet) {
			a.DeviceID = &newDevice
			a.OldMeasurementPointID = &f.MeasurementPoints[0].ID
			a.OldAssignmentID = &oldAssignment
			a.NewMeasurementPointID = &f.MeasurementPoints[0].ID
		}},
		{name: "relocate conflicting assignment", action: "relocate", mutate: func(a *AdminAuditFact, f *FactSet) {
			a.DeviceID = ptrUint(1)
			a.OldMeasurementPointID = &f.MeasurementPoints[0].ID
			a.NewMeasurementPointID = &newPoint
			a.OldAssignmentID = &oldAssignment
			a.NewAssignmentID = &oldAssignment
		}},
		{name: "unbind extra new references", action: "unbind", mutate: func(a *AdminAuditFact, f *FactSet) {
			a.DeviceID = ptrUint(1)
			a.OldMeasurementPointID = &f.MeasurementPoints[0].ID
			a.OldAssignmentID = &oldAssignment
			a.NewMeasurementPointID = &f.MeasurementPoints[0].ID
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := testFacts()
			f.MeasurementPoints = append(f.MeasurementPoints, MeasurementPointFact{ID: newPoint, ShopID: 100})
			f.Devices = append(f.Devices, DeviceFact{ID: 4})
			f.DeviceAssignments = append(f.DeviceAssignments, DeviceAssignmentFact{ID: newAssignment, DeviceID: 4, MeasurementPointID: newPoint, ValidFrom: f.AsOf.Add(-time.Hour)})
			f.Users = []UserFact{{ID: 7}}
			opID := StableID("admin-matrix-op", tc.name)
			auditID := StableID("admin-matrix-audit", tc.name)
			f.AdminOperations = []AdminOperationFact{{OperationID: opID, Operation: tc.action, ActorID: 7}}
			f.AdminAudits = []AdminAuditFact{{ID: auditID, OperationID: opID, Action: tc.action, ActorID: 7}}
			tc.mutate(&f.AdminAudits[0], &f)
			classes, err := ClassifyFacts(f)
			if err != nil {
				t.Fatal(err)
			}
			for _, class := range classes {
				if class.Kind == FactKindAdminProvenance && class.Classification != BlockingIntegrityError {
					t.Fatalf("typed evidence was not blocked: %+v", class)
				}
			}
		})
	}
}

func TestAdminAuthorityRejectsDuplicateFactsAndUnresolvedFuture(t *testing.T) {
	f := testFacts()
	f.Devices = append(f.Devices, DeviceFact{ID: 1})
	if _, err := DeriveAdminAuthority(f, AdminAuthorityRequest{Action: AdminBind, AsOf: f.AsOf, DeviceID: 1, TargetMeasurementPointID: f.MeasurementPoints[0].ID}); err == nil {
		t.Fatal("accepted duplicate Device facts")
	}
	f = testFacts()
	f.DeviceAssignments = append(f.DeviceAssignments, f.DeviceAssignments[0])
	if _, err := DeriveAdminAuthority(f, AdminAuthorityRequest{Action: AdminBind, AsOf: f.AsOf, DeviceID: 1, TargetMeasurementPointID: f.MeasurementPoints[0].ID}); err == nil {
		t.Fatal("accepted duplicate DeviceAssignment facts")
	}
	f = testFacts()
	f.Shops[1].ClientID = nil
	f.DeviceAssignments = append(f.DeviceAssignments, DeviceAssignmentFact{ID: uuid.MustParse("10000000-0000-0000-0000-000000000010"), DeviceID: 1, MeasurementPointID: f.MeasurementPoints[1].ID, ValidFrom: f.AsOf.Add(time.Hour)})
	if _, err := DeriveAdminAuthority(f, AdminAuthorityRequest{Action: AdminBind, AsOf: f.AsOf, DeviceID: 1, TargetMeasurementPointID: f.MeasurementPoints[0].ID}); err == nil {
		t.Fatal("borrowed unresolved future Client authority")
	}
}

func TestAdminAuthorityUsesRelationalClientAndRejectsCrossClient(t *testing.T) {
	f := testFacts()
	point1 := f.MeasurementPoints[0].ID
	point2 := f.MeasurementPoints[1].ID
	if _, err := DeriveAdminAuthority(f, AdminAuthorityRequest{Action: AdminBind, AsOf: f.AsOf, DeviceID: 3, TargetMeasurementPointID: point1}); err == nil {
		t.Fatal("accepted bind without active relational device authority")
	}
	f.DeviceAssignments = append(f.DeviceAssignments,
		DeviceAssignmentFact{ID: uuid.MustParse("10000000-0000-0000-0000-000000000008"), DeviceID: 3, MeasurementPointID: point1, ValidFrom: f.AsOf.Add(-time.Hour)},
		DeviceAssignmentFact{ID: uuid.MustParse("10000000-0000-0000-0000-000000000009"), DeviceID: 1, MeasurementPointID: point2, ValidFrom: f.AsOf.Add(time.Hour)},
	)
	got, err := DeriveAdminAuthority(f, AdminAuthorityRequest{Action: AdminBind, AsOf: f.AsOf, DeviceID: 3, TargetMeasurementPointID: point1})
	if err != nil || got.ClientID != 10 {
		t.Fatalf("bind authority=%+v err=%v", got, err)
	}
	if _, err := DeriveAdminAuthority(f, AdminAuthorityRequest{Action: AdminBind, AsOf: f.AsOf, DeviceID: 1, TargetMeasurementPointID: point1}); err == nil {
		t.Fatal("accepted future cross-client authority")
	}
}
