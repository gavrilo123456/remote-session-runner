ALTER TABLE local_remote_job_projections
    ADD COLUMN queue_blocked_reason TEXT NOT NULL DEFAULT ''
    CHECK (queue_blocked_reason IN ('', 'lost_capacity_recovery_pending'));
