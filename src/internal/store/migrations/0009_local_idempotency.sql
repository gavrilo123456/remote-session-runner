CREATE TABLE local_idempotency (
    controller_type TEXT NOT NULL CHECK (controller_type IN ('local_user', 'queued_mac', 'direct_mtls')),
    controller_id TEXT NOT NULL CHECK (length(controller_id) > 0),
    operation TEXT NOT NULL CHECK (operation IN ('create_session', 'submit_command', 'cancel_command', 'close_session', 'run')),
    idempotency_key TEXT NOT NULL CHECK (length(idempotency_key) > 0),
    request_hash_version INTEGER NOT NULL CHECK (request_hash_version > 0),
    request_hash BLOB NOT NULL CHECK (length(request_hash) = 32),
    intent_id TEXT NOT NULL CHECK (length(intent_id) > 0),
    resource_id TEXT NOT NULL CHECK (length(resource_id) > 0),
    created_at TEXT NOT NULL CHECK (length(created_at) > 0),
    expires_at TEXT NOT NULL CHECK (length(expires_at) > 0),
    PRIMARY KEY (controller_type, controller_id, operation, idempotency_key),
    FOREIGN KEY (intent_id) REFERENCES local_intents(intent_id) ON DELETE RESTRICT
);

CREATE INDEX ix_local_idempotency_expiry
    ON local_idempotency(expires_at, controller_type, controller_id, operation);
