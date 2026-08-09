package simulator

import (
	"fmt"
	"math"
	"strings"
	"time"
)

const DefaultMAC = "AABBCCDDEEFF"

// Telemetry is the exact Device Protocol v1 telemetry envelope. Keep the wire
// names aligned with docs/iot/DEVICE_PROTOCOL_V1.md.
type Telemetry struct {
	MAC             string  `json:"mac"`
	Voltage         float64 `json:"v"`
	Current         float64 `json:"c"`
	Power           float64 `json:"p"`
	Kwh             float64 `json:"kwh"`
	Timestamp       int64   `json:"ts"`
	ProtocolVersion int     `json:"protocol_version"`
	BootID          string  `json:"boot_id"`
	BootCounter     int64   `json:"boot_counter"`
	Sequence        int64   `json:"seq"`
	PowerFactor     float64 `json:"pf"`
	EnergyDeltaKwh  float64 `json:"energy_delta_kwh"`
	RSSI            int     `json:"rssi"`
	ValidSamples    int     `json:"valid_samples"`
	InvalidSamples  int     `json:"invalid_samples"`
	FirmwareVersion string  `json:"fw"`
}

// Generator produces internally consistent, deterministic electrical samples.
// It is intentionally not a random independent-field generator: power is
// derived from voltage, current, and power factor, while kWh accumulates from
// the generated power and elapsed interval.
type Generator struct {
	MAC             string
	FirmwareVersion string
	BootCounter     int64
	Sequence        int64
	Kwh             float64
}

func NewGenerator(mac, firmware string, bootCounter, startSequence int64) (Generator, error) {
	canonical, err := NormalizeMAC(mac)
	if err != nil {
		return Generator{}, err
	}
	if firmware == "" {
		firmware = "simulator-1.0.0"
	}
	if bootCounter < 0 || startSequence < 0 {
		return Generator{}, fmt.Errorf("boot counter and sequence must be non-negative")
	}
	return Generator{MAC: canonical, FirmwareVersion: firmware, BootCounter: bootCounter, Sequence: startSequence}, nil
}

func (g *Generator) Next(now time.Time, interval time.Duration) Telemetry {
	sequence := g.Sequence
	// A slow periodic load variation keeps the values realistic without making
	// duplicate mode depend on another random source.
	phase := float64(sequence%120) / 120 * 2 * math.Pi
	voltage := 110 + 1.8*math.Sin(phase)
	current := 1.15 + 0.45*(0.5+0.5*math.Sin(phase+0.7))
	pf := 0.90 + 0.07*(0.5+0.5*math.Sin(phase+1.4))
	power := voltage * current * pf
	delta := power * interval.Hours() / 1000
	if delta < 0 {
		delta = 0
	}
	g.Kwh += delta
	g.Sequence++
	return Telemetry{
		MAC: g.MAC, Voltage: voltage, Current: current, Power: power, Kwh: g.Kwh,
		Timestamp: now.Unix(), ProtocolVersion: 1, BootID: fmt.Sprintf("%s-%d", g.MAC, g.BootCounter),
		BootCounter: g.BootCounter, Sequence: sequence, PowerFactor: pf,
		EnergyDeltaKwh: delta, RSSI: -55, ValidSamples: 60, InvalidSamples: 0,
		FirmwareVersion: g.FirmwareVersion,
	}
}

func (t Telemetry) IsPhysicallyConsistent() bool {
	return t.ProtocolVersion == 1 && t.Voltage >= 108 && t.Voltage <= 112 &&
		t.Current >= 0.5 && t.Current <= 2 && t.Power >= 0 &&
		math.Abs(t.Power-t.Voltage*t.Current*t.PowerFactor) < 0.000001 &&
		t.PowerFactor >= 0.8 && t.PowerFactor <= 1.0 && t.Kwh >= 0
}

func NormalizeMAC(value string) (string, error) {
	canonical := strings.ToUpper(strings.NewReplacer(":", "", "-", "", " ", "").Replace(strings.TrimSpace(value)))
	if len(canonical) != 12 {
		return "", fmt.Errorf("invalid device MAC %q", value)
	}
	for _, r := range canonical {
		if !((r >= '0' && r <= '9') || (r >= 'A' && r <= 'F')) {
			return "", fmt.Errorf("invalid device MAC %q", value)
		}
	}
	return canonical, nil
}
