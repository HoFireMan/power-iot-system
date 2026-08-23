package iot

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestDecodeLegacyTelemetry(t *testing.T) {
	payload, err := DecodeTelemetry([]byte(`{"mac":"aa:bb:cc:dd:ee:ff","v":110.2,"c":1.3,"p":143.0,"kwh":12.3,"ts":1786021200}`))
	if err != nil {
		t.Fatal(err)
	}
	if payload.ProtocolVersion != 0 || payload.Voltage != 110.2 {
		t.Fatalf("unexpected legacy payload: %+v", payload)
	}
}

func TestDecodeProtocolV1Telemetry(t *testing.T) {
	payload, err := DecodeTelemetry([]byte(`{"mac":"AABBCCDDEEFF","v":110.25,"c":1.32,"p":142.85,"kwh":12.345,"ts":1786021200,"protocol_version":1,"boot_id":"AABBCCDDEEFF-7","boot_counter":7,"seq":123,"pf":0.98,"energy_delta_kwh":0.00238,"rssi":-61,"valid_samples":117,"invalid_samples":3,"fw":"1.0.0-test1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if payload.ProtocolVersion != 1 || payload.BootCounter != 7 || payload.Sequence != 123 || payload.EnergyDeltaKwh == nil || *payload.EnergyDeltaKwh != 0.00238 {
		t.Fatalf("unexpected v1 payload: %+v", payload)
	}
}

func TestDecodeProtocolV1AcceptsExplicitZeroEnergyDelta(t *testing.T) {
	payload, err := DecodeTelemetry([]byte(`{"mac":"AABBCCDDEEFF","v":110,"c":1,"p":110,"kwh":12,"ts":1786021200,"protocol_version":1,"boot_counter":7,"seq":123,"energy_delta_kwh":0}`))
	if err != nil {
		t.Fatal(err)
	}
	if payload.EnergyDeltaKwh == nil || *payload.EnergyDeltaKwh != 0 {
		t.Fatalf("explicit zero was not preserved: %+v", payload)
	}
}

func TestDecodeProtocolV1RequiresExplicitNonNullEnergyDelta(t *testing.T) {
	base := `{"mac":"AABBCCDDEEFF","v":110,"c":1,"p":110,"kwh":12,"ts":1786021200,"protocol_version":1,"boot_counter":7,"seq":123}`
	for _, input := range []string{
		base,
		strings.Replace(base, `,"protocol_version"`, `,"energy_delta_kwh":null,"protocol_version"`, 1),
	} {
		if _, err := DecodeTelemetry([]byte(input)); err == nil {
			t.Fatalf("accepted missing/null v1 energy delta: %s", input)
		}
	}
}

func TestDecodeProtocolV1RejectsEnergyDeltaOutsideRange(t *testing.T) {
	base := `{"mac":"AABBCCDDEEFF","v":110,"c":1,"p":110,"kwh":12,"ts":1786021200,"protocol_version":1,"boot_counter":7,"seq":123,"energy_delta_kwh":%s}`
	for _, value := range []string{"-0.001", "10000"} {
		if _, err := DecodeTelemetry([]byte(fmt.Sprintf(base, value))); err == nil {
			t.Errorf("accepted invalid v1 energy delta %s", value)
		}
	}
}

func TestDecodeRejectsUnsafeNumbers(t *testing.T) {
	cases := []string{
		`{"mac":"AABBCCDDEEFF","v":NaN,"c":1,"p":1,"kwh":1,"ts":1}`,
		`{"mac":"AABBCCDDEEFF","v":110,"c":1,"p":1,"kwh":-1,"ts":1}`,
		`{"mac":"AABBCCDDEEFF","v":110,"c":1,"p":1,"kwh":1,"ts":1,"protocol_version":1,"boot_counter":1,"seq":1,"rssi":-151}`,
		`{"mac":"AABBCCDDEEFF","v":110,"c":1,"p":1,"kwh":1,"ts":1,"protocol_version":1,"boot_counter":1,"seq":1,"energy_delta_kwh":Infinity}`,
	}
	for _, input := range cases {
		if _, err := DecodeTelemetry([]byte(input)); err == nil {
			t.Errorf("accepted unsafe payload: %s", input)
		}
	}
	if _, err := DecodeTelemetry([]byte(`{"mac":"AABBCCDDEEFF","v":1e999,"c":1,"p":1,"kwh":1,"ts":1}`)); err == nil {
		t.Error("accepted overflow number")
	}
}

func TestDecodeRejectsUnknownAndTrailingJSON(t *testing.T) {
	base := `{"mac":"AABBCCDDEEFF","v":110,"c":1,"p":110,"kwh":12,"ts":1786021200}`
	for _, input := range []string{
		strings.Replace(base, `}`, `,"unexpected":true}`, 1),
		base + ` {"another":true}`,
		base + ` trailing`,
	} {
		if _, err := DecodeTelemetry([]byte(input)); err == nil {
			t.Errorf("accepted non-strict telemetry JSON: %s", input)
		}
	}
}

func TestNormalizeMAC(t *testing.T) {
	for _, input := range []string{"AA:BB:CC:DD:EE:FF", "aa-bb-cc-dd-ee-ff", "AABBCCDDEEFF"} {
		got, err := NormalizeMAC(input)
		if err != nil || got != "AABBCCDDEEFF" {
			t.Fatalf("input %q got %q, %v", input, got, err)
		}
	}
	if _, err := NormalizeMAC("AABBCCDDEE"); err == nil {
		t.Fatal("accepted short MAC")
	}
}

func TestServerTimeFallback(t *testing.T) {
	before := time.Now().UTC()
	got := telemetryTime(1)
	after := time.Now().UTC()
	if got.Before(before) || got.After(after) {
		t.Fatalf("fallback time %v outside %v..%v", got, before, after)
	}
	if telemetryTime(1786021200).Unix() != 1786021200 {
		t.Fatal("valid device time was replaced")
	}
}

func TestCommandPayloadGeneration(t *testing.T) {
	command := CommandEnvelope{CommandID: "cmd-unique", Action: "diagnostics", ExpiresAt: time.Now().Add(time.Minute).Unix()}
	if err := command.Validate(time.Now()); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) == "" || !strings.Contains(string(body), "command_id") || !strings.Contains(string(body), "expires_at") {
		t.Fatalf("missing command envelope fields: %s", body)
	}
	for _, action := range []string{"factory_reset", "clear_telemetry_queue"} {
		command.Action = action
		if command.Validate(time.Now()) == nil {
			t.Fatalf("destructive action accepted: %s", action)
		}
	}
}
