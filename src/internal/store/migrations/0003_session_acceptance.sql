CREATE TABLE exec_capacity_reservations (
    session_id TEXT NOT NULL PRIMARY KEY,
    host_key TEXT NOT NULL CHECK (length(host_key) > 0),
    reserved_at TEXT NOT NULL CHECK (length(reserved_at) > 0),
    cleanup_confirmed_at TEXT,
    released_at TEXT,
    CHECK (
        (cleanup_confirmed_at IS NULL AND released_at IS NULL)
        OR (cleanup_confirmed_at IS NOT NULL AND released_at IS NOT NULL)
    ),
    FOREIGN KEY (session_id) REFERENCES exec_sessions(session_id) ON DELETE RESTRICT
);

CREATE INDEX ix_exec_capacity_reservations_live
    ON exec_capacity_reservations(host_key, cleanup_confirmed_at, released_at);

CREATE TABLE exec_idempotency (
    controller_type TEXT NOT NULL CHECK (controller_type IN ('local_user', 'queued_mac', 'direct_mtls')),
    controller_id TEXT NOT NULL CHECK (length(controller_id) > 0),
    operation TEXT NOT NULL CHECK (length(operation) > 0),
    idempotency_key TEXT NOT NULL CHECK (length(idempotency_key) > 0),
    canonical_hash_version INTEGER NOT NULL CHECK (canonical_hash_version > 0),
    canonical_hash BLOB NOT NULL CHECK (length(canonical_hash) = 32),
    resource_id TEXT NOT NULL CHECK (length(resource_id) > 0),
    created_at TEXT NOT NULL CHECK (length(created_at) > 0),
    expires_at TEXT NOT NULL CHECK (length(expires_at) > 0),
    PRIMARY KEY (controller_type, controller_id, operation, idempotency_key)
);

CREATE INDEX ix_exec_idempotency_expiry
    ON exec_idempotency(expires_at);
