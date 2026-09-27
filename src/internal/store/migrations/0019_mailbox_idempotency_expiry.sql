ALTER TABLE mailbox_exchanges ADD COLUMN idempotency_key_expires_at TEXT;
ALTER TABLE mailbox_exchanges ADD COLUMN idempotency_binding_active INTEGER NOT NULL DEFAULT 1 CHECK (idempotency_binding_active IN (0, 1));
ALTER TABLE mailbox_exchanges ADD COLUMN deduplication_warning INTEGER NOT NULL DEFAULT 0 CHECK (deduplication_warning IN (0, 1));

UPDATE mailbox_exchanges SET idempotency_binding_active = 0 WHERE idempotency_key = '';

CREATE INDEX ix_mailbox_exchanges_active_key
    ON mailbox_exchanges(controller_type, controller_id, operation, idempotency_key, created_at, request_id)
    WHERE idempotency_key <> '' AND idempotency_binding_active = 1;
