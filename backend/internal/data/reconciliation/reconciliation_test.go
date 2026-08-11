package reconciliation

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testFacts() FactSet {
	now := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	client1, client2 := uint(10), uint(20)
	return FactSet{SchemaVersion: SchemaVersion, AsOf: now,
		Clients:           []ClientFact{{ID: 10}, {ID: 20}},
		Shops:             []ShopFact{{ID: 100, ClientID: &client1}, {ID: 200, ClientID: &client2}},
		Devices:           []DeviceFact{{ID: 1}, {ID: 2}, {ID: 3}},
		MeasurementPoints: []MeasurementPointFact{{ID: uuid.MustParse("00000000-0000-0000-0000-000000000001"), ShopID: 100}, {ID: uuid.MustParse("00000000-0000-0000-0000-000000000002"), ShopID: 200}},
		DeviceAssignments: []DeviceAssignmentFact{{ID: uuid.MustParse("10000000-0000-0000-0000-000000000001"), DeviceID: 1, MeasurementPointID: uuid.MustParse("00000000-0000-0000-0000-000000000001"), ValidFrom: now.Add(-time.Hour)}, {ID: uuid.MustParse("10000000-0000-0000-0000-000000000002"), DeviceID: 2, MeasurementPointID: uuid.MustParse("00000000-0000-0000-0000-000000000002"), ValidFrom: now.Add(-2 * time.Hour), ValidTo: timePtr(now.Add(-time.Hour))}},
	}
}

func TestNullShopIsExplicitAndProducesPlanIntent(t *testing.T) {
	facts := testFacts()
	facts.Shops[0].ClientID = nil
	classes, err := ClassifyFacts(facts)
	if err != nil {
		t.Fatal(err)
	}
	var shopClass FactClassification
	for _, c := range classes {
		if c.Kind == FactKindShop && c.StableID == StableID("security-reconciliation-shop/v5", "100") {
			shopClass = c
		}
	}
	if shopClass.Classification != ExplicitMappingRequired {
		t.Fatalf("shop=%+v", shopClass)
	}
	_, digest, _ := CanonicalSourceFacts(facts)
	artifact := &MappingArtifact{SchemaVersion: MappingSchema, Version: 5, SourceFactsDigest: hex.EncodeToString(digest), Mappings: []MappingEntry{{Category: MappingShop, ShopID: 100, ClientID: 10}}}
	plan, err := BuildPlan(facts, artifact)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range plan.Items {
		if item.Kind == PlanItemShop && item.ShopID == 100 {
			found = item.SetShopClient && valueUint(item.IntendedClientID) == 10
		}
	}
	if !found {
		t.Fatalf("missing shop intent: %+v", plan.Items)
	}
}

func TestDuplicateLogicalMembershipIsBlocking(t *testing.T) {
	facts := testFacts()
	facts.Users = []UserFact{{ID: 7}}
	facts.UserShopRelations = []UserShopRelationFact{{ID: 1, UserID: 7, ShopID: 100}, {ID: 2, UserID: 7, ShopID: 100}}
	classes, err := ClassifyFacts(facts)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, c := range classes {
		if c.Kind == FactKindMembership && c.Classification == BlockingIntegrityError {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("duplicate membership blockers=%d", count)
	}
}

func TestClassifyAuthorityAndIgnoreLegacyShop(t *testing.T) {
	facts := testFacts()
	legacyShop := uint(200)
	facts.Devices[0].ShopID = legacyShop
	decisions, err := Classify(facts)
	if err != nil {
		t.Fatal(err)
	}
	if decisions[0].Classification != AutoReconcilable || valueUint(decisions[0].AuthorityClientID) != 10 {
		t.Fatalf("device 1 decision=%+v", decisions[0])
	}
	if decisions[1].Classification != ExplicitMappingRequired {
		t.Fatalf("historical-only device should require mapping: %+v", decisions[1])
	}
	if decisions[2].Classification != ExplicitMappingRequired {
		t.Fatalf("unassigned device should require mapping: %+v", decisions[2])
	}
}

func TestInvalidAssignmentDeviceIDsRemainBlockingThroughPlan(t *testing.T) {
	facts := testFacts()
	facts.DeviceAssignments = append(facts.DeviceAssignments,
		DeviceAssignmentFact{ID: uuid.MustParse("10000000-0000-0000-0000-000000000101"), DeviceID: 0, MeasurementPointID: facts.MeasurementPoints[0].ID, ValidFrom: facts.AsOf.Add(-time.Minute)},
		DeviceAssignmentFact{ID: uuid.MustParse("10000000-0000-0000-0000-000000000102"), DeviceID: 999, MeasurementPointID: facts.MeasurementPoints[0].ID, ValidFrom: facts.AsOf.Add(-time.Minute)},
	)
	decisions, err := Classify(facts)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[uint]Decision{}
	for _, decision := range decisions {
		byID[decision.DeviceID] = decision
	}
	for _, id := range []uint{0, 999} {
		if byID[id].Classification != BlockingIntegrityError {
			t.Fatalf("assignment DeviceID %d was not retained as blocking: %+v", id, byID[id])
		}
	}
	plan, err := BuildPlan(facts, nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[uint]bool{}
	for _, item := range plan.Items {
		if item.Kind == PlanItemDevice && (item.DeviceID == 0 || item.DeviceID == 999) {
			seen[item.DeviceID] = true
			if item.Classification != BlockingIntegrityError || item.ExpectedAffectedCount != 0 {
				t.Fatalf("invalid assignment plan item=%+v", item)
			}
		}
	}
	if !seen[0] || !seen[999] {
		t.Fatalf("invalid assignment rows disappeared from plan: seen=%v items=%+v", seen, plan.Items)
	}
}

func TestClassifyFutureCrossClientBlocksAndOwnerCAS(t *testing.T) {
	facts := testFacts()
	facts.DeviceAssignments = append(facts.DeviceAssignments, DeviceAssignmentFact{ID: uuid.MustParse("10000000-0000-0000-0000-000000000003"), DeviceID: 1, MeasurementPointID: uuid.MustParse("00000000-0000-0000-0000-000000000002"), ValidFrom: facts.AsOf.Add(time.Hour)})
	decisions, err := Classify(facts)
	if err != nil {
		t.Fatal(err)
	}
	if decisions[0].Classification != BlockingIntegrityError {
		t.Fatalf("cross-client future assignment=%+v", decisions[0])
	}
	facts = testFacts()
	owner := uint(20)
	facts.Devices[0].InventoryOwnerClientID = &owner
	decisions, err = Classify(facts)
	if err != nil {
		t.Fatal(err)
	}
	if decisions[0].Classification != BlockingIntegrityError {
		t.Fatalf("conflicting owner=%+v", decisions[0])
	}
}

func TestClassifyFactsCoversMembershipCurrentShopAndAuthNoWrite(t *testing.T) {
	facts := testFacts()
	shop := uint(100)
	facts.Users = []UserFact{{ID: 7, CurrentShopID: &shop, AuthEnabled: true}}
	facts.UserShopRelations = []UserShopRelationFact{{ID: 1, UserID: 7, ShopID: 100}}
	classes, err := ClassifyFacts(facts)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, c := range classes {
		seen[c.Kind] = true
		if c.Kind == FactKindAuthNoWrite && c.Classification != AlreadyConsistent {
			t.Fatalf("auth classification=%+v", c)
		}
	}
	for _, kind := range []string{FactKindShop, FactKindDevice, FactKindMembership, FactKindAuthNoWrite} {
		if !seen[kind] {
			t.Fatalf("missing classification kind %s", kind)
		}
	}
	shop = 200
	classes, err = ClassifyFacts(facts)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range classes {
		if c.Kind == FactKindMembership && c.Classification == BlockingIntegrityError {
			return
		}
	}
	t.Fatal("current_shop without membership was not blocked")
}

func TestCanonicalFactsRejectDuplicateTopLevelRows(t *testing.T) {
	facts := testFacts()
	facts.Devices = append(facts.Devices, facts.Devices[0])
	if _, _, err := CanonicalSourceFacts(facts); err == nil {
		t.Fatal("canonical facts accepted duplicate Device row")
	}
}

func TestCanonicalAdminEvidenceIsDeterministicAndComplete(t *testing.T) {
	facts := testFacts()
	facts.Users = []UserFact{{ID: 7}}
	opID := uuid.MustParse("20000000-0000-0000-0000-000000000005")
	operationRowID := uuid.MustParse("40000000-0000-0000-0000-000000000005")
	auditID := uuid.MustParse("30000000-0000-0000-0000-000000000005")
	facts.AdminOperations = []AdminOperationFact{{ID: operationRowID, OperationID: opID, IdempotencyKey: "idem", Operation: "bind", ActorID: 7, ScopeSnapshot: []byte(`{"b":2,"a":1}`), CanonicalRequestHash: []byte{1, 2}, CommittedResponse: []byte(`{"ok":true}`), CreatedAt: facts.AsOf}}
	facts.AdminAudits = []AdminAuditFact{{ID: auditID, OperationID: opID, RequestIdentity: "request", ScopeSnapshot: []byte(`{"z":0,"a":1}`), OccurredAt: facts.AsOf, Metadata: []byte(`{"b":2,"a":1}`)}}
	first, _, err := CanonicalSourceFacts(facts)
	if err != nil {
		t.Fatal(err)
	}
	facts.AdminOperations[0].ScopeSnapshot = []byte(` { "a": 1, "b": 2 } `)
	facts.AdminAudits[0].Metadata = []byte(`{"a":1,"b":2}`)
	second, _, err := CanonicalSourceFacts(facts)
	if err != nil {
		t.Fatal(err)
	}
	if !EqualCanonical(first, second) {
		t.Fatal("admin JSON evidence canonicalization depends on encoding")
	}
	for _, field := range []string{"id", "idempotency_key", "scope_snapshot", "canonical_request_hash", "committed_response", "created_at", "request_identity", "occurred_at", "metadata"} {
		if !strings.Contains(string(first), `"`+field+`"`) {
			t.Fatalf("canonical facts omitted persisted admin field %q: %s", field, first)
		}
	}
	if !strings.Contains(string(first), operationRowID.String()) {
		t.Fatalf("canonical facts omitted AdminOperation primary row ID: %s", first)
	}
}

func TestCanonicalFactsAndPlanAreOrderIndependent(t *testing.T) {
	a := testFacts()
	b := testFacts()
	b.Clients[0], b.Clients[1] = b.Clients[1], b.Clients[0]
	b.Devices[0], b.Devices[2] = b.Devices[2], b.Devices[0]
	ca, da, err := CanonicalSourceFacts(a)
	if err != nil {
		t.Fatal(err)
	}
	cb, db, err := CanonicalSourceFacts(b)
	if err != nil {
		t.Fatal(err)
	}
	if string(ca) != string(cb) || hex.EncodeToString(da) != hex.EncodeToString(db) {
		t.Fatal("fact canonicalization depends on input order")
	}
	pa, err := BuildPlan(a, nil)
	if err != nil {
		t.Fatal(err)
	}
	pb, err := BuildPlan(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(pa.Canonical) != string(pb.Canonical) || pa.PlanID != pb.PlanID {
		t.Fatal("plan canonicalization depends on input order")
	}
	if !pa.Items[0].SetInventoryOwner || pa.Items[0].ExpectedCurrent.ActiveAssignmentID == nil {
		t.Fatalf("missing auto plan CAS: %+v", pa.Items[0])
	}
	for _, item := range pa.Items {
		if item.Kind == PlanItemDevice && item.SetInventoryOwner && item.ExpectedAffectedCount != 1 {
			t.Fatalf("device intent missing expected affected count: %+v", item)
		}
	}
	for _, item := range pa.Items {
		if item.DeviceID != 0 && item.Kind != PlanItemDevice {
			t.Fatalf("device plan item kind=%q", item.Kind)
		}
	}
	for _, key := range []string{"shop_client_updates", "admin_client_provenance_updates", "inventory_owner_updates"} {
		if _, ok := pa.ExpectedAffectedCounts[key]; !ok {
			t.Fatalf("missing stable expected count key %q", key)
		}
	}
}

func TestMappingCategoriesAndStaleSourceBinding(t *testing.T) {
	facts := testFacts()
	_, digest, err := CanonicalSourceFacts(facts)
	if err != nil {
		t.Fatal(err)
	}
	artifact := MappingArtifact{SchemaVersion: MappingSchema, Version: 5, SourceFactsDigest: hex.EncodeToString(digest), Mappings: []MappingEntry{{Category: MappingDevice, DeviceID: 3, ClientID: 20}}}
	if _, err := BuildPlan(facts, &artifact); err != nil {
		t.Fatal(err)
	}
	artifact.SourceFactsDigest = strings.Repeat("0", 64)
	if _, err := BuildPlan(facts, &artifact); err == nil {
		t.Fatal("accepted stale mapping artifact")
	}
	for _, raw := range []string{
		`{"schema":"security-reconciliation-explicit-mapping","version":5,"mappings":[{"category":"unknown","device_id":3,"client_id":20}]}`,
		`{"schema":"security-reconciliation-explicit-mapping","version":5,"mappings":[{"category":"shop_id->client_id","shop_id":100,"device_id":3,"client_id":20}]}`,
	} {
		if _, err := ParseMappingArtifact([]byte(raw)); err == nil {
			t.Fatalf("accepted invalid category artifact %s", raw)
		}
	}
}

func TestMappingStrictCanonicalDigestAndStructuralUse(t *testing.T) {
	facts := testFacts()
	_, sourceDigest, err := CanonicalSourceFacts(facts)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := ParseMappingArtifact([]byte(`{"schema":"security-reconciliation-explicit-mapping","version":5,"source_facts_digest":"0000000000000000000000000000000000000000000000000000000000000000","mappings":[{"device_id":3,"client_id":20}]}`))
	if err != nil {
		t.Fatal(err)
	}
	canonical, digest, err := artifact.CanonicalDigest()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(canonical), `"device_id":3`) || len(digest) != 32 {
		t.Fatalf("canonical=%s digest=%x", canonical, digest)
	}
	for _, raw := range []string{`{"schema":"security-reconciliation-explicit-mapping","version":5,"mappings":[],"unknown":1}`, `{"schema":"security-reconciliation-explicit-mapping","version":5,"mappings":[{"device_id":3,"device_id":3,"client_id":20}]}`} {
		if _, err := ParseMappingArtifact([]byte(raw)); err == nil {
			t.Fatalf("accepted invalid artifact %s", raw)
		}
	}
	artifact.SourceFactsDigest = hex.EncodeToString(sourceDigest)
	plan, err := BuildPlan(facts, &artifact)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Items[2].SetInventoryOwner || valueUint(plan.Items[2].AuthorityClientID) != 20 {
		t.Fatalf("mapping not applied: %+v", plan.Items[2])
	}
}

func TestDeviceFutureAndIntegrityClassificationMatrix(t *testing.T) {
	facts := testFacts()
	futureSame := DeviceAssignmentFact{ID: uuid.MustParse("10000000-0000-0000-0000-000000000003"), DeviceID: 1, MeasurementPointID: facts.MeasurementPoints[0].ID, ValidFrom: facts.AsOf.Add(time.Hour)}
	facts.DeviceAssignments = append(facts.DeviceAssignments, futureSame)
	decisions, err := Classify(facts)
	if err != nil {
		t.Fatal(err)
	}
	if decisions[0].Classification != AutoReconcilable {
		t.Fatalf("active plus same-client future=%+v", decisions[0])
	}
	facts.DeviceAssignments = append(facts.DeviceAssignments,
		DeviceAssignmentFact{ID: uuid.MustParse("10000000-0000-0000-0000-000000000004"), DeviceID: 4, MeasurementPointID: facts.MeasurementPoints[0].ID, ValidFrom: facts.AsOf.Add(time.Hour)},
		DeviceAssignmentFact{ID: uuid.MustParse("10000000-0000-0000-0000-000000000005"), DeviceID: 5, MeasurementPointID: facts.MeasurementPoints[0].ID, ValidFrom: facts.AsOf, ValidTo: timePtr(facts.AsOf)},
		DeviceAssignmentFact{ID: uuid.MustParse("10000000-0000-0000-0000-000000000006"), DeviceID: 6, MeasurementPointID: facts.MeasurementPoints[0].ID, ValidFrom: facts.AsOf.Add(-time.Minute)},
		DeviceAssignmentFact{ID: uuid.MustParse("10000000-0000-0000-0000-000000000006"), DeviceID: 6, MeasurementPointID: facts.MeasurementPoints[0].ID, ValidFrom: facts.AsOf.Add(-time.Minute)},
		DeviceAssignmentFact{ID: uuid.MustParse("10000000-0000-0000-0000-000000000007"), DeviceID: 7, MeasurementPointID: uuid.MustParse("00000000-0000-0000-0000-000000000099"), ValidFrom: facts.AsOf.Add(-time.Minute)},
	)
	facts.Devices = append(facts.Devices, DeviceFact{ID: 4}, DeviceFact{ID: 5}, DeviceFact{ID: 6}, DeviceFact{ID: 7})
	decisions, err = Classify(facts)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[uint]Decision{}
	for _, d := range decisions {
		byID[d.DeviceID] = d
	}
	if byID[4].Classification != ExplicitMappingRequired {
		t.Fatalf("future-only=%+v", byID[4])
	}
	if byID[5].Classification != BlockingIntegrityError {
		t.Fatalf("invalid interval=%+v", byID[5])
	}
	if byID[6].Classification != BlockingIntegrityError {
		t.Fatalf("duplicate active identity=%+v", byID[6])
	}
	if byID[7].Classification != BlockingIntegrityError {
		t.Fatalf("unresolved measurement point=%+v", byID[7])
	}
	owner := uint(10)
	facts.Devices = append(facts.Devices, DeviceFact{ID: 8, InventoryOwnerClientID: &owner})
	decisions, err = Classify(facts)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range decisions {
		if d.DeviceID == 8 && d.Classification != BlockingIntegrityError {
			t.Fatalf("owner without current authority=%+v", d)
		}
	}
}

func TestMappingCategoriesAllowSameNumericIDsAcrossKinds(t *testing.T) {
	raw := `{"schema":"security-reconciliation-explicit-mapping","version":5,"source_facts_digest":"0000000000000000000000000000000000000000000000000000000000000000","mappings":[{"category":"shop_id->client_id","shop_id":7,"client_id":10},{"category":"device_id->client_id","device_id":7,"client_id":10}]}`
	if _, err := ParseMappingArtifact([]byte(raw)); err != nil {
		t.Fatalf("same numeric IDs in distinct categories rejected: %v", err)
	}
}

type collectingStub struct{}

func (collectingStub) CollectV5(context.Context, time.Time) (FactSet, error) { return testFacts(), nil }

type pinnedCollectingStub struct {
	seen ReadOnlyConnection
}

func (p *pinnedCollectingStub) CollectV5(context.Context, time.Time) (FactSet, error) {
	return testFacts(), nil
}
func (p *pinnedCollectingStub) CollectV5Pinned(_ context.Context, _ time.Time, conn ReadOnlyConnection) (FactSet, error) {
	p.seen = conn
	return testFacts(), nil
}

type trackingCallerTransaction struct {
	queries int
}

func (tx *trackingCallerTransaction) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	tx.queries++
	return nil, errors.New("tracking transaction has no query rows")
}
func (tx *trackingCallerTransaction) QueryRowContext(context.Context, string, ...any) *sql.Row {
	tx.queries++
	return nil
}

type trackingFence struct{ transaction ReadOnlyConnection }

func (trackingFence) ExclusiveReconciliationFence() bool { return true }
func (f trackingFence) PinnedTransaction() ReadOnlyConnection {
	return f.transaction
}

func TestFreshRecheckUsesCallerOwnedPinnedTransaction(t *testing.T) {
	unpinned := FencedRechecker{Collector: collectingStub{}, Now: func() time.Time { return time.Unix(1, 0) }}
	if _, err := unpinned.RecheckV5(context.Background(), ReadOnlyFence{}); err == nil {
		t.Fatal("recheck accepted collector without pinned query interface")
	}
	collector := &pinnedCollectingStub{}
	pinned := FencedRechecker{Collector: collector, Now: func() time.Time { return time.Unix(1, 0) }}
	if _, err := pinned.RecheckV5(context.Background(), nil); err == nil {
		t.Fatal("recheck accepted missing fence")
	}
	transaction := &trackingCallerTransaction{}
	facts, err := pinned.RecheckV5(context.Background(), trackingFence{transaction: transaction})
	if err != nil || facts.SchemaVersion != SchemaVersion {
		t.Fatalf("recheck=%+v err=%v", facts, err)
	}
	if collector.seen != transaction {
		t.Fatal("collector did not receive the caller-owned transaction")
	}
}
