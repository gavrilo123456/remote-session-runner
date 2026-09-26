CREATE TABLE local_remote_session_projections (
    session_id TEXT NOT NULL PRIMARY KEY CHECK (length(session_id) > 0),
    target_kind TEXT NOT NULL CHECK (target_kind = 'remote'),
    target_profile TEXT NOT NULL CHECK (length(target_profile) > 0),
    controller_type TEXT NOT NULL CHECK (length(controller_type) > 0),
    controller_id TEXT NOT NULL CHECK (length(controller_id) > 0),
    session_state TEXT NOT NULL CHECK (session_state IN ('requested', 'creating', 'ready', 'busy', 'closing', 'closed', 'expired', 'failed', 'lost')),
    environment TEXT NOT NULL CHECK (length(environment) > 0),
    source_json TEXT NOT NULL CHECK (length(source_json) > 0),
    capabilities_json TEXT NOT NULL CHECK (length(capabilities_json) > 0),
    runtime_generation TEXT NOT NULL DEFAULT '',
    resolved_revision TEXT NOT NULL DEFAULT '',
    observed_at TEXT NOT NULL CHECK (length(observed_at) > 0),
    is_stale INTEGER NOT NULL DEFAULT 0 CHECK (is_stale IN (0, 1))
);

CREATE TABLE local_remote_command_projections (
    command_id TEXT NOT NULL PRIMARY KEY CHECK (length(command_id) > 0),
    session_id TEXT NOT NULL CHECK (length(session_id) > 0),
    ordinal INTEGER NOT NULL CHECK (ordinal > 0),
    command_state TEXT NOT NULL CHECK (command_state IN ('queued', 'running', 'cancelling', 'succeeded', 'failed', 'cancelled', 'timed_out', 'rejected', 'lost')),
    exit_code INTEGER,
    final_event_sequence INTEGER CHECK (final_event_sequence IS NULL OR final_event_sequence > 0),
    output_complete INTEGER NOT NULL CHECK (output_complete IN (0, 1)),
    output_truncated INTEGER NOT NULL CHECK (output_truncated IN (0, 1)),
    output_unavailable_reason TEXT NOT NULL DEFAULT '',
    target_kind TEXT NOT NULL CHECK (target_kind = 'remote'),
    target_profile TEXT NOT NULL CHECK (length(target_profile) > 0),
    controller_type TEXT NOT NULL CHECK (length(controller_type) > 0),
    controller_id TEXT NOT NULL CHECK (length(controller_id) > 0),
    environment TEXT NOT NULL CHECK (length(environment) > 0),
    source_json TEXT NOT NULL CHECK (length(source_json) > 0),
    capabilities_json TEXT NOT NULL CHECK (length(capabilities_json) > 0),
    observed_at TEXT NOT NULL CHECK (length(observed_at) > 0),
    is_stale INTEGER NOT NULL DEFAULT 0 CHECK (is_stale IN (0, 1))
);

CREATE INDEX ix_local_remote_session_projections_observed
    ON local_remote_session_projections(observed_at, session_id);

CREATE INDEX ix_local_remote_command_projections_session_ordinal
    ON local_remote_command_projections(session_id, ordinal);
