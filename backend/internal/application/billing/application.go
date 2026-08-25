package billing

import (
	"context"
	"errors"
	"strconv"
	"time"

	"gorm.io/gorm"
	"power-iot-backend/internal/adapters/persistence"
	corebilling "power-iot-backend/internal/core/billing"
)

type Plan struct {
	Code       string
	Tariff     string
	UsageClass *string
}

type Assignment struct {
	PlanCode  string
	ValidFrom time.Time
	ValidTo   *time.Time
}

type Configuration struct {
	ShopID            string
	ElectricityTariff *string
	Supported         bool
	Plans             []Plan
	Current           *Assignment
	Scheduled         *Assignment
}

type Repository interface {
	FindBillingConfiguration(context.Context, uint, uint, time.Time) (persistence.BillingConfigurationProjection, error)
	SetBillingPlan(context.Context, uint, uint, string, time.Time) error
}

type Service struct {
	repository Repository
	now        func() time.Time
}

var (
	ErrInvalidPlan           = errors.New("invalid billing plan")
	ErrConfigurationNotFound = errors.New("billing configuration not found")
)

func New(repository Repository, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{repository: repository, now: now}
}

func (s *Service) GetConfiguration(ctx context.Context, userID, shopID uint) (Configuration, error) {
	if s == nil || s.repository == nil || userID == 0 || shopID == 0 {
		return Configuration{}, ErrConfigurationNotFound
	}
	projection, err := s.repository.FindBillingConfiguration(ctx, userID, shopID, s.now().UTC())
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Configuration{}, ErrConfigurationNotFound
	}
	if err != nil {
		return Configuration{}, err
	}
	out := Configuration{ShopID: formatID(projection.ShopID), ElectricityTariff: projection.ElectricityTariff, Supported: projection.Supported, Plans: make([]Plan, 0, len(projection.Plans))}
	for _, plan := range projection.Plans {
		out.Plans = append(out.Plans, Plan{Code: plan.Code, Tariff: plan.Tariff, UsageClass: plan.UsageClass})
	}
	out.Current = assignmentModel(projection.Current)
	out.Scheduled = assignmentModel(projection.Scheduled)
	return out, nil
}

func (s *Service) SetConfiguration(ctx context.Context, actorID, shopID uint, planCode string) error {
	if s == nil || s.repository == nil || actorID == 0 || shopID == 0 || !corebilling.IsSupportedPlan(planCode) {
		return ErrInvalidPlan
	}
	now := s.now().In(mustBusinessLocation())
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, mustBusinessLocation())
	projection, err := s.repository.FindBillingConfiguration(ctx, actorID, shopID, now)
	if err != nil {
		return err
	}
	if !projection.Supported || projection.ElectricityTariff == nil || !corebilling.CompatiblePlan(*projection.ElectricityTariff, planCode) {
		return corebilling.ErrBillingTariffMismatch
	}
	effective := start
	if projection.Current != nil {
		effective = start.AddDate(0, 1, 0)
	}
	err = s.repository.SetBillingPlan(ctx, actorID, shopID, planCode, effective)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrConfigurationNotFound
	}
	return err
}

func assignmentModel(value *persistence.BillingAssignmentProjection) *Assignment {
	if value == nil {
		return nil
	}
	return &Assignment{PlanCode: value.PlanCode, ValidFrom: value.ValidFrom, ValidTo: value.ValidTo}
}

func formatID(value uint) string {
	return strconv.FormatUint(uint64(value), 10)
}

func mustBusinessLocation() *time.Location {
	location, err := time.LoadLocation("Asia/Taipei")
	if err != nil {
		panic(err)
	}
	return location
}
