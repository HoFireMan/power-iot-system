package iot

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strings"
	"time"
)

const (
	TelemetryTopic = "device/upload/data"
	StatusTopic    = "device/+/status"
	CommandPrefix  = "device"
)

var macPattern = regexp.MustCompile(`^[0-9A-F]{12}$`)

// MqttPayload supports both the original six-field payload and Device Protocol v1.
// EnergyDeltaKwh is a pointer so Protocol v1 can distinguish an explicit zero
// from an omitted or null wire value.
type MqttPayload struct {
	MacAddress string  `json:"mac"`
	Voltage    float64 `json:"v"`
	Current    float64 `json:"c"`
	Power      float64 `json:"p"`
	KwhTotal   float64 `json:"kwh"`
	Timestamp  int64   `json:"ts"`

	ProtocolVersion int      `json:"protocol_version,omitempty"`
	BootID          string   `json:"boot_id,omitempty"`
	BootCounter     int64    `json:"boot_counter,omitempty"`
	Sequence        int64    `json:"seq,omitempty"`
	Seq             int64    `json:"-"` // compatibility alias for callers using the wire name
	PowerFactor     float64  `json:"pf,omitempty"`
	PF              float64  `json:"-"` // compatibility alias
	EnergyDeltaKwh  *float64 `json:"energy_delta_kwh,omitempty"`
	RSSI            int      `json:"rssi,omitempty"`
	ValidSamples    int      `json:"valid_samples,omitempty"`
	InvalidSamples  int      `json:"invalid_samples,omitempty"`
	FirmwareVersion string   `json:"fw,omitempty"`
	FW              string   `json:"-"` // compatibility alias
}

// DecodeTelemetry strictly decodes a payload and validates all values that can
// affect persistence. Unknown fields are rejected to catch firmware drift.
func DecodeTelemetry(data []byte) (MqttPayload, error) {
	var payload MqttPayload
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return MqttPayload{}, fmt.Errorf("decode telemetry: %w", err)
	}
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return MqttPayload{}, errors.New("telemetry must contain one JSON object")
		}
		return MqttPayload{}, fmt.Errorf("decode telemetry trailing data: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return MqttPayload{}, fmt.Errorf("decode telemetry object: %w", err)
	}
	if len(fields) == 0 {
		return MqttPayload{}, errors.New("telemetry must be a non-empty object")
	}
	if err := validateTelemetry(payload, fields); err != nil {
		return MqttPayload{}, err
	}
	payload.Seq = payload.Sequence
	payload.PF = payload.PowerFactor
	payload.FW = payload.FirmwareVersion
	return payload, nil
}

func validateTelemetry(p MqttPayload, fields map[string]json.RawMessage) error {
	for _, required := range []string{"mac", "v", "c", "p", "kwh", "ts"} {
		if !has(fields, required) || string(fields[required]) == "null" {
			return fmt.Errorf("telemetry field %s is required", required)
		}
	}
	mac, err := NormalizeMAC(p.MacAddress)
	if err != nil {
		return err
	}
	p.MacAddress = mac
	if !finiteRange("v", p.Voltage, 0, 999.99) || !finiteRange("c", p.Current, 0, 999.99) || !finiteRange("p", p.Power, 0, 999999.99) || !finiteRange("kwh", p.KwhTotal, 0, 9999999.999) {
		return errors.New("telemetry electrical value is outside the safe range")
	}
	if p.ProtocolVersion != 0 && p.ProtocolVersion != 1 {
		return fmt.Errorf("unsupported protocol_version %d", p.ProtocolVersion)
	}
	if p.ProtocolVersion == 1 {
		if !has(fields, "boot_counter") || !has(fields, "seq") || string(fields["boot_counter"]) == "null" || string(fields["seq"]) == "null" {
			return errors.New("protocol v1 requires boot_counter and seq")
		}
		if p.BootCounter < 0 || p.Sequence < 0 {
			return errors.New("protocol v1 identity is invalid")
		}
		if !finiteRange("pf", p.PowerFactor, -1, 1) || p.RSSI < -150 || p.RSSI > 0 || p.ValidSamples < 0 || p.InvalidSamples < 0 {
			return errors.New("protocol v1 diagnostics are outside the safe range")
		}
		if err := validateEnergyDelta(p.EnergyDeltaKwh); err != nil {
			return err
		}
	}
	return nil
}

func has(fields map[string]json.RawMessage, name string) bool {
	_, ok := fields[name]
	return ok
}

func validateEnergyDelta(value *float64) error {
	if value == nil || !finiteRange("energy_delta_kwh", *value, 0, 9999.999999) {
		return errors.New("protocol v1 requires a finite energy_delta_kwh between 0 and 9999.999999")
	}
	return nil
}

func validatePersistableTelemetry(p MqttPayload) error {
	if p.ProtocolVersion != 1 {
		return nil
	}
	return validateEnergyDelta(p.EnergyDeltaKwh)
}

func finiteRange(name string, value, min, max float64) bool {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < min || value > max {
		return false
	}
	return true
}

// NormalizeMAC returns the canonical topic/database representation: twelve
// uppercase hexadecimal characters without separators.
func NormalizeMAC(value string) (string, error) {
	canonical := strings.ToUpper(strings.NewReplacer(":", "", "-", "", " ", "").Replace(strings.TrimSpace(value)))
	if !macPattern.MatchString(canonical) {
		return "", fmt.Errorf("invalid MAC address %q", value)
	}
	return canonical, nil
}

func ackTopic(mac string) string     { return fmt.Sprintf("%s/%s/telemetry/ack", CommandPrefix, mac) }
func commandTopic(mac string) string { return fmt.Sprintf("%s/%s/command", CommandPrefix, mac) }

// TelemetryAck is the only terminal device queue response.
type TelemetryAck struct {
	BootCounter int64  `json:"boot_counter"`
	Sequence    int64  `json:"seq"`
	Status      string `json:"status"`
}

type DeviceStatusPayload struct {
	Online     bool   `json:"online"`
	MAC        string `json:"mac,omitempty"`
	DeviceID   string `json:"device_id,omitempty"` // legacy MAC-valued compatibility alias
	BootID     string `json:"boot_id,omitempty"`
	Firmware   string `json:"fw,omitempty"`
	IP         string `json:"ip,omitempty"`
	RSSI       int    `json:"rssi,omitempty"`
	QueueCount int    `json:"queue_count,omitempty"`
	SafeMode   bool   `json:"safe_mode,omitempty"`
	TimeSynced bool   `json:"time_synced,omitempty"`
}

type CommandEnvelope struct {
	CommandID string `json:"command_id"`
	Action    string `json:"action"`
	ExpiresAt int64  `json:"expires_at"`
	Version   string `json:"version,omitempty"`
	URL       string `json:"url,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
	Size      int64  `json:"size,omitempty"`
	Force     bool   `json:"force,omitempty"`
}

var supportedActions = map[string]bool{
	"diagnostics": true, "reboot": true, "open_config_portal": true,
	"reconnect_wifi": true, "reconnect_mqtt": true, "ota": true,
}

func (c CommandEnvelope) Validate(now time.Time) error {
	if c.CommandID == "" || c.Action == "" || !supportedActions[c.Action] {
		return errors.New("invalid or unsupported command")
	}
	if c.ExpiresAt <= now.Unix() {
		return errors.New("command has expired")
	}
	if c.Action == "ota" {
		if c.URL == "" || !strings.HasPrefix(c.URL, "https://") || len(c.SHA256) != 64 || !regexp.MustCompile(`^[0-9a-fA-F]{64}$`).MatchString(c.SHA256) || c.Size <= 0 {
			return errors.New("invalid OTA command")
		}
	}
	return nil
}
