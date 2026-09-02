// Package historicalreport projects the accepted BillingEnergy facts into a
// read-only Measurement Point historical energy report. It contains no money,
// tariff, or persistence concerns.
package historicalreport

import (
	"context"
	"errors"
	"math/big"
	"time"

	appbillingenergy "power-iot-backend/internal/application/billingenergy"
	corebillingenergy "power-iot-backend/internal/core/billingenergy"
)

type Status string

const (
	StatusNoExpectedWindow Status = "NO_EXPECTED_WINDOW"
	StatusNoData           Status = "NO_DATA"
	StatusPartial          Status = "PARTIAL"
	StatusComplete         Status = "COMPLETE"
)

var ErrInvalidFacts = errors.New("invalid historical report facts")

// Repository is the existing BillingEnergy read seam. The report deliberately
// consumes facts instead of introducing a second historical-energy query.
type Repository = appbillingenergy.Repository

type Service struct {
	energy *appbillingenergy.Service
}

func New(repository Repository, now func() time.Time) *Service {
	return &Service{energy: appbillingenergy.New(repository, now)}
}

type Period struct {
	Start    time.Time
	End      time.Time
	Cutoff   time.Time
	Snapshot time.Time
}

type Facts struct {
	Status                  Status
	UsageKwh                *string
	ExpectedDurationSeconds int64
	ObservedDurationSeconds int64
	Coverage                *string
}

type MeasurementPoint struct {
	MeasurementPointID      string
	Status                  Status
	UsageKwh                *string
	ExpectedDurationSeconds int64
	ObservedDurationSeconds int64
	Coverage                *string
	Warnings                []string
}

type Report struct {
	Month             string
	Timezone          string
	Period            Period
	Summary           Facts
	MeasurementPoints []MeasurementPoint
	Warnings          []string
}

func (s *Service) Find(ctx context.Context, userID, shopID uint, month string) (Report, error) {
	if s == nil || s.energy == nil {
		return Report{}, appbillingenergy.ErrBillingEnergyNotFound
	}
	facts, err := s.energy.Find(ctx, userID, shopID, month)
	if err != nil {
		return Report{}, err
	}
	return project(month, facts)
}

func project(month string, facts corebillingenergy.Facts) (Report, error) {
	summary, err := projectFacts(facts.UsageMicros, facts.ExpectedDuration, facts.ObservedDuration, facts.Coverage, facts.Warnings)
	if err != nil {
		return Report{}, err
	}
	points := make([]MeasurementPoint, 0, len(facts.Points))
	for _, point := range facts.Points {
		projected, err := projectFacts(point.UsageMicros, point.ExpectedDuration, point.ObservedDuration, point.Coverage, point.Warnings)
		if err != nil {
			return Report{}, err
		}
		points = append(points, MeasurementPoint{
			MeasurementPointID:      point.MeasurementPointID,
			Status:                  projected.Status,
			UsageKwh:                projected.UsageKwh,
			ExpectedDurationSeconds: projected.ExpectedDurationSeconds,
			ObservedDurationSeconds: projected.ObservedDurationSeconds,
			Coverage:                projected.Coverage,
			Warnings:                projectedWarnings(point.Warnings),
		})
	}
	return Report{
		Month:             month,
		Timezone:          corebillingenergy.BusinessTimezone,
		Period:            Period{Start: facts.PeriodStart, End: facts.PeriodEnd, Cutoff: facts.Cutoff, Snapshot: facts.Snapshot},
		Summary:           summary,
		MeasurementPoints: points,
		Warnings:          projectedWarnings(facts.Warnings),
	}, nil
}

func projectFacts(usage *int64, expected, observed time.Duration, coverage *big.Rat, warnings []corebillingenergy.WarningCode) (Facts, error) {
	if expected < 0 || observed < 0 || (usage != nil && *usage < 0) {
		return Facts{}, ErrInvalidFacts
	}
	status := statusFor(usage, expected, observed)
	result := Facts{
		Status:                  status,
		ExpectedDurationSeconds: int64(expected / time.Second),
		ObservedDurationSeconds: int64(observed / time.Second),
	}
	if usage != nil {
		ratio := new(big.Rat).SetFrac(big.NewInt(*usage), big.NewInt(1_000_000))
		value := corebillingenergy.FormatDecimal(ratio)
		result.UsageKwh = &value
	}
	if coverage != nil {
		value := corebillingenergy.FormatDecimal(coverage)
		result.Coverage = &value
	}
	return result, nil
}

func statusFor(usage *int64, expected, observed time.Duration) Status {
	if expected == 0 {
		return StatusNoExpectedWindow
	}
	if usage == nil {
		return StatusNoData
	}
	if observed < expected {
		return StatusPartial
	}
	return StatusComplete
}

func projectedWarnings(values []corebillingenergy.WarningCode) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, string(value))
	}
	return out
}
