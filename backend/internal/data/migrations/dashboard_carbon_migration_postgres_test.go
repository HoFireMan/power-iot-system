package migrations

import (
	"context"
	"database/sql"
	"io/fs"
	"testing"

	_ "github.com/lib/pq"
)

func dashboardCarbonSQL(t *testing.T, name string) string {
	t.Helper()
	body, err := fs.ReadFile(Files, "sql/"+name)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func openDashboardCarbonDB(t *testing.T, databaseURL string) *sql.DB {
	t.Helper()
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestDashboardCarbonMigrationUpDownUpAndFactorAuthority(t *testing.T) {
	database := newB02Database(t)
	migrateB02ForTest(t, database.DSN())
	db := openDashboardCarbonDB(t, database.DSN())

	var activeCount int
	if err := db.QueryRow(`SELECT count(*) FROM carbon_factor_sets WHERE is_active`).Scan(&activeCount); err != nil {
		t.Fatal(err)
	}
	if activeCount != 1 {
		t.Fatalf("active factor sets=%d, want 1", activeCount)
	}
	var year, roc int
	var organization, document, sourceURL, unit string
	if err := db.QueryRow(`SELECT version_year, roc_year, organization, document_title, source_url, unit
		FROM carbon_factor_sets WHERE is_active`).Scan(&year, &roc, &organization, &document, &sourceURL, &unit); err != nil {
		t.Fatal(err)
	}
	if year != 2025 || roc != 114 || organization != "經濟部能源署" || document != "114年度電力排碳係數" || sourceURL != "https://www.moeaea.gov.tw/ecw/populace/content/ContentDesc.aspx?menu_id=27028" || unit != "kgCO2e/kWh" {
		t.Fatalf("factor metadata=%d/%d/%s/%s/%s/%s", year, roc, organization, document, sourceURL, unit)
	}
	var rates int
	if err := db.QueryRow(`SELECT count(*) FROM carbon_factor_rates WHERE set_id=(SELECT id FROM carbon_factor_sets WHERE is_active)`).Scan(&rates); err != nil {
		t.Fatal(err)
	}
	if rates != 6 {
		t.Fatalf("active rates=%d, want 6", rates)
	}
	var low, high string
	if err := db.QueryRow(`SELECT factor_kgco2e_per_kwh::text, (SELECT factor_kgco2e_per_kwh::text FROM carbon_factor_rates WHERE set_id=carbon_factor_rates.set_id AND tariff_code='HIGH_VOLTAGE')
		FROM carbon_factor_rates WHERE tariff_code='LOW_VOLTAGE' AND set_id=(SELECT id FROM carbon_factor_sets WHERE is_active)`).Scan(&low, &high); err != nil {
		t.Fatal(err)
	}
	if low != "0.466000" || high != "0.466000" {
		t.Fatalf("2025 rates low/high=%s/%s", low, high)
	}
	if _, err := db.Exec(`INSERT INTO carbon_factor_sets
		(organization, document_title, source_url, version_year, roc_year, unit, is_active)
		VALUES ('future', 'future', 'https://example.invalid', 2026, 115, 'kgCO2e/kWh', true)`); err == nil {
		t.Fatal("second active factor set was accepted")
	}
	if _, err := db.Exec(`INSERT INTO carbon_factor_sets
		(organization, document_title, source_url, version_year, roc_year, unit, is_active)
		VALUES ('future', 'future', 'https://example.invalid', 2026, 115, 'kgCO2e/kWh', false)`); err != nil {
		t.Fatal(err)
	}
	var shopColumn bool
	if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='shops' AND column_name='electricity_tariff')`).Scan(&shopColumn); err != nil || !shopColumn {
		t.Fatalf("shop tariff column exists=%t err=%v", shopColumn, err)
	}

	if _, err := db.Exec(dashboardCarbonSQL(t, "000008_dashboard_carbon_summary.down.sql")); err != nil {
		t.Fatal(err)
	}
	var relation sql.NullString
	if err := db.QueryRow(`SELECT to_regclass('carbon_factor_sets')`).Scan(&relation); err != nil {
		t.Fatal(err)
	}
	if relation.Valid {
		t.Fatalf("carbon_factor_sets survived DOWN: %s", relation.String)
	}

	if _, err := db.Exec(dashboardCarbonSQL(t, "000008_dashboard_carbon_summary.up.sql")); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM carbon_factor_rates WHERE set_id=(SELECT id FROM carbon_factor_sets WHERE is_active)`).Scan(&rates); err != nil {
		t.Fatal(err)
	}
	if rates != 6 {
		t.Fatalf("re-UP active rates=%d, want 6", rates)
	}

	if _, err := db.Exec(dashboardCarbonSQL(t, "000008_dashboard_carbon_summary.down.sql")); err != nil {
		t.Fatal(err)
	}
	if err := RunDashboardCarbonMigration(context.Background(), database.DSN(), b02TestAdmission()); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM carbon_factor_rates WHERE set_id=(SELECT id FROM carbon_factor_sets WHERE is_active)`).Scan(&rates); err != nil {
		t.Fatal(err)
	}
	if rates != 6 {
		t.Fatalf("protected re-application active rates=%d, want 6", rates)
	}

}
