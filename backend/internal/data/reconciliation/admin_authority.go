package reconciliation

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// AdminAction names the five existing admin operations. This seam derives the
// Client exclusively from relational facts; it intentionally has no scope or
// request payload fields.
type AdminAction string

const (
	AdminCreateMeasurementPoint AdminAction = "Create Measurement Point"
	AdminBind                   AdminAction = "Bind"
	AdminReplace                AdminAction = "Replace"
	AdminRelocate               AdminAction = "Relocate"
	AdminUnbind                 AdminAction = "Unbind"
)

type AdminAuthorityRequest struct {
	Action                   AdminAction
	AsOf                     time.Time
	ShopID                   uint // only for Create Measurement Point
	DeviceID                 uint
	ReplacementDeviceID      uint
	SourceMeasurementPointID uuid.UUID
	TargetMeasurementPointID uuid.UUID
}

type AdminAuthority struct{ ClientID uint }

// DeriveAdminAuthority validates the relational Client implied by an admin
// action. UserShopRelation remains an authorization concern and is absent from
// this request by design. Existing Device.ShopID and inventory owner values are
// never read.
func DeriveAdminAuthority(facts FactSet, req AdminAuthorityRequest) (AdminAuthority, error) {
	if err := facts.validateVersion(); err != nil {
		return AdminAuthority{}, err
	}
	if req.AsOf.IsZero() {
		req.AsOf = facts.AsOf
	}
	if req.AsOf.IsZero() {
		return AdminAuthority{}, errors.New("admin authority time is required")
	}
	clients := map[uint]bool{}
	for _, c := range facts.Clients {
		if c.ID == 0 || clients[c.ID] {
			return AdminAuthority{}, errors.New("duplicate or invalid Client fact")
		}
		clients[c.ID] = true
	}
	devices := map[uint]bool{}
	for _, d := range facts.Devices {
		if d.ID == 0 || devices[d.ID] {
			return AdminAuthority{}, errors.New("duplicate or invalid Device fact")
		}
		devices[d.ID] = true
	}
	assignmentIDs := map[uuid.UUID]bool{}
	for _, a := range facts.DeviceAssignments {
		if a.ID == uuid.Nil || assignmentIDs[a.ID] {
			return AdminAuthority{}, errors.New("duplicate or invalid DeviceAssignment fact")
		}
		assignmentIDs[a.ID] = true
		if a.DeviceID == 0 || !devices[a.DeviceID] || a.MeasurementPointID == uuid.Nil || a.ValidFrom.IsZero() || (a.ValidTo != nil && !a.ValidTo.After(a.ValidFrom)) {
			return AdminAuthority{}, fmt.Errorf("malformed DeviceAssignment %s", a.ID)
		}
	}
	shops := map[uint]ShopFact{}
	for _, s := range facts.Shops {
		if s.ID == 0 || shops[s.ID].ID != 0 {
			return AdminAuthority{}, errors.New("duplicate or invalid Shop fact")
		}
		shops[s.ID] = s
	}
	points := map[uuid.UUID]MeasurementPointFact{}
	for _, p := range facts.MeasurementPoints {
		if p.ID == uuid.Nil || p.ShopID == 0 || points[p.ID].ID != uuid.Nil {
			return AdminAuthority{}, errors.New("duplicate or invalid MeasurementPoint fact")
		}
		points[p.ID] = p
	}
	for _, a := range facts.DeviceAssignments {
		if _, ok := points[a.MeasurementPointID]; !ok {
			return AdminAuthority{}, fmt.Errorf("malformed DeviceAssignment %s", a.ID)
		}
	}
	pointClient := func(id uuid.UUID) (uint, error) {
		p, ok := points[id]
		if !ok {
			return 0, fmt.Errorf("measurement point %s is missing", id)
		}
		s, ok := shops[p.ShopID]
		if !ok || s.ClientID == nil || *s.ClientID == 0 || !clients[*s.ClientID] {
			return 0, fmt.Errorf("measurement point %s has no relational Client authority", id)
		}
		return *s.ClientID, nil
	}
	deviceClient := func(id uint) (uint, error) {
		if id == 0 {
			return 0, errors.New("device is required")
		}
		if !devices[id] {
			return 0, fmt.Errorf("device %d is missing", id)
		}
		var owner uint
		activeCount := 0
		futureClients := []uint{}
		for _, a := range facts.DeviceAssignments {
			if a.DeviceID != id {
				continue
			}
			if a.ID == uuid.Nil || a.MeasurementPointID == uuid.Nil || a.ValidFrom.IsZero() || (a.ValidTo != nil && !a.ValidTo.After(a.ValidFrom)) {
				return 0, fmt.Errorf("device %d has malformed assignment", id)
			}
			c, err := pointClient(a.MeasurementPointID)
			if err != nil {
				return 0, err
			}
			active := !a.ValidFrom.After(req.AsOf) && (a.ValidTo == nil || req.AsOf.Before(*a.ValidTo))
			if active {
				activeCount++
				owner = c
			} else if a.ValidFrom.After(req.AsOf) {
				futureClients = append(futureClients, c)
			}
		}
		if activeCount == 0 {
			return 0, fmt.Errorf("device %d has no active relational assignment", id)
		}
		if activeCount > 1 {
			return 0, fmt.Errorf("device %d has ambiguous active assignments", id)
		}
		for _, futureClient := range futureClients {
			if futureClient != owner {
				return 0, fmt.Errorf("device %d future assignment changes Client authority", id)
			}
		}
		return owner, nil
	}
	combine := func(values ...uint) (AdminAuthority, error) {
		var result uint
		for _, v := range values {
			if v == 0 {
				continue
			}
			if result != 0 && result != v {
				return AdminAuthority{}, errors.New("admin action crosses Client authority")
			}
			result = v
		}
		if result == 0 {
			return AdminAuthority{}, errors.New("admin action has no relational Client authority")
		}
		return AdminAuthority{ClientID: result}, nil
	}
	switch req.Action {
	case AdminCreateMeasurementPoint:
		if req.ShopID == 0 {
			return AdminAuthority{}, errors.New("shop is required")
		}
		s, ok := shops[req.ShopID]
		if !ok || s.ClientID == nil || *s.ClientID == 0 || !clients[*s.ClientID] {
			return AdminAuthority{}, errors.New("shop has no relational Client authority")
		}
		return combine(*s.ClientID)
	case AdminBind:
		target, err := pointClient(req.TargetMeasurementPointID)
		if err != nil {
			return AdminAuthority{}, err
		}
		device, err := deviceClient(req.DeviceID)
		if err != nil {
			return AdminAuthority{}, err
		}
		return combine(target, device)
	case AdminReplace:
		target, err := pointClient(req.TargetMeasurementPointID)
		if err != nil {
			return AdminAuthority{}, err
		}
		current, err := deviceClient(req.DeviceID)
		if err != nil {
			return AdminAuthority{}, err
		}
		replacement, err := deviceClient(req.ReplacementDeviceID)
		if err != nil {
			return AdminAuthority{}, err
		}
		return combine(target, current, replacement)
	case AdminRelocate:
		source, err := pointClient(req.SourceMeasurementPointID)
		if err != nil {
			return AdminAuthority{}, err
		}
		target, err := pointClient(req.TargetMeasurementPointID)
		if err != nil {
			return AdminAuthority{}, err
		}
		device, err := deviceClient(req.DeviceID)
		if err != nil {
			return AdminAuthority{}, err
		}
		return combine(source, target, device)
	case AdminUnbind:
		source, err := pointClient(req.SourceMeasurementPointID)
		if err != nil {
			return AdminAuthority{}, err
		}
		device, err := deviceClient(req.DeviceID)
		if err != nil {
			return AdminAuthority{}, err
		}
		return combine(source, device)
	default:
		return AdminAuthority{}, fmt.Errorf("unsupported admin action %q", req.Action)
	}
}
