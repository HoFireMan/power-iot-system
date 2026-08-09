DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM system_configs LIMIT 1)
       OR EXISTS (SELECT 1 FROM clients LIMIT 1)
       OR EXISTS (SELECT 1 FROM shops LIMIT 1)
       OR EXISTS (SELECT 1 FROM users LIMIT 1)
       OR EXISTS (SELECT 1 FROM user_shop_relations LIMIT 1)
       OR EXISTS (SELECT 1 FROM devices LIMIT 1)
       OR EXISTS (SELECT 1 FROM measurement_points LIMIT 1)
       OR EXISTS (SELECT 1 FROM device_assignments LIMIT 1)
       OR EXISTS (SELECT 1 FROM telemetry_ingest_keys LIMIT 1)
       OR EXISTS (SELECT 1 FROM power_readings LIMIT 1)
       OR EXISTS (SELECT 1 FROM alert_logs LIMIT 1)
       OR EXISTS (SELECT 1 FROM daily_usages LIMIT 1) THEN
        RAISE EXCEPTION 'refusing initial schema rollback while application data exists';
    END IF;
END
$$;

DROP TABLE IF EXISTS daily_usages;
DROP TABLE IF EXISTS alert_logs;
DROP TABLE IF EXISTS power_readings;
DROP TABLE IF EXISTS telemetry_ingest_keys;
DROP TABLE IF EXISTS device_assignments;
DROP TABLE IF EXISTS measurement_points;
DROP TABLE IF EXISTS device_alert_settings;
DROP TABLE IF EXISTS devices;
DROP TABLE IF EXISTS device_types;
DROP TABLE IF EXISTS user_shop_relations;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS shops;
DROP TABLE IF EXISTS clients;
DROP TABLE IF EXISTS system_configs;

DROP EXTENSION IF EXISTS btree_gist;
DROP EXTENSION IF EXISTS timescaledb;
DROP EXTENSION IF EXISTS pgcrypto;
