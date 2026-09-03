-- IDENT-002: MeasurementPoint is the logical identity for derived facts.
-- This body is applied only through the protected post-B-02 migration
-- operator. Existing derived rows are never guessed or deleted.

ALTER TABLE alert_logs
    ADD COLUMN IF NOT EXISTS measurement_point_id UUID;
ALTER TABLE alert_logs
    ADD COLUMN IF NOT EXISTS legacy_unresolved BOOLEAN NOT NULL DEFAULT true;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'alert_logs_measurement_point_fk'
    ) THEN
        ALTER TABLE alert_logs
            ADD CONSTRAINT alert_logs_measurement_point_fk
            FOREIGN KEY (measurement_point_id)
            REFERENCES measurement_points(id)
            ON DELETE RESTRICT;
    END IF;
END
$$;

-- AlertLog.CreatedAt is the existing authoritative event timestamp. An old
-- row is backfilled only when exactly one historical assignment contains that
-- instant under [valid_from, valid_to). Device.ShopID and current assignment
-- are intentionally not consulted. Zero or overlapping matches stay legacy.
UPDATE alert_logs AS logs
SET measurement_point_id = (
        SELECT assignment.measurement_point_id
        FROM device_assignments AS assignment
        WHERE assignment.device_id = logs.device_id
          AND assignment.valid_from <= logs.created_at
          AND (assignment.valid_to IS NULL OR logs.created_at < assignment.valid_to)
    ),
    legacy_unresolved = false
WHERE logs.measurement_point_id IS NULL
  AND (
      SELECT count(*)
      FROM device_assignments AS assignment
      WHERE assignment.device_id = logs.device_id
        AND assignment.valid_from <= logs.created_at
        AND (assignment.valid_to IS NULL OR logs.created_at < assignment.valid_to)
  ) = 1;

UPDATE alert_logs
SET legacy_unresolved = (measurement_point_id IS NULL);

ALTER TABLE alert_logs
    DROP CONSTRAINT IF EXISTS alert_logs_identity_state_check;
ALTER TABLE alert_logs
    ADD CONSTRAINT alert_logs_identity_state_check CHECK (
        (measurement_point_id IS NULL) = legacy_unresolved
    );

CREATE INDEX IF NOT EXISTS idx_alert_logs_measurement_point_created_at
    ON alert_logs (measurement_point_id, created_at)
    WHERE measurement_point_id IS NOT NULL;

ALTER TABLE daily_usages
    ADD COLUMN IF NOT EXISTS measurement_point_id UUID;
ALTER TABLE daily_usages
    ADD COLUMN IF NOT EXISTS legacy_unresolved BOOLEAN NOT NULL DEFAULT true;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'daily_usages_measurement_point_fk'
    ) THEN
        ALTER TABLE daily_usages
            ADD CONSTRAINT daily_usages_measurement_point_fk
            FOREIGN KEY (measurement_point_id)
            REFERENCES measurement_points(id)
            ON DELETE RESTRICT;
    END IF;
END
$$;

-- DailyUsage has no active writer or reader and has no accepted exact
-- reconstruction authority. Every pre-existing device-day row is therefore
-- retained as unresolved legacy evidence; no duration split or energy guess
-- is performed. A pre-populated MP value from a future/replayed run remains
-- authoritative only when it satisfies the state check below.
UPDATE daily_usages
SET legacy_unresolved = (measurement_point_id IS NULL);

ALTER TABLE daily_usages
    ALTER COLUMN device_id DROP NOT NULL;

DROP INDEX IF EXISTS idx_daily_device;
ALTER TABLE daily_usages
    DROP CONSTRAINT IF EXISTS daily_usages_identity_state_check;
ALTER TABLE daily_usages
    ADD CONSTRAINT daily_usages_identity_state_check CHECK (
        (measurement_point_id IS NULL) = legacy_unresolved
    );

-- Only MP-centered authoritative rows participate in uniqueness. Unresolved
-- legacy rows may share a date/device because their device identity is not a
-- valid logical derived-fact key.
CREATE UNIQUE INDEX IF NOT EXISTS daily_usages_measurement_point_date_key
    ON daily_usages (date, measurement_point_id)
    WHERE measurement_point_id IS NOT NULL AND NOT legacy_unresolved;

CREATE INDEX IF NOT EXISTS idx_daily_usages_measurement_point_date
    ON daily_usages (measurement_point_id, date)
    WHERE measurement_point_id IS NOT NULL;
