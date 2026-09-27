CREATE TABLE mailbox_remote_event_file_references (
    request_id TEXT NOT NULL PRIMARY KEY,
    command_id TEXT NOT NULL,
    created_at TEXT NOT NULL CHECK (length(created_at) > 0),
    FOREIGN KEY (request_id) REFERENCES mailbox_exchanges(request_id) ON DELETE RESTRICT,
    FOREIGN KEY (command_id) REFERENCES local_remote_command_projections(command_id) ON DELETE RESTRICT
);

CREATE INDEX ix_mailbox_remote_event_file_references_command
    ON mailbox_remote_event_file_references(command_id, request_id);

CREATE TABLE mailbox_remote_event_file_cleanup (
    command_id TEXT NOT NULL PRIMARY KEY,
    cleanup_started_at TEXT NOT NULL CHECK (length(cleanup_started_at) > 0),
    file_removed_at TEXT,
    FOREIGN KEY (command_id) REFERENCES local_remote_command_projections(command_id) ON DELETE RESTRICT
);
