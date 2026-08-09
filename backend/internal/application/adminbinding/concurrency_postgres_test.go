package adminbinding

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"power-iot-backend/internal/core/domain"
)

func TestExecutorConcurrentBindsSameDeviceHaveOneWinner(t *testing.T) {
	db := openExecutorDB(t)
	fixture := newExecutorFixture(t, db, 2, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		i := i
		go func() {
			_, err := NewExecutor(db).BindDevice(ctx, domain.BindDeviceCommand{DeviceRef: idRef(fixture.devices[0].ID), MeasurementPointID: fixture.points[i].ID, RequestIdentity: fmt.Sprintf("same-device-%d-%s", i, uuid.NewString()), Actor: commandActor(fixture)})
			results <- err
		}()
	}
	wins := 0
	for i := 0; i < 2; i++ {
		if err := <-results; err == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("same Device concurrent binds winners=%d, want one", wins)
	}
}

func TestExecutorReplacementCandidateVsBindHasOneWinner(t *testing.T) {
	db := openExecutorDB(t)
	fixture := newExecutorFixture(t, db, 2, 2)
	assignment := domain.DeviceAssignment{ID: uuid.New(), DeviceID: fixture.devices[0].ID, MeasurementPointID: fixture.points[0].ID, ValidFrom: time.Now().UTC().Add(-time.Hour)}
	if err := db.Create(&assignment).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	results := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := NewExecutor(db).ReplaceDevice(ctx, domain.ReplaceDeviceCommand{CurrentAssignmentID: assignment.ID, ReplacementDeviceRef: idRef(fixture.devices[1].ID), RequestIdentity: "replace-vs-bind-" + uuid.NewString(), Actor: commandActor(fixture)})
		results <- err
	}()
	go func() {
		defer wg.Done()
		_, err := NewExecutor(db).BindDevice(ctx, domain.BindDeviceCommand{DeviceRef: idRef(fixture.devices[1].ID), MeasurementPointID: fixture.points[1].ID, RequestIdentity: "bind-vs-replace-" + uuid.NewString(), Actor: commandActor(fixture)})
		results <- err
	}()
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("replacement-candidate race winners=%d, want one", wins)
	}
}

func TestExecutorReplaceVsUnbindHasOneWinner(t *testing.T) {
	db := openExecutorDB(t)
	fixture := newExecutorFixture(t, db, 1, 2)
	assignment := domain.DeviceAssignment{ID: uuid.New(), DeviceID: fixture.devices[0].ID, MeasurementPointID: fixture.points[0].ID, ValidFrom: time.Now().UTC().Add(-time.Hour)}
	if err := db.Create(&assignment).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	results := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := NewExecutor(db).ReplaceDevice(ctx, domain.ReplaceDeviceCommand{CurrentAssignmentID: assignment.ID, ReplacementDeviceRef: idRef(fixture.devices[1].ID), RequestIdentity: "replace-vs-unbind-" + uuid.NewString(), Actor: commandActor(fixture)})
		results <- err
	}()
	go func() {
		defer wg.Done()
		_, err := NewExecutor(db).UnbindDevice(ctx, domain.UnbindDeviceCommand{CurrentAssignmentID: assignment.ID, RequestIdentity: "unbind-vs-replace-" + uuid.NewString(), Actor: commandActor(fixture)})
		results <- err
	}()
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("replace/unbind race winners=%d, want one", wins)
	}
}

func TestExecutorReplaceVsRelocateHasOneWinner(t *testing.T) {
	db := openExecutorDB(t)
	fixture := newExecutorFixture(t, db, 2, 2)
	assignment := domain.DeviceAssignment{ID: uuid.New(), DeviceID: fixture.devices[0].ID, MeasurementPointID: fixture.points[0].ID, ValidFrom: time.Now().UTC().Add(-time.Hour)}
	if err := db.Create(&assignment).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	results := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := NewExecutor(db).ReplaceDevice(ctx, domain.ReplaceDeviceCommand{CurrentAssignmentID: assignment.ID, ReplacementDeviceRef: idRef(fixture.devices[1].ID), RequestIdentity: "replace-vs-relocate-" + uuid.NewString(), Actor: commandActor(fixture)})
		results <- err
	}()
	go func() {
		defer wg.Done()
		_, err := NewExecutor(db).RelocateDevice(ctx, domain.RelocateDeviceCommand{CurrentAssignmentID: assignment.ID, TargetMeasurementPointID: fixture.points[1].ID, RequestIdentity: "relocate-vs-replace-" + uuid.NewString(), Actor: commandActor(fixture)})
		results <- err
	}()
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("replace/relocate race winners=%d, want one", wins)
	}
}

func TestExecutorRelocateVsUnbindHasOneWinner(t *testing.T) {
	db := openExecutorDB(t)
	fixture := newExecutorFixture(t, db, 2, 1)
	assignment := domain.DeviceAssignment{ID: uuid.New(), DeviceID: fixture.devices[0].ID, MeasurementPointID: fixture.points[0].ID, ValidFrom: time.Now().UTC().Add(-time.Hour)}
	if err := db.Create(&assignment).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	results := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := NewExecutor(db).RelocateDevice(ctx, domain.RelocateDeviceCommand{CurrentAssignmentID: assignment.ID, TargetMeasurementPointID: fixture.points[1].ID, RequestIdentity: "relocate-vs-unbind-" + uuid.NewString(), Actor: commandActor(fixture)})
		results <- err
	}()
	go func() {
		defer wg.Done()
		_, err := NewExecutor(db).UnbindDevice(ctx, domain.UnbindDeviceCommand{CurrentAssignmentID: assignment.ID, RequestIdentity: "unbind-vs-relocate-" + uuid.NewString(), Actor: commandActor(fixture)})
		results <- err
	}()
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("relocate/unbind race winners=%d, want one", wins)
	}
}
