CREATE TABLE exec_commands (
    command_id TEXT NOT NULL PRIMARY KEY CHECK (length(command_id) > 0),
    session_id TEXT NOT NULL,
    ordinal INTEGER NOT NULL CHECK (ordinal > 0),
    intent_ordinal INTEGER CHECK (intent_ordinal IS NULL OR intent_ordinal > 0),
    request_hash_version INTEGER NOT NULL CHECK (request_hash_version > 0),
    request_hash BLOB NOT NULL CHECK (length(request_hash) = 32),
    script_bytes BLOB NOT NULL,
    script_sha256 BLOB NOT NULL CHECK (length(script_sha256) = 32),
    state TEXT NOT NULL CHECK (state IN ('queued', 'running', 'cancelling', 'succeeded', 'failed', 'cancelled', 'timed_out', 'rejected', 'lost')),
    timeout_ns INTEGER NOT NULL CHECK (timeout_ns > 0),
    exit_code INTEGER,
    final_event_sequence INTEGER CHECK (final_event_sequence IS NULL OR final_event_sequence > 0),
    output_truncated INTEGER NOT NULL DEFAULT 0 CHECK (output_truncated IN (0, 1)),
    output_complete INTEGER NOT NULL DEFAULT 0 CHECK (output_complete IN (0, 1)),
    created_at TEXT NOT NULL CHECK (length(created_at) > 0),
    updated_at TEXT NOT NULL CHECK (length(updated_at) > 0),
    UNIQUE (session_id, ordinal),
    FOREIGN KEY (session_id) REFERENCES exec_sessions(session_id) ON DELETE RESTRICT
);

CREATE INDEX ix_exec_commands_eligibility
    ON exec_commands(session_id, ordinal, state);

CREATE TABLE exec_command_events (
    command_id TEXT NOT NULL,
    sequence INTEGER NOT NULL CHECK (sequence > 0),
    event_type TEXT NOT NULL CHECK (event_type IN (
        'command_queued', 'command_started', 'stdout', 'stderr',
        'output_truncated', 'command_succeeded', 'command_failed',
        'command_cancelled', 'command_timed_out', 'command_rejected',
        'command_lost'
    )),
    payload BLOB NOT NULL DEFAULT X'',
    byte_count INTEGER NOT NULL DEFAULT 0 CHECK (byte_count >= 0),
    occurred_at TEXT NOT NULL CHECK (length(occurred_at) > 0),
    PRIMARY KEY (command_id, sequence),
    FOREIGN KEY (command_id) REFERENCES exec_commands(command_id) ON DELETE RESTRICT
);

CREATE INDEX ix_exec_command_events_command
    ON exec_command_events(command_id, sequence);
