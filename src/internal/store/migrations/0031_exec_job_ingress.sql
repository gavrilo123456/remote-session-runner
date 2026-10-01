-- Persist the trusted server-side ingress that accepted a one-off job. The
-- default preserves truthful unknown provenance for rows accepted before this
-- migration, whose original transport was not retained.
ALTER TABLE exec_jobs
    ADD COLUMN ingress TEXT NOT NULL DEFAULT 'unknown'
    CHECK (ingress IN ('local_unix', 'mailbox', 'local_executor', 'ssh_bridge', 'direct_mtls', 'internal', 'unknown'));

-- Historical queued jobs can still need a later create/deny audit. Permit the
-- truthful migration-only unknown value while retaining the audit table's
-- append-only triggers and all current mailbox-selection columns.
CREATE TABLE runner_audit_records_v31 (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    principal_type TEXT NOT NULL CHECK (length(principal_type) BETWEEN 1 AND 64),
    principal_id TEXT NOT NULL CHECK (length(principal_id) BETWEEN 1 AND 256),
    ingress TEXT NOT NULL CHECK (ingress IN ('local_unix', 'mailbox', 'local_executor', 'ssh_bridge', 'direct_mtls', 'internal', 'unknown')),
    environment TEXT CHECK (environment IS NULL OR length(environment) <= 256),
    session_id TEXT,
    command_id TEXT,
    job_id TEXT,
    action TEXT NOT NULL CHECK (action IN ('create', 'submit', 'cancel', 'close', 'run', 'read_session', 'read_command', 'read_job', 'runtime_cleanup')),
    outcome TEXT NOT NULL CHECK (outcome IN ('allowed', 'denied', 'failed')),
    reason_code TEXT NOT NULL DEFAULT '' CHECK (reason_code IN ('', 'environment_denied', 'policy_denied', 'controller_denied', 'runtime_cleanup_unconfirmed')),
    occurred_at TEXT NOT NULL,
    mailbox_id TEXT,
    execution_context TEXT,
    execution_selection_source TEXT,
    resolved_target_kind TEXT,
    resolved_target_profile TEXT,
    repository_alias TEXT,
    repository_aliases_json TEXT,
    CHECK (
        (outcome = 'allowed' AND action <> 'runtime_cleanup' AND reason_code = '') OR
        (outcome = 'denied' AND action <> 'runtime_cleanup' AND reason_code IN ('environment_denied', 'policy_denied', 'controller_denied')) OR
        (outcome = 'failed' AND action = 'runtime_cleanup' AND reason_code = 'runtime_cleanup_unconfirmed' AND session_id IS NOT NULL)
    )
) STRICT;

INSERT INTO runner_audit_records_v31 (
    id, principal_type, principal_id, ingress, environment, session_id, command_id, job_id,
    action, outcome, reason_code, occurred_at,
    mailbox_id, execution_context, execution_selection_source,
    resolved_target_kind, resolved_target_profile, repository_alias,
    repository_aliases_json
)
SELECT
    id, principal_type, principal_id, ingress, environment, session_id, command_id, job_id,
    action, outcome, reason_code, occurred_at,
    mailbox_id, execution_context, execution_selection_source,
    resolved_target_kind, resolved_target_profile, repository_alias,
    repository_aliases_json
FROM runner_audit_records;

DROP TABLE runner_audit_records;
ALTER TABLE runner_audit_records_v31 RENAME TO runner_audit_records;

CREATE INDEX runner_audit_records_by_principal
    ON runner_audit_records(principal_type, principal_id, id);

CREATE TRIGGER runner_audit_records_no_update
BEFORE UPDATE ON runner_audit_records
BEGIN
    SELECT RAISE(ABORT, 'audit records are append-only');
END;

CREATE TRIGGER runner_audit_records_no_delete
BEFORE DELETE ON runner_audit_records
BEGIN
    SELECT RAISE(ABORT, 'audit records are append-only');
END;
