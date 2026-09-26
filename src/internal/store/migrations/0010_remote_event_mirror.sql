CREATE TABLE local_remote_events (
    command_id TEXT NOT NULL CHECK (length(command_id) > 0),
    sequence INTEGER NOT NULL CHECK (sequence > 0),
    event_type TEXT NOT NULL CHECK (length(event_type) > 0),
    payload BLOB NOT NULL DEFAULT X'',
    byte_count INTEGER NOT NULL DEFAULT 0 CHECK (byte_count >= 0),
    occurred_at TEXT NOT NULL CHECK (length(occurred_at) > 0),
    PRIMARY KEY (command_id, sequence)
);

CREATE INDEX ix_local_remote_events_command
    ON local_remote_events(command_id, sequence);

CREATE TABLE local_remote_event_cursors (
    command_id TEXT NOT NULL PRIMARY KEY CHECK (length(command_id) > 0),
    last_sequence INTEGER NOT NULL CHECK (last_sequence >= 0),
    updated_at TEXT NOT NULL CHECK (length(updated_at) > 0)
);
