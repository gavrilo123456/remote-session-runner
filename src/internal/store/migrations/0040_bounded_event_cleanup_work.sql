-- Event-file cleanup keeps a small durable work set for references whose
-- physical event file has not yet been removed. Completed history stays in
-- the immutable reference tables but is not revisited by each cleanup pass.
CREATE TABLE mailbox_event_cleanup_work (
    mailbox_id TEXT NOT NULL CHECK (length(mailbox_id) > 0),
    command_id TEXT NOT NULL CHECK (length(command_id) > 0),
    is_remote INTEGER NOT NULL CHECK (is_remote IN (0, 1)),
    last_exchange_id TEXT NOT NULL DEFAULT '',
    scanned_refs INTEGER NOT NULL DEFAULT 0 CHECK (scanned_refs >= 0),
    scan_complete INTEGER NOT NULL DEFAULT 0 CHECK (scan_complete IN (0, 1)),
    all_terminal INTEGER NOT NULL DEFAULT 1 CHECK (all_terminal IN (0, 1)),
    latest_cleanup_at TEXT,
    PRIMARY KEY (mailbox_id, command_id, is_remote)
) WITHOUT ROWID, STRICT;

CREATE TABLE mailbox_event_cleanup_work_cursor (
    mailbox_id TEXT PRIMARY KEY CHECK (length(mailbox_id) > 0),
    last_command_id TEXT NOT NULL DEFAULT '',
    last_is_remote INTEGER NOT NULL DEFAULT -1 CHECK (last_is_remote BETWEEN -1 AND 1)
) WITHOUT ROWID, STRICT;

-- Only outstanding or interrupted unlinks enter the work set. Previously
-- removed event files remain historical references and are never rehashed.
INSERT INTO mailbox_event_cleanup_work (mailbox_id, command_id, is_remote)
SELECT exchanges.mailbox_id, refs.command_id, 0
FROM mailbox_event_file_references AS refs
JOIN mailbox_exchanges AS exchanges ON exchanges.exchange_id = refs.exchange_id
LEFT JOIN mailbox_event_file_cleanup AS cleanup
  ON cleanup.mailbox_id = exchanges.mailbox_id AND cleanup.command_id = refs.command_id
WHERE cleanup.file_removed_at IS NULL
GROUP BY exchanges.mailbox_id, refs.command_id;

INSERT INTO mailbox_event_cleanup_work (mailbox_id, command_id, is_remote)
SELECT exchanges.mailbox_id, refs.command_id, 1
FROM mailbox_remote_event_file_references AS refs
JOIN mailbox_exchanges AS exchanges ON exchanges.exchange_id = refs.exchange_id
LEFT JOIN mailbox_remote_event_file_cleanup AS cleanup
  ON cleanup.mailbox_id = exchanges.mailbox_id AND cleanup.command_id = refs.command_id
WHERE cleanup.file_removed_at IS NULL
GROUP BY exchanges.mailbox_id, refs.command_id;

-- A response publication/ACK can change the cleanup deadline. Restart the
-- bounded metadata walk for the affected shared event file.
CREATE TRIGGER mailbox_event_cleanup_work_exchange_update
AFTER UPDATE OF request_state, response_cleanup_at ON mailbox_exchanges
WHEN NEW.request_state IS NOT OLD.request_state
  OR NEW.response_cleanup_at IS NOT OLD.response_cleanup_at
BEGIN
    UPDATE mailbox_event_cleanup_work
    SET last_exchange_id = '', scanned_refs = 0, scan_complete = 0,
        all_terminal = 1, latest_cleanup_at = NULL
    WHERE mailbox_id = NEW.mailbox_id
      AND (
        (is_remote = 0 AND EXISTS (
            SELECT 1 FROM mailbox_event_file_references AS refs
            WHERE refs.exchange_id = NEW.exchange_id
              AND refs.command_id = mailbox_event_cleanup_work.command_id
        ))
        OR
        (is_remote = 1 AND EXISTS (
            SELECT 1 FROM mailbox_remote_event_file_references AS refs
            WHERE refs.exchange_id = NEW.exchange_id
              AND refs.command_id = mailbox_event_cleanup_work.command_id
        ))
      );
END;
