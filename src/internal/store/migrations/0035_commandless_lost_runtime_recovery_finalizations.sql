-- A commandless lost session can retain only its session reservation. It
-- cannot use the command-pair finalization ledger because no exec_commands
-- row exists. Keep a separate durable finalization record so a crash after
-- reservation release and before ownership-marker removal is retried before
-- startup audits runtime ownership.
CREATE TABLE exec_commandless_lost_runtime_recovery_finalizations (
    session_id TEXT NOT NULL PRIMARY KEY,
    job_id TEXT NOT NULL UNIQUE,
    capacity_released_at TEXT NOT NULL CHECK (length(capacity_released_at) > 0),
    FOREIGN KEY (session_id) REFERENCES exec_sessions(session_id) ON DELETE RESTRICT,
    FOREIGN KEY (job_id) REFERENCES exec_jobs(job_id) ON DELETE RESTRICT
) STRICT;

CREATE INDEX ix_exec_commandless_lost_runtime_recovery_finalizations_pending
    ON exec_commandless_lost_runtime_recovery_finalizations(capacity_released_at, session_id);
