-- B-02 rollback is protected. Ordinary migration DOWN must not remove
-- immutable coverage evidence or conflict provenance.
DO $$
BEGIN
    RAISE EXCEPTION 'MIGRATION_GUARDED_DOWN';
END;
$$;
