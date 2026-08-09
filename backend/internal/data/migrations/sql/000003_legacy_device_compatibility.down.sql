-- This migration is additive compatibility for pre-existing GORM schemas.
-- The columns may also be part of the baseline schema on a fresh database;
-- retaining them on rollback is safer than guessing their provenance and
-- dropping user data or breaking the legacy model.
SELECT 1;
