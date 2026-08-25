package migrations

import (
	"context"
	"database/sql"
	"io/fs"
	"strconv"
	"testing"
)

func billingV1SQL(t *testing.T, name string) string {
	t.Helper()
	body, err := fs.ReadFile(Files, "sql/"+name)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestBillingV1MigrationSeedProtectionAndRoundTrip(t *testing.T) {
	database := newB02Database(t)
	before, err := sql.Open("postgres", database.DSN())
	if err != nil {
		t.Fatal(err)
	}
	var existingClientID, existingShopID int64
	if err := before.QueryRow(`INSERT INTO clients (name, code) VALUES ('Existing Billing Client', 'existing-billing-client') RETURNING id`).Scan(&existingClientID); err != nil {
		t.Fatal(err)
	}
	if err := before.QueryRow(`INSERT INTO shops (client_id, code, name) VALUES ($1, 'existing-billing-shop', 'Existing Billing Shop') RETURNING id`, existingClientID).Scan(&existingShopID); err != nil {
		t.Fatal(err)
	}
	if err := before.Close(); err != nil {
		t.Fatal(err)
	}
	migrateB02ForTest(t, database.DSN())
	db, err := sql.Open("postgres", database.DSN())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var sets, plans, ratePlans, tiers, assignments int
	if err := db.QueryRow(`SELECT count(*) FROM electricity_rate_sets`).Scan(&sets); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM electricity_tariff_plans`).Scan(&plans); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM electricity_rate_plans`).Scan(&ratePlans); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM electricity_rate_tiers`).Scan(&tiers); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM shop_billing_assignments`).Scan(&assignments); err != nil {
		t.Fatal(err)
	}
	if sets != 1 || plans != 3 || ratePlans != 3 || tiers != 34 || assignments != 0 {
		t.Fatalf("seed counts sets=%d plans=%d ratePlans=%d tiers=%d assignments=%d", sets, plans, ratePlans, tiers, assignments)
	}
	var preservedShopCode, preservedTariff sql.NullString
	if err := db.QueryRow(`SELECT code, electricity_tariff FROM shops WHERE id = $1`, existingShopID).Scan(&preservedShopCode, &preservedTariff); err != nil {
		t.Fatal(err)
	}
	if preservedShopCode.String != "existing-billing-shop" || preservedTariff.Valid {
		t.Fatalf("existing Shop changed code=%q tariff_valid=%t", preservedShopCode.String, preservedTariff.Valid)
	}
	var provider, version, organization, document, approval, currency, status string
	var effectiveFrom string
	var effectiveTo sql.NullString
	var includesTax bool
	if err := db.QueryRow(`SELECT provider, version_code, effective_from::text, effective_to::text,
source_organization, source_document, approval_reference, currency, includes_tax, status
FROM electricity_rate_sets`).Scan(&provider, &version, &effectiveFrom, &effectiveTo, &organization, &document, &approval, &currency, &includesTax, &status); err != nil {
		t.Fatal(err)
	}
	if provider != "TAIPOWER" || version != "TAIPOWER_2025_10_01" || effectiveFrom != "2025-10-01" || effectiveTo.Valid || organization != "台灣電力公司 / 經濟部核定" || document != "電價表" || approval != "114年9月26日經能字第11458000380號函" || currency != "TWD" || !includesTax || status != "AUTHORITATIVE" {
		t.Fatalf("rate set metadata=%s/%s/%s/%t/%s/%s/%s/%s/%t/%s", provider, version, effectiveFrom, effectiveTo.Valid, organization, document, approval, currency, includesTax, status)
	}
	var minimums int
	if err := db.QueryRow(`SELECT count(*) FROM electricity_rate_plans WHERE minimum_monthly_charge = 100.000000`).Scan(&minimums); err != nil {
		t.Fatal(err)
	}
	if minimums != 3 {
		t.Fatalf("minimum charges=%d", minimums)
	}

	expected := map[string]string{}
	for _, item := range []struct {
		plan, season string
		rates        []string
	}{
		{"LIGHTING_COMMERCIAL_NON_TOU", "SUMMER", []string{"2.710000", "3.760000", "4.460000", "7.080000", "7.430000"}},
		{"LIGHTING_COMMERCIAL_NON_TOU", "NON_SUMMER", []string{"2.280000", "3.100000", "3.610000", "5.560000", "5.830000"}},
		{"LIGHTING_NONCOMMERCIAL_RESIDENTIAL_NON_TOU", "SUMMER", []string{"1.780000", "2.550000", "3.800000", "5.140000", "6.440000", "8.860000"}},
		{"LIGHTING_NONCOMMERCIAL_RESIDENTIAL_NON_TOU", "NON_SUMMER", []string{"1.780000", "2.260000", "3.130000", "4.240000", "5.270000", "7.030000"}},
		{"LIGHTING_NONCOMMERCIAL_NONRESIDENTIAL_NON_TOU", "SUMMER", []string{"1.780000", "2.550000", "3.800000", "5.140000", "6.440000", "8.860000"}},
		{"LIGHTING_NONCOMMERCIAL_NONRESIDENTIAL_NON_TOU", "NON_SUMMER", []string{"1.780000", "2.260000", "3.130000", "4.240000", "5.270000", "7.030000"}},
	} {
		for index, rate := range item.rates {
			expected[item.plan+"|"+item.season+"|"+strconv.Itoa(index+1)] = rate
		}
	}
	rows, err := db.Query(`SELECT p.plan_code, t.season, t.tier_order, t.lower_kwh::text, COALESCE(t.upper_kwh::text, ''), t.rate_per_kwh::text
FROM electricity_rate_tiers t JOIN electricity_rate_plans rp ON rp.id=t.rate_plan_id
JOIN electricity_tariff_plans p ON p.id=rp.tariff_plan_id ORDER BY p.plan_code, t.season, t.tier_order`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	previousUpper := map[string]string{}
	for rows.Next() {
		var plan, season, lower, upper, rate string
		var order int
		if err := rows.Scan(&plan, &season, &order, &lower, &upper, &rate); err != nil {
			t.Fatal(err)
		}
		key := plan + "|" + season + "|" + strconv.Itoa(order)
		if expected[key] != rate {
			t.Fatalf("tier %s rate=%s want=%s", key, rate, expected[key])
		}
		if lower == "" || plan == "" || season == "" || order <= 0 {
			t.Fatalf("invalid tier seed %s %s %d %s %s %s", plan, season, order, lower, upper, rate)
		}
		series := plan + "|" + season
		if order == 1 && lower != "0.000000" {
			t.Fatalf("first tier lower=%s", lower)
		}
		if order > 1 && previousUpper[series] != lower {
			t.Fatalf("non-contiguous tier %s lower=%s previous=%s", key, lower, previousUpper[series])
		}
		previousUpper[series] = upper
		seen++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if seen != len(expected) {
		t.Fatalf("exact tier rows=%d want=%d", seen, len(expected))
	}
	for series, upper := range previousUpper {
		if upper != "" {
			t.Fatalf("final tier %s upper=%s", series, upper)
		}
	}

	if _, err := db.Exec(`UPDATE electricity_rate_sets SET source_document='changed' WHERE version_code='TAIPOWER_2025_10_01'`); err == nil {
		t.Fatal("authoritative rate set was mutable")
	}
	if _, err := db.Exec(`UPDATE electricity_rate_plans SET minimum_monthly_charge=101`); err == nil {
		t.Fatal("authoritative minimum charge was mutable")
	}
	if _, err := db.Exec(`UPDATE electricity_rate_tiers SET rate_per_kwh=99`); err == nil {
		t.Fatal("authoritative tier rate was mutable")
	}
	var authoritativeSetID, extraPlanID int64
	if err := db.QueryRow(`SELECT id FROM electricity_rate_sets WHERE version_code='TAIPOWER_2025_10_01'`).Scan(&authoritativeSetID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO electricity_tariff_plans
(plan_code, electricity_tariff, billing_method, calculator_kind)
VALUES ('BILLING_V1_IMMUTABILITY_PROBE', 'LIGHTING_COMMERCIAL', 'NON_TOU', 'PROGRESSIVE_NON_TOU')
RETURNING id`).Scan(&extraPlanID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO electricity_rate_plans (rate_set_id, tariff_plan_id, minimum_monthly_charge) VALUES ($1, $2, 100)`, authoritativeSetID, extraPlanID); err == nil {
		t.Fatal("authoritative rate plan insertion was accepted")
	}
	if _, err := db.Exec(`INSERT INTO electricity_rate_sets
(provider, version_code, effective_from, source_organization, source_document, approval_reference, currency, includes_tax, status)
VALUES ('TAIPOWER', 'TAIPOWER_OVERLAP', DATE '2025-09-01', 'x', 'x', 'x', 'TWD', true, 'AUTHORITATIVE')`); err == nil {
		t.Fatal("overlapping authoritative rate set was accepted")
	}
	if _, err := db.Exec(`UPDATE electricity_rate_sets SET effective_to=DATE '2026-01-01' WHERE version_code='TAIPOWER_2025_10_01'`); err != nil {
		t.Fatal("controlled rate-set closure: ", err)
	}
	if _, err := db.Exec(`INSERT INTO electricity_rate_sets
(provider, version_code, effective_from, source_organization, source_document, approval_reference, currency, includes_tax, status)
VALUES ('TAIPOWER', 'TAIPOWER_2026_01_01', DATE '2026-01-01', 'x', 'x', 'x', 'TWD', true, 'AUTHORITATIVE')`); err != nil {
		t.Fatal("successor rate set: ", err)
	}

	var clientID int64
	if err := db.QueryRow(`INSERT INTO clients (name, code) VALUES ('Billing V1 Seed Client', 'billing-v1-seed-client') RETURNING id`).Scan(&clientID); err != nil {
		t.Fatal(err)
	}
	var shopID int64
	if err := db.QueryRow(`INSERT INTO shops (client_id, code, name) VALUES ($1, 'billing-v1-seed', 'Billing V1 Seed') RETURNING id`, clientID).Scan(&shopID); err != nil {
		t.Fatal(err)
	}
	var planID int64
	if err := db.QueryRow(`SELECT id FROM electricity_tariff_plans WHERE plan_code='LIGHTING_COMMERCIAL_NON_TOU'`).Scan(&planID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO shop_billing_assignments (shop_id, tariff_plan_id, valid_from, valid_to) VALUES ($1,$2,DATE '2026-01-01',DATE '2026-02-01')`, shopID, planID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO shop_billing_assignments (shop_id, tariff_plan_id, valid_from) VALUES ($1,$2,DATE '2026-01-15')`, shopID, planID); err == nil {
		t.Fatal("overlapping billing assignments were accepted")
	}
	if _, err := db.Exec(`INSERT INTO shop_billing_assignments (shop_id, tariff_plan_id, valid_from) VALUES ($1,$2,DATE '2026-02-01')`, shopID, planID); err != nil {
		t.Fatal(err)
	}

	if err := RunBillingV1Migration(context.Background(), database.DSN(), b02TestAdmission()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(billingV1SQL(t, "000009_billing_v1_catalog.down.sql")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(billingV1SQL(t, "000009_billing_v1_catalog.up.sql")); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM electricity_rate_tiers`).Scan(&tiers); err != nil {
		t.Fatal(err)
	}
	if tiers != 34 {
		t.Fatalf("re-up tiers=%d", tiers)
	}
}
