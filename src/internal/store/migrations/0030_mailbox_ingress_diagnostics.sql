-- P164 stores safely classified malformed mailbox input separately from an
-- accepted mailbox exchange. The frozen diagnostic never includes raw input,
-- a script, an operation, an idempotency key, a target, or parser details.
CREATE TABLE mailbox_ingress_diagnostics (
    mailbox_id TEXT NOT NULL CHECK (length(mailbox_id) BETWEEN 1 AND 128),
    client_request_id TEXT NOT NULL CHECK (length(client_request_id) BETWEEN 1 AND 256),
    request_sha256 BLOB NOT NULL CHECK (length(request_sha256) = 32),
    diagnostic_code TEXT NOT NULL CHECK (diagnostic_code IN (
        'malformed_json',
        'invalid_request_schema',
        'request_identity_mismatch',
        'invalid_script',
        'request_too_large',
        'request_id_reused_after_rejection'
    )),
    diagnostic_revision INTEGER NOT NULL CHECK (diagnostic_revision = 1),
    diagnostic_bytes BLOB NOT NULL CHECK (length(diagnostic_bytes) BETWEEN 2 AND 1048576),
    diagnostic_sha256 BLOB NOT NULL CHECK (length(diagnostic_sha256) = 32),
    observed_at TEXT NOT NULL CHECK (length(observed_at) > 0),
    projected_at TEXT,
    input_cleanup_started_at TEXT,
    input_pair_removed_at TEXT,
    diagnostic_cleanup_at TEXT NOT NULL CHECK (length(diagnostic_cleanup_at) > 0),
    diagnostic_cleanup_started_at TEXT,
    diagnostic_file_removed_at TEXT,
    PRIMARY KEY (mailbox_id, client_request_id)
) STRICT;

CREATE INDEX ix_mailbox_ingress_diagnostics_recovery
    ON mailbox_ingress_diagnostics(mailbox_id, diagnostic_file_removed_at,
       diagnostic_cleanup_started_at, input_pair_removed_at, observed_at,
       client_request_id);

CREATE INDEX ix_mailbox_ingress_diagnostics_cleanup
    ON mailbox_ingress_diagnostics(diagnostic_cleanup_at,
       diagnostic_cleanup_started_at, diagnostic_file_removed_at);

CREATE INDEX ix_mailbox_ingress_diagnostics_metadata_gc
    ON mailbox_ingress_diagnostics(observed_at, input_pair_removed_at,
       diagnostic_file_removed_at);

-- The diagnostic's identity, raw-byte fingerprint, and frozen safe output
-- are immutable. Lifecycle timestamps are advanced only by store methods.
CREATE TRIGGER mailbox_ingress_diagnostics_frozen_core
BEFORE UPDATE ON mailbox_ingress_diagnostics
WHEN NEW.mailbox_id != OLD.mailbox_id
  OR NEW.client_request_id != OLD.client_request_id
  OR NEW.request_sha256 != OLD.request_sha256
  OR NEW.diagnostic_code != OLD.diagnostic_code
  OR NEW.diagnostic_revision != OLD.diagnostic_revision
  OR NEW.diagnostic_bytes != OLD.diagnostic_bytes
  OR NEW.diagnostic_sha256 != OLD.diagnostic_sha256
  OR NEW.observed_at != OLD.observed_at
  OR NEW.diagnostic_cleanup_at != OLD.diagnostic_cleanup_at
BEGIN
    SELECT RAISE(ABORT, 'mailbox ingress diagnostic frozen core is immutable');
END;

-- Lifecycle timestamps advance one stage at a time. They are intentionally
-- separate from the frozen core because a process can stop between a durable
-- record, projection, pair cleanup, and artifact cleanup; once a stage is
-- recorded it cannot be cleared, changed, or skipped by a later writer.
CREATE TRIGGER mailbox_ingress_diagnostics_monotonic_lifecycle
BEFORE UPDATE ON mailbox_ingress_diagnostics
WHEN (OLD.projected_at IS NOT NULL AND NEW.projected_at IS NOT OLD.projected_at)
  OR (OLD.input_cleanup_started_at IS NOT NULL AND NEW.input_cleanup_started_at IS NOT OLD.input_cleanup_started_at)
  OR (OLD.input_pair_removed_at IS NOT NULL AND NEW.input_pair_removed_at IS NOT OLD.input_pair_removed_at)
  OR (OLD.diagnostic_cleanup_started_at IS NOT NULL AND NEW.diagnostic_cleanup_started_at IS NOT OLD.diagnostic_cleanup_started_at)
  OR (OLD.diagnostic_file_removed_at IS NOT NULL AND NEW.diagnostic_file_removed_at IS NOT OLD.diagnostic_file_removed_at)
  OR (NEW.input_cleanup_started_at IS NOT NULL AND NEW.projected_at IS NULL)
  OR (NEW.input_pair_removed_at IS NOT NULL AND NEW.input_cleanup_started_at IS NULL)
  OR (OLD.projected_at IS NULL AND NEW.input_cleanup_started_at IS NOT NULL)
  OR (OLD.input_cleanup_started_at IS NULL AND NEW.input_pair_removed_at IS NOT NULL)
  OR (NEW.diagnostic_cleanup_started_at IS NOT NULL AND NEW.input_pair_removed_at IS NULL)
  OR (OLD.input_pair_removed_at IS NULL AND NEW.diagnostic_cleanup_started_at IS NOT NULL)
  OR (NEW.diagnostic_file_removed_at IS NOT NULL AND NEW.diagnostic_cleanup_started_at IS NULL)
  OR (OLD.diagnostic_cleanup_started_at IS NULL AND NEW.diagnostic_file_removed_at IS NOT NULL)
BEGIN
    SELECT RAISE(ABORT, 'mailbox ingress diagnostic lifecycle is monotonic');
END;

-- A request identity with a retained malformed-input diagnostic can never
-- become a normal accepted exchange. Both directions are guarded because the
-- database is the final boundary if a caller bypasses the mailbox processor.
CREATE TRIGGER mailbox_ingress_diagnostics_reject_exchange_identity
BEFORE INSERT ON mailbox_ingress_diagnostics
WHEN EXISTS (
    SELECT 1 FROM mailbox_exchanges
    WHERE mailbox_id = NEW.mailbox_id
      AND client_request_id = NEW.client_request_id
)
BEGIN
    SELECT RAISE(ABORT, 'mailbox ingress diagnostic conflicts with accepted exchange');
END;

CREATE TRIGGER mailbox_exchanges_reject_ingress_diagnostic_identity
BEFORE INSERT ON mailbox_exchanges
WHEN EXISTS (
    SELECT 1 FROM mailbox_ingress_diagnostics
    WHERE mailbox_id = NEW.mailbox_id
      AND client_request_id = NEW.client_request_id
)
BEGIN
    SELECT RAISE(ABORT, 'mailbox exchange conflicts with ingress diagnostic');
END;
