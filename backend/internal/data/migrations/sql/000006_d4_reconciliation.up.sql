-- D5 physical realization. This file is consumed only by the protected
-- migration runner; generic migrations.Up remains capped at version 5.

CREATE TABLE d4_operation_ledger (
    operation_id UUID NOT NULL,
    attempt_id UUID NOT NULL,
    target_fingerprint BYTEA NOT NULL,
    generation BIGINT NOT NULL,
    state TEXT NOT NULL,
    claim_id UUID,
    disposition TEXT,
    commit_status TEXT,
    post_verification_status TEXT,
    cleanup_status TEXT,
    certainty TEXT,
    unknown BOOLEAN NOT NULL DEFAULT false,
    recovery_required BOOLEAN NOT NULL DEFAULT false,
    recovery_class TEXT NOT NULL DEFAULT '',
    replay_disposition TEXT,
    safe_result JSONB,
    safe_correlation JSONB,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (operation_id, attempt_id, target_fingerprint, generation),
    CONSTRAINT d4_operation_ledger_operation_attempt_key UNIQUE (operation_id, attempt_id),
    CONSTRAINT d4_operation_ledger_target_length CHECK (octet_length(target_fingerprint) = 32),
    CONSTRAINT d4_operation_ledger_generation_check CHECK (generation > 0),
    CONSTRAINT d4_operation_ledger_state_check CHECK (state IN (
        'RECEIVED', 'ADMITTED', 'EXECUTING', 'RESULT_RECORDED',
        'CONTINUATION_PENDING', 'CONTINUATION_CONSUMED',
        'WAITING_FOR_MAPPING', 'TERMINAL', 'RECOVERY_REQUIRED'
    )),
    CONSTRAINT d4_operation_ledger_claim_check CHECK (
        (state = 'RECEIVED' AND claim_id IS NULL)
        OR (state IN ('ADMITTED', 'EXECUTING', 'RESULT_RECORDED', 'CONTINUATION_PENDING', 'CONTINUATION_CONSUMED', 'WAITING_FOR_MAPPING') AND claim_id IS NOT NULL)
        OR state IN ('TERMINAL', 'RECOVERY_REQUIRED')
    ),
    CONSTRAINT d4_operation_ledger_recovery_check CHECK (
        recovery_class IN ('', 'UNKNOWN_COMMIT_OR_CLEANUP', 'COMMITTED_POSTVERIFY_FAILED', 'STALE_OR_REVALIDATION_REQUIRED')
    ),
    CONSTRAINT d4_operation_ledger_disposition_check CHECK (disposition IS NULL OR disposition IN ('SUCCESS', 'NON_SUCCESS')),
    CONSTRAINT d4_operation_ledger_commit_check CHECK (commit_status IS NULL OR commit_status IN ('NOT_COMMITTED', 'COMMITTED', 'COMMIT_UNKNOWN')),
    CONSTRAINT d4_operation_ledger_post_check CHECK (post_verification_status IS NULL OR post_verification_status IN ('NOT_VERIFIED', 'VERIFIED', 'FAILED')),
    CONSTRAINT d4_operation_ledger_cleanup_check CHECK (cleanup_status IS NULL OR cleanup_status IN ('CONFIRMED', 'UNCERTAIN')),
    CONSTRAINT d4_operation_ledger_certainty_check CHECK (certainty IS NULL OR certainty IN ('KNOWN', 'UNKNOWN')),
    CONSTRAINT d4_operation_ledger_unknown_check CHECK (
        disposition IS NULL OR unknown = (commit_status = 'COMMIT_UNKNOWN' OR cleanup_status = 'UNCERTAIN')
    ),
    CONSTRAINT d4_operation_ledger_recovery_required_check CHECK (
        disposition IS NULL OR recovery_required = (unknown OR post_verification_status = 'FAILED')
    ),
    CONSTRAINT d4_operation_ledger_result_truth_check CHECK (
        disposition IS NULL OR disposition <> 'SUCCESS'
        OR (commit_status = 'COMMITTED' AND post_verification_status = 'VERIFIED' AND cleanup_status = 'CONFIRMED' AND certainty = 'KNOWN' AND unknown = false AND recovery_required = false)
    )
);

CREATE INDEX d4_operation_ledger_state_idx ON d4_operation_ledger (state, updated_at);
CREATE INDEX d4_operation_ledger_recovery_idx ON d4_operation_ledger (updated_at)
    WHERE state = 'RECOVERY_REQUIRED';

CREATE TABLE d4_operation_journal (
    event_id UUID PRIMARY KEY,
    event_version BIGINT NOT NULL,
    operation_id UUID NOT NULL,
    attempt_id UUID NOT NULL,
    target_fingerprint BYTEA NOT NULL,
    generation BIGINT NOT NULL,
    from_state TEXT NOT NULL,
    to_state TEXT NOT NULL,
    recovery_class TEXT NOT NULL DEFAULT '',
    correlation TEXT NOT NULL DEFAULT '',
    safe_payload JSONB NOT NULL,
    payload_digest BYTEA NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT d4_operation_journal_target_length CHECK (octet_length(target_fingerprint) = 32),
    CONSTRAINT d4_operation_journal_generation_check CHECK (generation > 0),
    CONSTRAINT d4_operation_journal_payload_digest_length CHECK (octet_length(payload_digest) = 32),
    CONSTRAINT d4_operation_journal_ledger_fk
        FOREIGN KEY (operation_id, attempt_id, target_fingerprint, generation)
        REFERENCES d4_operation_ledger (operation_id, attempt_id, target_fingerprint, generation)
        ON DELETE RESTRICT
);

CREATE INDEX d4_operation_journal_tuple_time_idx
    ON d4_operation_journal (operation_id, attempt_id, target_fingerprint, generation, occurred_at);

CREATE OR REPLACE FUNCTION prevent_d4_terminal_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.state = 'TERMINAL' THEN
        RAISE EXCEPTION 'd4_operation_ledger terminal row is immutable';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS d4_operation_ledger_immutable ON d4_operation_ledger;
CREATE TRIGGER d4_operation_ledger_immutable
BEFORE UPDATE OR DELETE ON d4_operation_ledger
FOR EACH ROW EXECUTE FUNCTION prevent_d4_terminal_mutation();

CREATE OR REPLACE FUNCTION prevent_d4_journal_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'd4_operation_journal is append-only';
END;
$$;

DROP TRIGGER IF EXISTS d4_operation_journal_append_only ON d4_operation_journal;
CREATE TRIGGER d4_operation_journal_append_only
BEFORE UPDATE OR DELETE ON d4_operation_journal
FOR EACH ROW EXECUTE FUNCTION prevent_d4_journal_mutation();

ALTER TABLE shops VALIDATE CONSTRAINT security_shops_client_id_fkey;
ALTER TABLE devices VALIDATE CONSTRAINT security_devices_inventory_owner_client_id_fkey;
ALTER TABLE user_shop_relations VALIDATE CONSTRAINT security_user_shop_relations_user_id_fkey;
ALTER TABLE user_shop_relations VALIDATE CONSTRAINT security_user_shop_relations_shop_id_fkey;
ALTER TABLE admin_binding_operations VALIDATE CONSTRAINT security_admin_binding_operations_client_id_fkey;
ALTER TABLE admin_binding_audits VALIDATE CONSTRAINT security_admin_binding_audits_client_id_fkey;
ALTER TABLE admin_binding_audits VALIDATE CONSTRAINT security_admin_binding_audits_client_provenance_fkey;

ALTER TABLE shops ALTER COLUMN client_id SET NOT NULL;
ALTER TABLE devices ALTER COLUMN inventory_owner_client_id SET NOT NULL;
ALTER TABLE admin_binding_operations ALTER COLUMN client_id SET NOT NULL;
ALTER TABLE admin_binding_audits ALTER COLUMN client_id SET NOT NULL;
