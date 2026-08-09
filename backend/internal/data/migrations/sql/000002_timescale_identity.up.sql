-- Compatibility-first identity hardening. Existing installations may have
-- only the old GORM tables, so the target relational tables are created here
-- additively before their constraints are hardened.
CREATE TABLE IF NOT EXISTS measurement_points (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    shop_id BIGINT NOT NULL REFERENCES shops(id),
    name VARCHAR(100) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS device_assignments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    device_id BIGINT NOT NULL REFERENCES devices(id),
    measurement_point_id UUID NOT NULL REFERENCES measurement_points(id),
    valid_from TIMESTAMPTZ NOT NULL,
    valid_to TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT device_assignments_valid_range_check CHECK (valid_to IS NULL OR valid_to > valid_from)
);
CREATE TABLE IF NOT EXISTS telemetry_ingest_keys (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    device_id BIGINT NOT NULL REFERENCES devices(id),
    boot_counter BIGINT NOT NULL,
    sequence BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    received_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Existing MAC values are checked for invalid formats and canonical collisions
-- before any rewrite. A collision aborts the migration; no row is deleted or
-- selected automatically.
ALTER TABLE devices ADD COLUMN IF NOT EXISTS type_id BIGINT NOT NULL DEFAULT 0;
ALTER TABLE devices ADD COLUMN IF NOT EXISTS serial_number VARCHAR(128);
ALTER TABLE devices ADD COLUMN IF NOT EXISTS location VARCHAR(100);
ALTER TABLE devices ADD COLUMN IF NOT EXISTS memo TEXT;
ALTER TABLE devices ADD COLUMN IF NOT EXISTS is_online BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE devices ADD COLUMN IF NOT EXISTS last_seen TIMESTAMPTZ;
ALTER TABLE devices ADD COLUMN IF NOT EXISTS boot_id VARCHAR(80);
ALTER TABLE devices ADD COLUMN IF NOT EXISTS firmware_version VARCHAR(80);
ALTER TABLE devices ADD COLUMN IF NOT EXISTS ip_address VARCHAR(45);
ALTER TABLE devices ADD COLUMN IF NOT EXISTS rssi INTEGER;
ALTER TABLE devices ADD COLUMN IF NOT EXISTS queue_count INTEGER;
ALTER TABLE devices ADD COLUMN IF NOT EXISTS safe_mode BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE devices ADD COLUMN IF NOT EXISTS time_synced BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE devices ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT now();

DO $$
DECLARE
    invalid_count BIGINT;
    collision_count BIGINT;
BEGIN
    SELECT count(*) INTO invalid_count
    FROM devices
    WHERE mac_address IS NULL
       OR mac_address !~* '^[0-9a-f]{2}([:-]?[0-9a-f]{2}){5}$';
    IF invalid_count > 0 THEN
        RAISE EXCEPTION 'cannot canonicalize devices.mac_address: % invalid value(s) found; migration stopped without rewriting data', invalid_count;
    END IF;

    SELECT count(*) INTO collision_count
    FROM (
        SELECT upper(replace(replace(mac_address, ':', ''), '-', '')) AS canonical_mac
        FROM devices
        GROUP BY upper(replace(replace(mac_address, ':', ''), '-', ''))
        HAVING count(*) > 1
    ) collisions;
    IF collision_count > 0 THEN
        RAISE EXCEPTION 'cannot canonicalize devices.mac_address: % canonical collision group(s) found; migration stopped without deleting data', collision_count;
    END IF;

    UPDATE devices
       SET mac_address = upper(replace(replace(mac_address, ':', ''), '-', ''))
     WHERE mac_address <> upper(replace(replace(mac_address, ':', ''), '-', ''));
END
$$;

ALTER TABLE devices
    DROP CONSTRAINT IF EXISTS devices_mac_canonical_check;
ALTER TABLE devices
    ADD CONSTRAINT devices_mac_canonical_check
    CHECK (mac_address ~ '^[0-9A-F]{12}$');
CREATE UNIQUE INDEX IF NOT EXISTS devices_mac_address_canonical_key
    ON devices (mac_address);
CREATE UNIQUE INDEX IF NOT EXISTS devices_serial_number_key
    ON devices (serial_number)
    WHERE serial_number IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS telemetry_ingest_keys_identity_key
    ON telemetry_ingest_keys (device_id, boot_counter, "sequence");

CREATE INDEX IF NOT EXISTS idx_device_assignments_device_history
    ON device_assignments (device_id, valid_from, valid_to);
CREATE INDEX IF NOT EXISTS idx_device_assignments_measurement_point_history
    ON device_assignments (measurement_point_id, valid_from, valid_to);

ALTER TABLE device_assignments
    DROP CONSTRAINT IF EXISTS device_assignments_device_no_overlap;
ALTER TABLE device_assignments
    ADD CONSTRAINT device_assignments_device_no_overlap
    EXCLUDE USING gist (
        device_id WITH =,
        tstzrange(valid_from, COALESCE(valid_to, 'infinity'::timestamptz), '[)') WITH &&
    );
ALTER TABLE device_assignments
    DROP CONSTRAINT IF EXISTS device_assignments_measurement_point_no_overlap;
ALTER TABLE device_assignments
    ADD CONSTRAINT device_assignments_measurement_point_no_overlap
    EXCLUDE USING gist (
        measurement_point_id WITH =,
        tstzrange(valid_from, COALESCE(valid_to, 'infinity'::timestamptz), '[)') WITH &&
    );

-- Legacy installations have Time but not the architecture's two explicit
-- timestamps. recorded_at is recoverable from Time; received_at is left
-- nullable for legacy rows because receive time was not recorded truthfully.
ALTER TABLE power_readings ADD COLUMN IF NOT EXISTS time TIMESTAMPTZ;
ALTER TABLE power_readings ADD COLUMN IF NOT EXISTS recorded_at TIMESTAMPTZ;
ALTER TABLE power_readings ADD COLUMN IF NOT EXISTS received_at TIMESTAMPTZ;
ALTER TABLE power_readings ADD COLUMN IF NOT EXISTS measurement_point_id UUID;
ALTER TABLE power_readings ADD COLUMN IF NOT EXISTS active_power NUMERIC(8,2);
ALTER TABLE power_readings ADD COLUMN IF NOT EXISTS firmware_version VARCHAR(80);
ALTER TABLE power_readings ADD COLUMN IF NOT EXISTS fw VARCHAR(80);
ALTER TABLE power_readings ADD COLUMN IF NOT EXISTS energy_delta_kwh NUMERIC(10,6);
ALTER TABLE power_readings ADD COLUMN IF NOT EXISTS power_factor NUMERIC(5,4);
ALTER TABLE power_readings ADD COLUMN IF NOT EXISTS rssi INTEGER;
ALTER TABLE power_readings ADD COLUMN IF NOT EXISTS valid_samples INTEGER;
ALTER TABLE power_readings ADD COLUMN IF NOT EXISTS invalid_samples INTEGER;
ALTER TABLE power_readings ADD COLUMN IF NOT EXISTS protocol_version INTEGER NOT NULL DEFAULT 0;
ALTER TABLE power_readings ADD COLUMN IF NOT EXISTS boot_id VARCHAR(80);
ALTER TABLE power_readings ADD COLUMN IF NOT EXISTS boot_counter BIGINT;
ALTER TABLE power_readings ADD COLUMN IF NOT EXISTS sequence BIGINT;

UPDATE power_readings
   SET recorded_at = COALESCE(recorded_at, time),
       active_power = COALESCE(active_power, power),
       firmware_version = COALESCE(firmware_version, fw)
 WHERE recorded_at IS NULL
    OR active_power IS NULL
    OR firmware_version IS NULL;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM power_readings WHERE recorded_at IS NULL) THEN
        RAISE EXCEPTION 'cannot create power_readings hypertable: existing rows have no recoverable recorded_at (legacy Time is NULL)';
    END IF;
END
$$;

ALTER TABLE power_readings ALTER COLUMN recorded_at SET NOT NULL;
DROP INDEX IF EXISTS idx_power_reading_identity;
ALTER TABLE power_readings DROP CONSTRAINT IF EXISTS power_readings_pkey;
ALTER TABLE power_readings
    ADD CONSTRAINT power_readings_pkey PRIMARY KEY (recorded_at, id);

DO $$
DECLARE
    already_hypertable BOOLEAN;
    has_recorded_at_dimension BOOLEAN;
BEGIN
    SELECT EXISTS (
        SELECT 1
        FROM timescaledb_information.hypertables
        WHERE hypertable_schema = current_schema()
          AND hypertable_name = 'power_readings'
    ) INTO already_hypertable;
    IF already_hypertable THEN
        SELECT EXISTS (
            SELECT 1
            FROM timescaledb_information.dimensions
            WHERE hypertable_schema = current_schema()
              AND hypertable_name = 'power_readings'
              AND column_name = 'recorded_at'
              AND dimension_type = 'Time'
        ) INTO has_recorded_at_dimension;
        IF NOT has_recorded_at_dimension THEN
            RAISE EXCEPTION 'power_readings is already a hypertable on a non-recorded_at dimension; migration stopped without changing its partition strategy';
        END IF;
    END IF;
END
$$;

SELECT create_hypertable('power_readings', 'recorded_at', if_not_exists => TRUE);

CREATE INDEX IF NOT EXISTS idx_power_readings_measurement_point_recorded_at
    ON power_readings (measurement_point_id, recorded_at DESC);
CREATE INDEX IF NOT EXISTS idx_power_readings_device_recorded_at
    ON power_readings (device_id, recorded_at DESC);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'power_readings_measurement_point_fk'
    ) THEN
        ALTER TABLE power_readings
            ADD CONSTRAINT power_readings_measurement_point_fk
            FOREIGN KEY (measurement_point_id) REFERENCES measurement_points(id);
    END IF;
END
$$;
