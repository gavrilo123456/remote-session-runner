-- A successful command can release its command slot while its ephemeral
-- session teardown is conservatively lost. Keep its session-only release and
-- runtime finalization separate from ordinary lost-command recovery.
CREATE TABLE exec_completed_teardown_recovery_finalizations (
    session_id TEXT NOT NULL PRIMARY KEY,
    command_id TEXT NOT NULL UNIQUE,
    job_id TEXT NOT NULL UNIQUE,
    capacity_released_at TEXT NOT NULL CHECK (length(capacity_released_at) > 0),
    FOREIGN KEY (session_id) REFERENCES exec_sessions(session_id) ON DELETE RESTRICT,
    FOREIGN KEY (command_id) REFERENCES exec_commands(command_id) ON DELETE RESTRICT,
    FOREIGN KEY (job_id) REFERENCES exec_jobs(job_id) ON DELETE RESTRICT
) STRICT;

CREATE INDEX ix_exec_completed_teardown_recovery_finalizations_pending
    ON exec_completed_teardown_recovery_finalizations(capacity_released_at, session_id);
