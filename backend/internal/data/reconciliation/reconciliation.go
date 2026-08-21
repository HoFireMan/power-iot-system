// Package reconciliation contains the read-only v5 security reconciliation
// boundary. It deliberately has no persistence methods: a later, fenced stage
// may consume Plan, but this package cannot apply it.
package reconciliation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	SchemaVersion = "v5"
	FactsSchema   = "security-reconciliation-source-facts"
	MappingSchema = "security-reconciliation-explicit-mapping"
	PlanSchema    = "security-reconciliation-plan"
)

type Classification string

const (
	AlreadyConsistent       Classification = "ALREADY-CONSISTENT"
	AutoReconcilable        Classification = "AUTO-RECONCILABLE"
	ExplicitMappingRequired Classification = "EXPLICIT-MAPPING-REQUIRED"
	BlockingIntegrityError  Classification = "BLOCKING-INTEGRITY-ERROR"

	// Long names are useful to callers that prefer a type-like spelling.
	ClassificationAlreadyConsistent       = AlreadyConsistent
	ClassificationAutoReconcilable        = AutoReconcilable
	ClassificationExplicitMappingRequired = ExplicitMappingRequired
	ClassificationBlockingIntegrityError  = BlockingIntegrityError
)

// The fact structs are intentionally independent of GORM models. They are the
// complete, versioned input to classification and planning.
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
	ShopID                 uint  `json:"shop_id"` // compatibility fact; never authority
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

// Admin provenance is source evidence, never authority by itself. The
// relational subject references are retained so every action can be checked
// against its Shop/MeasurementPoint/Device path.
type AdminOperationFact struct {
	// ID is the persisted primary-row identity. OperationID is the durable
	// idempotency identity and is intentionally retained separately.
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

// FactSet is a point-in-time, read-only source snapshot. AsOf is the only time
// used for active interval authority; it is not taken from Device.ShopID.
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

func (f FactSet) validateVersion() error {
	if f.SchemaVersion != SchemaVersion {
		return fmt.Errorf("source facts schema_version must be %q", SchemaVersion)
	}
	if f.AsOf.IsZero() {
		return errors.New("source facts as_of is required")
	}
	return nil
}

// Decision is the classification and authority explanation for one Device.
type Decision struct {
	DeviceID           uint           `json:"device_id"`
	Classification     Classification `json:"classification"`
	AuthorityClientID  *uint          `json:"authority_client_id,omitempty"`
	ActiveAssignmentID *uuid.UUID     `json:"active_assignment_id,omitempty"`
	Reason             string         `json:"reason,omitempty"`
}

// FactClassification gives the plan a deterministic classification for
// non-device evidence too. Kind values are closed and are not authorization
// inputs: auth_enabled and current_shop_id are explicitly no-write facts.
type FactClassification struct {
	Kind              string         `json:"kind"`
	StableID          uuid.UUID      `json:"stable_id"`
	Classification    Classification `json:"classification"`
	Reason            string         `json:"reason,omitempty"`
	AuthorityClientID *uint          `json:"authority_client_id,omitempty"`
}

type ExpectedCurrent struct {
	ClientID               *uint      `json:"client_id,omitempty"`
	InventoryOwnerClientID *uint      `json:"inventory_owner_client_id,omitempty"`
	ActiveAssignmentID     *uuid.UUID `json:"active_assignment_id,omitempty"`
	MeasurementPointID     *uuid.UUID `json:"measurement_point_id,omitempty"`
	ValidFrom              *time.Time `json:"valid_from,omitempty"`
	ValidTo                *time.Time `json:"valid_to,omitempty"`
}

type PlanItem struct {
	StableID uuid.UUID `json:"stable_id"`
	// Kind identifies the write target. Device is the historical default;
	// shop and admin-provenance items make explicit mappings first-class.
	Kind                  string          `json:"kind"`
	DeviceID              uint            `json:"device_id,omitempty"`
	ShopID                uint            `json:"shop_id,omitempty"`
	OperationID           uuid.UUID       `json:"operation_id,omitempty"`
	AuditID               uuid.UUID       `json:"audit_id,omitempty"`
	Classification        Classification  `json:"classification"`
	AuthorityClientID     *uint           `json:"authority_client_id,omitempty"`
	ExpectedCurrent       ExpectedCurrent `json:"expected_current"`
	IntendedOwnerClientID *uint           `json:"intended_owner_client_id,omitempty"`
	IntendedClientID      *uint           `json:"intended_client_id,omitempty"`
	AuthoritativeEvidence []string        `json:"authoritative_evidence,omitempty"`
	SetInventoryOwner     bool            `json:"set_inventory_owner"`
	SetShopClient         bool            `json:"set_shop_client"`
	SetAdminClient        bool            `json:"set_admin_client"`
	ExpectedAffectedCount int             `json:"expected_affected_count"`
	Reason                string          `json:"reason,omitempty"`
}

type Plan struct {
	SchemaVersion            string               `json:"schema_version"`
	PlanID                   uuid.UUID            `json:"plan_id"`
	AsOf                     time.Time            `json:"as_of"`
	SourceFactsDigest        string               `json:"source_facts_digest"`
	MappingDigest            string               `json:"mapping_digest,omitempty"`
	FactClassifications      []FactClassification `json:"fact_classifications"`
	Items                    []PlanItem           `json:"items"`
	ExpectedAffectedCounts   map[string]int       `json:"expected_affected_counts"`
	Blockers                 []string             `json:"blockers,omitempty"`
	RequiredExplicitMappings []string             `json:"required_explicit_mappings,omitempty"`
	PostWriteVerification    []string             `json:"post_write_verification"`
	Canonical                []byte               `json:"-"`
	Digest                   []byte               `json:"-"`
}

// StableID is deterministic and does not allocate a random identity. The
// namespace/key pair is part of the public plan identity contract.
func StableID(namespace, key string) uuid.UUID {
	sum := sha256.Sum256([]byte(namespace + "\x00" + key))
	var id uuid.UUID
	copy(id[:], sum[:16])
	id[6] = (id[6] & 0x0f) | 0x50
	id[8] = (id[8] & 0x3f) | 0x80
	return id
}

func SHA256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

// CanonicalSourceFacts returns compact, deterministic JSON and its SHA-256
// digest. IDs and collections are sorted, and nil collections become arrays.
func CanonicalSourceFacts(facts FactSet) ([]byte, []byte, error) {
	if err := facts.validateVersion(); err != nil {
		return nil, nil, err
	}
	n := normalizeFacts(facts)
	if err := validateFactIdentities(n); err != nil {
		return nil, nil, err
	}
	if err := canonicalizeAdminJSON(&n); err != nil {
		return nil, nil, err
	}
	body, err := json.Marshal(n)
	if err != nil {
		return nil, nil, err
	}
	sum := sha256.Sum256(body)
	return body, sum[:], nil
}

func SourceFactsDigest(facts FactSet) ([]byte, error) {
	_, digest, err := CanonicalSourceFacts(facts)
	return digest, err
}

// CanonicalMappingBasis returns the stable authority evidence bound into a
// v5 explicit mapping artifact. The execution snapshot timestamp itself is
// omitted, while assignment interval rows and their planner-derived temporal
// states remain present. Thus harmless wall-clock movement does not stale an
// artifact, but a validity-boundary transition or any source/authority change
// does.
func CanonicalMappingBasis(facts FactSet) ([]byte, []byte, error) {
	if err := facts.validateVersion(); err != nil {
		return nil, nil, err
	}
	n := normalizeFacts(facts)
	if err := validateFactIdentities(n); err != nil {
		return nil, nil, err
	}
	if err := canonicalizeAdminJSON(&n); err != nil {
		return nil, nil, err
	}
	decisions, err := Classify(n)
	if err != nil {
		return nil, nil, err
	}
	classes, err := ClassifyFacts(n)
	if err != nil {
		return nil, nil, err
	}
	factsJSON, err := json.Marshal(n)
	if err != nil {
		return nil, nil, err
	}
	var factObject map[string]json.RawMessage
	if err := json.Unmarshal(factsJSON, &factObject); err != nil {
		return nil, nil, err
	}
	delete(factObject, "as_of")
	states := make([]mappingAssignmentState, 0, len(n.DeviceAssignments))
	for _, assignment := range n.DeviceAssignments {
		states = append(states, mappingAssignmentState{ID: assignment.ID, State: assignmentTemporalState(assignment, n.AsOf)})
	}
	type basis struct {
		Facts               map[string]json.RawMessage `json:"facts"`
		AssignmentStates    []mappingAssignmentState   `json:"assignment_temporal_states"`
		Decisions           []Decision                 `json:"device_decisions"`
		FactClassifications []FactClassification       `json:"fact_classifications"`
	}
	body, err := json.Marshal(basis{Facts: factObject, AssignmentStates: states, Decisions: decisions, FactClassifications: classes})
	if err != nil {
		return nil, nil, err
	}
	sum := sha256.Sum256(body)
	return body, sum[:], nil
}

func MappingSourceFactsDigest(facts FactSet) ([]byte, error) {
	_, digest, err := CanonicalMappingBasis(facts)
	return digest, err
}

type mappingAssignmentState struct {
	ID    uuid.UUID `json:"id"`
	State string    `json:"state"`
}

func assignmentTemporalState(assignment DeviceAssignmentFact, asOf time.Time) string {
	if assignment.ID == uuid.Nil || assignment.DeviceID == 0 || assignment.MeasurementPointID == uuid.Nil || assignment.ValidFrom.IsZero() || (assignment.ValidTo != nil && !assignment.ValidTo.After(assignment.ValidFrom)) {
		return "invalid"
	}
	if assignment.ValidFrom.After(asOf) {
		return "future"
	}
	if assignment.ValidTo == nil || asOf.Before(*assignment.ValidTo) {
		return "active"
	}
	return "historical"
}

// validateFactIdentities makes malformed duplicate snapshots fail closed before
// they can be bound to a digest. Classification performs richer relational
// validation; this guard only covers top-level row identities.
func validateFactIdentities(f FactSet) error {
	clients := map[uint]bool{}
	for _, c := range f.Clients {
		if c.ID == 0 || clients[c.ID] {
			return fmt.Errorf("duplicate or invalid client %d", c.ID)
		}
		clients[c.ID] = true
	}
	shops := map[uint]bool{}
	for _, s := range f.Shops {
		if s.ID == 0 || shops[s.ID] {
			return fmt.Errorf("duplicate or invalid shop %d", s.ID)
		}
		shops[s.ID] = true
	}
	users := map[uint]bool{}
	for _, u := range f.Users {
		if u.ID == 0 || users[u.ID] {
			return fmt.Errorf("duplicate or invalid user %d", u.ID)
		}
		users[u.ID] = true
	}
	relations := map[uint]bool{}
	for _, r := range f.UserShopRelations {
		if r.ID == 0 || relations[r.ID] {
			return fmt.Errorf("duplicate or invalid user shop relation %d", r.ID)
		}
		relations[r.ID] = true
	}
	devices := map[uint]bool{}
	for _, d := range f.Devices {
		if d.ID == 0 || devices[d.ID] {
			return fmt.Errorf("duplicate or invalid device %d", d.ID)
		}
		devices[d.ID] = true
	}
	points := map[uuid.UUID]bool{}
	for _, p := range f.MeasurementPoints {
		if p.ID == uuid.Nil || points[p.ID] {
			return fmt.Errorf("duplicate or invalid measurement point %s", p.ID)
		}
		points[p.ID] = true
	}
	assignments := map[uuid.UUID]bool{}
	for _, a := range f.DeviceAssignments {
		if a.ID == uuid.Nil || assignments[a.ID] {
			return fmt.Errorf("duplicate or invalid device assignment %s", a.ID)
		}
		assignments[a.ID] = true
	}
	operations := map[uuid.UUID]bool{}
	operationRows := map[uuid.UUID]bool{}
	for _, op := range f.AdminOperations {
		if op.OperationID == uuid.Nil || operations[op.OperationID] {
			return fmt.Errorf("duplicate or invalid admin operation %s", op.OperationID)
		}
		operations[op.OperationID] = true
		// Older in-memory fixtures may omit the primary row ID; PostgreSQL
		// snapshots always populate it. When present, it is part of identity
		// validation and the canonical evidence below.
		if op.ID != uuid.Nil {
			if operationRows[op.ID] {
				return fmt.Errorf("duplicate admin operation row %s", op.ID)
			}
			operationRows[op.ID] = true
		}
	}
	audits := map[uuid.UUID]bool{}
	for _, audit := range f.AdminAudits {
		if audit.ID == uuid.Nil || audits[audit.ID] {
			return fmt.Errorf("duplicate or invalid admin audit %s", audit.ID)
		}
		audits[audit.ID] = true
	}
	return nil
}

func canonicalJSONMessage(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value interface{}
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	var extra interface{}
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("JSON evidence contains multiple values")
		}
		return nil, err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(canonical), nil
}

func canonicalizeAdminJSON(f *FactSet) error {
	for i := range f.AdminOperations {
		var err error
		if f.AdminOperations[i].ScopeSnapshot, err = canonicalJSONMessage(f.AdminOperations[i].ScopeSnapshot); err != nil {
			return fmt.Errorf("admin operation %s scope_snapshot: %w", f.AdminOperations[i].OperationID, err)
		}
		if f.AdminOperations[i].CommittedResponse, err = canonicalJSONMessage(f.AdminOperations[i].CommittedResponse); err != nil {
			return fmt.Errorf("admin operation %s committed_response: %w", f.AdminOperations[i].OperationID, err)
		}
	}
	for i := range f.AdminAudits {
		var err error
		if f.AdminAudits[i].ScopeSnapshot, err = canonicalJSONMessage(f.AdminAudits[i].ScopeSnapshot); err != nil {
			return fmt.Errorf("admin audit %s scope_snapshot: %w", f.AdminAudits[i].ID, err)
		}
		if f.AdminAudits[i].Metadata, err = canonicalJSONMessage(f.AdminAudits[i].Metadata); err != nil {
			return fmt.Errorf("admin audit %s metadata: %w", f.AdminAudits[i].ID, err)
		}
	}
	return nil
}

func normalizeFacts(f FactSet) FactSet {
	f.AsOf = f.AsOf.UTC()
	f.Clients = append([]ClientFact(nil), f.Clients...)
	f.Shops = append([]ShopFact(nil), f.Shops...)
	f.Users = append([]UserFact(nil), f.Users...)
	f.UserShopRelations = append([]UserShopRelationFact(nil), f.UserShopRelations...)
	f.Devices = append([]DeviceFact(nil), f.Devices...)
	f.MeasurementPoints = append([]MeasurementPointFact(nil), f.MeasurementPoints...)
	f.DeviceAssignments = append([]DeviceAssignmentFact(nil), f.DeviceAssignments...)
	f.AdminOperations = append([]AdminOperationFact(nil), f.AdminOperations...)
	f.AdminAudits = append([]AdminAuditFact(nil), f.AdminAudits...)
	for i := range f.DeviceAssignments {
		f.DeviceAssignments[i].ValidFrom = f.DeviceAssignments[i].ValidFrom.UTC()
		if f.DeviceAssignments[i].ValidTo != nil {
			value := f.DeviceAssignments[i].ValidTo.UTC()
			f.DeviceAssignments[i].ValidTo = &value
		}
	}
	for i := range f.AdminOperations {
		f.AdminOperations[i].CreatedAt = f.AdminOperations[i].CreatedAt.UTC()
		if f.AdminOperations[i].CommittedAt != nil {
			value := f.AdminOperations[i].CommittedAt.UTC()
			f.AdminOperations[i].CommittedAt = &value
		}
	}
	for i := range f.AdminAudits {
		f.AdminAudits[i].OccurredAt = f.AdminAudits[i].OccurredAt.UTC()
		if f.AdminAudits[i].EffectiveAt != nil {
			value := f.AdminAudits[i].EffectiveAt.UTC()
			f.AdminAudits[i].EffectiveAt = &value
		}
	}
	if f.Clients == nil {
		f.Clients = []ClientFact{}
	}
	sort.Slice(f.Clients, func(i, j int) bool { return f.Clients[i].ID < f.Clients[j].ID })
	if f.Shops == nil {
		f.Shops = []ShopFact{}
	}
	sort.Slice(f.Shops, func(i, j int) bool { return f.Shops[i].ID < f.Shops[j].ID })
	if f.Users == nil {
		f.Users = []UserFact{}
	}
	sort.Slice(f.Users, func(i, j int) bool { return f.Users[i].ID < f.Users[j].ID })
	if f.UserShopRelations == nil {
		f.UserShopRelations = []UserShopRelationFact{}
	}
	sort.Slice(f.UserShopRelations, func(i, j int) bool {
		if f.UserShopRelations[i].UserID != f.UserShopRelations[j].UserID {
			return f.UserShopRelations[i].UserID < f.UserShopRelations[j].UserID
		}
		if f.UserShopRelations[i].ShopID != f.UserShopRelations[j].ShopID {
			return f.UserShopRelations[i].ShopID < f.UserShopRelations[j].ShopID
		}
		return f.UserShopRelations[i].ID < f.UserShopRelations[j].ID
	})
	if f.Devices == nil {
		f.Devices = []DeviceFact{}
	}
	sort.Slice(f.Devices, func(i, j int) bool { return f.Devices[i].ID < f.Devices[j].ID })
	if f.MeasurementPoints == nil {
		f.MeasurementPoints = []MeasurementPointFact{}
	}
	sort.Slice(f.MeasurementPoints, func(i, j int) bool { return f.MeasurementPoints[i].ID.String() < f.MeasurementPoints[j].ID.String() })
	if f.DeviceAssignments == nil {
		f.DeviceAssignments = []DeviceAssignmentFact{}
	}
	sort.Slice(f.DeviceAssignments, func(i, j int) bool { return f.DeviceAssignments[i].ID.String() < f.DeviceAssignments[j].ID.String() })
	if f.AdminOperations == nil {
		f.AdminOperations = []AdminOperationFact{}
	}
	sort.Slice(f.AdminOperations, func(i, j int) bool {
		return f.AdminOperations[i].OperationID.String() < f.AdminOperations[j].OperationID.String()
	})
	if f.AdminAudits == nil {
		f.AdminAudits = []AdminAuditFact{}
	}
	sort.Slice(f.AdminAudits, func(i, j int) bool { return f.AdminAudits[i].ID.String() < f.AdminAudits[j].ID.String() })
	return f
}

// Classify performs no I/O and never mutates facts. All integrity failures are
// represented as BLOCKING-INTEGRITY-ERROR decisions, allowing a caller to
// inspect the complete snapshot before deciding whether to stop.
func Classify(facts FactSet) ([]Decision, error) {
	if err := facts.validateVersion(); err != nil {
		return nil, err
	}
	clients := map[uint]bool{}
	for _, c := range facts.Clients {
		if c.ID == 0 || clients[c.ID] {
			return nil, fmt.Errorf("duplicate or invalid client %d", c.ID)
		}
		clients[c.ID] = true
	}
	shops := map[uint]ShopFact{}
	for _, s := range facts.Shops {
		if s.ID == 0 || shops[s.ID].ID != 0 {
			return nil, fmt.Errorf("duplicate or invalid shop %d", s.ID)
		}
		shops[s.ID] = s
	}
	points := map[uuid.UUID]MeasurementPointFact{}
	for _, p := range facts.MeasurementPoints {
		if p.ID == uuid.Nil || p.ShopID == 0 || points[p.ID].ID != uuid.Nil {
			return nil, fmt.Errorf("duplicate or invalid measurement point %s", p.ID)
		}
		points[p.ID] = p
	}
	devices := map[uint]DeviceFact{}
	for _, d := range facts.Devices {
		if d.ID == 0 || devices[d.ID].ID != 0 {
			return nil, fmt.Errorf("duplicate or invalid device %d", d.ID)
		}
		devices[d.ID] = d
	}

	assignments := map[uint][]DeviceAssignmentFact{}
	assignmentIDs := map[uuid.UUID]bool{}
	duplicateAssignmentIDs := map[uuid.UUID]bool{}
	for _, a := range facts.DeviceAssignments {
		if a.ID != uuid.Nil {
			if assignmentIDs[a.ID] {
				duplicateAssignmentIDs[a.ID] = true
			}
			assignmentIDs[a.ID] = true
		}
		// Keep every row, including DeviceID==0 and references to a missing
		// Device. The per-device decision below turns these rows into explicit
		// blockers instead of silently shrinking the evidence set.
		assignments[a.DeviceID] = append(assignments[a.DeviceID], a)
	}
	ids := make([]uint, 0, len(devices))
	for id := range devices {
		ids = append(ids, id)
	}
	// Retain malformed and orphan assignment device IDs as explicit blocking
	// decisions; an invalid row must never disappear from the evidence set.
	for id := range assignments {
		if id == 0 {
			ids = append(ids, 0)
		} else if _, ok := devices[id]; !ok {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]Decision, 0, len(ids))
	for _, id := range ids {
		d, deviceExists := devices[id]
		list := assignments[id]
		if !deviceExists {
			reason := "assignment references missing Device"
			if id == 0 {
				reason = "assignment has invalid Device ID"
			}
			out = append(out, Decision{DeviceID: id, Classification: BlockingIntegrityError, Reason: reason})
			continue
		}
		blocking := ""
		for _, a := range list {
			if duplicateAssignmentIDs[a.ID] {
				blocking = "duplicate DeviceAssignment identity"
				break
			}
		}
		if d.InventoryOwnerClientID != nil && (valueUint(d.InventoryOwnerClientID) == 0 || !clients[valueUint(d.InventoryOwnerClientID)]) {
			blocking = "existing inventory owner references a missing Client"
		}
		var active []DeviceAssignmentFact
		var authority *uint
		var activeID *uuid.UUID
		for _, a := range list {
			p, pok := points[a.MeasurementPointID]
			s, sok := shops[p.ShopID]
			_, dok := devices[a.DeviceID]
			if a.ID == uuid.Nil || a.MeasurementPointID == uuid.Nil || a.ValidFrom.IsZero() || (a.ValidTo != nil && !a.ValidTo.After(a.ValidFrom)) || !pok || !sok || !dok || (s.ClientID != nil && (*s.ClientID == 0 || !clients[valueUint(s.ClientID)])) {
				blocking = "invalid, missing, or orphaned assignment"
				continue
			}
			if a.ValidFrom.Before(facts.AsOf) || a.ValidFrom.Equal(facts.AsOf) {
				if a.ValidTo == nil || facts.AsOf.Before(*a.ValidTo) {
					active = append(active, a)
				}
			}
		}
		if blocking != "" {
			out = append(out, Decision{DeviceID: id, Classification: BlockingIntegrityError, Reason: blocking})
			continue
		}
		if len(active) > 1 {
			out = append(out, Decision{DeviceID: id, Classification: BlockingIntegrityError, Reason: "ambiguous active assignments"})
			continue
		}
		if len(active) == 1 {
			a := active[0]
			p := points[a.MeasurementPointID]
			s := shops[p.ShopID]
			authority = cloneUint(s.ClientID)
			activeID = &a.ID
			if authority == nil {
				class := ExplicitMappingRequired
				reason := "active assignment reaches Shop with NULL Client"
				if d.InventoryOwnerClientID != nil {
					class = BlockingIntegrityError
					reason = "existing inventory owner has no independent current authority"
				}
				out = append(out, Decision{DeviceID: id, Classification: class, ActiveAssignmentID: activeID, Reason: reason})
				continue
			}
			for _, future := range list {
				if future.ValidFrom.After(facts.AsOf) {
					fp := points[future.MeasurementPointID]
					fs := shops[fp.ShopID]
					if fs.ClientID == nil || *fs.ClientID != *authority {
						out = append(out, Decision{DeviceID: id, Classification: BlockingIntegrityError, Reason: "future assignment changes Client authority"})
						authority = nil
						break
					}
				}
			}
			if authority == nil {
				continue
			}
			if d.InventoryOwnerClientID != nil && *d.InventoryOwnerClientID != *authority {
				out = append(out, Decision{DeviceID: id, Classification: BlockingIntegrityError, Reason: "existing inventory owner conflicts with relational Client authority"})
				continue
			}
			class := AutoReconcilable
			if d.InventoryOwnerClientID != nil {
				class = AlreadyConsistent
			}
			out = append(out, Decision{DeviceID: id, Classification: class, AuthorityClientID: cloneUint(authority), ActiveAssignmentID: cloneUUID(activeID), Reason: "active DeviceAssignment derives Client through MeasurementPoint and Shop"})
		} else {
			// Future and historical assignments are useful evidence but not
			// current authority. A pre-existing owner without an independent
			// current relational authority cannot be certified or remapped.
			if d.InventoryOwnerClientID != nil {
				out = append(out, Decision{DeviceID: id, Classification: BlockingIntegrityError, Reason: "existing inventory owner has no independent current authority"})
			} else {
				out = append(out, Decision{DeviceID: id, Classification: ExplicitMappingRequired, Reason: "no structurally valid active assignment"})
			}
		}
	}
	return out, nil
}

const (
	FactKindShop            = "shop"
	FactKindDevice          = "device"
	FactKindMembership      = "membership/current-shop"
	FactKindAuthNoWrite     = "auth no-write"
	FactKindAdminProvenance = "admin provenance"
	PlanItemShop            = "shop"
	PlanItemDevice          = "device"
	PlanItemAdmin           = "admin provenance"

	// Aggregate keys are versioned plan vocabulary, not labels derived from
	// implementation ordering. They remain present with zero values when a
	// snapshot has no intent in that category.
	ExpectedCountInventoryOwnerUpdates = "inventory_owner_updates"
	ExpectedCountShopClientUpdates     = "shop_client_updates"
	ExpectedCountAdminClientUpdates    = "admin_client_provenance_updates"
)

// ClassifyFacts classifies every v5 evidence family. Membership and
// auth_enabled are evidence-only: they can never produce a write intent.
func ClassifyFacts(facts FactSet) ([]FactClassification, error) {
	if err := facts.validateVersion(); err != nil {
		return nil, err
	}
	decisions, err := Classify(facts)
	if err != nil {
		return nil, err
	}
	out := make([]FactClassification, 0, len(facts.Shops)+len(facts.Devices)+len(facts.Users)+len(facts.UserShopRelations)+len(facts.AdminOperations)+len(facts.AdminAudits))
	shops := map[uint]ShopFact{}
	for _, s := range facts.Shops {
		shops[s.ID] = s
	}
	users := map[uint]UserFact{}
	for _, u := range facts.Users {
		if u.ID == 0 || users[u.ID].ID != 0 {
			return nil, fmt.Errorf("duplicate or invalid User %d", u.ID)
		}
		users[u.ID] = u
	}
	memberships := map[string]bool{}
	membershipIDs := map[uint]bool{}
	logicalMemberships := map[string]bool{}
	duplicateMemberships := map[string]bool{}
	for _, r := range facts.UserShopRelations {
		if r.ID == 0 || membershipIDs[r.ID] {
			return nil, fmt.Errorf("duplicate or invalid UserShopRelation %d", r.ID)
		}
		membershipIDs[r.ID] = true
		key := fmt.Sprintf("%d/%d", r.UserID, r.ShopID)
		if logicalMemberships[key] {
			duplicateMemberships[key] = true
		}
		logicalMemberships[key] = true
		memberships[key] = true
	}
	clients := map[uint]bool{}
	for _, c := range facts.Clients {
		clients[c.ID] = true
	}
	for _, s := range facts.Shops {
		class := AlreadyConsistent
		reason := "Shop relational Client evidence is valid"
		var authority *uint
		if s.ClientID == nil {
			class, reason = ExplicitMappingRequired, "Shop Client is NULL and has no unique authority"
		} else if s.ClientID == nil || valueUint(s.ClientID) == 0 || !clients[valueUint(s.ClientID)] {
			class, reason = BlockingIntegrityError, "Shop has orphan or contradictory Client authority"
		} else {
			authority = cloneUint(s.ClientID)
		}
		out = append(out, FactClassification{Kind: FactKindShop, StableID: StableID("security-reconciliation-shop/v5", fmt.Sprint(s.ID)), Classification: class, AuthorityClientID: authority, Reason: reason})
	}
	for _, d := range decisions {
		out = append(out, FactClassification{Kind: FactKindDevice, StableID: StableID("security-reconciliation-device/v5", fmt.Sprint(d.DeviceID)), Classification: d.Classification, AuthorityClientID: cloneUint(d.AuthorityClientID), Reason: d.Reason})
	}
	for _, r := range facts.UserShopRelations {
		class := AlreadyConsistent
		reason := "membership relation is explicit"
		key := fmt.Sprintf("%d/%d", r.UserID, r.ShopID)
		if r.ID == 0 || r.UserID == 0 || r.ShopID == 0 {
			class, reason = BlockingIntegrityError, "membership relation has invalid identity"
		} else if _, ok := users[r.UserID]; !ok {
			class, reason = BlockingIntegrityError, "membership relation references missing User"
		} else if _, ok := shops[r.ShopID]; !ok {
			class, reason = BlockingIntegrityError, "membership relation references missing Shop"
		} else if duplicateMemberships[key] {
			class, reason = BlockingIntegrityError, "duplicate logical UserShopRelation"
		}
		out = append(out, FactClassification{Kind: FactKindMembership, StableID: StableID("security-reconciliation-membership/v5", fmt.Sprintf("%d/%d/%d", r.ID, r.UserID, r.ShopID)), Classification: class, Reason: reason})
	}
	for _, u := range facts.Users {
		class, reason := AlreadyConsistent, "current_shop_id is evidence only; auth_enabled is no-write"
		if u.ID == 0 {
			class, reason = BlockingIntegrityError, "user has invalid identity"
		}
		out = append(out, FactClassification{Kind: FactKindAuthNoWrite, StableID: StableID("security-reconciliation-user/v5", fmt.Sprint(u.ID)), Classification: class, Reason: reason})
		if u.CurrentShopID != nil {
			currentClass, currentReason := AlreadyConsistent, "current_shop_id has explicit membership"
			if !memberships[fmt.Sprintf("%d/%d", u.ID, *u.CurrentShopID)] {
				currentClass, currentReason = BlockingIntegrityError, "current_shop_id has no explicit membership"
			}
			out = append(out, FactClassification{Kind: FactKindMembership, StableID: StableID("security-reconciliation-current-shop/v5", fmt.Sprintf("%d/%d", u.ID, *u.CurrentShopID)), Classification: currentClass, Reason: currentReason})
		}
	}
	operationIDs := map[uuid.UUID]bool{}
	operationRowIDs := map[uuid.UUID]bool{}
	for _, op := range facts.AdminOperations {
		if op.OperationID == uuid.Nil || operationIDs[op.OperationID] {
			return nil, fmt.Errorf("duplicate or invalid AdminOperation %s", op.OperationID)
		}
		operationIDs[op.OperationID] = true
		if op.ID != uuid.Nil {
			if operationRowIDs[op.ID] {
				return nil, fmt.Errorf("duplicate AdminOperation row %s", op.ID)
			}
			operationRowIDs[op.ID] = true
		}
	}
	adminAuditIDs := map[uuid.UUID]bool{}
	for _, audit := range facts.AdminAudits {
		if audit.ID == uuid.Nil || adminAuditIDs[audit.ID] {
			return nil, fmt.Errorf("duplicate or invalid AdminAudit %s", audit.ID)
		}
		adminAuditIDs[audit.ID] = true
	}
	// Expected authority is derived from the action's relational path, never
	// scope_key, JSON, Device.ShopID, or an existing provenance value.
	auditByOperation := map[uuid.UUID][]AdminAuditFact{}
	for _, audit := range facts.AdminAudits {
		auditByOperation[audit.OperationID] = append(auditByOperation[audit.OperationID], audit)
	}
	for _, op := range facts.AdminOperations {
		class, reason, expected := classifyAdminRow(facts, clients, users, op, auditByOperation[op.OperationID])
		out = append(out, FactClassification{Kind: FactKindAdminProvenance, StableID: StableID("security-reconciliation-admin-operation/v5", op.OperationID.String()), Classification: class, AuthorityClientID: expected, Reason: reason})
	}
	for _, audit := range facts.AdminAudits {
		op, ok := findAdminOperation(facts.AdminOperations, audit.OperationID)
		if !ok {
			out = append(out, FactClassification{Kind: FactKindAdminProvenance, StableID: StableID("security-reconciliation-admin-audit/v5", audit.ID.String()), Classification: BlockingIntegrityError, Reason: "admin audit references missing operation"})
			continue
		}
		class, reason, expected := classifyAdminAudit(facts, clients, users, op, audit)
		out = append(out, FactClassification{Kind: FactKindAdminProvenance, StableID: StableID("security-reconciliation-admin-audit/v5", audit.ID.String()), Classification: class, AuthorityClientID: expected, Reason: reason})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StableID.String() < out[j].StableID.String() })
	return out, nil
}

func findAdminOperation(ops []AdminOperationFact, id uuid.UUID) (AdminOperationFact, bool) {
	for _, op := range ops {
		if op.OperationID == id {
			return op, true
		}
	}
	return AdminOperationFact{}, false
}

func normalizedAdminAction(action string) AdminAction {
	a := strings.ToLower(strings.TrimSpace(action))
	switch a {
	case "create", "create measurement point", "create_measurement_point", "create-measurement-point":
		return AdminCreateMeasurementPoint
	case "bind", "binding":
		return AdminBind
	case "replace", "replacement":
		return AdminReplace
	case "relocate", "relocation":
		return AdminRelocate
	case "unbind", "unbinding":
		return AdminUnbind
	default:
		return AdminAction(strings.TrimSpace(action))
	}
}

// adminRequestFromAudit reconstructs an action from the typed relational
// evidence written by the Admin Binding executor. The action-specific shape is
// intentionally strict: request identity, scope snapshots, and JSON metadata
// are provenance only and cannot fill or override a typed reference.
func adminRequestFromAudit(facts FactSet, audit AdminAuditFact) (AdminAuthorityRequest, error) {
	action := normalizedAdminAction(audit.Action)
	if !isSupportedAdminAction(action) {
		return AdminAuthorityRequest{}, fmt.Errorf("unsupported admin action %q", audit.Action)
	}
	req := AdminAuthorityRequest{Action: action, AsOf: facts.AsOf}
	assignments := map[uuid.UUID]DeviceAssignmentFact{}
	for _, a := range facts.DeviceAssignments {
		if a.ID == uuid.Nil {
			return AdminAuthorityRequest{}, errors.New("audit references invalid assignment evidence")
		}
		if _, exists := assignments[a.ID]; exists {
			return AdminAuthorityRequest{}, errors.New("audit references duplicate assignment evidence")
		}
		assignments[a.ID] = a
	}
	points := map[uuid.UUID]MeasurementPointFact{}
	for _, p := range facts.MeasurementPoints {
		points[p.ID] = p
	}
	shops := map[uint]ShopFact{}
	for _, s := range facts.Shops {
		shops[s.ID] = s
	}
	devices := map[uint]DeviceFact{}
	for _, d := range facts.Devices {
		devices[d.ID] = d
	}

	lookupPoint := func(id uuid.UUID, label string) (MeasurementPointFact, error) {
		if id == uuid.Nil {
			return MeasurementPointFact{}, fmt.Errorf("audit %s measurement point is required", label)
		}
		point, ok := points[id]
		if !ok || point.ShopID == 0 {
			return MeasurementPointFact{}, fmt.Errorf("audit %s measurement point is missing", label)
		}
		if _, ok := shops[point.ShopID]; !ok {
			return MeasurementPointFact{}, fmt.Errorf("audit %s measurement point Shop is missing", label)
		}
		return point, nil
	}
	lookupAssignment := func(ref *uuid.UUID, label string) (DeviceAssignmentFact, error) {
		if ref == nil || *ref == uuid.Nil {
			return DeviceAssignmentFact{}, fmt.Errorf("audit %s assignment evidence is required", label)
		}
		a, ok := assignments[*ref]
		if !ok {
			return DeviceAssignmentFact{}, fmt.Errorf("audit %s assignment reference is missing", label)
		}
		if a.DeviceID == 0 || a.MeasurementPointID == uuid.Nil || a.ValidFrom.IsZero() || (a.ValidTo != nil && !a.ValidTo.After(a.ValidFrom)) {
			return DeviceAssignmentFact{}, fmt.Errorf("audit %s assignment evidence is malformed", label)
		}
		if _, ok := devices[a.DeviceID]; !ok {
			return DeviceAssignmentFact{}, fmt.Errorf("audit %s assignment references missing Device", label)
		}
		if _, err := lookupPoint(a.MeasurementPointID, label); err != nil {
			return DeviceAssignmentFact{}, err
		}
		return a, nil
	}
	checkDevice := func(ref *uint) (DeviceFact, error) {
		if ref == nil || *ref == 0 {
			return DeviceFact{}, errors.New("audit Device evidence is required")
		}
		device, ok := devices[*ref]
		if !ok {
			return DeviceFact{}, fmt.Errorf("audit references missing Device %d", *ref)
		}
		return device, nil
	}
	checkShop := func(shopID *uint, point MeasurementPointFact) error {
		if shopID == nil {
			return nil
		}
		if *shopID == 0 {
			return errors.New("audit Shop evidence is invalid")
		}
		if _, ok := shops[*shopID]; !ok {
			return errors.New("audit Shop evidence references a missing Shop")
		}
		if point.ShopID != *shopID {
			return errors.New("audit Shop conflicts with measurement-point evidence")
		}
		return nil
	}
	badUUID := func(ref *uuid.UUID, label string) error {
		if ref != nil && *ref == uuid.Nil {
			return fmt.Errorf("audit %s measurement point is invalid", label)
		}
		return nil
	}
	badUint := func(ref *uint, label string) error {
		if ref != nil && *ref == 0 {
			return fmt.Errorf("audit %s Device/Shop evidence is invalid", label)
		}
		return nil
	}
	if err := badUUID(audit.MeasurementPointID, "direct"); err != nil {
		return AdminAuthorityRequest{}, err
	}
	if err := badUUID(audit.OldMeasurementPointID, "old"); err != nil {
		return AdminAuthorityRequest{}, err
	}
	if err := badUUID(audit.NewMeasurementPointID, "new"); err != nil {
		return AdminAuthorityRequest{}, err
	}
	if err := badUint(audit.DeviceID, "Device"); err != nil {
		return AdminAuthorityRequest{}, err
	}
	if err := badUint(audit.ShopID, "Shop"); err != nil {
		return AdminAuthorityRequest{}, err
	}

	sameTarget := func() (uuid.UUID, error) {
		target := valueUUID(audit.NewMeasurementPointID)
		legacy := valueUUID(audit.MeasurementPointID)
		if target != uuid.Nil && legacy != uuid.Nil && target != legacy {
			return uuid.Nil, errors.New("audit target measurement points conflict")
		}
		if target == uuid.Nil {
			target = legacy
		}
		if target == uuid.Nil {
			return uuid.Nil, errors.New("audit target measurement point evidence is required")
		}
		return target, nil
	}

	switch action {
	case AdminCreateMeasurementPoint:
		// The writer persists the newly-created MP in MeasurementPointID and
		// the owning Shop. Assignment and Device references are not valid for
		// this action.
		if audit.ShopID == nil || *audit.ShopID == 0 || audit.MeasurementPointID == nil {
			return AdminAuthorityRequest{}, errors.New("create audit requires Shop and newly-created MeasurementPoint evidence")
		}
		if audit.NewMeasurementPointID != nil || audit.OldMeasurementPointID != nil || audit.OldAssignmentID != nil || audit.NewAssignmentID != nil || audit.DeviceID != nil || audit.DeviceSerialNumber != nil || audit.DeviceMAC != nil {
			return AdminAuthorityRequest{}, errors.New("create audit contains contradictory assignment or Device evidence")
		}
		point, err := lookupPoint(*audit.MeasurementPointID, "created")
		if err != nil {
			return AdminAuthorityRequest{}, err
		}
		if err := checkShop(audit.ShopID, point); err != nil {
			return AdminAuthorityRequest{}, err
		}
		req.ShopID = *audit.ShopID
		req.TargetMeasurementPointID = point.ID

	case AdminBind:
		if audit.OldMeasurementPointID != nil || audit.OldAssignmentID != nil {
			return AdminAuthorityRequest{}, errors.New("bind audit contains old assignment/source measurement-point evidence")
		}
		if audit.NewMeasurementPointID == nil || audit.NewAssignmentID == nil {
			return AdminAuthorityRequest{}, errors.New("bind audit requires target MeasurementPoint and new assignment evidence")
		}
		device, err := checkDevice(audit.DeviceID)
		if err != nil {
			return AdminAuthorityRequest{}, err
		}
		targetID, err := sameTarget()
		if err != nil {
			return AdminAuthorityRequest{}, err
		}
		target, err := lookupPoint(targetID, "target")
		if err != nil {
			return AdminAuthorityRequest{}, err
		}
		newAssignment, err := lookupAssignment(audit.NewAssignmentID, "new")
		if err != nil {
			return AdminAuthorityRequest{}, err
		}
		if newAssignment.DeviceID != device.ID || newAssignment.MeasurementPointID != target.ID {
			return AdminAuthorityRequest{}, errors.New("bind new assignment does not converge on target MeasurementPoint and Device")
		}
		if err := checkShop(audit.ShopID, target); err != nil {
			return AdminAuthorityRequest{}, err
		}
		req.DeviceID, req.TargetMeasurementPointID = device.ID, target.ID

	case AdminReplace:
		if audit.OldAssignmentID == nil || audit.NewAssignmentID == nil || audit.OldMeasurementPointID == nil || audit.NewMeasurementPointID == nil {
			return AdminAuthorityRequest{}, errors.New("replace audit requires old/new assignment and MeasurementPoint evidence")
		}
		oldAssignment, err := lookupAssignment(audit.OldAssignmentID, "old")
		if err != nil {
			return AdminAuthorityRequest{}, err
		}
		newAssignment, err := lookupAssignment(audit.NewAssignmentID, "new")
		if err != nil {
			return AdminAuthorityRequest{}, err
		}
		if oldAssignment.ID == newAssignment.ID || oldAssignment.MeasurementPointID != *audit.OldMeasurementPointID || newAssignment.MeasurementPointID != *audit.NewMeasurementPointID {
			return AdminAuthorityRequest{}, errors.New("replace assignment and MeasurementPoint references do not converge")
		}
		if oldAssignment.MeasurementPointID != newAssignment.MeasurementPointID {
			return AdminAuthorityRequest{}, errors.New("replace cannot change the current MeasurementPoint")
		}
		if audit.MeasurementPointID != nil && *audit.MeasurementPointID != newAssignment.MeasurementPointID {
			return AdminAuthorityRequest{}, errors.New("replace direct MeasurementPoint conflicts with target")
		}
		replacement, err := checkDevice(audit.DeviceID)
		if err != nil {
			return AdminAuthorityRequest{}, err
		}
		if newAssignment.DeviceID != replacement.ID || oldAssignment.DeviceID == replacement.ID {
			return AdminAuthorityRequest{}, errors.New("replace assignment and replacement Device references do not converge")
		}
		oldPoint, err := lookupPoint(oldAssignment.MeasurementPointID, "old")
		if err != nil {
			return AdminAuthorityRequest{}, err
		}
		target, err := lookupPoint(newAssignment.MeasurementPointID, "new")
		if err != nil {
			return AdminAuthorityRequest{}, err
		}
		if err := checkShop(audit.ShopID, target); err != nil {
			return AdminAuthorityRequest{}, err
		}
		req.DeviceID, req.ReplacementDeviceID = oldAssignment.DeviceID, replacement.ID
		req.SourceMeasurementPointID, req.TargetMeasurementPointID = oldPoint.ID, target.ID

	case AdminRelocate:
		if audit.OldAssignmentID == nil || audit.NewAssignmentID == nil || audit.OldMeasurementPointID == nil || audit.NewMeasurementPointID == nil {
			return AdminAuthorityRequest{}, errors.New("relocate audit requires old/new assignment and MeasurementPoint evidence")
		}
		oldAssignment, err := lookupAssignment(audit.OldAssignmentID, "old")
		if err != nil {
			return AdminAuthorityRequest{}, err
		}
		newAssignment, err := lookupAssignment(audit.NewAssignmentID, "new")
		if err != nil {
			return AdminAuthorityRequest{}, err
		}
		device, err := checkDevice(audit.DeviceID)
		if err != nil {
			return AdminAuthorityRequest{}, err
		}
		if oldAssignment.ID == newAssignment.ID || oldAssignment.DeviceID != device.ID || newAssignment.DeviceID != device.ID || oldAssignment.MeasurementPointID != *audit.OldMeasurementPointID || newAssignment.MeasurementPointID != *audit.NewMeasurementPointID || oldAssignment.MeasurementPointID == newAssignment.MeasurementPointID {
			return AdminAuthorityRequest{}, errors.New("relocate assignment, MeasurementPoint, and Device references do not converge")
		}
		if audit.MeasurementPointID != nil && *audit.MeasurementPointID != newAssignment.MeasurementPointID {
			return AdminAuthorityRequest{}, errors.New("relocate direct MeasurementPoint conflicts with target")
		}
		oldPoint, err := lookupPoint(oldAssignment.MeasurementPointID, "old")
		if err != nil {
			return AdminAuthorityRequest{}, err
		}
		target, err := lookupPoint(newAssignment.MeasurementPointID, "new")
		if err != nil {
			return AdminAuthorityRequest{}, err
		}
		if err := checkShop(audit.ShopID, target); err != nil {
			return AdminAuthorityRequest{}, err
		}
		req.DeviceID = device.ID
		req.SourceMeasurementPointID, req.TargetMeasurementPointID = oldPoint.ID, target.ID

	case AdminUnbind:
		if audit.NewMeasurementPointID != nil || audit.NewAssignmentID != nil || audit.MeasurementPointID != nil {
			return AdminAuthorityRequest{}, errors.New("unbind audit contains contradictory new assignment/MeasurementPoint evidence")
		}
		if audit.OldAssignmentID == nil || audit.OldMeasurementPointID == nil {
			return AdminAuthorityRequest{}, errors.New("unbind audit requires old assignment and source MeasurementPoint evidence")
		}
		oldAssignment, err := lookupAssignment(audit.OldAssignmentID, "old")
		if err != nil {
			return AdminAuthorityRequest{}, err
		}
		device, err := checkDevice(audit.DeviceID)
		if err != nil {
			return AdminAuthorityRequest{}, err
		}
		if oldAssignment.DeviceID != device.ID || oldAssignment.MeasurementPointID != *audit.OldMeasurementPointID {
			return AdminAuthorityRequest{}, errors.New("unbind old assignment, source MeasurementPoint, and Device references do not converge")
		}
		source, err := lookupPoint(oldAssignment.MeasurementPointID, "old")
		if err != nil {
			return AdminAuthorityRequest{}, err
		}
		if err := checkShop(audit.ShopID, source); err != nil {
			return AdminAuthorityRequest{}, err
		}
		req.DeviceID, req.SourceMeasurementPointID = device.ID, source.ID
	}
	return req, nil
}

func valueUUID(v *uuid.UUID) uuid.UUID {
	if v == nil {
		return uuid.Nil
	}
	return *v
}

func classifyAdminAudit(facts FactSet, clients map[uint]bool, users map[uint]UserFact, op AdminOperationFact, audit AdminAuditFact) (Classification, string, *uint) {
	if audit.ID == uuid.Nil || audit.OperationID == uuid.Nil || audit.Action == "" || users[audit.ActorID].ID == 0 || op.OperationID != audit.OperationID || op.ActorID != audit.ActorID || op.ScopeKey != audit.ScopeKey {
		return BlockingIntegrityError, "admin audit provenance is malformed or mismatched with operation", nil
	}
	req, err := adminRequestFromAudit(facts, audit)
	if err != nil || !isSupportedAdminAction(req.Action) {
		return BlockingIntegrityError, "admin audit has unsupported or malformed action", nil
	}
	expected, structural, err := deriveAdminAuthorityNullable(facts, req)
	if err != nil || !structural {
		return BlockingIntegrityError, "admin action has missing, ambiguous, or cross-client relational path", nil
	}
	if audit.ClientID == nil {
		return ExplicitMappingRequired, "admin audit Client is NULL; relational path is structurally valid", expected
	}
	if valueUint(audit.ClientID) == 0 || !clients[valueUint(audit.ClientID)] {
		return BlockingIntegrityError, "admin audit references orphan Client", nil
	}
	if expected != nil && *audit.ClientID != *expected {
		return BlockingIntegrityError, "admin audit Client contradicts action relational authority", expected
	}
	if expected == nil {
		return BlockingIntegrityError, "admin audit has no unique relational Client authority", nil
	}
	return AlreadyConsistent, "admin audit Client matches action relational authority", expected
}

func classifyAdminRow(facts FactSet, clients map[uint]bool, users map[uint]UserFact, op AdminOperationFact, audits []AdminAuditFact) (Classification, string, *uint) {
	if op.OperationID == uuid.Nil || op.ActorID == 0 || op.Operation == "" || users[op.ActorID].ID == 0 {
		return BlockingIntegrityError, "admin operation provenance is malformed", nil
	}
	if len(audits) == 0 {
		return BlockingIntegrityError, "admin operation has no action audit relational authority", nil
	}
	var expected *uint
	for _, audit := range audits {
		if normalizedAdminAction(op.Operation) != normalizedAdminAction(audit.Action) {
			return BlockingIntegrityError, "admin operation and audit actions do not match", nil
		}
		class, reason, auditExpected := classifyAdminAudit(facts, clients, users, op, audit)
		if class == BlockingIntegrityError {
			return class, reason, auditExpected
		}
		if auditExpected != nil {
			if expected != nil && *expected != *auditExpected {
				return BlockingIntegrityError, "admin operation audits cross Client authority", nil
			}
			expected = cloneUint(auditExpected)
		}
	}
	if op.ClientID == nil {
		return ExplicitMappingRequired, "admin operation Client is NULL; relational path is structurally valid", expected
	}
	if valueUint(op.ClientID) == 0 || !clients[valueUint(op.ClientID)] {
		return BlockingIntegrityError, "admin operation references orphan Client", nil
	}
	if expected == nil || *op.ClientID != *expected {
		return BlockingIntegrityError, "admin operation Client contradicts action relational authority", expected
	}
	return AlreadyConsistent, "admin operation Client matches action relational authority", expected
}

func isSupportedAdminAction(a AdminAction) bool {
	return a == AdminCreateMeasurementPoint || a == AdminBind || a == AdminReplace || a == AdminRelocate || a == AdminUnbind
}

// deriveAdminAuthorityNullable is the classification counterpart of the
// public authority helper: NULL Shop.Client is an unknown-but-structurally-
// valid path, whereas missing/orphan/ambiguous rows are integrity blockers.
func deriveAdminAuthorityNullable(facts FactSet, req AdminAuthorityRequest) (*uint, bool, error) {
	clients := map[uint]bool{}
	shops := map[uint]ShopFact{}
	points := map[uuid.UUID]MeasurementPointFact{}
	devices := map[uint]bool{}
	for _, c := range facts.Clients {
		clients[c.ID] = true
	}
	for _, s := range facts.Shops {
		shops[s.ID] = s
	}
	for _, p := range facts.MeasurementPoints {
		points[p.ID] = p
	}
	for _, d := range facts.Devices {
		devices[d.ID] = true
	}
	point := func(id uuid.UUID) (*uint, error) {
		p, ok := points[id]
		if !ok || id == uuid.Nil {
			return nil, errors.New("measurement point is missing")
		}
		s, ok := shops[p.ShopID]
		if !ok || s.ID == 0 {
			return nil, errors.New("measurement point Shop is missing")
		}
		if s.ClientID == nil {
			return nil, nil
		}
		if valueUint(s.ClientID) == 0 || !clients[valueUint(s.ClientID)] {
			return nil, errors.New("measurement point Shop has orphan Client")
		}
		return cloneUint(s.ClientID), nil
	}
	deviceExists := func(id uint) error {
		if id == 0 || !devices[id] {
			return errors.New("device is missing")
		}
		return nil
	}
	device := func(id uint) (*uint, error) {
		if err := deviceExists(id); err != nil {
			return nil, err
		}
		var active []DeviceAssignmentFact
		for _, a := range facts.DeviceAssignments {
			if a.DeviceID != id {
				continue
			}
			if a.ID == uuid.Nil || a.MeasurementPointID == uuid.Nil || a.ValidFrom.IsZero() || (a.ValidTo != nil && !a.ValidTo.After(a.ValidFrom)) {
				return nil, errors.New("device assignment is malformed")
			}
			if !a.ValidFrom.After(req.AsOf) && (a.ValidTo == nil || req.AsOf.Before(*a.ValidTo)) {
				active = append(active, a)
			}
		}
		if len(active) != 1 {
			return nil, errors.New("device has missing or ambiguous active assignment")
		}
		var result *uint
		for _, a := range active {
			c, err := point(a.MeasurementPointID)
			if err != nil {
				return nil, err
			}
			result = c
		}
		for _, a := range facts.DeviceAssignments {
			if a.DeviceID != id || !a.ValidFrom.After(req.AsOf) {
				continue
			}
			c, err := point(a.MeasurementPointID)
			if err != nil {
				return nil, err
			}
			if result != nil && c == nil {
				return nil, errors.New("future assignment has unresolved Client authority")
			}
			if c != nil && result != nil && *c != *result {
				return nil, errors.New("future assignment crosses Client")
			}
			// A future-only Client never supplies current authority. When the
			// active path is unresolved, retain nil and let classification
			// require explicit mapping rather than borrowing future evidence.
		}
		return result, nil
	}
	combine := func(values ...*uint) (*uint, error) {
		var result *uint
		for _, c := range values {
			if c == nil {
				continue
			}
			if result != nil && *result != *c {
				return nil, errors.New("action crosses Client")
			}
			result = cloneUint(c)
		}
		return result, nil
	}
	switch req.Action {
	case AdminCreateMeasurementPoint:
		s, ok := shops[req.ShopID]
		if !ok || req.ShopID == 0 {
			return nil, false, errors.New("shop is missing")
		}
		if s.ClientID == nil {
			return nil, true, nil
		}
		if valueUint(s.ClientID) == 0 || !clients[valueUint(s.ClientID)] {
			return nil, false, errors.New("shop has orphan Client")
		}
		return cloneUint(s.ClientID), true, nil
	case AdminBind:
		t, err := point(req.TargetMeasurementPointID)
		if err != nil {
			return nil, false, err
		}
		d, err := device(req.DeviceID)
		if err != nil {
			return nil, false, err
		}
		c, err := combine(t, d)
		return c, err == nil, err
	case AdminReplace:
		// The old assignment is commonly closed by the time the immutable
		// audit is collected. Its source MeasurementPoint is therefore the
		// relational evidence for the replaced device; requiring an active
		// assignment for req.DeviceID would reject valid post-transition rows.
		s, err := point(req.SourceMeasurementPointID)
		if err != nil {
			return nil, false, err
		}
		t, err := point(req.TargetMeasurementPointID)
		if err != nil {
			return nil, false, err
		}
		if err := deviceExists(req.DeviceID); err != nil {
			return nil, false, err
		}
		r, err := device(req.ReplacementDeviceID)
		if err != nil {
			return nil, false, err
		}
		c, err := combine(s, t, r)
		return c, err == nil, err
	case AdminRelocate:
		s, err := point(req.SourceMeasurementPointID)
		if err != nil {
			return nil, false, err
		}
		t, err := point(req.TargetMeasurementPointID)
		if err != nil {
			return nil, false, err
		}
		d, err := device(req.DeviceID)
		if err != nil {
			return nil, false, err
		}
		c, err := combine(s, t, d)
		return c, err == nil, err
	case AdminUnbind:
		// Unbind closes the old assignment. The source point remains the
		// relational authority even when no active assignment exists at the
		// collection timestamp.
		s, err := point(req.SourceMeasurementPointID)
		if err != nil {
			return nil, false, err
		}
		if err := deviceExists(req.DeviceID); err != nil {
			return nil, false, err
		}
		return cloneUint(s), true, nil
	default:
		return nil, false, errors.New("unsupported action")
	}
}

func valueUint(v *uint) uint {
	if v == nil {
		return 0
	}
	return *v
}
func cloneUint(v *uint) *uint {
	if v == nil {
		return nil
	}
	x := *v
	return &x
}
func cloneUUID(v *uuid.UUID) *uuid.UUID {
	if v == nil {
		return nil
	}
	x := *v
	return &x
}

// BuildPlan creates a deterministic, write-free plan. Explicit mappings are
// only accepted for structurally valid EXPLICIT-MAPPING-REQUIRED devices.
func BuildPlan(facts FactSet, artifact *MappingArtifact) (Plan, error) {
	var plan Plan
	decisions, err := Classify(facts)
	if err != nil {
		return plan, err
	}
	factClasses, err := ClassifyFacts(facts)
	if err != nil {
		return plan, err
	}
	_, sourceDigest, err := CanonicalSourceFacts(facts)
	if err != nil {
		return plan, err
	}
	_, mappingBasisDigest, err := CanonicalMappingBasis(facts)
	if err != nil {
		return plan, err
	}
	var mappingDigest []byte
	deviceMappings := map[uint]MappingEntry{}
	shopMappings := map[uint]MappingEntry{}
	adminMappings := map[uuid.UUID]MappingEntry{}
	if artifact != nil {
		if err := artifact.validate(); err != nil {
			return plan, err
		}
		if artifact.SourceFactsDigest == "" {
			return plan, errors.New("explicit mapping artifact must bind source facts digest")
		}
		mappingDigestHex := hex.EncodeToString(mappingBasisDigest)
		rawDigestHex := hex.EncodeToString(sourceDigest)
		// Accept the pre-F1 raw snapshot digest only as a compatibility form;
		// it remains time-bound and therefore cannot bypass stale protection.
		if artifact.SourceFactsDigest != mappingDigestHex && artifact.SourceFactsDigest != rawDigestHex {
			return plan, errors.New("explicit mapping artifact is stale for source facts")
		}
		mappingDigest, err = artifact.Digest()
		if err != nil {
			return plan, err
		}
		for _, m := range artifact.Mappings {
			category, _, _ := mappingKey(m)
			switch category {
			case MappingDevice:
				deviceMappings[m.DeviceID] = m
			case MappingShop:
				shopMappings[m.ShopID] = m
			case MappingAdminProvenance:
				adminMappings[m.OperationID] = m
			}
		}
	}
	clients := map[uint]bool{}
	for _, c := range facts.Clients {
		clients[c.ID] = true
	}
	devices := map[uint]DeviceFact{}
	for _, d := range facts.Devices {
		devices[d.ID] = d
	}
	points := map[uuid.UUID]MeasurementPointFact{}
	for _, p := range facts.MeasurementPoints {
		points[p.ID] = p
	}
	assignments := map[uint][]DeviceAssignmentFact{}
	for _, a := range facts.DeviceAssignments {
		assignments[a.DeviceID] = append(assignments[a.DeviceID], a)
	}
	// An admin mapping may fill exactly the NULL side(s) of one operation /
	// audit pair. A matching non-NULL side is valid evidence and must not make
	// the mapping invalid; a BLOCKING side or a contradictory known relational
	// Client must still reject the whole mapping.
	adminClassMatches := func(fc FactClassification, operationID uuid.UUID) bool {
		if fc.Kind != FactKindAdminProvenance {
			return false
		}
		if fc.StableID == StableID("security-reconciliation-admin-operation/v5", operationID.String()) {
			return true
		}
		for _, audit := range facts.AdminAudits {
			if audit.OperationID == operationID && fc.StableID == StableID("security-reconciliation-admin-audit/v5", audit.ID.String()) {
				return true
			}
		}
		return false
	}
	adminCurrentClient := func(fc FactClassification) *uint {
		for _, op := range facts.AdminOperations {
			if fc.StableID == StableID("security-reconciliation-admin-operation/v5", op.OperationID.String()) {
				return cloneUint(op.ClientID)
			}
		}
		for _, audit := range facts.AdminAudits {
			if fc.StableID == StableID("security-reconciliation-admin-audit/v5", audit.ID.String()) {
				return cloneUint(audit.ClientID)
			}
		}
		return nil
	}
	for id, entry := range adminMappings {
		if !adminOperationExists(facts, id) {
			return plan, fmt.Errorf("admin provenance mapping references unknown operation %s", id)
		}
		if !clients[entry.ClientID] {
			return plan, fmt.Errorf("admin mapping references unknown Client %d", entry.ClientID)
		}
		hasNullSide := false
		matched := false
		for i := range factClasses {
			if !adminClassMatches(factClasses[i], id) {
				continue
			}
			matched = true
			fc := &factClasses[i]
			if fc.Classification == BlockingIntegrityError {
				return plan, fmt.Errorf("admin provenance mapping %s matches a blocking row", id)
			}
			if fc.AuthorityClientID != nil && *fc.AuthorityClientID != entry.ClientID {
				return plan, fmt.Errorf("admin mapping Client %d contradicts relational authority %d", entry.ClientID, *fc.AuthorityClientID)
			}
			if entry.ExpectedCurrentClientID != nil {
				current := adminCurrentClient(*fc)
				if current == nil || *current != *entry.ExpectedCurrentClientID {
					return plan, fmt.Errorf("admin provenance row %s has stale expected current Client", fc.StableID)
				}
			}
			if fc.Classification == ExplicitMappingRequired {
				hasNullSide = true
				fc.Classification = AutoReconcilable
				fc.AuthorityClientID = cloneUint(&entry.ClientID)
				fc.Reason = "explicit admin provenance mapping artifact"
			}
		}
		if !matched || !hasNullSide {
			return plan, fmt.Errorf("admin provenance mapping %s has no NULL row to fill", id)
		}
	}
	for _, fc := range factClasses {
		if fc.Kind == FactKindAdminProvenance && fc.Classification == ExplicitMappingRequired {
			plan.RequiredExplicitMappings = append(plan.RequiredExplicitMappings, "admin-provenance:"+fc.StableID.String())
		}
	}
	plan.SchemaVersion = SchemaVersion
	plan.AsOf = facts.AsOf.UTC()
	plan.SourceFactsDigest = hex.EncodeToString(sourceDigest)
	plan.FactClassifications = factClasses
	plan.ExpectedAffectedCounts = map[string]int{
		ExpectedCountInventoryOwnerUpdates: 0,
		ExpectedCountShopClientUpdates:     0,
		ExpectedCountAdminClientUpdates:    0,
	}
	for _, fc := range factClasses {
		if fc.Classification == BlockingIntegrityError {
			plan.Blockers = append(plan.Blockers, fc.Kind+":"+fc.StableID.String()+":"+fc.Reason)
		}
	}
	for _, fc := range factClasses {
		if fc.Kind == FactKindAdminProvenance && fc.Classification == AutoReconcilable && strings.Contains(fc.Reason, "explicit") {
			plan.ExpectedAffectedCounts[ExpectedCountAdminClientUpdates]++
		}
	}
	plan.PostWriteVerification = []string{"re-read source facts under the exclusive pinned fence", "verify expected-current CAS values and intended relational Client values"}
	if artifact != nil {
		plan.MappingDigest = hex.EncodeToString(mappingDigest)
	}
	for _, d := range decisions {
		if d.Classification == BlockingIntegrityError {
			plan.Blockers = append(plan.Blockers, fmt.Sprintf("device:%d:%s", d.DeviceID, d.Reason))
		}
		item := PlanItem{StableID: StableID("security-reconciliation-plan-item/v5", fmt.Sprint(d.DeviceID)), Kind: PlanItemDevice, DeviceID: d.DeviceID, Classification: d.Classification, AuthorityClientID: cloneUint(d.AuthorityClientID), Reason: d.Reason}
		if d.ActiveAssignmentID != nil {
			item.AuthoritativeEvidence = []string{"device_assignment:" + d.ActiveAssignmentID.String(), "measurement_point->shop->client"}
		}
		if device, ok := devices[d.DeviceID]; ok {
			item.ExpectedCurrent.InventoryOwnerClientID = cloneUint(device.InventoryOwnerClientID)
			item.IntendedOwnerClientID = cloneUint(d.AuthorityClientID)
		}
		if d.ActiveAssignmentID != nil {
			item.ExpectedCurrent.ActiveAssignmentID = cloneUUID(d.ActiveAssignmentID)
			for _, a := range assignments[d.DeviceID] {
				if a.ID == *d.ActiveAssignmentID {
					item.ExpectedCurrent.MeasurementPointID = cloneUUID(&a.MeasurementPointID)
					item.ExpectedCurrent.ValidFrom = timePtr(a.ValidFrom)
					item.ExpectedCurrent.ValidTo = cloneTime(a.ValidTo)
				}
			}
		}
		if d.Classification == ExplicitMappingRequired {
			if entry, ok := deviceMappings[d.DeviceID]; ok {
				if entry.ExpectedCurrentClientID != nil && valueUint(devices[d.DeviceID].InventoryOwnerClientID) != *entry.ExpectedCurrentClientID {
					return plan, fmt.Errorf("explicit mapping for device %d has stale expected current value", d.DeviceID)
				}
				if !clients[entry.ClientID] {
					return plan, fmt.Errorf("explicit mapping for device %d references unknown Client %d", d.DeviceID, entry.ClientID)
				}
				item.AuthorityClientID = cloneUint(&entry.ClientID)
				item.IntendedOwnerClientID = cloneUint(&entry.ClientID)
				item.SetInventoryOwner = true
				item.ExpectedAffectedCount = 1
				plan.ExpectedAffectedCounts[ExpectedCountInventoryOwnerUpdates]++
				item.Reason = "explicit device mapping artifact"
			} else {
				plan.RequiredExplicitMappings = append(plan.RequiredExplicitMappings, fmt.Sprintf("device:%d", d.DeviceID))
			}
		}
		if _, ok := deviceMappings[d.DeviceID]; ok && d.Classification != ExplicitMappingRequired {
			return plan, fmt.Errorf("explicit mapping for device %d is not structurally required", d.DeviceID)
		}
		if d.Classification == AutoReconcilable {
			item.SetInventoryOwner = true
			item.IntendedOwnerClientID = cloneUint(d.AuthorityClientID)
			item.ExpectedAffectedCount = 1
			plan.ExpectedAffectedCounts[ExpectedCountInventoryOwnerUpdates]++
		}
		if item.SetInventoryOwner && item.IntendedOwnerClientID == nil {
			item.IntendedOwnerClientID = cloneUint(item.AuthorityClientID)
		}
		plan.Items = append(plan.Items, item)
	}
	// Shop and admin mappings are write intents, not validation-only hints.
	for _, fc := range factClasses {
		if fc.Kind == FactKindShop && fc.Classification == ExplicitMappingRequired {
			shopID := stableNumericID(fc.StableID, facts.Shops, "security-reconciliation-shop/v5")
			entry, mapped := shopMappings[shopID]
			if !mapped {
				plan.RequiredExplicitMappings = append(plan.RequiredExplicitMappings, fmt.Sprintf("shop:%d", shopID))
				plan.Items = append(plan.Items, PlanItem{StableID: StableID("security-reconciliation-plan-shop/v5", fmt.Sprint(shopID)), Kind: PlanItemShop, ShopID: shopID, Classification: ExplicitMappingRequired, ExpectedCurrent: ExpectedCurrent{ClientID: nil}, Reason: fc.Reason, AuthoritativeEvidence: []string{fmt.Sprintf("shop:%d", shopID)}})
				continue
			}
			if !clients[entry.ClientID] {
				return plan, fmt.Errorf("shop mapping references unknown Client %d", entry.ClientID)
			}
			client := entry.ClientID
			shop, _ := mapShop(facts.Shops, shopID)
			if entry.ExpectedCurrentClientID != nil && (shop.ClientID == nil || *shop.ClientID != *entry.ExpectedCurrentClientID) {
				return plan, fmt.Errorf("shop %d has stale expected current Client", shopID)
			}
			plan.Items = append(plan.Items, PlanItem{StableID: StableID("security-reconciliation-plan-shop/v5", fmt.Sprint(shopID)), Kind: PlanItemShop, ShopID: shopID, Classification: AutoReconcilable, ExpectedCurrent: ExpectedCurrent{ClientID: cloneUint(shop.ClientID)}, IntendedClientID: &client, AuthorityClientID: &client, SetShopClient: true, ExpectedAffectedCount: 1, AuthoritativeEvidence: []string{fmt.Sprintf("shop:%d", shopID)}, Reason: "explicit Shop Client mapping artifact"})
			plan.ExpectedAffectedCounts[ExpectedCountShopClientUpdates]++
		} else if fc.Kind == FactKindShop {
			if _, mapped := shopMappings[stableNumericID(fc.StableID, facts.Shops, "security-reconciliation-shop/v5")]; mapped {
				return plan, errors.New("shop mapping is not structurally required")
			}
		}
	}
	for _, fc := range factClasses {
		if fc.Kind != FactKindAdminProvenance || fc.Classification != AutoReconcilable || !strings.Contains(fc.Reason, "explicit") {
			continue
		}
		var opID, auditID uuid.UUID
		for _, op := range facts.AdminOperations {
			if fc.StableID == StableID("security-reconciliation-admin-operation/v5", op.OperationID.String()) {
				opID = op.OperationID
				break
			}
		}
		for _, audit := range facts.AdminAudits {
			if fc.StableID == StableID("security-reconciliation-admin-audit/v5", audit.ID.String()) {
				auditID = audit.ID
				opID = audit.OperationID
				break
			}
		}
		entry, ok := adminMappings[opID]
		if !ok {
			return plan, errors.New("admin explicit mapping coverage disappeared")
		}
		client := entry.ClientID
		item := PlanItem{StableID: fc.StableID, Kind: PlanItemAdmin, OperationID: opID, AuditID: auditID, Classification: AutoReconcilable, AuthorityClientID: &client, IntendedClientID: &client, SetAdminClient: true, ExpectedAffectedCount: 1, AuthoritativeEvidence: []string{"action relational path", "explicit admin mapping artifact"}, Reason: "explicit admin provenance mapping artifact"}
		if opID != uuid.Nil {
			for _, op := range facts.AdminOperations {
				if op.OperationID == opID {
					item.ExpectedCurrent.ClientID = cloneUint(op.ClientID)
					if entry.ExpectedCurrentClientID != nil && (op.ClientID == nil || *op.ClientID != *entry.ExpectedCurrentClientID) {
						return plan, fmt.Errorf("admin operation %s has stale expected current Client", opID)
					}
				}
			}
		}
		if auditID != uuid.Nil {
			for _, audit := range facts.AdminAudits {
				if audit.ID == auditID {
					item.ExpectedCurrent.ClientID = cloneUint(audit.ClientID)
					if entry.ExpectedCurrentClientID != nil && (audit.ClientID == nil || *audit.ClientID != *entry.ExpectedCurrentClientID) {
						return plan, fmt.Errorf("admin audit %s has stale expected current Client", auditID)
					}
				}
			}
		}
		plan.Items = append(plan.Items, item)
	}
	for id, entry := range deviceMappings {
		if _, ok := devices[id]; !ok {
			return plan, fmt.Errorf("explicit mapping references unknown device %d", id)
		}
		if !clients[entry.ClientID] {
			return plan, fmt.Errorf("explicit mapping references unknown Client %d", entry.ClientID)
		}
	}
	for id, entry := range shopMappings {
		shop, ok := mapShop(facts.Shops, id)
		if !ok || shop.ClientID != nil {
			return plan, fmt.Errorf("shop mapping %d cannot repair structurally valid or missing Shop", id)
		}
		if !clients[entry.ClientID] {
			return plan, fmt.Errorf("shop mapping references unknown Client %d", entry.ClientID)
		}
	}
	for id, entry := range adminMappings {
		if !adminOperationExists(facts, id) {
			return plan, fmt.Errorf("admin provenance mapping references unknown operation %s", id)
		}
		if !clients[entry.ClientID] {
			return plan, fmt.Errorf("admin mapping references unknown Client %d", entry.ClientID)
		}
	}
	sort.Strings(plan.RequiredExplicitMappings)
	sort.Strings(plan.Blockers)
	plan.Items = normalizeItems(plan.Items)
	canonical, err := json.Marshal(struct {
		SchemaVersion            string               `json:"schema_version"`
		PlanID                   uuid.UUID            `json:"plan_id"`
		AsOf                     time.Time            `json:"as_of"`
		SourceFactsDigest        string               `json:"source_facts_digest"`
		MappingDigest            string               `json:"mapping_digest,omitempty"`
		FactClassifications      []FactClassification `json:"fact_classifications"`
		Items                    []PlanItem           `json:"items"`
		ExpectedAffectedCounts   map[string]int       `json:"expected_affected_counts"`
		Blockers                 []string             `json:"blockers,omitempty"`
		RequiredExplicitMappings []string             `json:"required_explicit_mappings,omitempty"`
		PostWriteVerification    []string             `json:"post_write_verification"`
	}{SchemaVersion: plan.SchemaVersion, PlanID: uuid.Nil, AsOf: plan.AsOf, SourceFactsDigest: plan.SourceFactsDigest, MappingDigest: plan.MappingDigest, FactClassifications: plan.FactClassifications, Items: plan.Items, ExpectedAffectedCounts: plan.ExpectedAffectedCounts, Blockers: plan.Blockers, RequiredExplicitMappings: plan.RequiredExplicitMappings, PostWriteVerification: plan.PostWriteVerification})
	if err != nil {
		return plan, err
	}
	plan.PlanID = StableID("security-reconciliation-plan/v5", string(canonical))
	canonical, err = json.Marshal(struct {
		SchemaVersion            string               `json:"schema_version"`
		PlanID                   uuid.UUID            `json:"plan_id"`
		AsOf                     time.Time            `json:"as_of"`
		SourceFactsDigest        string               `json:"source_facts_digest"`
		MappingDigest            string               `json:"mapping_digest,omitempty"`
		FactClassifications      []FactClassification `json:"fact_classifications"`
		Items                    []PlanItem           `json:"items"`
		ExpectedAffectedCounts   map[string]int       `json:"expected_affected_counts"`
		Blockers                 []string             `json:"blockers,omitempty"`
		RequiredExplicitMappings []string             `json:"required_explicit_mappings,omitempty"`
		PostWriteVerification    []string             `json:"post_write_verification"`
	}{plan.SchemaVersion, plan.PlanID, plan.AsOf, plan.SourceFactsDigest, plan.MappingDigest, plan.FactClassifications, plan.Items, plan.ExpectedAffectedCounts, plan.Blockers, plan.RequiredExplicitMappings, plan.PostWriteVerification})
	if err != nil {
		return plan, err
	}
	plan.Canonical = canonical
	sum := sha256.Sum256(canonical)
	plan.Digest = sum[:]
	return plan, nil
}

func stableNumericID(id uuid.UUID, shops []ShopFact, namespace string) uint {
	for _, s := range shops {
		if StableID(namespace, fmt.Sprint(s.ID)) == id {
			return s.ID
		}
	}
	return 0
}

func mapShop(shops []ShopFact, id uint) (ShopFact, bool) {
	for _, shop := range shops {
		if shop.ID == id {
			return shop, true
		}
	}
	return ShopFact{}, false
}
func adminOperationExists(facts FactSet, id uuid.UUID) bool {
	for _, op := range facts.AdminOperations {
		if op.OperationID == id {
			return true
		}
	}
	for _, audit := range facts.AdminAudits {
		if audit.OperationID == id {
			return true
		}
	}
	return false
}

func normalizeItems(items []PlanItem) []PlanItem {
	sort.Slice(items, func(i, j int) bool {
		if items[i].Kind != items[j].Kind {
			return items[i].Kind < items[j].Kind
		}
		if items[i].DeviceID != items[j].DeviceID {
			return items[i].DeviceID < items[j].DeviceID
		}
		if items[i].ShopID != items[j].ShopID {
			return items[i].ShopID < items[j].ShopID
		}
		return items[i].StableID.String() < items[j].StableID.String()
	})
	return items
}
func timePtr(v time.Time) *time.Time { x := v.UTC(); return &x }
func cloneTime(v *time.Time) *time.Time {
	if v == nil {
		return nil
	}
	return timePtr(*v)
}

// ExclusiveFence is the coarse compatibility interface. Fresh rechecks also
// require PinnedExclusiveFence: the explicit pinned-connection seam prevents
// a boolean marker from being the sole safety contract.
type ExclusiveFence interface{ ExclusiveReconciliationFence() bool }

// ReadOnlyConnection is deliberately query-only: the A2.1 collector cannot
// expose Exec/Prepare/write capabilities. It is a capability view over a
// caller-owned *sql.Tx, *sql.Conn, or equivalent query handle; the name does
// not require the database transaction to be read-only.
type ReadOnlyConnection interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type PinnedExclusiveFence interface {
	ExclusiveFence
	// PinnedTransaction returns the transaction already begun by the caller
	// after acquiring the exclusive fence. A2.1 never begins, commits, or
	// rolls back this transaction.
	PinnedTransaction() ReadOnlyConnection
}
type FreshFactRechecker interface {
	RecheckV5(context.Context, ExclusiveFence) (FactSet, error)
}

// ReadOnlyCollector is the only capability needed by the recheck wrapper.
type ReadOnlyCollector interface {
	CollectV5(context.Context, time.Time) (FactSet, error)
}
type ReadOnlyCollectorWithConnection interface {
	CollectV5Pinned(context.Context, time.Time, ReadOnlyConnection) (FactSet, error)
}
type FencedRechecker struct {
	Collector ReadOnlyCollector
	Now       func() time.Time
}

func (r FencedRechecker) RecheckV5(ctx context.Context, fence ExclusiveFence) (FactSet, error) {
	pinned, ok := fence.(PinnedExclusiveFence)
	if !ok || pinned == nil || !fence.ExclusiveReconciliationFence() {
		return FactSet{}, errors.New("exclusive pinned transaction is required")
	}
	transaction := pinned.PinnedTransaction()
	if transaction == nil {
		return FactSet{}, errors.New("caller-owned pinned transaction is required")
	}
	if r.Collector == nil {
		return FactSet{}, errors.New("read-only fact collector is required")
	}
	collector, ok := r.Collector.(ReadOnlyCollectorWithConnection)
	if !ok {
		return FactSet{}, errors.New("collector with pinned query connection is required")
	}
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	facts, err := collector.CollectV5Pinned(ctx, now().UTC(), transaction)
	if err != nil {
		return FactSet{}, err
	}
	return facts, nil
}

// ReadOnlyFence is useful for orchestration tests and carries no database
// capability. Production fence holders should own a caller-started transaction.
type ReadOnlyFence struct{}
type readOnlyFenceConnection struct{}

func (readOnlyFenceConnection) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, errors.New("test fence has no database connection")
}
func (readOnlyFenceConnection) QueryRowContext(context.Context, string, ...any) *sql.Row { return nil }
func (ReadOnlyFence) ExclusiveReconciliationFence() bool                                 { return true }
func (ReadOnlyFence) PinnedTransaction() ReadOnlyConnection                              { return readOnlyFenceConnection{} }

// EqualCanonical is a small test/integration helper that compares canonical
// bytes rather than Go's map/slice representation.
func EqualCanonical(a, b []byte) bool { return bytes.Equal(a, b) }
