-- D5 rollback is guarded. The protected runner owns explicit recovery.
DO $$
BEGIN
    RAISE EXCEPTION 'MIGRATION_GUARDED_DOWN';
END;
$$;
