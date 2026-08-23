// Package me contains the read-only application capability for GET /api/v1/me.
package me

import (
	"context"
	"errors"
	"strconv"

	"gorm.io/gorm"
	"power-iot-backend/internal/adapters/persistence"
	applicationauth "power-iot-backend/internal/application/auth"
)

// Profile is a safe application projection. Pointer fields preserve JSON null
// semantics at the HTTP boundary without exposing database or auth internals.
type Profile struct {
	ID            string
	Account       string
	Name          string
	Email         *string
	Phone         *string
	IsAdmin       bool
	CurrentShopID *string
}

// Query is the sole application capability required by the HTTP handler.
type Query interface {
	GetMe(context.Context, uint) (Profile, error)
}

// Service maps the persistence projection to a safe, string-ID application
// projection. It contains no authorization policy beyond the authoritative
// current-shop sanitization performed by persistence.
type Service struct {
	repository persistence.UserQueryPersistence
}

var _ Query = (*Service)(nil)

func New(repository persistence.UserQueryPersistence) *Service {
	return &Service{repository: repository}
}

func (s *Service) GetMe(ctx context.Context, userID uint) (Profile, error) {
	if s == nil || s.repository == nil || userID == 0 {
		return Profile{}, applicationauth.ErrInfrastructure
	}
	projection, err := s.repository.FindUserProfile(ctx, userID)
	if err != nil {
		if errors.Is(err, persistence.ErrUserQueryRecordNotFound) {
			return Profile{}, applicationauth.ErrUnauthorized
		}
		return Profile{}, err
	}
	profile := Profile{
		ID: strconv.FormatUint(uint64(projection.ID), 10), Account: projection.Account,
		Name: projection.Name, Email: projection.Email, Phone: projection.Phone,
		IsAdmin: projection.IsAdmin,
	}
	if projection.CurrentShopID != nil {
		id := strconv.FormatUint(uint64(*projection.CurrentShopID), 10)
		profile.CurrentShopID = &id
	}
	return profile, nil
}

// GormQueryRunner is the production composition seam. It uses the supplied
// handle only for the parameterized read and never starts a write transaction.
type GormQueryRunner struct {
	db *gorm.DB
}

var _ Query = (*GormQueryRunner)(nil)

func NewGormQueryRunner(db *gorm.DB) *GormQueryRunner {
	return &GormQueryRunner{db: db}
}

func (r *GormQueryRunner) GetMe(ctx context.Context, userID uint) (Profile, error) {
	if r == nil || r.db == nil {
		return Profile{}, applicationauth.ErrInfrastructure
	}
	return New(persistence.NewUserQueryRepository(r.db)).GetMe(ctx, userID)
}
