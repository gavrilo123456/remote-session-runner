-- BUG-003 keeps a durable, bounded diagnostic count for repeated strict
-- remote-status reads. The count is diagnostic only: it never changes the
-- accepted delivery state, replays a mutation, or moves the existing deadline.
ALTER TABLE local_remote_status_failures
ADD COLUMN reconciliation_attempts INTEGER NOT NULL DEFAULT 0
CHECK (reconciliation_attempts >= 0);

-- A pre-existing BUG-002 marker already represents at least one failed read.
UPDATE local_remote_status_failures
SET reconciliation_attempts = 1
WHERE reconciliation_attempts = 0;
