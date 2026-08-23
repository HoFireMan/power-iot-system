package domain

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// AlertLog records a notification generated from a telemetry reading.
type AlertLog struct {
	ID        uint64 `gorm:"primaryKey"`
	DeviceID  uint   `gorm:"index;not null"`
	Type      string `gorm:"size:50"`
	Message   string
	Voltage   float64   `gorm:"type:numeric(5,2)"`
	Current   float64   `gorm:"type:numeric(5,2)"`
	Power     float64   `gorm:"type:numeric(8,2)"`
	IsRead    bool      `gorm:"default:false"`
	CreatedAt time.Time `gorm:"index"`
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

// DailyUsage is a derived daily aggregate.
type DailyUsage struct {
	ID       uint64 `gorm:"primaryKey"`
	Date     string `gorm:"index;size:10"`
	DeviceID uint   `gorm:"index;uniqueIndex:idx_daily_device"`

	KwhUsage float64 `gorm:"type:numeric(10,3)"`
	CarbonKg float64 `gorm:"type:numeric(10,3)"`

	CreatedAt time.Time
	UpdatedAt time.Time
}
