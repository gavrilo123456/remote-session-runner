ALTER TABLE exec_idempotency
ADD COLUMN deduplication_warning INTEGER NOT NULL DEFAULT 0 CHECK (deduplication_warning IN (0, 1));

CREATE TABLE exec_idempotency_expiry_warnings (
    controller_type TEXT NOT NULL CHECK (controller_type IN ('local_user', 'queued_mac', 'direct_mtls')),
    controller_id TEXT NOT NULL CHECK (length(controller_id) > 0),
    operation TEXT NOT NULL CHECK (length(operation) > 0),
    key_fingerprint BLOB NOT NULL CHECK (length(key_fingerprint) = 32),
    warning_until TEXT NOT NULL CHECK (length(warning_until) > 0),
    PRIMARY KEY (controller_type, controller_id, operation, key_fingerprint)
);

DROP INDEX ux_exec_jobs_controller_key;

CREATE INDEX ix_exec_jobs_controller_key
    ON exec_jobs(controller_type, controller_id, idempotency_key);
