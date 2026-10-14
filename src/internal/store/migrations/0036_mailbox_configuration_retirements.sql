-- A mailbox root may be explicitly retired only after the store has recorded
-- the exact immutable registry identity. The original registry row remains
-- historical evidence; this separate append-only ledger prevents a retired
-- ID or root from being silently revived by a later configuration.
CREATE UNIQUE INDEX ux_mailbox_configuration_registry_id_root
    ON mailbox_configuration_registry(mailbox_id, mailbox_root);

CREATE TABLE mailbox_configuration_retirements (
    mailbox_id TEXT NOT NULL PRIMARY KEY CHECK (length(mailbox_id) BETWEEN 1 AND 128),
    mailbox_root TEXT NOT NULL UNIQUE CHECK (length(mailbox_root) BETWEEN 1 AND 4096),
    retired_at TEXT NOT NULL,
    FOREIGN KEY (mailbox_id, mailbox_root)
        REFERENCES mailbox_configuration_registry(mailbox_id, mailbox_root)
        ON DELETE RESTRICT
) STRICT;

CREATE TRIGGER mailbox_configuration_retirements_no_update
BEFORE UPDATE ON mailbox_configuration_retirements
BEGIN
    SELECT RAISE(ABORT, 'mailbox configuration retirements are append-only');
END;

CREATE TRIGGER mailbox_configuration_retirements_no_delete
BEFORE DELETE ON mailbox_configuration_retirements
BEGIN
    SELECT RAISE(ABORT, 'mailbox configuration retirements are append-only');
END;
