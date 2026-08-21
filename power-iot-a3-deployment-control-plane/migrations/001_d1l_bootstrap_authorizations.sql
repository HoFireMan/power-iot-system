-- D1L provider authority schema (provider database only).
-- Sole embedded migration source. Raw secret S is never stored; only H(S).
CREATE SCHEMA IF NOT EXISTS d1l_provider;

CREATE TABLE IF NOT EXISTS d1l_provider.schema_version (
  version integer PRIMARY KEY,
  applied_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  CHECK (version > 0)
);
CREATE TABLE IF NOT EXISTS d1l_provider.provider_epochs (
  epoch_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  instance_id uuid NOT NULL,
  started_at timestamptz NOT NULL,
  live boolean NOT NULL DEFAULT true,
  CHECK (epoch_id > 0)
);
CREATE TABLE IF NOT EXISTS d1l_provider.provider_control (
  singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
  schema_version integer NOT NULL DEFAULT 1 CHECK (schema_version = 1),
  current_epoch bigint REFERENCES d1l_provider.provider_epochs(epoch_id),
  instance_id uuid,
  updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
INSERT INTO d1l_provider.provider_control(singleton, schema_version)
VALUES (true, 1) ON CONFLICT (singleton) DO NOTHING;

-- Attempt identity is namespaced by the selected issuer role.
CREATE TABLE IF NOT EXISTS d1l_provider.d1l_issue_requests (
  issuer_request_id uuid PRIMARY KEY,
  issuer_role text NOT NULL CHECK (issuer_role = 'deployment-runbook'),
  attempt_id uuid NOT NULL,
  state text NOT NULL CHECK (state IN ('REQUESTED','ISSUED','TERMINAL','CANCELLED')),
  authorization_id uuid,
  terminal_at timestamptz,
  terminal_code text,
  terminal_consumer text,
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  UNIQUE (issuer_role, attempt_id),
  CHECK (state = 'REQUESTED' OR authorization_id IS NOT NULL OR state IN ('CANCELLED','TERMINAL')),
  CHECK (state <> 'TERMINAL' OR (terminal_at IS NOT NULL AND terminal_code IS NOT NULL AND terminal_consumer IS NOT NULL AND length(btrim(terminal_code)) > 0 AND length(btrim(terminal_consumer)) > 0)),
  CHECK (state <> 'REQUESTED' OR authorization_id IS NULL)
);

CREATE TABLE IF NOT EXISTS d1l_provider.d1l_bootstrap_authorizations (
  authorization_id uuid PRIMARY KEY,
  issuer_request_id uuid NOT NULL UNIQUE REFERENCES d1l_provider.d1l_issue_requests(issuer_request_id),
  epoch_id bigint NOT NULL REFERENCES d1l_provider.provider_epochs(epoch_id),
  nonce bytea NOT NULL CHECK (octet_length(nonce) = 16),
  secret_verifier bytea NOT NULL CHECK (octet_length(secret_verifier) = 32),
  scope text NOT NULL CHECK (scope = 'allow_control_catalog_install'),
  operation text NOT NULL CHECK (length(btrim(operation)) > 0),
  attempt_id uuid NOT NULL,
  target_id text NOT NULL CHECK (length(btrim(target_id)) > 0),
  installer_id text NOT NULL CHECK (length(btrim(installer_id)) > 0),
  evidence_hash text NOT NULL CHECK (length(btrim(evidence_hash)) > 0),
  bindings jsonb NOT NULL,
  state text NOT NULL CHECK (state IN ('ISSUED','CONSUME_PENDING','CONSUMED','REVOKED','EXPIRED','CONSUME_UNKNOWN')),
  expires_at timestamptz NOT NULL,
  revoked_reason text,
  consume_request_id uuid,
  consume_epoch_id bigint,
  consume_issuer_request_id uuid,
  consume_attempt_id uuid,
  consume_nonce bytea,
  consume_operation text,
  consume_target_id text,
  consume_installer_id text,
  consume_evidence_hash text,
  consume_scope text,
  consume_claimed_at timestamptz,
  consume_terminal_at timestamptz,
  consume_terminal_code text,
  consume_consumer text,
  terminal_at timestamptz,
  terminal_code text,
  terminal_consumer text,
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  UNIQUE (authorization_id, issuer_request_id),
  UNIQUE (authorization_id, issuer_request_id, epoch_id, nonce, operation, attempt_id, target_id, installer_id, evidence_hash, scope),
  CHECK (expires_at > created_at),
  CHECK (state NOT IN ('REVOKED','EXPIRED','CONSUME_UNKNOWN') OR revoked_reason IS NOT NULL),
  CHECK (state NOT IN ('ISSUED','CONSUME_PENDING') OR revoked_reason IS NULL),
  CHECK (state NOT IN ('CONSUMED','REVOKED','EXPIRED','CONSUME_UNKNOWN') OR (terminal_at IS NOT NULL AND terminal_code IS NOT NULL AND terminal_consumer IS NOT NULL AND length(btrim(terminal_code)) > 0 AND length(btrim(terminal_consumer)) > 0)),
  CHECK (bindings = jsonb_build_object('operation', operation, 'attempt_id', attempt_id::text, 'target_id', target_id, 'installer_id', installer_id, 'evidence_hash', evidence_hash))
);
DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'issue_auth_fk') THEN
    ALTER TABLE d1l_provider.d1l_issue_requests ADD CONSTRAINT issue_auth_fk FOREIGN KEY (authorization_id)
      REFERENCES d1l_provider.d1l_bootstrap_authorizations(authorization_id) DEFERRABLE INITIALLY DEFERRED;
  END IF;
END $$;
CREATE UNIQUE INDEX IF NOT EXISTS d1l_provider_issue_authorization_unique
  ON d1l_provider.d1l_issue_requests (authorization_id)
  WHERE authorization_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS d1l_provider.d1l_bootstrap_consume_intents (
  consume_request_id uuid PRIMARY KEY,
  authorization_id uuid NOT NULL,
  issuer_request_id uuid NOT NULL,
  epoch_id bigint NOT NULL REFERENCES d1l_provider.provider_epochs(epoch_id),
  nonce bytea NOT NULL CHECK (octet_length(nonce) = 16),
  operation text NOT NULL CHECK (length(btrim(operation)) > 0),
  attempt_id uuid NOT NULL,
  target_id text NOT NULL CHECK (length(btrim(target_id)) > 0),
  installer_id text NOT NULL CHECK (length(btrim(installer_id)) > 0),
  evidence_hash text NOT NULL CHECK (length(btrim(evidence_hash)) > 0),
  scope text NOT NULL CHECK (scope = 'allow_control_catalog_install'),
  consumer_identity text NOT NULL CHECK (length(btrim(consumer_identity)) > 0),
  state text NOT NULL CHECK (state IN ('PENDING','CLAIMED','CONSUMED','ABORTED','CONSUME_UNKNOWN','UNKNOWN')),
  claimed_at timestamptz,
  consumed_at timestamptz,
  terminal_at timestamptz,
  terminal_code text,
  terminal_consumer text,
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  FOREIGN KEY (authorization_id, issuer_request_id) REFERENCES d1l_provider.d1l_bootstrap_authorizations(authorization_id, issuer_request_id),
  FOREIGN KEY (authorization_id, issuer_request_id, epoch_id, nonce, operation, attempt_id, target_id, installer_id, evidence_hash, scope)
    REFERENCES d1l_provider.d1l_bootstrap_authorizations(authorization_id, issuer_request_id, epoch_id, nonce, operation, attempt_id, target_id, installer_id, evidence_hash, scope),
  UNIQUE (consume_request_id, authorization_id),
  CHECK (updated_at >= created_at),
  CHECK (state IN ('PENDING','CLAIMED') OR (terminal_at IS NOT NULL AND terminal_code IS NOT NULL AND terminal_consumer IS NOT NULL AND length(btrim(terminal_code)) > 0 AND length(btrim(terminal_consumer)) > 0)),
  CHECK (state <> 'CLAIMED' OR claimed_at IS NOT NULL)
);
-- There may be only one live presentation child for an authorization. Terminal
-- history remains durable, but cannot strand a second PENDING/CLAIMED child.
CREATE UNIQUE INDEX IF NOT EXISTS d1l_provider_one_live_consume_intent
  ON d1l_provider.d1l_bootstrap_consume_intents (authorization_id)
  WHERE state IN ('PENDING','CLAIMED');

INSERT INTO d1l_provider.schema_version(version) VALUES (1) ON CONFLICT (version) DO NOTHING;
