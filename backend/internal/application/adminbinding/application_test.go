package adminbinding

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"power-iot-backend/internal/core/domain"
)

type fakeLookup struct {
	shops       map[uint]*domain.Shop
	points      map[uuid.UUID]*domain.MeasurementPoint
	devices     map[uint]*domain.Device
	bySerial    map[string]uint
	byMAC       map[string]uint
	assignments map[uuid.UUID]*domain.DeviceAssignment
	activeDev   map[uint]uuid.UUID
	activePoint map[uuid.UUID]uuid.UUID
	relations   map[uint]map[uint]bool
}

func (f *fakeLookup) FindShop(_ context.Context, id uint) (*domain.Shop, error) {
	return f.shops[id], nil
}

func (f *fakeLookup) FindMeasurementPoint(_ context.Context, id uuid.UUID) (*domain.MeasurementPoint, error) {
	return f.points[id], nil
}

func (f *fakeLookup) UserHasShop(_ context.Context, userID, shopID uint) (bool, error) {
	return f.relations[userID][shopID], nil
}

func (f *fakeLookup) FindDeviceByID(_ context.Context, id uint) (*domain.Device, error) {
	return f.devices[id], nil
}

func (f *fakeLookup) FindDeviceBySerial(_ context.Context, serial string) (*domain.Device, error) {
	return f.devices[f.bySerial[serial]], nil
}

func (f *fakeLookup) FindDeviceByMAC(_ context.Context, mac string) (*domain.Device, error) {
	return f.devices[f.byMAC[mac]], nil
}

func (f *fakeLookup) FindAssignment(_ context.Context, id uuid.UUID) (*domain.DeviceAssignment, error) {
	return f.assignments[id], nil
}

func (f *fakeLookup) FindActiveAssignmentByDevice(_ context.Context, id uint) (*domain.DeviceAssignment, error) {
	return f.assignments[f.activeDev[id]], nil
}

func (f *fakeLookup) FindActiveAssignmentByMeasurementPoint(_ context.Context, id uuid.UUID) (*domain.DeviceAssignment, error) {
	return f.assignments[f.activePoint[id]], nil
}

func newBindingFixture() (*fakeLookup, domain.ActorContext, uint, uint, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) {
	shop1, shop2 := uint(11), uint(22)
	mp1, mp2, mp3 := uuid.New(), uuid.New(), uuid.New()
	devA, devB, devC := uint(101), uint(102), uint(103)
	serialA, serialB, serialC := "SERIAL-A", "SERIAL-B", "SERIAL-C"
	lookup := &fakeLookup{
		shops: map[uint]*domain.Shop{
			shop1: {ID: shop1, ClientID: 1, Name: "Shop 1"},
			shop2: {ID: shop2, ClientID: 1, Name: "Shop 2"},
		},
		points: map[uuid.UUID]*domain.MeasurementPoint{
			mp1: {ID: mp1, ShopID: shop1, Name: "MP 1"},
			mp2: {ID: mp2, ShopID: shop1, Name: "MP 2"},
			mp3: {ID: mp3, ShopID: shop2, Name: "MP 3"},
		},
		devices: map[uint]*domain.Device{
			devA: {ID: devA, ShopID: shop2, MacAddress: "AABBCCDDEEFF", SerialNumber: &serialA, Name: "A"},
			devB: {ID: devB, ShopID: shop2, MacAddress: "BBCCDDEEFF00", SerialNumber: &serialB, Name: "B"},
			devC: {ID: devC, ShopID: shop1, MacAddress: "CCDDEEFF0011", SerialNumber: &serialC, Name: "C"},
		},
		bySerial:    map[string]uint{serialA: devA, serialB: devB, serialC: devC},
		byMAC:       map[string]uint{"AABBCCDDEEFF": devA, "BBCCDDEEFF00": devB, "CCDDEEFF0011": devC},
		assignments: map[uuid.UUID]*domain.DeviceAssignment{},
		activeDev:   map[uint]uuid.UUID{},
		activePoint: map[uuid.UUID]uuid.UUID{},
		relations:   map[uint]map[uint]bool{900: {shop1: true, shop2: true}},
	}
	assignmentID := uuid.New()
	lookup.assignments[assignmentID] = &domain.DeviceAssignment{ID: assignmentID, DeviceID: devA, MeasurementPointID: mp1, ValidFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	lookup.activeDev[devA] = assignmentID
	lookup.activePoint[mp1] = assignmentID
	actor := domain.ActorContext{
		ActorID:  900,
		ScopeKey: "tenant:1|shops:11,22|devices:101,102,103",
		Scope: domain.ScopeSnapshot{
			TenantKey:      "tenant:1",
			ShopIDs:        []uint{shop1, shop2},
			DeviceIDs:      []uint{devA, devB, devC},
			AllowedActions: []domain.BindingAction{domain.ActionCreateMeasurementPoint, domain.ActionBind, domain.ActionReplace, domain.ActionRelocate, domain.ActionUnbind},
		},
	}
	return lookup, actor, devA, devB, mp1, mp2, mp3, assignmentID
}

func refID(id uint) domain.DeviceRef           { return domain.DeviceRef{DeviceID: &id} }
func refSerial(serial string) domain.DeviceRef { return domain.DeviceRef{SerialNumber: &serial} }
func refMAC(mac string) domain.DeviceRef       { return domain.DeviceRef{MAC: &mac} }
func codeOf(t *testing.T, err error) domain.ErrorCode {
	t.Helper()
	if err == nil {
		t.Fatal("expected domain error")
	}
	code := domain.CodeOf(err)
	if code == "" {
		t.Fatalf("error %v has no stable code", err)
	}
	return code
}

func assertNoEffectiveTime(t *testing.T, value any) {
	t.Helper()
	typ := reflect.TypeOf(value)
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	for _, field := range []string{"EffectiveAt", "EffectiveTime", "ValidFrom", "ValidTo"} {
		if _, ok := typ.FieldByName(field); ok {
			t.Fatalf("3B-2 plan exposes client/application effective time field %s", field)
		}
	}
}

func TestDeviceRefResolutionSemantics(t *testing.T) {
	lookup, actor, devA, devB, _, mp2, _, _ := newBindingFixture()
	app := New(lookup)
	base := domain.BindDeviceCommand{MeasurementPointID: uuid.New(), RequestIdentity: "bind-ref", Actor: actor}

	tests := []struct {
		name string
		ref  domain.DeviceRef
		want domain.ErrorCode
	}{
		{name: "device id", ref: refID(devA)},
		{name: "serial", ref: refSerial("SERIAL-A")},
		{name: "mac", ref: refMAC("AABBCCDDEEFF")},
		{name: "unknown", ref: refID(999), want: domain.ErrDeviceNotFound},
		{name: "malformed mac", ref: refMAC("AA:BB:CC:DD:EE:FF"), want: domain.ErrMalformedMAC},
		{name: "no identifier", ref: domain.DeviceRef{}, want: domain.ErrInvalidRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base.DeviceRef = tt.ref
			_, err := app.BindDevice(context.Background(), base)
			if tt.want == "" {
				if err == nil {
					t.Fatal("expected later MP occupancy check to be reached")
				}
				if domain.CodeOf(err) == domain.ErrDeviceNotFound {
					t.Fatal("valid DeviceRef unexpectedly failed resolution")
				}
				return
			}
			if got := codeOf(t, err); got != tt.want {
				t.Fatalf("code=%s want=%s", got, tt.want)
			}
		})
	}

	consistent := base
	consistent.MeasurementPointID = mp2
	consistent.DeviceRef = domain.DeviceRef{DeviceID: &devB, SerialNumber: func() *string { value := "SERIAL-B"; return &value }(), MAC: func() *string { value := "BBCCDDEEFF00"; return &value }()}
	if _, err := app.BindDevice(context.Background(), consistent); err != nil {
		t.Fatalf("consistent identifiers were rejected: %v", err)
	}

	serial := "SERIAL-B"
	mac := "BBCCDDEEFF00"
	inconsistent := base
	inconsistent.DeviceRef = domain.DeviceRef{DeviceID: &devA, SerialNumber: &serial, MAC: &mac}
	if got := codeOf(t, func() error { _, err := app.BindDevice(context.Background(), inconsistent); return err }()); got != domain.ErrIdentifiersInconsistent {
		t.Fatalf("inconsistent identifiers code=%s", got)
	}
}

func TestDeviceEligibilityRejectsLegacyAndMalformedDevices(t *testing.T) {
	lookup, actor, _, devB, _, mp2, _, _ := newBindingFixture()
	app := New(lookup)
	legacySerial := ""
	lookup.devices[devB].SerialNumber = &legacySerial
	cmd := domain.BindDeviceCommand{DeviceRef: refID(devB), MeasurementPointID: mp2, RequestIdentity: "legacy-serial", Actor: actor}
	if got := codeOf(t, func() error { _, err := app.BindDevice(context.Background(), cmd); return err }()); got != domain.ErrDeviceNotEligible {
		t.Fatalf("empty serial code=%s", got)
	}
	lookup.devices[devB].SerialNumber = func() *string { value := "SERIAL-B"; return &value }()
	lookup.devices[devB].MacAddress = "not-canonical"
	if got := codeOf(t, func() error { _, err := app.BindDevice(context.Background(), cmd); return err }()); got != domain.ErrDeviceNotEligible {
		t.Fatalf("malformed registered MAC code=%s", got)
	}
	badSerial := ""
	cmd.DeviceRef = domain.DeviceRef{SerialNumber: &badSerial}
	if got := codeOf(t, func() error { _, err := app.BindDevice(context.Background(), cmd); return err }()); got != domain.ErrInvalidSerial {
		t.Fatalf("malformed request serial code=%s", got)
	}
}

func TestLegacyTransitionEligibilitySemantics(t *testing.T) {
	lookup, actor, devA, devB, _, mp2, _, assignmentID := newBindingFixture()
	app := New(lookup)

	lookup.devices[devA].SerialNumber = nil
	replace := domain.ReplaceDeviceCommand{
		CurrentAssignmentID:  assignmentID,
		ReplacementDeviceRef: refID(devB),
		RequestIdentity:      "legacy-replace",
		Actor:                actor,
	}
	if plan, err := app.ReplaceDevice(context.Background(), replace); err != nil {
		t.Fatalf("legacy current Device blocked replacement: %v", err)
	} else if plan.DeviceID != devA || plan.ReplacementDeviceID == nil || *plan.ReplacementDeviceID != devB {
		t.Fatalf("unexpected legacy replacement plan: %+v", plan)
	}

	legacySerial := ""
	lookup.devices[devA].SerialNumber = &legacySerial
	lookup.devices[devA].MacAddress = "legacy-mac"
	relocate := domain.RelocateDeviceCommand{
		CurrentAssignmentID:      assignmentID,
		TargetMeasurementPointID: mp2,
		RequestIdentity:          "legacy-relocate",
		Actor:                    actor,
	}
	if plan, err := app.RelocateDevice(context.Background(), relocate); err != nil {
		t.Fatalf("legacy current Device blocked relocation: %v", err)
	} else if plan.DeviceID != devA || plan.TargetMeasurementPointID == nil || *plan.TargetMeasurementPointID != mp2 {
		t.Fatalf("unexpected legacy relocation plan: %+v", plan)
	}

	if _, err := app.UnbindDevice(context.Background(), domain.UnbindDeviceCommand{
		CurrentAssignmentID: assignmentID,
		RequestIdentity:     "legacy-unbind",
		Actor:               actor,
	}); err != nil {
		t.Fatalf("legacy current Device blocked unbind: %v", err)
	}

	legacyReplacement := *lookup.devices[devB]
	legacyReplacement.SerialNumber = nil
	lookup.devices[devB] = &legacyReplacement
	if got := codeOf(t, func() error { _, err := app.ReplaceDevice(context.Background(), replace); return err }()); got != domain.ErrDeviceNotEligible {
		t.Fatalf("legacy replacement Device code=%s", got)
	}

	if got := codeOf(t, func() error {
		_, err := app.BindDevice(context.Background(), domain.BindDeviceCommand{
			DeviceRef:          refID(devB),
			MeasurementPointID: mp2,
			RequestIdentity:    "legacy-bind",
			Actor:              actor,
		})
		return err
	}()); got != domain.ErrDeviceNotEligible {
		t.Fatalf("legacy new Bind code=%s", got)
	}

}

func TestAssignmentNotFoundIsDistinctFromStaleAssignment(t *testing.T) {
	lookup, actor, _, devB, _, mp2, _, _ := newBindingFixture()
	app := New(lookup)
	unknown := uuid.New()

	if got := codeOf(t, func() error {
		_, err := app.ReplaceDevice(context.Background(), domain.ReplaceDeviceCommand{
			CurrentAssignmentID:  unknown,
			ReplacementDeviceRef: refID(devB),
			RequestIdentity:      "missing-replace",
			Actor:                actor,
		})
		return err
	}()); got != domain.ErrAssignmentNotFound {
		t.Fatalf("missing Replace assignment code=%s", got)
	}
	if got := codeOf(t, func() error {
		_, err := app.RelocateDevice(context.Background(), domain.RelocateDeviceCommand{
			CurrentAssignmentID:      unknown,
			TargetMeasurementPointID: mp2,
			RequestIdentity:          "missing-relocate",
			Actor:                    actor,
		})
		return err
	}()); got != domain.ErrAssignmentNotFound {
		t.Fatalf("missing Relocate assignment code=%s", got)
	}
	if got := codeOf(t, func() error {
		_, err := app.UnbindDevice(context.Background(), domain.UnbindDeviceCommand{
			CurrentAssignmentID: unknown,
			RequestIdentity:     "missing-unbind",
			Actor:               actor,
		})
		return err
	}()); got != domain.ErrAssignmentNotFound {
		t.Fatalf("missing Unbind assignment code=%s", got)
	}
}

func TestAuthorizationSnapshotsAreFrozen(t *testing.T) {
	lookup, actor, _, devB, _, mp2, _, _ := newBindingFixture()
	app := New(lookup)
	cmd := domain.BindDeviceCommand{
		DeviceRef:          refID(devB),
		MeasurementPointID: mp2,
		RequestIdentity:    "snapshot-bind",
		Actor:              actor,
	}
	plan, err := app.BindDevice(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	wantActor := plan.Actor
	wantAudit := plan.Audit.ScopeSnapshot

	cmd.Actor.Scope.ShopIDs[0] = 999
	cmd.Actor.Scope.DeviceIDs[0] = 999
	cmd.Actor.Scope.AllowedActions[0] = domain.ActionUnbind
	if !reflect.DeepEqual(plan.Actor, wantActor) || !reflect.DeepEqual(plan.Audit.ScopeSnapshot, wantAudit) {
		t.Fatal("mutating command Actor changed returned authorization facts")
	}

	plan.Actor.Scope.ShopIDs[0] = 777
	if plan.Audit.ScopeSnapshot.ShopIDs[0] == 777 {
		t.Fatal("plan Actor and audit snapshot share mutable scope slices")
	}
	plan.Audit.ScopeSnapshot.DeviceIDs[0] = 888
	if plan.Actor.Scope.DeviceIDs[0] == 888 {
		t.Fatal("audit snapshot and plan Actor share mutable scope slices")
	}
}

func TestCreateMeasurementPointAuthorizationSnapshotIsFrozen(t *testing.T) {
	lookup, actor, _, _, _, _, _, _ := newBindingFixture()
	app := New(lookup)
	cmd := domain.CreateMeasurementPointCommand{ShopID: 11, Name: "Kitchen", RequestIdentity: "snapshot-create", Actor: actor}
	plan, err := app.CreateMeasurementPoint(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	wantActor := plan.Actor
	wantAudit := plan.Audit.ScopeSnapshot

	cmd.Actor.Scope.ShopIDs[0] = 999
	cmd.Actor.Scope.DeviceIDs[0] = 999
	cmd.Actor.Scope.AllowedActions[0] = domain.ActionUnbind
	if !reflect.DeepEqual(plan.Actor, wantActor) || !reflect.DeepEqual(plan.Audit.ScopeSnapshot, wantAudit) {
		t.Fatal("mutating Create command Actor changed returned authorization facts")
	}
	plan.Actor.Scope.AllowedActions[0] = domain.ActionUnbind
	if plan.Audit.ScopeSnapshot.AllowedActions[0] == domain.ActionUnbind {
		t.Fatal("Create plan Actor and audit snapshot share mutable action slices")
	}
}

func TestCreateMeasurementPointPlanning(t *testing.T) {
	lookup, actor, _, _, _, _, _, _ := newBindingFixture()
	app := New(lookup)
	cmd := domain.CreateMeasurementPointCommand{ShopID: 11, Name: "Kitchen", RequestIdentity: "mp-create-1", Actor: actor}
	first, err := app.CreateMeasurementPoint(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.CreateMeasurementPoint(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if first.MeasurementPointID == uuid.Nil || first.MeasurementPointID == second.MeasurementPointID {
		t.Fatal("measurement point identity was not backend-generated per plan")
	}
	if first.Name != "Kitchen" || first.ShopID != cmd.ShopID || first.Audit.Action != domain.ActionCreateMeasurementPoint {
		t.Fatalf("unexpected create plan: %+v", first)
	}

	for _, name := range []string{"", strings.Repeat("x", 101)} {
		cmd.Name = name
		if got := codeOf(t, func() error { _, err := app.CreateMeasurementPoint(context.Background(), cmd); return err }()); got != domain.ErrInvalidRequest {
			t.Fatalf("name %q code=%s", name, got)
		}
	}
	cmd.Name = "Unknown Shop"
	cmd.ShopID = 999
	if got := codeOf(t, func() error { _, err := app.CreateMeasurementPoint(context.Background(), cmd); return err }()); got != domain.ErrShopNotFound {
		t.Fatalf("unknown shop code=%s", got)
	}
	cmd.ShopID = 22
	unauthorized := actor
	unauthorized.Scope.ShopIDs = []uint{11}
	cmd.Actor = unauthorized
	if got := codeOf(t, func() error { _, err := app.CreateMeasurementPoint(context.Background(), cmd); return err }()); got != domain.ErrSiteScopeDenied {
		t.Fatalf("unauthorized shop code=%s", got)
	}
}

func TestCrossClientBindingTransitionsFailClosedOnRelationalClientFacts(t *testing.T) {
	lookup, actor, devA, devB, _, _, mp3, assignmentID := newBindingFixture()
	lookup.shops[22].ClientID = 2
	clientOne := uint(1)
	lookup.devices[devB].InventoryOwnerClientID = &clientOne
	app := New(lookup)
	crossBind := domain.BindDeviceCommand{DeviceRef: refID(devB), MeasurementPointID: mp3, RequestIdentity: "cross-client-bind", Actor: actor}
	if got := codeOf(t, func() error { _, err := app.BindDevice(context.Background(), crossBind); return err }()); got != domain.ErrTenantScopeDenied {
		t.Fatalf("cross-client bind code=%s", got)
	}

	clientTwo := uint(2)
	lookup.devices[devB].InventoryOwnerClientID = &clientTwo
	crossReplace := domain.ReplaceDeviceCommand{CurrentAssignmentID: assignmentID, ReplacementDeviceRef: refID(devB), RequestIdentity: "cross-client-replace", Actor: actor}
	if got := codeOf(t, func() error { _, err := app.ReplaceDevice(context.Background(), crossReplace); return err }()); got != domain.ErrTenantScopeDenied {
		t.Fatalf("cross-client replace code=%s", got)
	}

	// The device has no owner in this case; the source/target relational path
	// still makes relocation fail closed. Device.ShopID is not consulted.
	lookup.devices[devA].InventoryOwnerClientID = nil
	crossRelocate := domain.RelocateDeviceCommand{CurrentAssignmentID: assignmentID, TargetMeasurementPointID: mp3, RequestIdentity: "cross-client-relocate", Actor: actor}
	if got := codeOf(t, func() error { _, err := app.RelocateDevice(context.Background(), crossRelocate); return err }()); got != domain.ErrTenantScopeDenied {
		t.Fatalf("cross-client relocate code=%s", got)
	}
}

func TestBindPlansEligibilityOccupancyAndIgnoresDeviceShopID(t *testing.T) {
	lookup, actor, devA, devB, mp1, mp2, _, assignmentID := newBindingFixture()
	app := New(lookup)
	deviceScopedToTarget := actor
	deviceScopedToTarget.Scope.ShopIDs = []uint{11}
	deviceScopedToTarget.Scope.DeviceIDs = []uint{devB}
	cmd := domain.BindDeviceCommand{DeviceRef: refID(devB), MeasurementPointID: mp2, Reason: "initial", RequestIdentity: "bind-1", Actor: deviceScopedToTarget}
	plan, err := app.BindDevice(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != domain.ActionBind || plan.DeviceID != devB || plan.TargetMeasurementPointID == nil || *plan.TargetMeasurementPointID != mp2 || plan.CurrentAssignmentID != nil {
		t.Fatalf("unexpected bind plan: %+v", plan)
	}
	assertNoEffectiveTime(t, plan)
	if plan.Audit.DeviceID == nil || *plan.Audit.DeviceID != devB || plan.Audit.NewMeasurementPointID == nil || *plan.Audit.NewMeasurementPointID != mp2 {
		t.Fatalf("incomplete bind audit intent: %+v", plan.Audit)
	}

	alreadyAssigned := cmd
	alreadyAssigned.Actor = actor
	alreadyAssigned.DeviceRef = refID(devA)
	if got := codeOf(t, func() error { _, err := app.BindDevice(context.Background(), alreadyAssigned); return err }()); got != domain.ErrDeviceAlreadyAssigned {
		t.Fatalf("assigned device code=%s", got)
	}
	occupied := cmd
	occupied.Actor = actor
	occupied.DeviceRef = refID(devB)
	occupied.MeasurementPointID = mp1
	if got := codeOf(t, func() error { _, err := app.BindDevice(context.Background(), occupied); return err }()); got != domain.ErrMeasurementPointOccupied {
		t.Fatalf("occupied MP code=%s", got)
	}
	if assignmentID == uuid.Nil {
		t.Fatal("fixture assignment was not created")
	}
}

func TestReplacePlansProtectCurrentAssignmentAndKeepMP(t *testing.T) {
	lookup, actor, devA, devB, mp1, mp2, _, assignmentID := newBindingFixture()
	app := New(lookup)
	deviceScopedToShop := actor
	deviceScopedToShop.Scope.ShopIDs = []uint{11}
	cmd := domain.ReplaceDeviceCommand{CurrentAssignmentID: assignmentID, ReplacementDeviceRef: refSerial("SERIAL-B"), Reason: "replace", RequestIdentity: "replace-1", Actor: deviceScopedToShop}
	plan, err := app.ReplaceDevice(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != domain.ActionReplace || plan.DeviceID != devA || plan.ReplacementDeviceID == nil || *plan.ReplacementDeviceID != devB || plan.SourceMeasurementPointID == nil || *plan.SourceMeasurementPointID != mp1 || plan.TargetMeasurementPointID == nil || *plan.TargetMeasurementPointID != mp1 {
		t.Fatalf("replacement changed MP semantics: %+v", plan)
	}
	assertNoEffectiveTime(t, plan)

	closed := *lookup.assignments[assignmentID]
	closed.ValidTo = func() *time.Time { value := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC); return &value }()
	lookup.assignments[assignmentID] = &closed
	if got := codeOf(t, func() error { _, err := app.ReplaceDevice(context.Background(), cmd); return err }()); got != domain.ErrAssignmentNotCurrent {
		t.Fatalf("stale assignment code=%s", got)
	}

	lookup.assignments[assignmentID] = &domain.DeviceAssignment{ID: assignmentID, DeviceID: devA, MeasurementPointID: mp1}
	lookup.activeDev[devA] = assignmentID
	lookup.activePoint[mp1] = assignmentID
	sameDevice := cmd
	sameDevice.ReplacementDeviceRef = refID(devA)
	if got := codeOf(t, func() error { _, err := app.ReplaceDevice(context.Background(), sameDevice); return err }()); got != domain.ErrInvalidStateTransition {
		t.Fatalf("same physical replacement code=%s", got)
	}
	otherAssignment := uuid.New()
	lookup.assignments[otherAssignment] = &domain.DeviceAssignment{ID: otherAssignment, DeviceID: devB, MeasurementPointID: mp2}
	lookup.activeDev[devB] = otherAssignment
	lookup.activePoint[mp2] = otherAssignment
	if got := codeOf(t, func() error { _, err := app.ReplaceDevice(context.Background(), cmd); return err }()); got != domain.ErrDeviceAlreadyAssigned {
		t.Fatalf("replacement already-active device code=%s", got)
	}
}

func TestRelocatePlansScopeAndOccupancy(t *testing.T) {
	lookup, actor, devA, _, mp1, mp2, mp3, assignmentID := newBindingFixture()
	app := New(lookup)
	deviceScopedToShop := actor
	deviceScopedToShop.Scope.ShopIDs = []uint{11}
	deviceScopedToShop.Scope.DeviceIDs = []uint{devA}
	cmd := domain.RelocateDeviceCommand{CurrentAssignmentID: assignmentID, TargetMeasurementPointID: mp2, Reason: "move", RequestIdentity: "relocate-1", Actor: deviceScopedToShop}
	plan, err := app.RelocateDevice(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if plan.DeviceID != devA || plan.SourceMeasurementPointID == nil || *plan.SourceMeasurementPointID != mp1 || plan.TargetMeasurementPointID == nil || *plan.TargetMeasurementPointID != mp2 {
		t.Fatalf("unexpected relocation plan: %+v", plan)
	}
	assertNoEffectiveTime(t, plan)

	same := cmd
	same.TargetMeasurementPointID = mp1
	if got := codeOf(t, func() error { _, err := app.RelocateDevice(context.Background(), same); return err }()); got != domain.ErrInvalidStateTransition {
		t.Fatalf("same MP code=%s", got)
	}
	lookup.activePoint[mp2] = uuid.New()
	lookup.assignments[lookup.activePoint[mp2]] = &domain.DeviceAssignment{ID: lookup.activePoint[mp2], DeviceID: 103, MeasurementPointID: mp2}
	if got := codeOf(t, func() error { _, err := app.RelocateDevice(context.Background(), cmd); return err }()); got != domain.ErrMeasurementPointOccupied {
		t.Fatalf("occupied target code=%s", got)
	}

	cross := cmd
	cross.TargetMeasurementPointID = mp3
	limited := actor
	limited.Scope.ShopIDs = []uint{11}
	cross.Actor = limited
	if got := codeOf(t, func() error { _, err := app.RelocateDevice(context.Background(), cross); return err }()); got != domain.ErrSiteScopeDenied {
		t.Fatalf("cross-shop scope code=%s", got)
	}
}

func TestUnbindPlansClosureWithoutDeletionOrRetirement(t *testing.T) {
	lookup, actor, devA, _, mp1, _, _, assignmentID := newBindingFixture()
	app := New(lookup)
	cmd := domain.UnbindDeviceCommand{CurrentAssignmentID: assignmentID, Reason: "remove", RequestIdentity: "unbind-1", Actor: actor}
	plan, err := app.UnbindDevice(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != domain.ActionUnbind || plan.DeviceID != devA || plan.SourceMeasurementPointID == nil || *plan.SourceMeasurementPointID != mp1 || plan.TargetMeasurementPointID != nil {
		t.Fatalf("unexpected unbind plan: %+v", plan)
	}
	assertNoEffectiveTime(t, plan)
	if plan.Audit.DeviceID == nil || *plan.Audit.DeviceID != devA {
		t.Fatal("unbind audit did not retain Device identity")
	}

	closed := *lookup.assignments[assignmentID]
	closed.ValidTo = func() *time.Time { value := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC); return &value }()
	lookup.assignments[assignmentID] = &closed
	if got := codeOf(t, func() error { _, err := app.UnbindDevice(context.Background(), cmd); return err }()); got != domain.ErrAssignmentNotCurrent {
		t.Fatalf("stale unbind code=%s", got)
	}
}

func TestActorContextIsMandatoryAndIdentifiersDoNotAuthorize(t *testing.T) {
	lookup, actor, _, devB, _, mp2, _, _ := newBindingFixture()
	app := New(lookup)
	cmd := domain.BindDeviceCommand{DeviceRef: refID(devB), MeasurementPointID: mp2, RequestIdentity: "auth", Actor: domain.ActorContext{}}
	if got := codeOf(t, func() error { _, err := app.BindDevice(context.Background(), cmd); return err }()); got != domain.ErrAuthenticationRequired {
		t.Fatalf("missing actor code=%s", got)
	}
	cmd.Actor = actor
	cmd.Actor.Scope.DeviceIDs = nil
	if got := codeOf(t, func() error { _, err := app.BindDevice(context.Background(), cmd); return err }()); got != domain.ErrDeviceScopeDenied {
		t.Fatalf("identifier-only authorization code=%s", got)
	}
	cmd.Actor = actor
	cmd.Actor.Scope.TenantKey = ""
	if got := codeOf(t, func() error { _, err := app.BindDevice(context.Background(), cmd); return err }()); got != domain.ErrTenantScopeDenied {
		t.Fatalf("missing tenant scope code=%s", got)
	}
	cmd.Actor = actor
	cmd.Actor.Scope.AllowedActions = nil
	if got := codeOf(t, func() error { _, err := app.BindDevice(context.Background(), cmd); return err }()); got != domain.ErrOperationForbidden {
		t.Fatalf("missing operation permission code=%s", got)
	}
}
