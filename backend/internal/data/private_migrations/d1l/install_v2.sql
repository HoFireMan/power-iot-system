-- Additive D1-L provenance-ledger transition. install_v1.sql is immutable.
-- This artifact is executed by the protected migration owner in the same
-- transaction as the current-state manifest replacement and final catalog
-- proof. It never stores a raw activation secret.

ALTER TABLE security_control.control_schema_migrations
    DROP CONSTRAINT control_schema_migrations_version_check;

ALTER TABLE security_control.control_schema_migrations
    ADD CONSTRAINT control_schema_migrations_version_check
        CHECK (control_version IN (1, 2));

CREATE TABLE security_control.admission_provenance (
    provenance_id       UUID        NOT NULL,
    provenance_version  BIGINT      NOT NULL,
    owner_identity      TEXT        COLLATE "C" NOT NULL,
    owner_version       TEXT        COLLATE "C" NOT NULL,
    operation_id        UUID        NOT NULL,
    attempt_id          UUID        NOT NULL,
    target_fingerprint  BYTEA       NOT NULL,
    evidence_digest     BYTEA       NOT NULL,
    route_intent        TEXT        COLLATE "C" NOT NULL,
    state               TEXT        COLLATE "C" NOT NULL,
    issue_id            UUID        NULL,
    lease_id            UUID        NULL,
    lease_generation    BIGINT      NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    reserved_at         TIMESTAMPTZ NULL,
    resolved_at         TIMESTAMPTZ NULL,
    terminal_code       TEXT        COLLATE "C" NULL,

    CONSTRAINT admission_provenance_pkey
        PRIMARY KEY (provenance_id, provenance_version),

    CONSTRAINT admission_provenance_state_check
        CHECK (state IN ('AVAILABLE', 'RESERVED', 'CONSUMED', 'INVALIDATED')),

    CONSTRAINT admission_provenance_version_check
        CHECK (provenance_version > 0),

    CONSTRAINT admission_provenance_owner_identity_check
        CHECK (
            owner_identity = 'trusted-post-d1l-upstream'
            AND length(owner_identity) > 0
            AND length(owner_version) > 0
            AND btrim(owner_version) <> ''
        ),

    CONSTRAINT admission_provenance_route_check
        CHECK (route_intent = 'D1_ISSUE'),

    CONSTRAINT admission_provenance_digest_check
        CHECK (
            octet_length(target_fingerprint) = 32
            AND octet_length(evidence_digest) = 32
        ),

    CONSTRAINT admission_provenance_target_nonzero_check
        CHECK (target_fingerprint <> decode(repeat('00', 32), 'hex')),

    CONSTRAINT admission_provenance_issue_key
        UNIQUE (issue_id),

    CONSTRAINT admission_provenance_attempt_id_key
        UNIQUE (attempt_id),

    CONSTRAINT admission_provenance_lease_key
        UNIQUE (lease_id),

    CONSTRAINT admission_provenance_issue_state_check
        CHECK (
            (
                state = 'AVAILABLE'
                AND issue_id IS NULL
            )
            OR
            (
                state IN ('RESERVED', 'CONSUMED', 'INVALIDATED')
                AND issue_id IS NOT NULL
            )
        ),

    CONSTRAINT admission_provenance_lease_link_check
        CHECK (
            (
                state = 'CONSUMED'
                AND lease_id IS NOT NULL
                AND lease_generation IS NOT NULL
            )
            OR
            (
                state <> 'CONSUMED'
                AND lease_id IS NULL
                AND lease_generation IS NULL
            )
        ),

    CONSTRAINT admission_provenance_reserved_fields_check
        CHECK (
            (state = 'AVAILABLE' AND reserved_at IS NULL AND resolved_at IS NULL)
            OR (state = 'RESERVED' AND reserved_at IS NOT NULL AND resolved_at IS NULL)
            OR (state = 'CONSUMED' AND reserved_at IS NOT NULL AND resolved_at IS NOT NULL)
            OR (state = 'INVALIDATED' AND reserved_at IS NOT NULL AND resolved_at IS NOT NULL)
        ),

    CONSTRAINT admission_provenance_terminal_code_check
        CHECK (
            (state = 'INVALIDATED' AND terminal_code IN ('T1_ROLLBACK', 'OWNER_INVALIDATED', 'PROVIDER_REJECTED', 'RECOVERY_REQUIRED', 'CONSUME_ABORTED'))
            OR (state <> 'INVALIDATED' AND terminal_code IS NULL)
        ),

    CONSTRAINT admission_provenance_timestamp_order_check
        CHECK (
            (reserved_at IS NULL OR reserved_at >= created_at)
            AND (resolved_at IS NULL OR resolved_at >= COALESCE(reserved_at, created_at))
        ),

    CONSTRAINT admission_provenance_lease_identity_fk
        FOREIGN KEY (lease_id, lease_generation, attempt_id)
        REFERENCES security_control.admission_leases
            (lease_id, generation, attempt_id)
        ON UPDATE RESTRICT
        ON DELETE RESTRICT,

    CONSTRAINT admission_provenance_lease_operation_fk
        FOREIGN KEY (operation_id, lease_generation)
        REFERENCES security_control.admission_leases
            (operation_id, generation)
        ON UPDATE RESTRICT
        ON DELETE RESTRICT
)
USING heap;

CREATE UNIQUE INDEX admission_provenance_available_identity
    ON security_control.admission_provenance USING btree
        (provenance_id ASC, provenance_version ASC)
    WHERE state = 'AVAILABLE';

CREATE UNIQUE INDEX admission_provenance_reserved_identity
    ON security_control.admission_provenance USING btree
        (provenance_id ASC, provenance_version ASC)
    WHERE state = 'RESERVED';
