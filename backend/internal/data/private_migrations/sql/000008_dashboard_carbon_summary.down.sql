-- Dashboard Carbon Summary rollback. No telemetry or historical energy rows
-- are modified.
DROP TABLE IF EXISTS carbon_factor_rates;
DROP TABLE IF EXISTS carbon_factor_sets;
ALTER TABLE shops DROP CONSTRAINT IF EXISTS shops_electricity_tariff_check;
ALTER TABLE shops DROP COLUMN IF EXISTS electricity_tariff;
