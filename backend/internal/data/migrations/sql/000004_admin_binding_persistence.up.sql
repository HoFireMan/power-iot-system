-- Milestone 3B-1 persistence foundation.
-- Audit and operation-ledger rows are ordinary PostgreSQL tables; they are not
-- telemetry hypertables and do not introduce an authorization framework.

CREATE TABLE IF NOT EXISTS admin_binding_operations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    operation_id UUID NOT NULL UNIQUE,
    idempotency_key VARCHAR(255) NOT NULL,
    operation VARCHAR(100) NOT NULL,
    scope_key VARCHAR(255) NOT NULL,
    actor_id BIGINT NOT NULL REFERENCES users(id),
    scope_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    canonical_request_hash BYTEA NOT NULL,
    committed_response JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    committed_at TIMESTAMPTZ,
    CONSTRAINT admin_binding_operations_hash_length_check
        CHECK (octet_length(canonical_request_hash) = 32),
    CONSTRAINT admin_binding_operations_commit_pair_check
        CHECK ((committed_response IS NULL AND committed_at IS NULL)
            OR (committed_response IS NOT NULL AND committed_at IS NOT NULL))
);

-- The actor and resolved scope are part of the uniqueness boundary. A textual
-- scope key deliberately avoids inventing tenant/auth tables before Auth/JWT.
CREATE UNIQUE INDEX IF NOT EXISTS admin_binding_operations_scope_key
    ON admin_binding_operations (actor_id, scope_key, operation, idempotency_key);
ALTER TABLE admin_binding_operations
    ADD CONSTRAINT admin_binding_operations_provenance_key
    UNIQUE (operation_id, operation, actor_id, scope_key);

CREATE TABLE IF NOT EXISTS admin_binding_audits (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    operation_id UUID NOT NULL,
    request_identity VARCHAR(255) NOT NULL,
    actor_id BIGINT NOT NULL REFERENCES users(id),
    scope_key VARCHAR(255) NOT NULL,
    scope_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    action VARCHAR(40) NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    effective_at TIMESTAMPTZ,
    shop_id BIGINT REFERENCES shops(id),
    measurement_point_id UUID REFERENCES measurement_points(id),
    device_id BIGINT REFERENCES devices(id),
    device_serial_number VARCHAR(128),
    device_mac VARCHAR(12),
    old_measurement_point_id UUID REFERENCES measurement_points(id),
    new_measurement_point_id UUID REFERENCES measurement_points(id),
    old_assignment_id UUID REFERENCES device_assignments(id),
    new_assignment_id UUID REFERENCES device_assignments(id),
    reason TEXT,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    CONSTRAINT admin_binding_audits_operation_id_key
        UNIQUE (operation_id),
    CONSTRAINT admin_binding_audits_operation_provenance_fk
        FOREIGN KEY (operation_id, action, actor_id, scope_key)
        REFERENCES admin_binding_operations(operation_id, operation, actor_id, scope_key),
    CONSTRAINT admin_binding_audits_action_check
        CHECK (action IN ('create_measurement_point', 'bind', 'replace', 'relocate', 'unbind')),
    CONSTRAINT admin_binding_audits_device_mac_check
        CHECK (device_mac IS NULL OR device_mac ~ '^[0-9A-F]{12}$')
);

CREATE INDEX IF NOT EXISTS admin_binding_audits_actor_scope_time_idx
    ON admin_binding_audits (actor_id, scope_key, occurred_at DESC);
CREATE INDEX IF NOT EXISTS admin_binding_audits_device_time_idx
    ON admin_binding_audits (device_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS admin_binding_audits_measurement_point_time_idx
    ON admin_binding_audits (measurement_point_id, occurred_at DESC);

-- Successful Admin Binding audit facts are append-only. The application has no
-- update/delete API, and this database guard also protects ordinary SQL writes.
CREATE OR REPLACE FUNCTION prevent_admin_binding_audit_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'admin_binding_audits is immutable';
END;
$$;

DROP TRIGGER IF EXISTS admin_binding_audits_immutable ON admin_binding_audits;
CREATE TRIGGER admin_binding_audits_immutable
BEFORE UPDATE OR DELETE ON admin_binding_audits
FOR EACH ROW EXECUTE FUNCTION prevent_admin_binding_audit_mutation();
