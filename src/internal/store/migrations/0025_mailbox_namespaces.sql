CREATE TABLE mailbox_exchanges_v25 (
    exchange_id TEXT NOT NULL PRIMARY KEY CHECK (length(exchange_id) > 0),
    mailbox_id TEXT NOT NULL CHECK (length(mailbox_id) > 0),
    client_request_id TEXT NOT NULL CHECK (length(client_request_id) > 0),
    operation TEXT NOT NULL CHECK (length(operation) > 0),
    controller_type TEXT NOT NULL CHECK (controller_type IN ('local_user', 'queued_mac', 'direct_mtls')),
    controller_id TEXT NOT NULL CHECK (length(controller_id) > 0),
    client_idempotency_key TEXT NOT NULL DEFAULT '',
    execution_idempotency_key TEXT NOT NULL DEFAULT '',
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
    updated_at TEXT NOT NULL CHECK (length(updated_at) > 0),
    response_bytes BLOB,
    response_sha256 BLOB,
    acknowledged_at TEXT,
    response_cleanup_at TEXT,
    response_cleanup_started_at TEXT,
    response_file_removed_at TEXT,
    idempotency_key_expires_at TEXT,
    idempotency_binding_active INTEGER NOT NULL DEFAULT 1 CHECK (idempotency_binding_active IN (0, 1)),
    deduplication_warning INTEGER NOT NULL DEFAULT 0 CHECK (deduplication_warning IN (0, 1)),
    UNIQUE (mailbox_id, client_request_id)
);

INSERT INTO mailbox_exchanges_v25 (
    exchange_id, mailbox_id, client_request_id, operation, controller_type,
    controller_id, client_idempotency_key, execution_idempotency_key,
    canonical_hash_version, canonical_hash, canonical_payload, resource_id,
    request_state, response_revision, terminal_response_bytes,
    terminal_response_sha256, available_event_sequence, created_at, updated_at,
    response_bytes, response_sha256, acknowledged_at, response_cleanup_at,
    response_cleanup_started_at, response_file_removed_at,
    idempotency_key_expires_at, idempotency_binding_active, deduplication_warning
)
SELECT
    'mbx-exchange-v1-' || lower(hex('default' || X'00' || request_id)),
    'default', request_id, operation, controller_type, controller_id,
    idempotency_key, idempotency_key, canonical_hash_version, canonical_hash,
    canonical_payload, resource_id, request_state, response_revision,
    terminal_response_bytes, terminal_response_sha256, available_event_sequence,
    created_at, updated_at, response_bytes, response_sha256, acknowledged_at,
    response_cleanup_at, response_cleanup_started_at, response_file_removed_at,
    idempotency_key_expires_at, idempotency_binding_active, deduplication_warning
FROM mailbox_exchanges;

CREATE TABLE mailbox_event_file_references_v25 (
    exchange_id TEXT NOT NULL PRIMARY KEY,
    command_id TEXT NOT NULL,
    created_at TEXT NOT NULL CHECK (length(created_at) > 0),
    FOREIGN KEY (exchange_id) REFERENCES mailbox_exchanges_v25(exchange_id) ON DELETE RESTRICT,
    FOREIGN KEY (command_id) REFERENCES exec_commands(command_id) ON DELETE RESTRICT
);

INSERT INTO mailbox_event_file_references_v25 (exchange_id, command_id, created_at)
SELECT exchanges.exchange_id, references_v24.command_id, references_v24.created_at
FROM mailbox_event_file_references AS references_v24
JOIN mailbox_exchanges_v25 AS exchanges
  ON exchanges.mailbox_id = 'default' AND exchanges.client_request_id = references_v24.request_id;

CREATE TABLE mailbox_remote_event_file_references_v25 (
    exchange_id TEXT NOT NULL PRIMARY KEY,
    command_id TEXT NOT NULL,
    created_at TEXT NOT NULL CHECK (length(created_at) > 0),
    FOREIGN KEY (exchange_id) REFERENCES mailbox_exchanges_v25(exchange_id) ON DELETE RESTRICT,
    FOREIGN KEY (command_id) REFERENCES local_remote_command_projections(command_id) ON DELETE RESTRICT
);

INSERT INTO mailbox_remote_event_file_references_v25 (exchange_id, command_id, created_at)
SELECT exchanges.exchange_id, references_v24.command_id, references_v24.created_at
FROM mailbox_remote_event_file_references AS references_v24
JOIN mailbox_exchanges_v25 AS exchanges
  ON exchanges.mailbox_id = 'default' AND exchanges.client_request_id = references_v24.request_id;

CREATE TABLE mailbox_event_file_cleanup_v25 (
    mailbox_id TEXT NOT NULL,
    command_id TEXT NOT NULL,
    cleanup_started_at TEXT NOT NULL CHECK (length(cleanup_started_at) > 0),
    file_removed_at TEXT,
    PRIMARY KEY (mailbox_id, command_id),
    FOREIGN KEY (command_id) REFERENCES exec_commands(command_id) ON DELETE RESTRICT
);

INSERT INTO mailbox_event_file_cleanup_v25 (mailbox_id, command_id, cleanup_started_at, file_removed_at)
SELECT 'default', command_id, cleanup_started_at, file_removed_at
FROM mailbox_event_file_cleanup;

CREATE TABLE mailbox_remote_event_file_cleanup_v25 (
    mailbox_id TEXT NOT NULL,
    command_id TEXT NOT NULL,
    cleanup_started_at TEXT NOT NULL CHECK (length(cleanup_started_at) > 0),
    file_removed_at TEXT,
    PRIMARY KEY (mailbox_id, command_id),
    FOREIGN KEY (command_id) REFERENCES local_remote_command_projections(command_id) ON DELETE RESTRICT
);

INSERT INTO mailbox_remote_event_file_cleanup_v25 (mailbox_id, command_id, cleanup_started_at, file_removed_at)
SELECT 'default', command_id, cleanup_started_at, file_removed_at
FROM mailbox_remote_event_file_cleanup;

DROP TABLE mailbox_event_file_references;
DROP TABLE mailbox_remote_event_file_references;
DROP TABLE mailbox_event_file_cleanup;
DROP TABLE mailbox_remote_event_file_cleanup;
DROP TABLE mailbox_exchanges;

ALTER TABLE mailbox_exchanges_v25 RENAME TO mailbox_exchanges;
ALTER TABLE mailbox_event_file_references_v25 RENAME TO mailbox_event_file_references;
ALTER TABLE mailbox_remote_event_file_references_v25 RENAME TO mailbox_remote_event_file_references;
ALTER TABLE mailbox_event_file_cleanup_v25 RENAME TO mailbox_event_file_cleanup;
ALTER TABLE mailbox_remote_event_file_cleanup_v25 RENAME TO mailbox_remote_event_file_cleanup;

CREATE INDEX ix_mailbox_exchanges_key
    ON mailbox_exchanges(mailbox_id, controller_type, controller_id, operation, client_idempotency_key, created_at, exchange_id);

CREATE INDEX ix_mailbox_exchanges_state
    ON mailbox_exchanges(mailbox_id, request_state, updated_at);

CREATE INDEX ix_mailbox_exchanges_active_key
    ON mailbox_exchanges(mailbox_id, controller_type, controller_id, operation, client_idempotency_key, created_at, exchange_id)
    WHERE client_idempotency_key <> '' AND idempotency_binding_active = 1;

CREATE INDEX ix_mailbox_event_file_references_command
    ON mailbox_event_file_references(command_id, exchange_id);

CREATE INDEX ix_mailbox_remote_event_file_references_command
    ON mailbox_remote_event_file_references(command_id, exchange_id);
