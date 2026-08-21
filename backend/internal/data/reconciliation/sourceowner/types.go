// Package sourceowner owns trusted source evidence construction for
// reconciliation. Its evidence constructor is intentionally unexported;
// consumers can validate/read sealed evidence but cannot mint it.
package sourceowner

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

const SchemaVersion = "v5"

type ClientFact struct {
	ID uint `json:"id"`
}

type ShopFact struct {
	ID       uint  `json:"id"`
	ClientID *uint `json:"client_id"`
}

type UserFact struct {
	ID            uint  `json:"id"`
	CurrentShopID *uint `json:"current_shop_id,omitempty"`
	AuthEnabled   bool  `json:"auth_enabled"`
}

type UserShopRelationFact struct {
	ID     uint `json:"id"`
	UserID uint `json:"user_id"`
	ShopID uint `json:"shop_id"`
}

type DeviceFact struct {
	ID                     uint  `json:"id"`
	ShopID                 uint  `json:"shop_id"`
	InventoryOwnerClientID *uint `json:"inventory_owner_client_id,omitempty"`
}

type MeasurementPointFact struct {
	ID     uuid.UUID `json:"id"`
	ShopID uint      `json:"shop_id"`
}

type DeviceAssignmentFact struct {
	ID                 uuid.UUID  `json:"id"`
	DeviceID           uint       `json:"device_id"`
	MeasurementPointID uuid.UUID  `json:"measurement_point_id"`
	ValidFrom          time.Time  `json:"valid_from"`
	ValidTo            *time.Time `json:"valid_to,omitempty"`
}

type AdminOperationFact struct {
	ID                   uuid.UUID       `json:"id"`
	OperationID          uuid.UUID       `json:"operation_id"`
	IdempotencyKey       string          `json:"idempotency_key"`
	Operation            string          `json:"operation"`
	ActorID              uint            `json:"actor_id"`
	ScopeKey             string          `json:"scope_key"`
	ScopeSnapshot        json.RawMessage `json:"scope_snapshot"`
	CanonicalRequestHash []byte          `json:"canonical_request_hash"`
	ClientID             *uint           `json:"client_id,omitempty"`
	CommittedResponse    json.RawMessage `json:"committed_response,omitempty"`
	CommittedAt          *time.Time      `json:"committed_at,omitempty"`
	CreatedAt            time.Time       `json:"created_at"`
}

type AdminAuditFact struct {
	ID                    uuid.UUID       `json:"id"`
	OperationID           uuid.UUID       `json:"operation_id"`
	RequestIdentity       string          `json:"request_identity"`
	Action                string          `json:"action"`
	ActorID               uint            `json:"actor_id"`
	ScopeKey              string          `json:"scope_key"`
	ScopeSnapshot         json.RawMessage `json:"scope_snapshot"`
	OccurredAt            time.Time       `json:"occurred_at"`
	EffectiveAt           *time.Time      `json:"effective_at,omitempty"`
	ClientID              *uint           `json:"client_id,omitempty"`
	ShopID                *uint           `json:"shop_id,omitempty"`
	MeasurementPointID    *uuid.UUID      `json:"measurement_point_id,omitempty"`
	DeviceID              *uint           `json:"device_id,omitempty"`
	DeviceSerialNumber    *string         `json:"device_serial_number,omitempty"`
	DeviceMAC             *string         `json:"device_mac,omitempty"`
	OldMeasurementPointID *uuid.UUID      `json:"old_measurement_point_id,omitempty"`
	NewMeasurementPointID *uuid.UUID      `json:"new_measurement_point_id,omitempty"`
	OldAssignmentID       *uuid.UUID      `json:"old_assignment_id,omitempty"`
	NewAssignmentID       *uuid.UUID      `json:"new_assignment_id,omitempty"`
	Reason                string          `json:"reason"`
	Metadata              json.RawMessage `json:"metadata"`
}

type FactSet struct {
	SchemaVersion     string                 `json:"schema_version"`
	AsOf              time.Time              `json:"as_of"`
	Clients           []ClientFact           `json:"clients"`
	Shops             []ShopFact             `json:"shops"`
	Users             []UserFact             `json:"users"`
	UserShopRelations []UserShopRelationFact `json:"user_shop_relations"`
	Devices           []DeviceFact           `json:"devices"`
	MeasurementPoints []MeasurementPointFact `json:"measurement_points"`
	DeviceAssignments []DeviceAssignmentFact `json:"device_assignments"`
	AdminOperations   []AdminOperationFact   `json:"admin_operations"`
	AdminAudits       []AdminAuditFact       `json:"admin_audits"`
}

func ValidateVersion(facts FactSet) error {
	if facts.SchemaVersion != SchemaVersion {
		return fmt.Errorf("source facts schema_version must be %q", SchemaVersion)
	}
	if facts.AsOf.IsZero() {
		return errors.New("source facts as_of is required")
	}
	return nil
}
