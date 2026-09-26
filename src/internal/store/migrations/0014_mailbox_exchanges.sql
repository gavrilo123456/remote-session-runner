CREATE TABLE mailbox_exchanges (
    request_id TEXT NOT NULL PRIMARY KEY CHECK (length(request_id) > 0),
    operation TEXT NOT NULL CHECK (length(operation) > 0),
    controller_type TEXT NOT NULL CHECK (controller_type IN ('local_user', 'queued_mac', 'direct_mtls')),
    controller_id TEXT NOT NULL CHECK (length(controller_id) > 0),
    idempotency_key TEXT NOT NULL DEFAULT '',
    canonical_hash_version INTEGER NOT NULL CHECK (canonical_hash_version > 0),
    canonical_hash BLOB NOT NULL CHECK (length(canonical_hash) = 32),
    canonical_payload BLOB NOT NULL CHECK (length(canonical_payload) > 0),
    resource_id TEXT NOT NULL DEFAULT '',
    request_state TEXT NOT NULL CHECK (request_state IN ('accepted', 'complete', 'rejected', 'indeterminate')),
    response_revision INTEGER NOT NULL DEFAULT 0 CHECK (response_revision >= 0),
    terminal_response_bytes BLOB,
    terminal_response_sha256 BLOB,
    available_event_sequence INTEGER CHECK (available_event_sequence IS NULL OR available_event_sequence >= 0),
    created_at TEXT NOT NULL CHECK (length(created_at) > 0),
    updated_at TEXT NOT NULL CHECK (length(updated_at) > 0)
);

CREATE INDEX ix_mailbox_exchanges_key
    ON mailbox_exchanges(controller_type, controller_id, operation, idempotency_key, created_at, request_id);

CREATE INDEX ix_mailbox_exchanges_state
    ON mailbox_exchanges(request_state, updated_at);
