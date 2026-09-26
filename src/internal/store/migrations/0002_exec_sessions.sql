CREATE TABLE exec_sessions (
    session_id TEXT NOT NULL PRIMARY KEY CHECK (length(session_id) > 0),
    target_kind TEXT NOT NULL CHECK (target_kind IN ('local', 'remote')),
    target_profile TEXT NOT NULL CHECK (length(target_profile) > 0),
    environment TEXT NOT NULL CHECK (length(environment) > 0),
    controller_type TEXT NOT NULL CHECK (controller_type IN ('local_user', 'queued_mac', 'direct_mtls')),
    controller_id TEXT NOT NULL CHECK (length(controller_id) > 0),
    source_mode TEXT NOT NULL CHECK (source_mode IN ('empty', 'git_revision', 'local_worktree')),
    source_repository_alias TEXT NOT NULL DEFAULT '',
    source_requested_revision TEXT NOT NULL DEFAULT '',
    source_path TEXT NOT NULL DEFAULT '',
    source_resolved_revision TEXT NOT NULL DEFAULT '',
    runtime_generation TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL CHECK (state IN ('requested', 'creating', 'ready', 'busy', 'closing', 'closed', 'expired', 'failed', 'lost')),
    command_timeout_ns INTEGER NOT NULL CHECK (command_timeout_ns > 0),
    idle_timeout_ns INTEGER NOT NULL CHECK (idle_timeout_ns > 0),
    session_max_lifetime_ns INTEGER NOT NULL CHECK (session_max_lifetime_ns > 0),
    output_bytes_per_command INTEGER NOT NULL CHECK (output_bytes_per_command > 0),
    created_at TEXT NOT NULL CHECK (length(created_at) > 0),
    updated_at TEXT NOT NULL CHECK (length(updated_at) > 0),
    expires_at TEXT NOT NULL CHECK (length(expires_at) > 0),
    CHECK (
        (source_mode = 'empty' AND source_repository_alias = '' AND source_requested_revision = '' AND source_path = '')
        OR (source_mode = 'git_revision' AND source_repository_alias <> '' AND source_requested_revision <> '' AND source_path = '')
        OR (source_mode = 'local_worktree' AND source_repository_alias = '' AND source_requested_revision = '' AND source_path <> '')
    )
);

CREATE TABLE exec_session_lifecycle (
    session_id TEXT NOT NULL,
    lifecycle_sequence INTEGER NOT NULL CHECK (lifecycle_sequence > 0),
    previous_state TEXT CHECK (previous_state IS NULL OR previous_state IN ('requested', 'creating', 'ready', 'busy', 'closing', 'closed', 'expired', 'failed', 'lost')),
    new_state TEXT NOT NULL CHECK (new_state IN ('requested', 'creating', 'ready', 'busy', 'closing', 'closed', 'expired', 'failed', 'lost')),
    reason TEXT NOT NULL CHECK (length(reason) > 0),
    occurred_at TEXT NOT NULL CHECK (length(occurred_at) > 0),
    PRIMARY KEY (session_id, lifecycle_sequence),
    FOREIGN KEY (session_id) REFERENCES exec_sessions(session_id) ON DELETE RESTRICT
);

CREATE INDEX ix_exec_sessions_state ON exec_sessions(state);
CREATE INDEX ix_exec_session_lifecycle_session ON exec_session_lifecycle(session_id, lifecycle_sequence);

CREATE TRIGGER exec_sessions_immutable_identity
BEFORE UPDATE OF target_kind, target_profile, environment, controller_type, controller_id
ON exec_sessions
WHEN OLD.target_kind <> NEW.target_kind
  OR OLD.target_profile <> NEW.target_profile
  OR OLD.environment <> NEW.environment
  OR OLD.controller_type <> NEW.controller_type
  OR OLD.controller_id <> NEW.controller_id
BEGIN
    SELECT RAISE(ABORT, 'session target/controller identity is immutable');
END;
