-- BUG-002 compatibility repair. A lost command has an unconfirmed capture
-- boundary, rather than a complete result with an omitted reason. One-off
-- jobs whose command became lost also have an unconfirmed teardown boundary.
-- These updates change only durable status metadata. They never resume a job,
-- execute a script, or release a retained command slot.
UPDATE exec_commands
SET output_unavailable_reason = 'capture_boundary_unconfirmed'
WHERE state = 'lost'
  AND output_complete = 0
  AND output_unavailable_reason = '';

UPDATE exec_jobs
SET output_unavailable_reason = 'capture_boundary_unconfirmed'
WHERE command_state = 'lost'
  AND output_complete = 0
  AND output_unavailable_reason = '';

UPDATE exec_jobs
SET teardown_state = 'lost', teardown_reason = 'runtime_cleanup_unconfirmed'
WHERE phase = 'lost'
  AND command_state = 'lost'
  AND output_complete = 0
  AND teardown_state = 'pending';

UPDATE local_remote_command_projections
SET output_unavailable_reason = 'capture_boundary_unconfirmed'
WHERE command_state = 'lost'
  AND output_complete = 0
  AND output_unavailable_reason = '';

UPDATE local_remote_job_projections
SET output_unavailable_reason = 'capture_boundary_unconfirmed'
WHERE command_state = 'lost'
  AND output_complete = 0
  AND output_unavailable_reason = '';

UPDATE local_remote_job_projections
SET teardown_state = 'lost', teardown_reason = 'runtime_cleanup_unconfirmed'
WHERE job_phase = 'lost'
  AND command_state = 'lost'
  AND output_complete = 0
  AND teardown_state = 'pending';

CREATE TABLE local_remote_status_failures (
    intent_id TEXT NOT NULL PRIMARY KEY,
    first_observed_at TEXT NOT NULL CHECK (length(first_observed_at) > 0),
    reason TEXT NOT NULL CHECK (reason IN ('remote_status_unavailable')),
    FOREIGN KEY (intent_id) REFERENCES local_intents(intent_id) ON DELETE RESTRICT
);
