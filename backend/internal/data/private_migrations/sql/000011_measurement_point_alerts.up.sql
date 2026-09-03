-- MEASUREMENT_POINT_ALERTS_V1_01: move policy and lifecycle authority to the
-- permanent MeasurementPoint identity. Legacy device settings remain intact.
CREATE TABLE IF NOT EXISTS measurement_point_alert_settings (
    measurement_point_id UUID PRIMARY KEY REFERENCES measurement_points(id) ON DELETE CASCADE,
    quiet_hours_start VARCHAR(5) NOT NULL DEFAULT '',
    quiet_hours_end VARCHAR(5) NOT NULL DEFAULT '',
    power_threshold_w DOUBLE PRECISION NOT NULL DEFAULT 10.0,
    is_enabled BOOLEAN NOT NULL DEFAULT true,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT measurement_point_alert_settings_threshold_positive CHECK (power_threshold_w > 0 AND power_threshold_w < 'Infinity'::double precision AND power_threshold_w <> 'NaN'::double precision)
);

CREATE TABLE IF NOT EXISTS measurement_point_curfew_states (
    measurement_point_id UUID PRIMARY KEY REFERENCES measurement_points(id) ON DELETE CASCADE,
    in_curfew BOOLEAN NOT NULL DEFAULT false,
    last_event_at TIMESTAMPTZ
);

ALTER TABLE alert_logs ADD COLUMN IF NOT EXISTS recorded_at TIMESTAMPTZ;
UPDATE alert_logs SET recorded_at = created_at WHERE recorded_at IS NULL;
ALTER TABLE alert_logs ALTER COLUMN recorded_at SET DEFAULT now();
ALTER TABLE alert_logs ALTER COLUMN recorded_at SET NOT NULL;
CREATE INDEX IF NOT EXISTS idx_alert_logs_recorded_at ON alert_logs (recorded_at);
CREATE INDEX IF NOT EXISTS idx_alert_logs_measurement_point_recorded_at
    ON alert_logs (measurement_point_id, recorded_at)
    WHERE measurement_point_id IS NOT NULL AND NOT legacy_unresolved;

-- A legacy policy is copied only when exactly one distinct historical MP
-- proves the setting's ownership. Ambiguous MPs remain legacy-only.
WITH device_setting_points AS (
    SELECT DISTINCT setting.id AS setting_id, setting.is_enabled,
           setting.non_usage_start_time, setting.non_usage_end_time, setting.updated_at,
           assignment.measurement_point_id
    FROM device_alert_settings AS setting
    JOIN device_assignments AS assignment ON assignment.device_id = setting.device_id
), single_point_settings AS (
    SELECT setting_id, min(measurement_point_id::text)::uuid AS measurement_point_id,
           bool_and(is_enabled) AS is_enabled,
           min(non_usage_start_time) AS non_usage_start_time,
           min(non_usage_end_time) AS non_usage_end_time,
           min(updated_at) AS updated_at
    FROM device_setting_points
    GROUP BY setting_id
    HAVING count(DISTINCT measurement_point_id) = 1
), eligible AS (
    SELECT min(setting_id) AS setting_id, measurement_point_id,
           bool_and(is_enabled) AS is_enabled,
           min(non_usage_start_time) AS non_usage_start_time,
           min(non_usage_end_time) AS non_usage_end_time,
           min(updated_at) AS updated_at
    FROM single_point_settings
    GROUP BY measurement_point_id
    HAVING count(*) = 1
)
INSERT INTO measurement_point_alert_settings
    (measurement_point_id, quiet_hours_start, quiet_hours_end,
     power_threshold_w, is_enabled, updated_at)
SELECT measurement_point_id, COALESCE(non_usage_start_time, ''),
       COALESCE(non_usage_end_time, ''), 10.0, is_enabled, updated_at
FROM eligible
WHERE ((non_usage_start_time = '' AND non_usage_end_time = '')
   OR (non_usage_start_time ~ '^(?:[01][0-9]|2[0-3]):[0-5][0-9]$'
       AND non_usage_end_time ~ '^(?:[01][0-9]|2[0-3]):[0-5][0-9]$'
       AND non_usage_start_time <> non_usage_end_time))
  AND NOT EXISTS (
    SELECT 1 FROM measurement_point_alert_settings existing
    WHERE existing.measurement_point_id = eligible.measurement_point_id
);

COMMENT ON TABLE measurement_point_alert_settings IS
    'Authoritative MP-centered alert settings; device_alert_settings is legacy compatibility data';
COMMENT ON TABLE measurement_point_curfew_states IS
    'Durable edge-triggered curfew state, serialized by telemetry ingestion';
