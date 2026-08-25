// Package billingenergy exposes the application seam for historical Billing
// energy facts. It validates the small request contract and leaves evidence,
// attribution, and aggregation inside the deep persistence/core modules.
package billingenergy

import (
	"context"
	"errors"
	"time"

	core "power-iot-backend/internal/core/billingenergy"
)

var ErrBillingEnergyNotFound = errors.New("billing energy not found")

// Repository is intentionally one read method: callers do not need to know
// how authorization, MVCC, assignment history, evidence, and aggregation are
// implemented by the PostgreSQL adapter.
type Repository interface {
	FindBillingEnergy(context.Context, uint, uint, core.BillingMonth, func() time.Time) (core.Facts, error)
}

type Service struct {
	repository Repository
	now        func() time.Time
}

func New(repository Repository, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{repository: repository, now: now}
}

func (s *Service) Find(ctx context.Context, userID, shopID uint, monthValue string) (core.Facts, error) {
	if s == nil || s.repository == nil || userID == 0 || shopID == 0 {
		return core.Facts{}, ErrBillingEnergyNotFound
	}
	month, err := core.ParseBillingMonth(monthValue)
	if err != nil {
		return core.Facts{}, err
	}
	return s.repository.FindBillingEnergy(ctx, userID, shopID, month, s.now)
}
