CREATE TABLE local_intents (
    intent_id TEXT NOT NULL PRIMARY KEY CHECK (length(intent_id) > 0),
    operation TEXT NOT NULL CHECK (operation IN ('create_session', 'submit_command', 'cancel_command', 'close_session', 'run')),
    resource_id TEXT NOT NULL CHECK (length(resource_id) > 0),
    session_id TEXT NOT NULL DEFAULT '',
    command_id TEXT NOT NULL DEFAULT '',
    job_id TEXT NOT NULL DEFAULT '',
    target_kind TEXT NOT NULL CHECK (target_kind IN ('local', 'remote')),
    target_profile TEXT NOT NULL CHECK (length(target_profile) > 0),
    environment TEXT NOT NULL CHECK (length(environment) > 0),
    controller_type TEXT NOT NULL CHECK (controller_type IN ('local_user', 'queued_mac', 'direct_mtls')),
    controller_id TEXT NOT NULL CHECK (length(controller_id) > 0),
    source_mode TEXT NOT NULL CHECK (source_mode IN ('empty', 'git_revision', 'local_worktree')),
    source_repository_alias TEXT NOT NULL DEFAULT '',
    source_requested_revision TEXT NOT NULL DEFAULT '',
    source_path TEXT NOT NULL DEFAULT '',
    request_hash_version INTEGER NOT NULL CHECK (request_hash_version > 0),
    request_hash BLOB NOT NULL CHECK (length(request_hash) = 32),
    idempotency_key TEXT NOT NULL CHECK (length(idempotency_key) > 0),
    payload_json BLOB NOT NULL CHECK (length(payload_json) > 0),
    script_bytes BLOB NOT NULL DEFAULT X'',
    script_sha256 BLOB NOT NULL CHECK (length(script_sha256) = 32),
    intent_ordinal INTEGER CHECK (intent_ordinal IS NULL OR intent_ordinal > 0),
    delivery_state TEXT NOT NULL DEFAULT 'recorded' CHECK (delivery_state IN ('recorded', 'dispatching', 'uncertain', 'accepted', 'reconciled', 'not_delivered')),
    reason TEXT NOT NULL DEFAULT '',
    lease_owner TEXT NOT NULL DEFAULT '',
    lease_expires_at TEXT,
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    created_at TEXT NOT NULL CHECK (length(created_at) > 0),
    updated_at TEXT NOT NULL CHECK (length(updated_at) > 0)
);

CREATE UNIQUE INDEX ux_local_command_intent_order
    ON local_intents(session_id, intent_ordinal)
    WHERE operation = 'submit_command' AND intent_ordinal IS NOT NULL;

CREATE INDEX ix_local_intents_delivery
    ON local_intents(delivery_state, lease_expires_at, created_at, intent_id);

CREATE TABLE local_intent_lifecycle (
    intent_id TEXT NOT NULL,
    lifecycle_sequence INTEGER NOT NULL CHECK (lifecycle_sequence > 0),
    previous_state TEXT,
    new_state TEXT NOT NULL CHECK (new_state IN ('requested', 'recorded', 'dispatching', 'uncertain', 'accepted', 'reconciled', 'not_delivered')),
    reason TEXT NOT NULL DEFAULT '',
    occurred_at TEXT NOT NULL CHECK (length(occurred_at) > 0),
    PRIMARY KEY (intent_id, lifecycle_sequence),
    FOREIGN KEY (intent_id) REFERENCES local_intents(intent_id) ON DELETE RESTRICT,
    CHECK (previous_state IS NULL OR previous_state IN ('requested', 'recorded', 'dispatching', 'uncertain', 'accepted', 'reconciled', 'not_delivered'))
);

CREATE INDEX ix_local_intent_lifecycle_intent
    ON local_intent_lifecycle(intent_id, lifecycle_sequence);
