CREATE SCHEMA security_control;

CREATE SEQUENCE security_control.admission_generation_seq
    AS bigint
    START WITH 1
    INCREMENT BY 1
    MINVALUE 1
    MAXVALUE 9223372036854775807
    CACHE 1
    NO CYCLE;

CREATE TABLE security_control.control_schema_migrations (
    control_version     BIGINT      NOT NULL,
    dirty               BOOLEAN     NOT NULL,
    target_fingerprint  BYTEA       NOT NULL,
    installer_digest    BYTEA       NOT NULL,
    install_id          UUID        NOT NULL,
    installed_at        TIMESTAMPTZ NOT NULL,

    CONSTRAINT control_schema_migrations_pkey
        PRIMARY KEY (control_version),

    CONSTRAINT control_schema_migrations_version_check
        CHECK (control_version = 1),

    CONSTRAINT control_schema_migrations_dirty_check
        CHECK (dirty = false),

    CONSTRAINT control_schema_migrations_target_fingerprint_check
        CHECK (octet_length(target_fingerprint) = 32),

    CONSTRAINT control_schema_migrations_installer_digest_check
        CHECK (octet_length(installer_digest) = 32),

    CONSTRAINT control_schema_migrations_install_id_key
        UNIQUE (install_id)
)
USING heap;

CREATE TABLE security_control.admission_leases (
    lease_id                   UUID        NOT NULL,
    operation_id               UUID        NOT NULL,
    attempt_id                 UUID        NOT NULL,
    generation                 BIGINT      NOT NULL
        DEFAULT nextval('security_control.admission_generation_seq'::regclass),
    target_fingerprint         BYTEA       NOT NULL,
    evidence_digest             BYTEA       NOT NULL,
    capability_verifier_digest BYTEA       NOT NULL,
    status                     TEXT        COLLATE "C" NOT NULL,
    issued_at                  TIMESTAMPTZ NOT NULL,
    expires_at                 TIMESTAMPTZ NOT NULL,
    activated_at               TIMESTAMPTZ NULL,
    terminal_at                TIMESTAMPTZ NULL,
    terminal_code              TEXT        COLLATE "C" NULL,
    quarantined_at             TIMESTAMPTZ NULL,
    quarantine_code            TEXT        COLLATE "C" NULL,
    recovery_digest            BYTEA       NULL,

    CONSTRAINT admission_leases_pkey
        PRIMARY KEY (lease_id),

    CONSTRAINT admission_leases_attempt_id_key
        UNIQUE (attempt_id),

    CONSTRAINT admission_leases_operation_generation_key
        UNIQUE (operation_id, generation),

    CONSTRAINT admission_leases_identity_key
        UNIQUE (lease_id, generation, attempt_id),

    CONSTRAINT admission_leases_status_check
        CHECK (status IN (
            'ISSUED',
            'ACTIVE',
            'QUARANTINE_PENDING',
            'QUARANTINED',
            'CONSUMED',
            'EXPIRED',
            'REVOKED'
        )),

    CONSTRAINT admission_leases_target_fingerprint_check
        CHECK (octet_length(target_fingerprint) = 32),

    CONSTRAINT admission_leases_evidence_digest_check
        CHECK (octet_length(evidence_digest) = 32),

    CONSTRAINT admission_leases_capability_verifier_digest_check
        CHECK (octet_length(capability_verifier_digest) = 32),

    CONSTRAINT admission_leases_generation_check
        CHECK (generation > 0),

    CONSTRAINT admission_leases_expires_after_issued_check
        CHECK (expires_at > issued_at),

    CONSTRAINT admission_leases_terminal_fields_check
        CHECK (
            (
                status IN ('CONSUMED', 'EXPIRED', 'REVOKED')
                AND terminal_at IS NOT NULL
                AND terminal_code IS NOT NULL
            )
            OR
            (
                status IN (
                    'ISSUED',
                    'ACTIVE',
                    'QUARANTINE_PENDING',
                    'QUARANTINED'
                )
                AND terminal_at IS NULL
                AND terminal_code IS NULL
            )
        ),

    CONSTRAINT admission_leases_quarantine_fields_check
        CHECK (
            (
                status IN (
                    'ISSUED',
                    'ACTIVE',
                    'QUARANTINE_PENDING',
                    'CONSUMED',
                    'EXPIRED'
                )
                AND quarantined_at IS NULL
                AND quarantine_code IS NULL
            )
            OR
            (
                status = 'QUARANTINED'
                AND quarantined_at IS NOT NULL
                AND quarantine_code IS NOT NULL
            )
            OR
            (
                status = 'REVOKED'
                AND (
                    (
                        quarantined_at IS NULL
                        AND quarantine_code IS NULL
                    )
                    OR
                    (
                        quarantined_at IS NOT NULL
                        AND quarantine_code IS NOT NULL
                    )
                )
            )
        ),

    CONSTRAINT admission_leases_lifecycle_fields_check
        CHECK (
            (
                status = 'ISSUED'
                AND activated_at IS NULL
                AND terminal_at IS NULL
                AND terminal_code IS NULL
                AND quarantined_at IS NULL
                AND quarantine_code IS NULL
            )
            OR
            (
                status IN ('ACTIVE', 'QUARANTINE_PENDING')
                AND activated_at IS NOT NULL
                AND terminal_at IS NULL
                AND terminal_code IS NULL
                AND quarantined_at IS NULL
                AND quarantine_code IS NULL
            )
            OR
            (
                status = 'QUARANTINED'
                AND activated_at IS NOT NULL
                AND terminal_at IS NULL
                AND terminal_code IS NULL
                AND quarantined_at IS NOT NULL
                AND quarantine_code IS NOT NULL
            )
            OR
            (
                status = 'CONSUMED'
                AND activated_at IS NOT NULL
                AND terminal_at IS NOT NULL
                AND terminal_code IS NOT NULL
                AND quarantined_at IS NULL
                AND quarantine_code IS NULL
            )
            OR
            (
                status = 'EXPIRED'
                AND activated_at IS NULL
                AND terminal_at IS NOT NULL
                AND terminal_code IS NOT NULL
                AND quarantined_at IS NULL
                AND quarantine_code IS NULL
            )
            OR
            (
                status = 'REVOKED'
                AND terminal_at IS NOT NULL
                AND terminal_code IS NOT NULL
                AND (
                    (
                        quarantined_at IS NULL
                        AND quarantine_code IS NULL
                    )
                    OR
                    (
                        quarantined_at IS NOT NULL
                        AND quarantine_code IS NOT NULL
                    )
                )
            )
        ),

    CONSTRAINT admission_leases_recovery_digest_check
        CHECK (
            recovery_digest IS NULL
            OR octet_length(recovery_digest) = 32
        )
)
USING heap;

ALTER SEQUENCE security_control.admission_generation_seq
    OWNED BY security_control.admission_leases.generation;

CREATE UNIQUE INDEX admission_leases_one_live_generation_idx
    ON security_control.admission_leases USING btree (operation_id ASC)
    WHERE status IN (
        'ISSUED',
        'ACTIVE',
        'QUARANTINE_PENDING'
    );

CREATE TABLE security_control.admission_boundaries (
    boundary_id       UUID        NOT NULL,
    lease_id          UUID        NOT NULL,
    attempt_id        UUID        NOT NULL,
    generation        BIGINT      NOT NULL,
    boundary_nonce    UUID        NOT NULL,
    boundary_name     TEXT        COLLATE "C" NOT NULL,
    status            TEXT        COLLATE "C" NOT NULL,
    started_at        TIMESTAMPTZ NOT NULL,
    expires_at        TIMESTAMPTZ NOT NULL,
    closed_at         TIMESTAMPTZ NULL,
    outcome_code      TEXT        COLLATE "C" NULL,

    CONSTRAINT admission_boundaries_pkey
        PRIMARY KEY (boundary_id),

    CONSTRAINT admission_boundaries_boundary_nonce_key
        UNIQUE (boundary_nonce),

    CONSTRAINT admission_boundaries_lease_identity_name_key
        UNIQUE (lease_id, generation, boundary_name),

    CONSTRAINT admission_boundaries_lease_fk
        FOREIGN KEY (lease_id, generation, attempt_id)
        REFERENCES security_control.admission_leases
            (lease_id, generation, attempt_id)
        ON UPDATE RESTRICT
        ON DELETE RESTRICT,

    CONSTRAINT admission_boundaries_generation_check
        CHECK (generation > 0),

    CONSTRAINT admission_boundaries_name_check
        CHECK (boundary_name IN (
            'A2_COMMIT',
            'HANDOFF',
            'DIRTY_MARKER_COMMIT',
            'DDL_COMMIT',
            'FINAL_VERIFY',
            'FINAL_METADATA_COMMIT',
            'RECOVERY_METADATA_COMMIT'
        )),

    CONSTRAINT admission_boundaries_status_check
        CHECK (status IN (
            'OPEN',
            'COMMITTED',
            'ROLLED_BACK',
            'FAILED',
            'UNKNOWN'
        )),

    CONSTRAINT admission_boundaries_expiry_check
        CHECK (expires_at > started_at),

    CONSTRAINT admission_boundaries_open_fields_check
        CHECK (
            (
                status = 'OPEN'
                AND closed_at IS NULL
                AND outcome_code IS NULL
            )
            OR
            (
                status IN (
                    'COMMITTED',
                    'ROLLED_BACK',
                    'FAILED',
                    'UNKNOWN'
                )
                AND closed_at IS NOT NULL
                AND outcome_code IS NOT NULL
            )
        )
)
USING heap;

CREATE UNIQUE INDEX admission_boundaries_one_open_per_lease_idx
    ON security_control.admission_boundaries USING btree (lease_id ASC, generation ASC)
    WHERE status = 'OPEN';
