package adminbinding

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"power-iot-backend/internal/core/domain"
)

func TestSortedUniqueUintIDsDeterministicallyCopiesInput(t *testing.T) {
	input := []uint{9, 3, 9, 0, 3, 1}
	got := sortedUniqueUintIDs(input)
	want := []uint{0, 1, 3, 9}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sorted unique IDs=%v, want %v", got, want)
	}
	if !reflect.DeepEqual(input, []uint{9, 3, 9, 0, 3, 1}) {
		t.Fatalf("helper mutated input: %v", input)
	}
}

func TestCanonicalRequestHashIsStableAndExcludesGeneratedIdentity(t *testing.T) {
	deviceID := uint(7)
	ref := domain.DeviceRef{DeviceID: &deviceID}
	cmd := domain.BindDeviceCommand{DeviceRef: ref, MeasurementPointID: uuid.New(), Reason: "install"}
	first, err := canonicalBindHash(cmd)
	if err != nil {
		t.Fatal(err)
	}
	second, err := canonicalBindHash(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) || len(first) != 32 {
		t.Fatalf("hash is not stable SHA-256: %x / %x", first, second)
	}

	createFirst, err := canonicalCreateHash(domain.CreateMeasurementPointCommand{ShopID: 11, Name: "Kitchen"})
	if err != nil {
		t.Fatal(err)
	}
	createSecond, err := canonicalCreateHash(domain.CreateMeasurementPointCommand{ShopID: 11, Name: "Kitchen"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(createFirst, createSecond) {
		t.Fatal("Create MP hash changed between plans; generated UUID must not be hashed")
	}

	changed := cmd
	changed.Reason = "replacement note"
	changedHash, err := canonicalBindHash(changed)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, changedHash) {
		t.Fatal("semantic reason change did not change the canonical request hash")
	}
}

func TestCanonicalDeviceReferenceRepresentationIsSemantic(t *testing.T) {
	deviceID := uint(7)
	serial := "SERIAL-7"
	byID, err := canonicalBindHash(domain.BindDeviceCommand{DeviceRef: domain.DeviceRef{DeviceID: &deviceID}, MeasurementPointID: uuid.Nil})
	if err != nil {
		t.Fatal(err)
	}
	bySerial, err := canonicalBindHash(domain.BindDeviceCommand{DeviceRef: domain.DeviceRef{SerialNumber: &serial}, MeasurementPointID: uuid.Nil})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(byID, bySerial) {
		t.Fatal("different client-supplied DeviceRef representations shared a hash")
	}
}
