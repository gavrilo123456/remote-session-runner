-- A paired lost-runtime capacity release must leave a durable finalization
-- work item until the retained ownership marker and owned workspace have been
-- removed. A crash or transient error between those steps must be recoverable
-- before a later runnerd startup audits host ownership.
CREATE TABLE exec_lost_runtime_recovery_finalizations (
    command_id TEXT NOT NULL PRIMARY KEY,
    session_id TEXT NOT NULL UNIQUE,
    capacity_released_at TEXT NOT NULL CHECK (length(capacity_released_at) > 0),
    FOREIGN KEY (command_id) REFERENCES exec_commands(command_id) ON DELETE RESTRICT,
    FOREIGN KEY (session_id) REFERENCES exec_sessions(session_id) ON DELETE RESTRICT
) STRICT;

CREATE INDEX ix_exec_lost_runtime_recovery_finalizations_pending
    ON exec_lost_runtime_recovery_finalizations(capacity_released_at, command_id);

-- Do not backfill historical released-lost rows. They may have been released
-- by the ordinary startup reconciler, which has a different ownership-marker
-- protocol. New rows are created atomically only by the explicit Linux
-- lost-runtime recovery release transaction.
