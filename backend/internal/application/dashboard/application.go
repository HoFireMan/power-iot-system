// Package dashboard contains the read-only application capability for the
// shop dashboard. Current power and best-effort observed energy are supplied
// by the authoritative persistence projection.
package dashboard

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"time"

	"gorm.io/gorm"
	"power-iot-backend/internal/adapters/persistence"
)

var (
	// ErrShopNotFound covers nonexistent, inactive, and unauthorized shops.
	// Callers map all of these cases to the same public 404 response.
	ErrShopNotFound = errors.New("shop not found")
	// ErrAmbiguousProjection fails closed if database state violates the
	// assignment authority's exactly-once projection invariant.
	ErrAmbiguousProjection = errors.New("ambiguous dashboard device projection")
)

// Shop is the dashboard's safe shop projection.
type Shop struct {
	ID   string
	Code string
	Name string
}

// Device is the current assignment/device projection. Status fields come
// directly from devices and are independent of telemetry freshness.
type Device struct {
	// ID is retained internally for projection uniqueness; it is not a public
	// navigation identity. MeasurementPointRef is the stable page locator.
	ID                  string
	MeasurementPointRef string
	Name                string
	IsOnline            bool
	LastSeen            *time.Time
}

// Dashboard is the application result. Current power and shop energy are
// supplied by authoritative persistence projections; carbon remains deferred.
type Dashboard struct {
	Shop          Shop
	Devices       []Device
	CurrentPowerW *float64
	DailyKwh      *float64
	MonthlyKwh    *float64
	DailyKg       *float64
	MonthlyKg     *float64
	GeneratedAt   time.Time
}

type Query interface {
	FindDashboard(context.Context, uint, uint, func() time.Time) (persistence.DashboardProjection, error)
}

type Service struct {
	query Query
	now   func() time.Time
}

func New(query Query, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{query: query, now: now}
}

func (s *Service) GetDashboard(ctx context.Context, userID, shopID uint) (Dashboard, error) {
	if s == nil || s.query == nil || userID == 0 || shopID == 0 {
		return Dashboard{}, persistence.ErrDashboardNotFound
	}
	projection, err := s.query.FindDashboard(ctx, userID, shopID, s.now)
	if err != nil {
		if errors.Is(err, persistence.ErrDashboardNotFound) {
			return Dashboard{}, ErrShopNotFound
		}
		return Dashboard{}, err
	}
	sort.SliceStable(projection.Devices, func(i, j int) bool { return projection.Devices[i].ID < projection.Devices[j].ID })
	seen := make(map[uint]struct{}, len(projection.Devices))
	devices := make([]Device, 0, len(projection.Devices))
	for _, row := range projection.Devices {
		if _, exists := seen[row.ID]; exists {
			return Dashboard{}, ErrAmbiguousProjection
		}
		seen[row.ID] = struct{}{}
		id := strconv.FormatUint(uint64(row.ID), 10)
		var lastSeen *time.Time
		if row.LastSeen != nil {
			value := row.LastSeen.UTC()
			lastSeen = &value
		}
		devices = append(devices, Device{ID: id, MeasurementPointRef: row.MeasurementPointID.String(), Name: row.Name, IsOnline: row.IsOnline, LastSeen: lastSeen})
	}
	snapshotNow := projection.Snapshot.UTC()
	if snapshotNow.IsZero() {
		snapshotNow = s.now().UTC()
	}
	return Dashboard{
		Shop:          Shop{ID: strconv.FormatUint(uint64(projection.Shop.ID), 10), Code: projection.Shop.Code, Name: projection.Shop.Name},
		Devices:       devices,
		CurrentPowerW: projection.CurrentPowerW,
		DailyKwh:      projection.DailyKwh,
		MonthlyKwh:    projection.MonthlyKwh,
		GeneratedAt:   snapshotNow,
	}, nil
}

// GormQueryRunner is the production read-only composition seam.
type GormQueryRunner struct{ service *Service }

func NewGormQueryRunner(db *gorm.DB) *GormQueryRunner {
	return &GormQueryRunner{service: New(persistence.NewDashboardQueryRepository(db), nil)}
}

// NewQueryRunner adapts an already-constructed read-only query for isolated
// application tests without changing production wiring.
func NewQueryRunner(query Query, now func() time.Time) *GormQueryRunner {
	return &GormQueryRunner{service: New(query, now)}
}

func (r *GormQueryRunner) GetDashboard(ctx context.Context, userID, shopID uint) (Dashboard, error) {
	if r == nil || r.service == nil {
		return Dashboard{}, persistence.ErrDashboardNotFound
	}
	return r.service.GetDashboard(ctx, userID, shopID)
}

var _ interface {
	GetDashboard(context.Context, uint, uint) (Dashboard, error)
} = (*GormQueryRunner)(nil)
