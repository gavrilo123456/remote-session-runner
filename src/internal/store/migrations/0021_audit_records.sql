CREATE TABLE runner_audit_records (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    principal_type TEXT NOT NULL CHECK (length(principal_type) BETWEEN 1 AND 64),
    principal_id TEXT NOT NULL CHECK (length(principal_id) BETWEEN 1 AND 256),
    ingress TEXT NOT NULL CHECK (ingress IN ('local_unix', 'mailbox', 'local_executor', 'ssh_bridge', 'direct_mtls', 'internal')),
    environment TEXT CHECK (environment IS NULL OR length(environment) <= 256),
    session_id TEXT,
    command_id TEXT,
    job_id TEXT,
    action TEXT NOT NULL CHECK (action IN ('create', 'submit', 'cancel', 'close', 'run', 'read_session', 'read_command', 'read_job')),
    outcome TEXT NOT NULL CHECK (outcome IN ('allowed', 'denied')),
    reason_code TEXT NOT NULL DEFAULT '' CHECK (reason_code IN ('', 'environment_denied', 'policy_denied', 'controller_denied')),
    occurred_at TEXT NOT NULL,
    CHECK (
        (outcome = 'allowed' AND reason_code = '') OR
        (outcome = 'denied' AND reason_code <> '')
    )
) STRICT;

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
