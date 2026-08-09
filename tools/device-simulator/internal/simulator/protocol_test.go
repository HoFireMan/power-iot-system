package simulator

import (
	"strings"
	"testing"
	"time"
)

func TestStatusPayloadUsesFormalMACIdentity(t *testing.T) {
	payload := string(StatusPayload("AABBCCDDEEFF", "AABBCCDDEEFF-1", "simulator-1.0.0", true))
	if !strings.Contains(payload, `"mac":"AABBCCDDEEFF"`) {
		t.Fatalf("status payload does not use formal mac identity: %s", payload)
	}
	if strings.Contains(payload, `"device_id"`) {
		t.Fatalf("status payload still relies on legacy device_id identity: %s", payload)
	}
}

func TestParseACK(t *testing.T) {
	ack, err := ParseAck([]byte(`{"boot_counter":7,"seq":123,"status":"stored"}`))
	if err != nil {
		t.Fatal(err)
	}
	if ack.BootCounter != 7 || ack.Sequence != 123 || ack.Status != "stored" {
		t.Fatalf("unexpected ACK: %+v", ack)
	}
	for _, input := range []string{
		`{"boot_counter":7,"seq":123}`,
		`{"boot_counter":7,"seq":123,"status":"stored"} trailing`,
		`{"boot_counter":7,"seq":123,"status":"stored","secret":"x"}`,
	} {
		if _, err := ParseAck([]byte(input)); err == nil {
			t.Errorf("accepted malformed ACK: %s", input)
		}
	}
}

func TestAckTerminalClassification(t *testing.T) {
	for _, test := range []struct {
		status   string
		terminal bool
	}{
		{status: "stored", terminal: true},
		{status: "duplicate", terminal: true},
		{status: "unknown_device", terminal: false},
		{status: "unknown_assignment", terminal: false},
		{status: "invalid", terminal: false},
		{status: "failed", terminal: false},
	} {
		t.Run(test.status, func(t *testing.T) {
			ack := Ack{Status: test.status}
			if got := ack.IsTerminal(); got != test.terminal {
				t.Fatalf("status %q terminal=%v, want %v", test.status, got, test.terminal)
			}
		})
	}
}

func TestParseCommand(t *testing.T) {
	command, err := ParseCommand([]byte(`{"command_id":"cmd-1","action":"diagnostics","expires_at":1786107600}`))
	if err != nil {
		t.Fatal(err)
	}
	if command.CommandID != "cmd-1" || command.Action != "diagnostics" {
		t.Fatalf("unexpected command: %+v", command)
	}
	if err := command.Validate(time.Unix(1786000000, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseCommand([]byte(`{"command_id":"cmd-1","action":"reboot","expires_at":0}`)); err == nil {
		t.Fatal("accepted command without expiry")
	}
	for _, action := range []string{"factory_reset", "clear_telemetry_queue"} {
		command.Action = action
		if command.Validate(time.Unix(1786000000, 0)) == nil {
			t.Fatalf("accepted destructive action %s", action)
		}
	}
}
