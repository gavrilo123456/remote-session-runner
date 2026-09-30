ALTER TABLE local_intents
    ADD COLUMN remote_terminal_proof_version INTEGER NOT NULL DEFAULT 0
    CHECK (remote_terminal_proof_version >= 0);

CREATE INDEX ix_local_intents_remote_terminal_proof
    ON local_intents(target_kind, operation, delivery_state, remote_terminal_proof_version, created_at, intent_id);
