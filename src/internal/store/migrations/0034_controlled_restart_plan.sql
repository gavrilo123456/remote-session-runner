-- A controlled restart may preserve exactly one untouched queued empty-source
-- one-off while four terminal-lost runtimes retain the complete command
-- capacity. The plan is a singleton because the authority has one scheduler
-- host key. It contains identities and recovery boundaries only; scripts and
-- canonical request payloads remain in their existing immutable tables.
CREATE TABLE exec_controlled_restart_plans (
    plan_key INTEGER NOT NULL PRIMARY KEY CHECK (plan_key = 1),
    job_id TEXT NOT NULL UNIQUE CHECK (length(job_id) > 0),
    session_id TEXT NOT NULL UNIQUE CHECK (length(session_id) > 0),
    command_id TEXT NOT NULL UNIQUE CHECK (length(command_id) > 0),
    runtime_generation TEXT NOT NULL CHECK (length(runtime_generation) > 0),
    prepared_at TEXT NOT NULL CHECK (length(prepared_at) > 0),
    activation_state TEXT NOT NULL CHECK (activation_state IN ('prepared', 'active')),
    activated_at TEXT,
    CHECK (
        (activation_state = 'prepared' AND activated_at IS NULL)
        OR (activation_state = 'active' AND activated_at IS NOT NULL)
    ),
    FOREIGN KEY (job_id) REFERENCES exec_jobs(job_id) ON DELETE RESTRICT,
    FOREIGN KEY (session_id) REFERENCES exec_sessions(session_id) ON DELETE RESTRICT,
    FOREIGN KEY (command_id) REFERENCES exec_commands(command_id) ON DELETE RESTRICT
) STRICT;

-- The four exact retained terminal-lost session/command pairs that the
-- controlled restart is allowed to release after it has proved their owned
-- runtime boundaries. Ordinal storage makes the durable plan deterministic.
CREATE TABLE exec_controlled_restart_plan_lost_pairs (
    plan_key INTEGER NOT NULL CHECK (plan_key = 1),
    ordinal INTEGER NOT NULL CHECK (ordinal BETWEEN 1 AND 4),
    session_id TEXT NOT NULL CHECK (length(session_id) > 0),
    command_id TEXT NOT NULL CHECK (length(command_id) > 0),
    PRIMARY KEY (plan_key, ordinal),
    UNIQUE (plan_key, session_id),
    UNIQUE (plan_key, command_id),
    FOREIGN KEY (plan_key) REFERENCES exec_controlled_restart_plans(plan_key) ON DELETE CASCADE,
    FOREIGN KEY (session_id) REFERENCES exec_sessions(session_id) ON DELETE RESTRICT,
    FOREIGN KEY (command_id) REFERENCES exec_commands(command_id) ON DELETE RESTRICT
) STRICT;
