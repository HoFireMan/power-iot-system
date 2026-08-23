// Package coverage contains the pure B-02 coverage rules.
package coverage

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"
)

const (
	ProfileVersion          int64 = 1
	MinIntervalMilliseconds int64 = 1000
	BusinessTimezone              = "Asia/Taipei"
)

var (
	ErrCoverageVersion  = errors.New("invalid coverage version")
	ErrCoverageInterval = errors.New("invalid coverage interval")
	ErrCoverageBoundary = errors.New("coverage interval crosses Asia/Taipei midnight")
)

type OptionalInt64 struct {
	Value   int64
	Present bool
	IsNull  bool
}

func (o OptionalInt64) Valid() bool { return o.Present && !o.IsNull }

func (o *OptionalInt64) UnmarshalJSON(data []byte) error {
	o.Present = true
	if string(data) == "null" {
		o.IsNull = true
		o.Value = 0
		return nil
	}
	var value int64
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	o.IsNull = false
	o.Value = value
	return nil
}

func (o OptionalInt64) MarshalJSON() ([]byte, error) {
	if !o.Present || o.IsNull {
		return []byte("null"), nil
	}
	return json.Marshal(o.Value)
}

type Interval struct {
	StartMilliseconds int64
	EndMilliseconds   int64
	TimestampSeconds  int64
}

func (i Interval) Start() time.Time     { return time.UnixMilli(i.StartMilliseconds).UTC() }
func (i Interval) End() time.Time       { return time.UnixMilli(i.EndMilliseconds).UTC() }
func (i Interval) Timestamp() time.Time { return time.Unix(i.TimestampSeconds, 0).UTC() }

func (i Interval) Validate(maxMilliseconds int64) error {
	if i.StartMilliseconds >= i.EndMilliseconds {
		return ErrCoverageInterval
	}
	duration := i.EndMilliseconds - i.StartMilliseconds
	if duration < MinIntervalMilliseconds || maxMilliseconds < MinIntervalMilliseconds || duration > maxMilliseconds {
		return ErrCoverageInterval
	}
	start, end, timestamp := i.Start(), i.End(), i.Timestamp()
	if timestamp.Before(start) || !timestamp.Before(end) {
		return ErrCoverageInterval
	}
	loc, err := time.LoadLocation(BusinessTimezone)
	if err != nil {
		return fmt.Errorf("load %s: %w", BusinessTimezone, err)
	}
	// End is exclusive. Comparing the local dates of start and the last
	// representable millisecond in the interval accepts an exact midnight end
	// while rejecting any interval containing midnight.
	last := end.Add(-time.Millisecond)
	if start.In(loc).Format("2006-01-02") != last.In(loc).Format("2006-01-02") {
		return ErrCoverageBoundary
	}
	return nil
}

func Validate(version OptionalInt64, interval Interval, maxMilliseconds int64) error {
	if !version.Present {
		return nil
	}
	if version.IsNull || version.Value != ProfileVersion {
		return ErrCoverageVersion
	}
	return interval.Validate(maxMilliseconds)
}

type DigestInput struct {
	DeviceID        uint64
	ProfileVersion  int64
	BootCounter     int64
	Sequence        int64
	IntervalStartMs int64
	IntervalEndMs   int64
	RecordedAt      time.Time
	EnergyDeltaKwh  float64
}

// Digest is a fixed-order, versioned binary preimage. Timestamps use UTC Unix
// milliseconds and energy uses the NUMERIC(10,6) persisted scale.
func Digest(in DigestInput) [sha256.Size]byte {
	buf := make([]byte, 0, 8*9+32)
	buf = append(buf, []byte("power-iot/coverage-digest/v1")...)
	put := func(value uint64) {
		var encoded [8]byte
		binary.BigEndian.PutUint64(encoded[:], value)
		buf = append(buf, encoded[:]...)
	}
	put(in.DeviceID)
	put(uint64(in.ProfileVersion))
	put(uint64(in.BootCounter))
	put(uint64(in.Sequence))
	put(uint64(in.IntervalStartMs))
	put(uint64(in.IntervalEndMs))
	put(uint64(in.RecordedAt.UTC().UnixMilli()))
	put(uint64(CanonicalEnergyMicros(in.EnergyDeltaKwh)))
	return sha256.Sum256(buf)
}

func CanonicalEnergyMicros(value float64) int64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0
	}
	return int64(math.Round(value * 1_000_000))
}

type State string

const (
	Proven         State = "PROVEN"
	Gap            State = "GAP"
	Unknown        State = "UNKNOWN"
	Ambiguous      State = "AMBIGUOUS"
	Unattributable State = "UNATTRIBUTABLE"
)

type Evidence struct {
	Start        time.Time
	End          time.Time
	DeviceID     uint
	BootCounter  int64
	Sequence     int64
	EnergyKwh    float64
	Conflict     bool
	Attributable bool
	Barrier      State
}

type Result struct {
	Kwh       *float64
	ThroughAt *time.Time
	State     State
}

// Evaluate computes the continuously proven prefix. It deliberately evaluates
// in interval order, not arrival order, so late replay can repair a prior gap.
func Evaluate(periodStart, requestSnapshot time.Time, evidence []Evidence) Result {
	periodStart = periodStart.UTC()
	requestSnapshot = requestSnapshot.UTC()
	frontier := periodStart
	state := Unknown
	var totalMicros int64
	sort.SliceStable(evidence, func(i, j int) bool {
		if evidence[i].Start.Equal(evidence[j].Start) {
			return evidence[i].End.Before(evidence[j].End)
		}
		return evidence[i].Start.Before(evidence[j].Start)
	})
	var previous *Evidence
	proven := make([]Evidence, 0, len(evidence))

	for _, current := range evidence {
		current.Start = current.Start.UTC()
		current.End = current.End.UTC()
		if current.Barrier == "" && !current.End.After(periodStart) {
			continue
		}
		if current.Start.Before(periodStart) {
			if current.Barrier != "" {
				continue
			}
			state = Unknown
			break
		}
		if current.End.After(requestSnapshot) {
			state = Unknown
			break
		}
		if current.Barrier != "" {
			state = current.Barrier
			frontier = retreatToContainingInterval(frontier, current.Start, proven)
			proven, totalMicros = truncateProven(proven, frontier)
			break
		}
		if current.Start.Before(frontier) {
			state = Ambiguous
			frontier = retreatToContainingInterval(frontier, current.Start, proven)
			proven, totalMicros = truncateProven(proven, frontier)
			break
		}
		if current.Start.After(frontier) {
			state = Gap
			break
		}
		if current.Conflict {
			state = Ambiguous
			break
		}
		if !current.Attributable {
			state = Unattributable
			break
		}
		if previous != nil {
			if current.DeviceID != previous.DeviceID {
				// A physical replacement may begin a new producer epoch while
				// preserving MP continuity. Sequence continuity resumes only
				// once successive intervals come from the same new Device.
			} else {
				switch {
				case current.BootCounter < previous.BootCounter:
					state = Ambiguous
				case current.BootCounter == previous.BootCounter && current.Sequence != previous.Sequence+1:
					state = Gap
				case current.BootCounter != previous.BootCounter && current.Sequence != 0:
					state = Ambiguous
				}
				if state != Unknown && state != "" && state != Proven {
					break
				}
			}
		}
		if !current.Start.Equal(frontier) || !current.End.After(current.Start) {
			state = Gap
			break
		}
		frontier = current.End
		totalMicros += CanonicalEnergyMicros(current.EnergyKwh)
		previousCopy := current
		previous = &previousCopy
		proven = append(proven, current)
		state = Proven
	}

	if frontier.Equal(periodStart) {
		return Result{State: state}
	}
	through := frontier
	total := float64(totalMicros) / 1_000_000
	return Result{Kwh: &total, ThroughAt: &through, State: state}
}

func truncateProven(proven []Evidence, frontier time.Time) ([]Evidence, int64) {
	kept := make([]Evidence, 0, len(proven))
	var totalMicros int64
	for _, evidence := range proven {
		if evidence.End.After(frontier) {
			break
		}
		kept = append(kept, evidence)
		totalMicros += CanonicalEnergyMicros(evidence.EnergyKwh)
	}
	return kept, totalMicros
}

func retreatToContainingInterval(frontier, point time.Time, proven []Evidence) time.Time {
	for i := len(proven) - 1; i >= 0; i-- {
		if !point.Before(proven[i].Start) && point.Before(proven[i].End) {
			return proven[i].Start
		}
	}
	if point.Before(frontier) {
		return point
	}
	return frontier
}
