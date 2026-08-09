package simulator

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

type Ack struct {
	BootCounter int64  `json:"boot_counter"`
	Sequence    int64  `json:"seq"`
	Status      string `json:"status"`
}

// Identity returns the ACK identity used to match a telemetry waiter.
func (a Ack) Identity() TelemetryIdentity {
	return TelemetryIdentity{BootCounter: a.BootCounter, Sequence: a.Sequence}
}

// IsTerminal reports whether the ACK authorizes local success handling.
// Unknown, invalid, and failed statuses remain non-terminal.
func (a Ack) IsTerminal() bool {
	return a.Status == "stored" || a.Status == "duplicate"
}

func ParseAck(payload []byte) (Ack, error) {
	var ack Ack
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ack); err != nil {
		return Ack{}, fmt.Errorf("decode ACK: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return Ack{}, fmt.Errorf("decode ACK object: %w", err)
	}
	if raw, ok := fields["boot_counter"]; !ok || string(raw) == "null" {
		return Ack{}, fmt.Errorf("ACK is missing boot_counter")
	}
	if raw, ok := fields["seq"]; !ok || string(raw) == "null" {
		return Ack{}, fmt.Errorf("ACK is missing seq")
	}
	if raw, ok := fields["status"]; !ok || string(raw) == "null" {
		return Ack{}, fmt.Errorf("ACK is missing status")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Ack{}, fmt.Errorf("ACK must contain one JSON object")
		}
		return Ack{}, fmt.Errorf("decode ACK trailing data: %w", err)
	}
	if ack.BootCounter < 0 || ack.Sequence < 0 || ack.Status == "" {
		return Ack{}, fmt.Errorf("ACK has invalid identity or status")
	}
	return ack, nil
}

type Command struct {
	CommandID string `json:"command_id"`
	Action    string `json:"action"`
	ExpiresAt int64  `json:"expires_at"`
	Version   string `json:"version,omitempty"`
	URL       string `json:"url,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
	Size      int64  `json:"size,omitempty"`
	Force     bool   `json:"force,omitempty"`
}

func (c Command) Validate(now time.Time) error {
	switch c.Action {
	case "diagnostics", "report_diagnostics", "reboot", "open_config_portal", "reconnect_wifi", "reconnect_mqtt", "ota":
	default:
		return fmt.Errorf("unsupported or destructive action %q", c.Action)
	}
	if c.ExpiresAt <= now.Unix() {
		return fmt.Errorf("command has expired")
	}
	if c.Action == "ota" {
		_, shaErr := hex.DecodeString(c.SHA256)
		if !strings.HasPrefix(c.URL, "https://") || len(c.SHA256) != 64 || shaErr != nil || c.Size <= 0 {
			return fmt.Errorf("invalid OTA command")
		}
	}
	return nil
}

func ParseCommand(payload []byte) (Command, error) {
	var command Command
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&command); err != nil {
		return Command{}, fmt.Errorf("decode command: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Command{}, fmt.Errorf("command must contain one JSON object")
		}
		return Command{}, fmt.Errorf("decode command trailing data: %w", err)
	}
	if command.CommandID == "" || command.Action == "" || command.ExpiresAt <= 0 {
		return Command{}, fmt.Errorf("command is missing command_id, action, or expires_at")
	}
	return command, nil
}

func StatusPayload(mac, bootID, firmware string, online bool) []byte {
	payload := struct {
		Online     bool   `json:"online"`
		MAC        string `json:"mac"`
		BootID     string `json:"boot_id"`
		Firmware   string `json:"fw"`
		QueueCount int    `json:"queue_count"`
		SafeMode   bool   `json:"safe_mode"`
		TimeSynced bool   `json:"time_synced"`
	}{online, mac, bootID, firmware, 0, false, true}
	body, _ := json.Marshal(payload)
	return body
}
