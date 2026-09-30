-- P153 persists the mailbox-policy decision made before accepting new work.
-- Empty values represent pre-P153 rows and intentionally do not fabricate a
-- present-day inbox default for retained work.
ALTER TABLE mailbox_exchanges ADD COLUMN resolved_execution_context TEXT NOT NULL DEFAULT '';
ALTER TABLE mailbox_exchanges ADD COLUMN resolved_environment TEXT NOT NULL DEFAULT '';
ALTER TABLE mailbox_exchanges ADD COLUMN resolved_target_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE mailbox_exchanges ADD COLUMN resolved_target_profile TEXT NOT NULL DEFAULT '';
ALTER TABLE mailbox_exchanges ADD COLUMN execution_selection_source TEXT NOT NULL DEFAULT '';
-- Existing v25 rows are deliberately marked legacy. New P153 rows write
-- resolved or rejected before the processor can publish a response, closing
-- the crash window between selection validation and outbox publication.
ALTER TABLE mailbox_exchanges ADD COLUMN execution_selection_state TEXT NOT NULL DEFAULT 'legacy' CHECK (execution_selection_state IN ('legacy', 'resolved', 'rejected'));
ALTER TABLE mailbox_exchanges ADD COLUMN repository_alias TEXT NOT NULL DEFAULT '';
ALTER TABLE mailbox_exchanges ADD COLUMN repository_aliases_json TEXT NOT NULL DEFAULT '[]';

-- Selection fields are optional for legacy/non-new-work audit rows. New
-- mailbox-created work writes a complete bounded snapshot in the same
-- transaction as its local intent and allowed audit action.
ALTER TABLE runner_audit_records ADD COLUMN mailbox_id TEXT;
ALTER TABLE runner_audit_records ADD COLUMN execution_context TEXT;
ALTER TABLE runner_audit_records ADD COLUMN execution_selection_source TEXT;
ALTER TABLE runner_audit_records ADD COLUMN resolved_target_kind TEXT;
ALTER TABLE runner_audit_records ADD COLUMN resolved_target_profile TEXT;
ALTER TABLE runner_audit_records ADD COLUMN repository_alias TEXT;
ALTER TABLE runner_audit_records ADD COLUMN repository_aliases_json TEXT;
