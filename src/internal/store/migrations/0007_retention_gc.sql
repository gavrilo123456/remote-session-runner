ALTER TABLE exec_commands
ADD COLUMN output_unavailable_reason TEXT NOT NULL DEFAULT '';
