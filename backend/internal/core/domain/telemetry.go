package domain

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// AlertLog records a notification generated from a telemetry reading.
type AlertLog struct {
	ID                 uint64     `gorm:"primaryKey"`
	DeviceID           uint       `gorm:"index;not null"`
	MeasurementPointID *uuid.UUID `gorm:"column:measurement_point_id;index"`
	LegacyUnresolved   bool       `gorm:"column:legacy_unresolved;not null;default:false"`
	Type               string     `gorm:"size:50"`
	Message            string
	Voltage            float64 `gorm:"type:numeric(5,2)"`
	Current            float64 `gorm:"type:numeric(5,2)"`
	Power              float64 `gorm:"type:numeric(8,2)"`
	IsRead             bool    `gorm:"default:false"`
	// RecordedAt is the telemetry event instant. CreatedAt remains populated for
	// legacy consumers and is the Alert History ordering instant.
	RecordedAt time.Time `gorm:"column:recorded_at;not null;index"`
	CreatedAt  time.Time `gorm:"index"`
}

// MeasurementPointAlertSetting is the authoritative MP-centered alert policy.
type MeasurementPointAlertSetting struct {
	MeasurementPointID uuid.UUID `gorm:"column:measurement_point_id;type:uuid;primaryKey"`
	QuietHoursStart    string    `gorm:"column:quiet_hours_start"`
	QuietHoursEnd      string    `gorm:"column:quiet_hours_end"`
	PowerThresholdW    float64   `gorm:"column:power_threshold_w;not null;default:10"`
	IsEnabled          bool      `gorm:"not null;default:true"`
	UpdatedAt          time.Time `gorm:"column:updated_at"`
}

// MeasurementPointCurfewState stores the last accepted edge state. It is
// serialized in the telemetry transaction so duplicate/out-of-order delivery
// cannot produce duplicate rising-edge alerts.
type MeasurementPointCurfewState struct {
	MeasurementPointID uuid.UUID  `gorm:"column:measurement_point_id;type:uuid;primaryKey"`
	InCurfew           bool       `gorm:"column:in_curfew;not null;default:false"`
	LastEventAt        *time.Time `gorm:"column:last_event_at"`
}

func (a *AlertLog) BeforeCreate(_ *gorm.DB) error {
	if a.RecordedAt.IsZero() {
		a.RecordedAt = a.CreatedAt
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = a.RecordedAt
	}
	return nil
}

// PowerReading is the canonical persisted telemetry model. The domain package
// is the runtime source of truth; data/models/schema.go mirrors it for the
// legacy schema package until that package is removed.
type PowerReading struct {
	ID uint64 `gorm:"primaryKey"`
	// Time is retained for compatibility with the pre-migration backend.
	Time               time.Time  `gorm:"column:time"`
	RecordedAt         time.Time  `gorm:"column:recorded_at;not null"`
	ReceivedAt         time.Time  `gorm:"column:received_at"`
	MeasurementPointID *uuid.UUID `gorm:"column:measurement_point_id;index"`
	DeviceID           uint       `gorm:"index;not null"`

	Voltage        float64  `gorm:"type:numeric(5,2)"`
	Current        float64  `gorm:"type:numeric(5,2)"`
	Power          float64  `gorm:"type:numeric(8,2)"` // legacy name
	ActivePower    float64  `gorm:"column:active_power;type:numeric(8,2)"`
	KwhTotal       float64  `gorm:"type:numeric(10,3)"`
	EnergyDeltaKwh *float64 `gorm:"type:numeric(10,6)"`

	PowerFactor           *float64 `gorm:"type:numeric(5,4)"`
	RSSI                  *int     `gorm:"column:rssi"`
	ProtocolVersion       int      `gorm:"default:0"`
	BootID                string   `gorm:"size:80"`
	BootCounter           *int64
	Sequence              *int64     `gorm:"column:sequence"`
	ValidSamples          *int       `gorm:"column:valid_samples"`
	InvalidSamples        *int       `gorm:"column:invalid_samples"`
	FirmwareVersion       string     `gorm:"column:firmware_version;size:80"`
	LegacyFirmwareVersion string     `gorm:"column:fw;size:80"`
	CoverageVersion       *int64     `gorm:"column:coverage_version"`
	IntervalStart         *time.Time `gorm:"column:interval_start"`
	IntervalEnd           *time.Time `gorm:"column:interval_end"`
}

// BeforeCreate keeps legacy writers compatible while the runtime ingest path
// is migrated. It does not resolve assignments or perform deduplication.
func (p *PowerReading) BeforeCreate(_ *gorm.DB) error {
	if p.RecordedAt.IsZero() {
		if !p.Time.IsZero() {
			p.RecordedAt = p.Time
		} else {
			p.RecordedAt = time.Now().UTC()
		}
	}
	if p.Time.IsZero() {
		p.Time = p.RecordedAt
	}
	if p.ReceivedAt.IsZero() {
		p.ReceivedAt = time.Now().UTC()
	}
	if p.ActivePower == 0 && p.Power != 0 {
		p.ActivePower = p.Power
	}
	return nil
}

// MeasurementPoint is the permanent logical identity for a monitored point.
// Shop is the current compatibility implementation of the Site role.
type MeasurementPoint struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey"`
	ShopID    uint      `gorm:"column:shop_id;not null;index"`
	Name      string    `gorm:"size:100;not null"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

// DeviceAssignment records a historical half-open [ValidFrom, ValidTo)
// relationship. Exclusion constraints are owned by SQL migrations.
type DeviceAssignment struct {
	ID                 uuid.UUID `gorm:"type:uuid;primaryKey"`
	DeviceID           uint      `gorm:"index;not null"`
	MeasurementPointID uuid.UUID `gorm:"column:measurement_point_id;index;not null"`
	ValidFrom          time.Time `gorm:"not null"`
	ValidTo            *time.Time
	CreatedAt          time.Time
}

// TelemetryIngestKey is the ordinary PostgreSQL idempotency boundary.
type TelemetryIngestKey struct {
	ID                      uuid.UUID `gorm:"type:uuid;primaryKey"`
	DeviceID                uint      `gorm:"not null"`
	BootCounter             int64     `gorm:"not null"`
	Sequence                int64     `gorm:"column:sequence;not null"`
	CreatedAt               time.Time
	ReceivedAt              time.Time
	CanonicalCoverageDigest []byte `gorm:"column:canonical_coverage_digest;type:bytea"`
	ConflictDetected        bool   `gorm:"column:conflict_detected;not null;default:false"`
}

// DailyUsage is a legacy compatibility representation. There is currently no
// active writer or reader; any future authoritative row must be keyed by
// Date + MeasurementPointID. Existing device-day rows remain explicitly
// unresolved unless an approved source can prove their MP identity.
type DailyUsage struct {
	ID                 uint64     `gorm:"primaryKey"`
	Date               string     `gorm:"index;size:10"`
	MeasurementPointID *uuid.UUID `gorm:"column:measurement_point_id;index"`
	DeviceID           *uint      `gorm:"column:device_id;index"`
	LegacyUnresolved   bool       `gorm:"column:legacy_unresolved;not null;default:false"`

	KwhUsage float64 `gorm:"type:numeric(10,3)"`
	CarbonKg float64 `gorm:"type:numeric(10,3)"`

	CreatedAt time.Time
	UpdatedAt time.Time
}
