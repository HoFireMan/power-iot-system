package iot

import (
	"encoding/json"
	"testing"
)

func TestCoverageVersionPresenceAndNull(t *testing.T) {
	base := `{"mac":"AABBCCDDEEFF","v":110,"c":1,"p":110,"kwh":1,"ts":1000,"protocol_version":1,"boot_counter":1,"seq":0,"energy_delta_kwh":0}`
	old, err := DecodeTelemetry([]byte(base))
	if err != nil || old.CoverageVersion.Present {
		t.Fatalf("old=%+v err=%v", old, err)
	}
	withNull := base[:len(base)-1] + `,"coverage_version":null}`
	if _, err := DecodeTelemetry([]byte(withNull)); err == nil {
		t.Fatal("null coverage_version accepted")
	}
	withZero := base[:len(base)-1] + `,"coverage_version":0}`
	if _, err := DecodeTelemetry([]byte(withZero)); err == nil {
		t.Fatal("unsupported coverage_version accepted")
	}
}

func TestCoverageProfileRequiresAllFields(t *testing.T) {
	body := `{"mac":"AABBCCDDEEFF","v":110,"c":1,"p":110,"kwh":1,"ts":1000,"protocol_version":1,"boot_counter":1,"seq":0,"energy_delta_kwh":0,"coverage_version":1,"interval_start_ts":1000000,"interval_end_ts":1002000}`
	payload, err := DecodeTelemetry([]byte(body))
	if err != nil || !payload.CoverageVersion.Valid() || payload.CoverageVersion.Value != 1 {
		t.Fatalf("payload=%+v err=%v", payload, err)
	}
	for _, invalid := range []string{
		`{"mac":"AABBCCDDEEFF","v":110,"c":1,"p":110,"kwh":1,"ts":1000,"protocol_version":1,"boot_counter":1,"seq":0,"energy_delta_kwh":0,"coverage_version":1,"interval_start_ts":1000000}`,
		`{"mac":"AABBCCDDEEFF","v":110,"c":1,"p":110,"kwh":1,"ts":1000,"protocol_version":1,"boot_counter":1,"seq":0,"energy_delta_kwh":0,"coverage_version":1,"interval_start_ts":null,"interval_end_ts":1002000}`,
	} {
		if _, err := DecodeTelemetry([]byte(invalid)); err == nil {
			t.Fatalf("accepted invalid profile: %s", invalid)
		}
	}
}

func TestCoverageMarshalOmitsAbsentFields(t *testing.T) {
	energy := 0.0
	payload := MqttPayload{MacAddress: "AABBCCDDEEFF", Voltage: 110, Current: 1, Power: 110, KwhTotal: 1, Timestamp: 1000, ProtocolVersion: 1, BootCounter: 1, Sequence: 0, EnergyDeltaKwh: &energy}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) == "" || containsJSONKey(body, "coverage_version") {
		t.Fatalf("absent fields were emitted: %s", body)
	}
}

func containsJSONKey(body []byte, key string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return false
	}
	_, ok := fields[key]
	return ok
}
