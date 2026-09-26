CREATE TABLE exec_jobs (
    job_id TEXT NOT NULL PRIMARY KEY CHECK (length(job_id) > 0),
    session_id TEXT NOT NULL CHECK (length(session_id) > 0),
    command_id TEXT NOT NULL CHECK (length(command_id) > 0),
    controller_type TEXT NOT NULL CHECK (controller_type IN ('local_user', 'queued_mac', 'direct_mtls')),
    controller_id TEXT NOT NULL CHECK (length(controller_id) > 0),
    target_kind TEXT NOT NULL CHECK (target_kind IN ('local', 'remote')),
    target_profile TEXT NOT NULL CHECK (length(target_profile) > 0),
    environment TEXT NOT NULL CHECK (length(environment) > 0),
    source_mode TEXT NOT NULL CHECK (source_mode IN ('empty', 'git_revision', 'local_worktree')),
    source_repository_alias TEXT NOT NULL DEFAULT '',
    source_requested_revision TEXT NOT NULL DEFAULT '',
    source_path TEXT NOT NULL DEFAULT '',
    request_hash_version INTEGER NOT NULL CHECK (request_hash_version > 0),
    request_hash BLOB NOT NULL CHECK (length(request_hash) = 32),
    idempotency_key TEXT NOT NULL CHECK (length(idempotency_key) > 0),
    payload_json BLOB NOT NULL CHECK (length(payload_json) > 0),
    script_bytes BLOB NOT NULL,
    script_sha256 BLOB NOT NULL CHECK (length(script_sha256) = 32),
    phase TEXT NOT NULL CHECK (phase IN ('creating_session', 'accepting_command', 'awaiting_command', 'closing_session', 'complete', 'failed', 'lost')),
    command_state TEXT CHECK (command_state IS NULL OR command_state IN ('queued', 'running', 'cancelling', 'succeeded', 'failed', 'cancelled', 'timed_out', 'rejected', 'lost')),
    exit_code INTEGER,
    final_event_sequence INTEGER CHECK (final_event_sequence IS NULL OR final_event_sequence > 0),
    output_truncated INTEGER NOT NULL DEFAULT 0 CHECK (output_truncated IN (0, 1)),
    output_complete INTEGER NOT NULL DEFAULT 0 CHECK (output_complete IN (0, 1)),
    output_unavailable_reason TEXT NOT NULL DEFAULT '',
    teardown_state TEXT NOT NULL CHECK (teardown_state IN ('pending', 'closed', 'failed', 'lost', 'not_created')),
    teardown_reason TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL CHECK (length(created_at) > 0),
    updated_at TEXT NOT NULL CHECK (length(updated_at) > 0)
);

CREATE UNIQUE INDEX ux_exec_jobs_session ON exec_jobs(session_id);
CREATE UNIQUE INDEX ux_exec_jobs_command ON exec_jobs(command_id);
CREATE UNIQUE INDEX ux_exec_jobs_controller_key
    ON exec_jobs(controller_type, controller_id, idempotency_key);
CREATE INDEX ix_exec_jobs_phase ON exec_jobs(phase, updated_at);
