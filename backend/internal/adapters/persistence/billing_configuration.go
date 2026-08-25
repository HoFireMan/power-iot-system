package persistence

import (
	"context"
	"database/sql"
	"time"

	"gorm.io/gorm"
	"power-iot-backend/internal/core/billing"
)

type BillingPlanProjection struct {
	Code       string  `gorm:"column:plan_code"`
	Tariff     string  `gorm:"column:electricity_tariff"`
	UsageClass *string `gorm:"column:usage_class"`
	Method     string  `gorm:"column:billing_method"`
	Calculator string  `gorm:"column:calculator_kind"`
}

type BillingAssignmentProjection struct {
	PlanCode  string
	ValidFrom time.Time
	ValidTo   *time.Time
}

type BillingConfigurationProjection struct {
	ShopID            uint
	ElectricityTariff *string
	Supported         bool
	Plans             []BillingPlanProjection
	Current           *BillingAssignmentProjection
	Scheduled         *BillingAssignmentProjection
}

type BillingConfigurationRepository struct{ db *gorm.DB }

func NewBillingConfigurationRepository(db *gorm.DB) *BillingConfigurationRepository {
	return &BillingConfigurationRepository{db: db}
}

func (r *BillingConfigurationRepository) FindBillingConfiguration(ctx context.Context, userID, shopID uint, asOf time.Time) (BillingConfigurationProjection, error) {
	if r == nil || r.db == nil || userID == 0 || shopID == 0 {
		return BillingConfigurationProjection{}, gorm.ErrRecordNotFound
	}
	var row struct {
		ID     uint
		Tariff sql.NullString `gorm:"column:electricity_tariff"`
	}
	result := r.db.WithContext(queryContext(ctx)).Raw(`
SELECT s.id, s.electricity_tariff
FROM shops AS s
JOIN user_shop_relations AS relation ON relation.shop_id = s.id AND relation.user_id = ?
WHERE s.id = ? AND s.is_active = TRUE`, userID, shopID).Scan(&row)
	if result.Error != nil {
		return BillingConfigurationProjection{}, result.Error
	}
	if result.RowsAffected != 1 {
		return BillingConfigurationProjection{}, gorm.ErrRecordNotFound
	}
	var tariff *string
	if row.Tariff.Valid {
		value := row.Tariff.String
		tariff = &value
	}
	out := BillingConfigurationProjection{ShopID: row.ID, ElectricityTariff: tariff, Supported: tariff != nil && (billing.PlanTariff(billing.PlanCommercialNonTOU) == *tariff || billing.PlanTariff(billing.PlanNoncommercialResidentialNonTOU) == *tariff)}
	if out.Supported {
		plans := make([]BillingPlanProjection, 0)
		if err := r.db.WithContext(queryContext(ctx)).Raw(`
SELECT plan_code, electricity_tariff, usage_class, billing_method, calculator_kind
FROM electricity_tariff_plans
WHERE electricity_tariff = ?
ORDER BY plan_code`, *tariff).Scan(&plans).Error; err != nil {
			return BillingConfigurationProjection{}, err
		}
		out.Plans = plans
	}
	local := asOf.In(mustBusinessLocation())
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, mustBusinessLocation())
	var assignments []struct {
		PlanCode  string       `gorm:"column:plan_code"`
		ValidFrom time.Time    `gorm:"column:valid_from"`
		ValidTo   sql.NullTime `gorm:"column:valid_to"`
	}
	if err := r.db.WithContext(queryContext(ctx)).Raw(`
SELECT p.plan_code, a.valid_from, a.valid_to
FROM shop_billing_assignments AS a
JOIN electricity_tariff_plans AS p ON p.id = a.tariff_plan_id
WHERE a.shop_id = ?
  AND (a.valid_to IS NULL OR a.valid_to >= ?)
ORDER BY a.valid_from ASC`, shopID, day).Scan(&assignments).Error; err != nil {
		return BillingConfigurationProjection{}, err
	}
	for _, assignment := range assignments {
		from := assignment.ValidFrom.In(mustBusinessLocation())
		to := nullableTime(assignment.ValidTo)
		item := &BillingAssignmentProjection{PlanCode: assignment.PlanCode, ValidFrom: from}
		if to != nil {
			item.ValidTo = to
		}
		if !from.After(day) && (to == nil || day.Before(*to)) && out.Current == nil {
			out.Current = item
			continue
		}
		if from.After(day) && out.Scheduled == nil {
			out.Scheduled = item
		}
	}
	return out, nil
}

func (r *BillingConfigurationRepository) SetBillingPlan(ctx context.Context, actorID, shopID uint, planCode string, effectiveFrom time.Time) error {
	if r == nil || r.db == nil {
		return gorm.ErrInvalidDB
	}
	return r.db.WithContext(queryContext(ctx)).Transaction(func(tx *gorm.DB) error {
		var shop struct {
			ID     uint
			Tariff sql.NullString `gorm:"column:electricity_tariff"`
		}
		if err := tx.Raw(`
SELECT s.id, s.electricity_tariff
FROM shops AS s
JOIN users AS u ON u.id = ? AND u.is_admin = TRUE
JOIN user_shop_relations AS relation ON relation.user_id = u.id AND relation.shop_id = s.id
WHERE s.id = ? AND s.is_active = TRUE
FOR UPDATE OF s`, actorID, shopID).Scan(&shop).Error; err != nil {
			return err
		}
		if shop.ID == 0 {
			return gorm.ErrRecordNotFound
		}
		var plan struct {
			ID     uint64
			Tariff string `gorm:"column:electricity_tariff"`
		}
		if err := tx.Raw(`SELECT id, electricity_tariff FROM electricity_tariff_plans WHERE plan_code = ?`, planCode).Scan(&plan).Error; err != nil {
			return err
		}
		if plan.ID == 0 {
			return billing.ErrUnsupportedBillingPlan
		}
		if !shop.Tariff.Valid || !billing.CompatiblePlan(shop.Tariff.String, planCode) || shop.Tariff.String != plan.Tariff {
			return billing.ErrBillingTariffMismatch
		}
		date := effectiveFrom.In(mustBusinessLocation())
		start := time.Date(date.Year(), date.Month(), 1, 0, 0, 0, 0, mustBusinessLocation())
		var current struct {
			ID        string
			PlanCode  string
			ValidFrom time.Time
			ValidTo   sql.NullTime
		}
		query := tx.Raw(`
SELECT a.id, p.plan_code, a.valid_from, a.valid_to
FROM shop_billing_assignments a
JOIN electricity_tariff_plans p ON p.id = a.tariff_plan_id
WHERE a.shop_id = ? AND a.valid_from <= ?
  AND (a.valid_to IS NULL OR ? < a.valid_to)
ORDER BY a.valid_from DESC
LIMIT 1`, shopID, start, start).Scan(&current)
		if query.Error != nil {
			return query.Error
		}
		if query.RowsAffected == 1 && !current.ValidFrom.Before(start) {
			if current.PlanCode == planCode {
				return nil
			}
			return tx.Exec(`UPDATE shop_billing_assignments SET tariff_plan_id = ?, created_by_user_id = ?, updated_at = now() WHERE id = ?`, plan.ID, actorID, current.ID).Error
		}
		if query.RowsAffected == 1 {
			if current.PlanCode == planCode {
				return nil
			}
			if err := tx.Exec(`UPDATE shop_billing_assignments SET valid_to = ?, updated_at = now() WHERE id = ?`, start, current.ID).Error; err != nil {
				return err
			}
		}
		if err := tx.Exec(`
INSERT INTO shop_billing_assignments (shop_id, tariff_plan_id, valid_from, created_by_user_id)
VALUES (?, ?, ?, ?)`, shopID, plan.ID, start, actorID).Error; err != nil {
			return err
		}
		return nil
	})
}

func (r *BillingConfigurationRepository) HasBillingHistory(ctx context.Context, shopID uint) (bool, error) {
	if r == nil || r.db == nil || shopID == 0 {
		return false, gorm.ErrInvalidDB
	}
	var count int64
	if err := r.db.WithContext(queryContext(ctx)).Raw(`SELECT count(*) FROM shop_billing_assignments WHERE shop_id = ?`, shopID).Scan(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func nullableTime(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	out := value.Time.In(mustBusinessLocation())
	return &out
}

func mustBusinessLocation() *time.Location {
	location, err := time.LoadLocation("Asia/Taipei")
	if err != nil {
		panic(err)
	}
	return location
}
