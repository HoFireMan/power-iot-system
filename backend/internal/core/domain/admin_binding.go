package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// AdminBindingOperation is the durable, scoped idempotency ledger row for a
// future Admin Binding command. It is intentionally a persistence primitive;
// command orchestration and HTTP replay remain outside this milestone.
type AdminBindingOperation struct {
	ID                   uuid.UUID       `gorm:"type:uuid;primaryKey"`
	OperationID          uuid.UUID       `gorm:"column:operation_id;type:uuid;uniqueIndex;not null"`
	IdempotencyKey       string          `gorm:"column:idempotency_key;size:255;not null"`
	Operation            string          `gorm:"size:100;not null"`
	ScopeKey             string          `gorm:"column:scope_key;size:255;not null"`
	ActorID              uint            `gorm:"column:actor_id;not null"`
	ScopeSnapshot        json.RawMessage `gorm:"column:scope_snapshot;type:jsonb;not null"`
	CanonicalRequestHash []byte          `gorm:"column:canonical_request_hash;type:bytea;not null"`
	CommittedResponse    json.RawMessage `gorm:"column:committed_response;type:jsonb"`
	CreatedAt            time.Time
	CommittedAt          *time.Time
}

func (AdminBindingOperation) TableName() string { return "admin_binding_operations" }

// AdminBindingAudit is an append-only audit fact. Nullable references are
// intentional because CreateMeasurementPoint has no Device or assignment.
type AdminBindingAudit struct {
	ID                    uuid.UUID       `gorm:"type:uuid;primaryKey"`
	OperationID           uuid.UUID       `gorm:"column:operation_id;type:uuid;not null"`
	RequestIdentity       string          `gorm:"column:request_identity;size:255;not null"`
	ActorID               uint            `gorm:"column:actor_id;not null"`
	ScopeKey              string          `gorm:"column:scope_key;size:255;not null"`
	ScopeSnapshot         json.RawMessage `gorm:"column:scope_snapshot;type:jsonb;not null"`
	Action                string          `gorm:"size:40;not null"`
	OccurredAt            time.Time       `gorm:"column:occurred_at;not null"`
	EffectiveAt           *time.Time      `gorm:"column:effective_at"`
	ShopID                *uint           `gorm:"column:shop_id"`
	MeasurementPointID    *uuid.UUID      `gorm:"column:measurement_point_id"`
	DeviceID              *uint           `gorm:"column:device_id"`
	DeviceSerialNumber    *string         `gorm:"column:device_serial_number;size:128"`
	DeviceMAC             *string         `gorm:"column:device_mac;size:12"`
	OldMeasurementPointID *uuid.UUID      `gorm:"column:old_measurement_point_id"`
	NewMeasurementPointID *uuid.UUID      `gorm:"column:new_measurement_point_id"`
	OldAssignmentID       *uuid.UUID      `gorm:"column:old_assignment_id"`
	NewAssignmentID       *uuid.UUID      `gorm:"column:new_assignment_id"`
	Reason                string          `gorm:"column:reason"`
	Metadata              json.RawMessage `gorm:"column:metadata;type:jsonb;not null"`
}

func (AdminBindingAudit) TableName() string { return "admin_binding_audits" }
