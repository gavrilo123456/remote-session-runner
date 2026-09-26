CREATE TABLE exec_command_slots (
    command_id TEXT NOT NULL PRIMARY KEY,
    host_key TEXT NOT NULL CHECK (length(host_key) > 0),
    reserved_at TEXT NOT NULL CHECK (length(reserved_at) > 0),
    stop_confirmed_at TEXT,
    released_at TEXT,
    CHECK (
        (stop_confirmed_at IS NULL AND released_at IS NULL)
        OR (stop_confirmed_at IS NOT NULL AND released_at IS NOT NULL)
    ),
    FOREIGN KEY (command_id) REFERENCES exec_commands(command_id) ON DELETE RESTRICT
);

CREATE INDEX ix_exec_command_slots_live
    ON exec_command_slots(host_key, stop_confirmed_at, released_at);
