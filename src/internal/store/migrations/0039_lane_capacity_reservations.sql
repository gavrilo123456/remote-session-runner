CREATE TABLE lane_capacity_budgets (
    lane TEXT PRIMARY KEY CHECK (lane IN ('dev', 'qas', 'prd')),
    max_reserved_bytes INTEGER NOT NULL CHECK (max_reserved_bytes > 0),
    policy_digest TEXT NOT NULL CHECK (length(policy_digest) = 71),
    updated_at TEXT NOT NULL
) WITHOUT ROWID, STRICT;

CREATE TABLE lane_capacity_reservations (
    lane TEXT NOT NULL,
    reservation_id TEXT NOT NULL CHECK (length(reservation_id) BETWEEN 1 AND 128),
    category TEXT NOT NULL CHECK (category IN ('ready_pair', 'response', 'event', 'output', 'sqlite', 'wal', 'log', 'workspace', 'temp_script', 'backup', 'staging')),
    reserved_bytes INTEGER NOT NULL CHECK (reserved_bytes > 0),
    created_at TEXT NOT NULL,
    PRIMARY KEY (lane, reservation_id, category),
    FOREIGN KEY (lane) REFERENCES lane_capacity_budgets(lane) ON DELETE RESTRICT
) WITHOUT ROWID, STRICT;

CREATE INDEX lane_capacity_reservations_by_lane
    ON lane_capacity_reservations(lane, category, reservation_id);
