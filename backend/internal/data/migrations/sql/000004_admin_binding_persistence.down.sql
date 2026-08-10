-- golang-migrate's PostgreSQL driver executes this file through a raw
-- connection without wrapping the migration in a transaction. Keep the table
-- locks, durability check, and destructive rollback inside one DO statement so
-- PostgreSQL's implicit transaction covers the whole protected operation.
DO $$
BEGIN
    -- Keep the same parent-first order as the caller-owned operation → audit
    -- transaction path to avoid a lock-order deadlock.
    LOCK TABLE admin_binding_operations, admin_binding_audits
        IN ACCESS EXCLUSIVE MODE;

    IF EXISTS (SELECT 1 FROM admin_binding_audits)
       OR EXISTS (SELECT 1 FROM admin_binding_operations) THEN
        -- All guarded migration DOWN files use this stable signal. The
        -- migration runner verifies the expected dirty one-step metadata
        -- transition before restoring the original version.
        RAISE EXCEPTION USING
            ERRCODE = 'P0001',
            MESSAGE = 'MIGRATION_GUARDED_DOWN',
            DETAIL = 'cannot rollback admin binding persistence while audit or operation rows exist';
    END IF;

    EXECUTE 'DROP TRIGGER IF EXISTS admin_binding_audits_immutable ON admin_binding_audits';
    EXECUTE 'DROP TABLE IF EXISTS admin_binding_audits';
    EXECUTE 'DROP TABLE IF EXISTS admin_binding_operations';
    EXECUTE 'DROP FUNCTION IF EXISTS prevent_admin_binding_audit_mutation()';
END
$$;
