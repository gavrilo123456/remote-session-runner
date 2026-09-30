-- P154 records every Mac mailbox root that has been activated. The registry
-- is monotonic: a later configuration cannot silently remove or relocate an
-- ingress root, because a file-only producer can publish marker-last work
-- while runner-local is stopped for an upgrade.
CREATE TABLE mailbox_configuration_registry (
    mailbox_id TEXT NOT NULL PRIMARY KEY CHECK (length(mailbox_id) BETWEEN 1 AND 128),
    mailbox_root TEXT NOT NULL UNIQUE CHECK (length(mailbox_root) BETWEEN 1 AND 4096),
    registered_at TEXT NOT NULL
) STRICT;

CREATE TRIGGER mailbox_configuration_registry_no_update
BEFORE UPDATE ON mailbox_configuration_registry
BEGIN
    SELECT RAISE(ABORT, 'mailbox configuration registry is append-only');
END;

CREATE TRIGGER mailbox_configuration_registry_no_delete
BEFORE DELETE ON mailbox_configuration_registry
BEGIN
    SELECT RAISE(ABORT, 'mailbox configuration registry is append-only');
END;
