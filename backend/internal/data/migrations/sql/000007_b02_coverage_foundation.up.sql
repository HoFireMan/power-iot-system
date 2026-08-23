-- B-02 Coverage Foundation. This body is additive and must be executed by
-- the protected post-V6 migration authority, not generic migrations.Up.
ALTER TABLE power_readings
    ADD COLUMN IF NOT EXISTS coverage_version BIGINT,
    ADD COLUMN IF NOT EXISTS interval_start TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS interval_end TIMESTAMPTZ;

ALTER TABLE telemetry_ingest_keys
    ADD COLUMN IF NOT EXISTS canonical_coverage_digest BYTEA,
    ADD COLUMN IF NOT EXISTS conflict_detected BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE power_readings
    DROP CONSTRAINT IF EXISTS power_readings_coverage_profile_check;
ALTER TABLE power_readings
    ADD CONSTRAINT power_readings_coverage_profile_check CHECK (
        coverage_version IS NULL OR (
            coverage_version = 1
            AND protocol_version = 1
            AND measurement_point_id IS NOT NULL
            AND interval_start IS NOT NULL
            AND interval_end IS NOT NULL
            AND interval_start < interval_end
            AND recorded_at = interval_start
            AND energy_delta_kwh IS NOT NULL
            AND boot_counter IS NOT NULL
            AND sequence IS NOT NULL
        )
    );

ALTER TABLE telemetry_ingest_keys
    DROP CONSTRAINT IF EXISTS telemetry_ingest_keys_coverage_digest_length;
ALTER TABLE telemetry_ingest_keys
    ADD CONSTRAINT telemetry_ingest_keys_coverage_digest_length CHECK (
        canonical_coverage_digest IS NULL
        OR octet_length(canonical_coverage_digest) = 32
    );

CREATE INDEX IF NOT EXISTS idx_power_readings_coverage_mp_interval_start
    ON power_readings (measurement_point_id, interval_start)
    WHERE coverage_version = 1;

CREATE INDEX IF NOT EXISTS idx_power_readings_mp_recorded_at
    ON power_readings (measurement_point_id, recorded_at DESC);

-- No historical rows are backfilled. Existing rows remain NULL/non-profile.
