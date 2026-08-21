package sourceowner

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/google/uuid"
)

// CanonicalSourceFacts returns deterministic JSON and its SHA-256 digest.
// These functions operate on ordinary facts only; they do not mint evidence.
func CanonicalSourceFacts(facts FactSet) ([]byte, []byte, error) {
	if err := ValidateVersion(facts); err != nil {
		return nil, nil, err
	}
	n := NormalizeFacts(facts)
	if err := ValidateFactIdentities(n); err != nil {
		return nil, nil, err
	}
	if err := CanonicalizeAdminJSON(&n); err != nil {
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

func ValidateFactIdentities(f FactSet) error {
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

func CanonicalizeAdminJSON(f *FactSet) error {
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

func NormalizeFacts(f FactSet) FactSet {
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
