CREATE TABLE lane_ingress_admission (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    state_json BLOB NOT NULL,
    state_sha256 BLOB NOT NULL CHECK (length(state_sha256) = 32),
    updated_at TEXT NOT NULL
);
