CREATE TABLE local_remote_job_projections (
    job_id TEXT NOT NULL PRIMARY KEY CHECK (length(job_id) > 0),
    session_id TEXT NOT NULL CHECK (length(session_id) > 0),
    command_id TEXT NOT NULL CHECK (length(command_id) > 0),
    job_phase TEXT NOT NULL CHECK (job_phase IN ('creating_session', 'accepting_command', 'awaiting_command', 'closing_session', 'complete', 'failed', 'lost')),
    command_state TEXT,
    exit_code INTEGER,
    final_event_sequence INTEGER CHECK (final_event_sequence IS NULL OR final_event_sequence > 0),
    output_complete INTEGER NOT NULL CHECK (output_complete IN (0, 1)),
    output_truncated INTEGER NOT NULL CHECK (output_truncated IN (0, 1)),
    output_unavailable_reason TEXT NOT NULL DEFAULT '',
    teardown_state TEXT NOT NULL CHECK (teardown_state IN ('pending', 'closed', 'failed', 'lost', 'not_created')),
    teardown_reason TEXT NOT NULL DEFAULT '',
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

CREATE INDEX ix_local_remote_job_projections_session
    ON local_remote_job_projections(session_id, command_id);
