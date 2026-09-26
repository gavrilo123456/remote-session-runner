CREATE TABLE local_remote_event_gaps (
    command_id TEXT NOT NULL PRIMARY KEY CHECK (length(command_id) > 0),
    missing_from INTEGER NOT NULL CHECK (missing_from > 0),
    missing_to INTEGER NOT NULL CHECK (missing_to >= missing_from),
    available_sequence INTEGER NOT NULL CHECK (available_sequence >= 0),
    final_sequence INTEGER NOT NULL CHECK (final_sequence >= missing_to),
    terminal_state TEXT NOT NULL CHECK (terminal_state IN ('succeeded', 'failed', 'cancelled', 'timed_out', 'rejected', 'lost')),
    output_complete INTEGER NOT NULL DEFAULT 0 CHECK (output_complete = 0),
    output_unavailable_reason TEXT NOT NULL CHECK (output_unavailable_reason = 'remote_event_gap'),
    confirmed_at TEXT NOT NULL CHECK (length(confirmed_at) > 0)
);

CREATE INDEX ix_local_remote_event_gaps_sequence
    ON local_remote_event_gaps(command_id, missing_from, missing_to);
