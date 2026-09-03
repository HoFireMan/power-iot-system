-- IDENT-002 rollback is intentionally guarded. Removing MP identity columns
-- or indexes could destroy the only exact attribution and weaken the derived
-- fact contract. Use an explicitly designed recovery migration instead.
DO $$
BEGIN
    RAISE EXCEPTION 'MIGRATION_GUARDED_DOWN'
        USING ERRCODE = 'P0001';
END
$$;
