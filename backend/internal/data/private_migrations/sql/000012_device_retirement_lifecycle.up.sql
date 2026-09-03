-- DEVICE_RETIREMENT_LIFECYCLE_01 authoritative Device lifecycle.
ALTER TABLE devices ADD COLUMN IF NOT EXISTS lifecycle_status VARCHAR(16);
UPDATE devices SET lifecycle_status = 'ACTIVE' WHERE lifecycle_status IS NULL;
ALTER TABLE devices ALTER COLUMN lifecycle_status SET DEFAULT 'ACTIVE';
ALTER TABLE devices ALTER COLUMN lifecycle_status SET NOT NULL;
ALTER TABLE devices DROP CONSTRAINT IF EXISTS devices_lifecycle_status_check;
ALTER TABLE devices ADD CONSTRAINT devices_lifecycle_status_check
    CHECK (lifecycle_status IN ('ACTIVE', 'DISABLED', 'RETIRED'));
CREATE INDEX IF NOT EXISTS devices_lifecycle_status_idx ON devices (lifecycle_status);

CREATE OR REPLACE FUNCTION prevent_device_lifecycle_reactivation()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.lifecycle_status = 'RETIRED' AND NEW.lifecycle_status <> 'RETIRED' THEN
        RAISE EXCEPTION 'RETIRED devices are terminal';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS devices_lifecycle_terminal ON devices;
CREATE TRIGGER devices_lifecycle_terminal
BEFORE UPDATE OF lifecycle_status ON devices
FOR EACH ROW EXECUTE FUNCTION prevent_device_lifecycle_reactivation();
