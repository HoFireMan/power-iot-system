// Package shops contains the read-only application capability for GET
// /api/v1/shops.
package shops

import (
	"context"
	"strconv"

	"gorm.io/gorm"
	"power-iot-backend/internal/adapters/persistence"
	applicationauth "power-iot-backend/internal/application/auth"
)

// Shop is the safe API projection. IDs are strings at the application
// boundary, while nullable contact fields remain nullable.
type Shop struct {
	ID      string
	Code    string
	Name    string
	Address *string
	Phone   *string
	IsHead  bool
}

// Shops is the complete response projection for the list endpoint.
type Shops struct {
	Shops         []Shop
	CurrentShopID *string
}

// Query is the sole application capability required by the HTTP handler.
type Query interface {
	GetShops(context.Context, uint) (Shops, error)
}

type Service struct {
	repository persistence.AuthorizedShopsQuery
}

var _ Query = (*Service)(nil)

func New(repository persistence.AuthorizedShopsQuery) *Service {
	return &Service{repository: repository}
}

func (s *Service) GetShops(ctx context.Context, userID uint) (Shops, error) {
	if s == nil || s.repository == nil || userID == 0 {
		return Shops{}, applicationauth.ErrInfrastructure
	}
	projections, currentID, err := s.repository.FindAuthorizedShops(ctx, userID)
	if err != nil {
		return Shops{}, err
	}
	out := Shops{Shops: make([]Shop, 0, len(projections))}
	authorizedCurrent := false
	seen := make(map[uint]struct{}, len(projections))
	for _, projection := range projections {
		if _, exists := seen[projection.ID]; exists {
			continue
		}
		seen[projection.ID] = struct{}{}
		out.Shops = append(out.Shops, Shop{
			ID: strconv.FormatUint(uint64(projection.ID), 10), Code: projection.Code,
			Name: projection.Name, Address: projection.Address, Phone: projection.Phone,
			IsHead: projection.IsHead,
		})
		if currentID != nil && projection.ID == *currentID {
			authorizedCurrent = true
		}
	}
	if authorizedCurrent {
		id := strconv.FormatUint(uint64(*currentID), 10)
		out.CurrentShopID = &id
	}
	return out, nil
}

// GormQueryRunner is the production composition seam. It performs only the
// parameterized read and never mutates CurrentShopID or any other state.
type GormQueryRunner struct {
	repository persistence.AuthorizedShopsQuery
}

var _ Query = (*GormQueryRunner)(nil)

func NewGormQueryRunner(db *gorm.DB) *GormQueryRunner {
	return &GormQueryRunner{repository: persistence.NewUserShopQueryRepository(db)}
}

// NewQueryRunner adapts an already-constructed read-only repository, which is
// useful for isolated application tests without changing production wiring.
func NewQueryRunner(repository persistence.AuthorizedShopsQuery) *GormQueryRunner {
	return &GormQueryRunner{repository: repository}
}

func (r *GormQueryRunner) GetShops(ctx context.Context, userID uint) (Shops, error) {
	if r == nil || r.repository == nil {
		return Shops{}, applicationauth.ErrInfrastructure
	}
	return New(r.repository).GetShops(ctx, userID)
}
