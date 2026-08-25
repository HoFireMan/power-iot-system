package persistence

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"gorm.io/gorm"

	corebilling "power-iot-backend/internal/core/billing"
	corebillingenergy "power-iot-backend/internal/core/billingenergy"
	coreestimate "power-iot-backend/internal/core/billingestimate"
)

type BillingRateTierProjection struct {
	LowerKwh   string
	UpperKwh   *string
	RatePerKwh string
}

type BillingEstimateProjection struct {
	ShopID               uint
	ShopCode             string
	ShopName             string
	ElectricityTariff    string
	PlanCode             string
	UsageClass           *string
	RateSetVersion       string
	Currency             string
	IncludesTax          bool
	Season               string
	MinimumMonthlyCharge string
	Tiers                []BillingRateTierProjection
	Energy               corebillingenergy.Facts
}

type BillingEstimateQueryRepository struct{ db *gorm.DB }

func NewBillingEstimateQueryRepository(db *gorm.DB) *BillingEstimateQueryRepository {
	return &BillingEstimateQueryRepository{db: db}
}

func (r *BillingEstimateQueryRepository) FindBillingEstimate(ctx context.Context, userID, shopID uint, month corebillingenergy.BillingMonth, now func() time.Time) (BillingEstimateProjection, error) {
	if r == nil || r.db == nil || userID == 0 || shopID == 0 || now == nil {
		return BillingEstimateProjection{}, coreestimate.ErrEstimateAccess
	}
	if ctx == nil {
		ctx = context.Background()
	}
	database, err := r.db.DB()
	if err != nil {
		return BillingEstimateProjection{}, err
	}
	tx, err := database.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return BillingEstimateProjection{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	var databaseSnapshot time.Time
	if err := tx.QueryRowContext(ctx, "SELECT transaction_timestamp()").Scan(&databaseSnapshot); err != nil {
		return BillingEstimateProjection{}, err
	}
	snapshot := now().UTC()
	period, err := month.Period(snapshot)
	if errors.Is(err, corebillingenergy.ErrFutureBillingMonth) {
		return BillingEstimateProjection{}, coreestimate.ErrUnsupportedPeriod
	}
	if err != nil {
		return BillingEstimateProjection{}, err
	}
	projection, err := loadEstimateCatalog(ctx, tx, userID, shopID, period.Start, period.End)
	if err != nil {
		return BillingEstimateProjection{}, err
	}
	energy, err := findBillingEnergyInTx(ctx, tx, userID, shopID, month, snapshot)
	if err != nil {
		return BillingEstimateProjection{}, err
	}
	projection.Energy = energy
	if err := tx.Commit(); err != nil {
		return BillingEstimateProjection{}, err
	}
	committed = true
	return projection, nil
}

func loadEstimateCatalog(ctx context.Context, tx *sql.Tx, userID, shopID uint, periodStart, periodEnd time.Time) (BillingEstimateProjection, error) {
	location, err := time.LoadLocation(corebillingenergy.BusinessTimezone)
	if err != nil {
		return BillingEstimateProjection{}, err
	}
	startDate := periodStart.In(location).Format("2006-01-02")
	endDate := periodEnd.In(location).Format("2006-01-02")
	var shop struct {
		ID     uint
		Code   string
		Name   string
		Tariff sql.NullString
	}
	if err := tx.QueryRowContext(ctx, `
SELECT s.id, s.code, s.name, s.electricity_tariff
FROM shops AS s
JOIN user_shop_relations AS relation
  ON relation.shop_id = s.id AND relation.user_id = $1
WHERE s.id = $2 AND s.is_active = TRUE`, userID, shopID).Scan(&shop.ID, &shop.Code, &shop.Name, &shop.Tariff); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return BillingEstimateProjection{}, coreestimate.ErrEstimateAccess
		}
		return BillingEstimateProjection{}, err
	}
	if !shop.Tariff.Valid || len(corebilling.SupportedPlansForTariff(shop.Tariff.String)) == 0 {
		return BillingEstimateProjection{}, coreestimate.ErrUnsupportedTariff
	}
	plan, err := selectEstimatePlan(ctx, tx, shopID, startDate, endDate)
	if err != nil {
		return BillingEstimateProjection{}, err
	}
	if !corebilling.CompatiblePlan(shop.Tariff.String, plan.code) {
		return BillingEstimateProjection{}, coreestimate.ErrUnsupportedTariff
	}
	if plan.method != corebilling.BillingMethodNonTOU || plan.calculator != corebilling.CalculatorProgressiveNonTOU || !corebilling.IsSupportedPlan(plan.code) {
		return BillingEstimateProjection{}, coreestimate.ErrCatalogInvariant
	}
	expectedUsageClass := corebilling.PlanUsageClass(plan.code)
	if (expectedUsageClass == nil) != (plan.usageClass == nil) || expectedUsageClass != nil && *expectedUsageClass != *plan.usageClass {
		return BillingEstimateProjection{}, coreestimate.ErrCatalogInvariant
	}
	rateSet, err := selectEstimateRateSet(ctx, tx, startDate, endDate)
	if err != nil {
		return BillingEstimateProjection{}, err
	}
	var ratePlan struct {
		ID      int64
		Minimum string
	}
	if err := tx.QueryRowContext(ctx, `
SELECT rp.id, rp.minimum_monthly_charge::text
FROM electricity_rate_plans AS rp
JOIN electricity_tariff_plans AS tp ON tp.id = rp.tariff_plan_id
WHERE rp.rate_set_id = $1 AND tp.plan_code = $2`, rateSet.id, plan.code).Scan(&ratePlan.ID, &ratePlan.Minimum); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return BillingEstimateProjection{}, coreestimate.ErrCatalogInvariant
		}
		return BillingEstimateProjection{}, err
	}
	tiers, err := loadEstimateTiers(ctx, tx, ratePlan.ID, coreestimate.SeasonForMonth(periodStart.In(location).Month()))
	if err != nil {
		return BillingEstimateProjection{}, err
	}
	return BillingEstimateProjection{
		ShopID: shop.ID, ShopCode: shop.Code, ShopName: shop.Name,
		ElectricityTariff: shop.Tariff.String, PlanCode: plan.code, UsageClass: plan.usageClass,
		RateSetVersion: rateSet.version, Currency: rateSet.currency, IncludesTax: rateSet.includesTax,
		Season:               coreestimate.SeasonForMonth(periodStart.In(location).Month()),
		MinimumMonthlyCharge: ratePlan.Minimum, Tiers: tiers,
	}, nil
}

type selectedEstimatePlan struct {
	code       string
	usageClass *string
	method     string
	calculator string
	from       string
	to         sql.NullString
}

func selectEstimatePlan(ctx context.Context, tx *sql.Tx, shopID uint, startDate, endDate string) (selectedEstimatePlan, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT p.plan_code, p.usage_class, p.billing_method, p.calculator_kind, a.valid_from::text, a.valid_to::text
FROM shop_billing_assignments AS a
JOIN electricity_tariff_plans AS p ON p.id = a.tariff_plan_id
WHERE a.shop_id = $1
  AND a.valid_from < $3::date
  AND (a.valid_to IS NULL OR a.valid_to > $2::date)
ORDER BY a.valid_from, a.id`, shopID, startDate, endDate)
	if err != nil {
		return selectedEstimatePlan{}, err
	}
	defer rows.Close()
	var plans []selectedEstimatePlan
	for rows.Next() {
		var plan selectedEstimatePlan
		var usage sql.NullString
		if err := rows.Scan(&plan.code, &usage, &plan.method, &plan.calculator, &plan.from, &plan.to); err != nil {
			return selectedEstimatePlan{}, err
		}
		if usage.Valid {
			value := usage.String
			plan.usageClass = &value
		}
		plans = append(plans, plan)
	}
	if err := rows.Err(); err != nil {
		return selectedEstimatePlan{}, err
	}
	if len(plans) == 0 {
		return selectedEstimatePlan{}, coreestimate.ErrConfigurationRequired
	}
	if len(plans) != 1 || plans[0].from > startDate || (plans[0].to.Valid && plans[0].to.String < endDate) {
		return selectedEstimatePlan{}, coreestimate.ErrUnsupportedPeriod
	}
	return plans[0], nil
}

type selectedEstimateRateSet struct {
	id          int64
	version     string
	currency    string
	includesTax bool
	from        string
	to          sql.NullString
}

func selectEstimateRateSet(ctx context.Context, tx *sql.Tx, startDate, endDate string) (selectedEstimateRateSet, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT id, version_code, currency, includes_tax, effective_from::text, effective_to::text
FROM electricity_rate_sets
WHERE status = 'AUTHORITATIVE'
  AND effective_from < $2::date
  AND (effective_to IS NULL OR effective_to > $1::date)
ORDER BY effective_from, id`, startDate, endDate)
	if err != nil {
		return selectedEstimateRateSet{}, err
	}
	defer rows.Close()
	var sets []selectedEstimateRateSet
	for rows.Next() {
		var set selectedEstimateRateSet
		if err := rows.Scan(&set.id, &set.version, &set.currency, &set.includesTax, &set.from, &set.to); err != nil {
			return selectedEstimateRateSet{}, err
		}
		sets = append(sets, set)
	}
	if err := rows.Err(); err != nil {
		return selectedEstimateRateSet{}, err
	}
	if len(sets) == 0 {
		return selectedEstimateRateSet{}, coreestimate.ErrRateNotFound
	}
	if len(sets) != 1 || sets[0].from > startDate || (sets[0].to.Valid && sets[0].to.String < endDate) {
		return selectedEstimateRateSet{}, coreestimate.ErrUnsupportedPeriod
	}
	return sets[0], nil
}

func loadEstimateTiers(ctx context.Context, tx *sql.Tx, ratePlanID int64, season string) ([]BillingRateTierProjection, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT lower_kwh::text, upper_kwh::text, rate_per_kwh::text
FROM electricity_rate_tiers
WHERE rate_plan_id = $1 AND season = $2
ORDER BY tier_order`, ratePlanID, season)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tiers []BillingRateTierProjection
	for rows.Next() {
		var tier BillingRateTierProjection
		var upper sql.NullString
		if err := rows.Scan(&tier.LowerKwh, &upper, &tier.RatePerKwh); err != nil {
			return nil, err
		}
		if upper.Valid {
			tier.UpperKwh = &upper.String
		}
		tiers = append(tiers, tier)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(tiers) == 0 {
		return nil, coreestimate.ErrCatalogInvariant
	}
	return tiers, nil
}
