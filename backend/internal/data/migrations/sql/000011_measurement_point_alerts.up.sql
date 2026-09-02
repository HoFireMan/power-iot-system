-- MEASUREMENT_POINT_ALERTS_V1_01: move policy and lifecycle authority to the
-- permanent MeasurementPoint identity. Legacy device settings remain intact.
CREATE TABLE IF NOT EXISTS measurement_point_alert_settings (
    measurement_point_id UUID PRIMARY KEY REFERENCES measurement_points(id) ON DELETE CASCADE,
    daily_limit_kwh DOUBLE PRECISION,
    monthly_limit_kwh DOUBLE PRECISION,
    non_usage_start_time VARCHAR(255) NOT NULL DEFAULT '',
    non_usage_end_time VARCHAR(255) NOT NULL DEFAULT '',
    is_enabled BOOLEAN NOT NULL DEFAULT true,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
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

-- A legacy policy is copied only when exactly one active assignment proves the
-- MP identity. Ambiguous MPs remain represented by their legacy rows and are
-- not silently assigned a policy.
INSERT INTO measurement_point_alert_settings
    (measurement_point_id, daily_limit_kwh, monthly_limit_kwh,
     non_usage_start_time, non_usage_end_time, is_enabled, updated_at)
SELECT assignment.measurement_point_id, setting.daily_limit_kwh,
       setting.monthly_limit_kwh, setting.non_usage_start_time,
       setting.non_usage_end_time, setting.is_enabled, setting.updated_at
FROM device_alert_settings AS setting
JOIN device_assignments AS assignment ON assignment.device_id = setting.device_id
WHERE assignment.valid_to IS NULL
  AND setting.non_usage_start_time IS NOT NULL
  AND setting.non_usage_end_time IS NOT NULL
  AND NOT EXISTS (
      SELECT 1 FROM measurement_point_alert_settings existing
      WHERE existing.measurement_point_id = assignment.measurement_point_id
  )
  AND (
      SELECT count(*)
      FROM device_alert_settings s2
      JOIN device_assignments a2 ON a2.device_id = s2.device_id
      WHERE a2.measurement_point_id = assignment.measurement_point_id
        AND a2.valid_to IS NULL
  ) = 1;

COMMENT ON TABLE measurement_point_alert_settings IS
    'Authoritative MP-centered alert settings; device_alert_settings is legacy compatibility data';
COMMENT ON TABLE measurement_point_curfew_states IS
    'Durable edge-triggered curfew state, serialized by telemetry ingestion';
