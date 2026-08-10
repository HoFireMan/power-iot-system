-- Stage A DOWN is fail-closed for durable security state. The migration
-- runner restores clean version-4 metadata after the guarded signal.
DO $$
BEGIN
    LOCK TABLE admin_binding_operations,
               admin_binding_audits,
               refresh_sessions,
               refresh_tokens,
               user_shop_relations,
               devices,
               shops,
               users
        IN ACCESS EXCLUSIVE MODE;

    IF EXISTS (SELECT 1 FROM refresh_tokens)
       OR EXISTS (SELECT 1 FROM refresh_sessions)
       OR EXISTS (SELECT 1 FROM users WHERE auth_enabled)
       OR EXISTS (SELECT 1 FROM devices WHERE inventory_owner_client_id IS NOT NULL)
       OR EXISTS (SELECT 1 FROM admin_binding_operations WHERE client_id IS NOT NULL)
       OR EXISTS (SELECT 1 FROM admin_binding_audits WHERE client_id IS NOT NULL) THEN
        RAISE EXCEPTION USING
            ERRCODE = 'P0001',
            MESSAGE = 'MIGRATION_GUARDED_DOWN',
            DETAIL = 'cannot rollback security schema foundation while Stage A security state exists';
    END IF;

    DROP TABLE refresh_tokens;
    DROP TABLE refresh_sessions;

    EXECUTE 'DROP TRIGGER IF EXISTS admin_binding_audits_client_provenance ON admin_binding_audits';
    EXECUTE 'DROP FUNCTION IF EXISTS validate_admin_binding_audit_client_provenance()';

    ALTER TABLE admin_binding_audits
        DROP CONSTRAINT IF EXISTS security_admin_binding_audits_client_provenance_fkey;
    ALTER TABLE admin_binding_audits
        DROP CONSTRAINT IF EXISTS security_admin_binding_audits_client_id_fkey;
    ALTER TABLE admin_binding_operations
        DROP CONSTRAINT IF EXISTS security_admin_binding_operations_client_provenance_key;
    ALTER TABLE admin_binding_operations
        DROP CONSTRAINT IF EXISTS security_admin_binding_operations_client_id_fkey;
    DROP INDEX IF EXISTS security_admin_binding_audits_client_time_idx;
    DROP INDEX IF EXISTS security_admin_binding_operations_client_time_idx;
    ALTER TABLE admin_binding_audits DROP COLUMN client_id;
    ALTER TABLE admin_binding_operations DROP COLUMN client_id;

    ALTER TABLE user_shop_relations
        DROP CONSTRAINT IF EXISTS security_user_shop_relations_shop_id_fkey;
    ALTER TABLE user_shop_relations
        DROP CONSTRAINT IF EXISTS security_user_shop_relations_user_id_fkey;
    DROP INDEX IF EXISTS security_user_shop_relations_shop_user_idx;

    ALTER TABLE devices
        DROP CONSTRAINT IF EXISTS security_devices_inventory_owner_client_id_fkey;
    DROP INDEX IF EXISTS security_devices_inventory_owner_client_id_idx;
    ALTER TABLE devices DROP COLUMN inventory_owner_client_id;

    ALTER TABLE shops
        DROP CONSTRAINT IF EXISTS security_shops_client_id_fkey;
    DROP INDEX IF EXISTS security_shops_client_id_idx;

    ALTER TABLE users DROP COLUMN auth_enabled;
END
$$;
