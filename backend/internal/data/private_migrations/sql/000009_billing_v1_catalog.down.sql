DROP TABLE IF EXISTS shop_billing_assignments;
DROP TABLE IF EXISTS electricity_rate_tiers;
DROP TABLE IF EXISTS electricity_rate_plans;
DROP TABLE IF EXISTS electricity_tariff_plans;
DROP TABLE IF EXISTS electricity_rate_sets;
DROP FUNCTION IF EXISTS billing_rate_child_insert_guard();
DROP FUNCTION IF EXISTS billing_rate_children_immutable();
DROP FUNCTION IF EXISTS billing_rate_set_immutable();
