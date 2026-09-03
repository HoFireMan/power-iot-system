-- Lifecycle state is authoritative and terminal RETIRED data must not be
-- silently removed by a generic rollback.
DO $$
BEGIN
    RAISE EXCEPTION 'device lifecycle migration DOWN is protected';
END $$;
