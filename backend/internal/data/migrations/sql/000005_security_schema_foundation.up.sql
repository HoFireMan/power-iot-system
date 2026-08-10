-- Security Schema Stage A: additive, reconciliation-safe foundation.
-- Legacy ownership, tenant, membership, and provenance values are not
-- fabricated here. NOT VALID foreign keys protect new writes while allowing
-- existing unresolved rows to remain until the fenced reconciliation stage.

ALTER TABLE users
    ADD COLUMN auth_enabled BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE shops
    ADD CONSTRAINT security_shops_client_id_fkey
    FOREIGN KEY (client_id) REFERENCES clients(id)
    ON DELETE RESTRICT
    NOT VALID;
CREATE INDEX security_shops_client_id_idx
    ON shops (client_id);

ALTER TABLE devices
    ADD COLUMN inventory_owner_client_id BIGINT;
ALTER TABLE devices
    ADD CONSTRAINT security_devices_inventory_owner_client_id_fkey
    FOREIGN KEY (inventory_owner_client_id) REFERENCES clients(id)
    ON DELETE RESTRICT
    NOT VALID;
CREATE INDEX security_devices_inventory_owner_client_id_idx
    ON devices (inventory_owner_client_id);

ALTER TABLE user_shop_relations
    ADD CONSTRAINT security_user_shop_relations_user_id_fkey
    FOREIGN KEY (user_id) REFERENCES users(id)
    ON DELETE RESTRICT
    NOT VALID;
ALTER TABLE user_shop_relations
    ADD CONSTRAINT security_user_shop_relations_shop_id_fkey
    FOREIGN KEY (shop_id) REFERENCES shops(id)
    ON DELETE RESTRICT
    NOT VALID;
CREATE INDEX security_user_shop_relations_shop_user_idx
    ON user_shop_relations (shop_id, user_id);

-- One refresh session is one server-side refresh-token family. The presented
-- opaque token is never persisted; only its fixed-size digest is stored.
CREATE TABLE refresh_sessions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    CONSTRAINT refresh_sessions_expiry_check
        CHECK (expires_at > created_at),
    CONSTRAINT refresh_sessions_revoked_at_check
        CHECK (revoked_at IS NULL OR revoked_at >= created_at)
);

CREATE INDEX refresh_sessions_user_created_idx
    ON refresh_sessions (user_id, created_at DESC);
CREATE INDEX refresh_sessions_expires_idx
    ON refresh_sessions (expires_at);

CREATE TABLE refresh_tokens (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id UUID NOT NULL REFERENCES refresh_sessions(id) ON DELETE CASCADE,
    token_hash BYTEA NOT NULL,
    issued_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    replaced_by_token_id UUID,
    CONSTRAINT refresh_tokens_hash_length_check
        CHECK (octet_length(token_hash) = 32),
    CONSTRAINT refresh_tokens_expiry_check
        CHECK (expires_at > issued_at),
    CONSTRAINT refresh_tokens_consumed_at_check
        CHECK (consumed_at IS NULL OR consumed_at >= issued_at),
    CONSTRAINT refresh_tokens_revoked_at_check
        CHECK (revoked_at IS NULL OR revoked_at >= issued_at),
    CONSTRAINT refresh_tokens_not_self_replaced_check
        CHECK (replaced_by_token_id IS NULL OR replaced_by_token_id <> id),
    CONSTRAINT refresh_tokens_hash_key UNIQUE (token_hash),
    CONSTRAINT refresh_tokens_id_session_key UNIQUE (id, session_id),
    CONSTRAINT refresh_tokens_replaced_by_same_session_fkey
        FOREIGN KEY (replaced_by_token_id, session_id)
        REFERENCES refresh_tokens (id, session_id)
        ON DELETE RESTRICT
);

CREATE INDEX refresh_tokens_session_issued_idx
    ON refresh_tokens (session_id, issued_at DESC);
CREATE INDEX refresh_tokens_expires_idx
    ON refresh_tokens (expires_at);
CREATE INDEX refresh_tokens_replaced_by_token_idx
    ON refresh_tokens (replaced_by_token_id)
    WHERE replaced_by_token_id IS NOT NULL;
CREATE UNIQUE INDEX refresh_tokens_one_current_per_session_idx
    ON refresh_tokens (session_id)
    WHERE consumed_at IS NULL AND revoked_at IS NULL;

-- 000004 already records actor, Shop, and Device provenance. These nullable
-- scalar Client snapshots are additive and remain NULL for legacy rows whose
-- tenant cannot be derived without reconciliation. Existing operation
-- idempotency and four-column provenance uniqueness are unchanged.
ALTER TABLE admin_binding_operations
    ADD COLUMN client_id BIGINT;
ALTER TABLE admin_binding_operations
    ADD CONSTRAINT security_admin_binding_operations_client_id_fkey
    FOREIGN KEY (client_id) REFERENCES clients(id)
    ON DELETE RESTRICT
    NOT VALID;
ALTER TABLE admin_binding_operations
    ADD CONSTRAINT security_admin_binding_operations_client_provenance_key
    UNIQUE (operation_id, operation, actor_id, scope_key, client_id);
CREATE INDEX security_admin_binding_operations_client_time_idx
    ON admin_binding_operations (client_id, created_at DESC)
    WHERE client_id IS NOT NULL;

ALTER TABLE admin_binding_audits
    ADD COLUMN client_id BIGINT;
ALTER TABLE admin_binding_audits
    ADD CONSTRAINT security_admin_binding_audits_client_id_fkey
    FOREIGN KEY (client_id) REFERENCES clients(id)
    ON DELETE RESTRICT
    NOT VALID;
ALTER TABLE admin_binding_audits
    ADD CONSTRAINT security_admin_binding_audits_client_provenance_fkey
    FOREIGN KEY (operation_id, action, actor_id, scope_key, client_id)
    REFERENCES admin_binding_operations (operation_id, operation, actor_id, scope_key, client_id)
    ON DELETE RESTRICT
    NOT VALID;

CREATE OR REPLACE FUNCTION validate_admin_binding_audit_client_provenance()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    operation_client_id BIGINT;
BEGIN
    SELECT operation.client_id
      INTO operation_client_id
      FROM admin_binding_operations AS operation
     WHERE operation.operation_id = NEW.operation_id
       AND operation.operation = NEW.action
       AND operation.actor_id = NEW.actor_id
       AND operation.scope_key = NEW.scope_key;

    IF NOT FOUND OR operation_client_id IS DISTINCT FROM NEW.client_id THEN
        RAISE EXCEPTION 'admin binding audit Client provenance does not match its operation';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS admin_binding_audits_client_provenance ON admin_binding_audits;
CREATE TRIGGER admin_binding_audits_client_provenance
BEFORE INSERT ON admin_binding_audits
FOR EACH ROW EXECUTE FUNCTION validate_admin_binding_audit_client_provenance();

CREATE INDEX security_admin_binding_audits_client_time_idx
    ON admin_binding_audits (client_id, occurred_at DESC)
    WHERE client_id IS NOT NULL;
