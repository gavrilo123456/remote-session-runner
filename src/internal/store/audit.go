package store

import (
	"context"
	"database/sql"
	"fmt"

	"remote-session-runner/src/internal/audit"
	"remote-session-runner/src/internal/domain"
)

const maxAuditReadRows = 10000

// RecordAudit appends one denied or otherwise out-of-band audit decision. The
// row is logged only after its transaction commits.
func (s *AuthorityStore) RecordAudit(ctx context.Context, record audit.Record) error {
	if s == nil || s.db == nil {
		return ErrNilDatabase
	}
	saved, err := withImmediateTransaction(ctx, s, func(ctx context.Context, connection *sql.Conn) (audit.Record, error) {
		return insertAuditOnConnection(ctx, connection, record)
	})
	if err != nil {
		return err
	}
	audit.Log(ctx, saved)
	return nil
}

// ListAuditRecords returns the newest bounded page in ascending row order so
// callers can inspect the monotonic IDs without loading unbounded history.
func (s *AuthorityStore) ListAuditRecords(ctx context.Context, limit int) ([]audit.Record, error) {
	if s == nil || s.db == nil {
		return nil, ErrNilDatabase
	}
	if limit <= 0 || limit > maxAuditReadRows {
		return nil, fmt.Errorf("%w: audit row limit must be 1..%d", audit.ErrInvalidRecord, maxAuditReadRows)
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, principal_type, principal_id, ingress, environment, session_id, command_id, job_id,
       action, outcome, reason_code, occurred_at
FROM runner_audit_records
ORDER BY id DESC LIMIT ?
`, limit)
	if err != nil {
		if IsSQLiteError(err) {
			s.storageErrors.Add(1)
		}
		return nil, fmt.Errorf("list audit records: %w", err)
	}
	defer rows.Close()
	records := make([]audit.Record, 0, limit)
	for rows.Next() {
		var record audit.Record
		var principalType, principalID, ingress, action, outcome, reasonCode, occurredAt string
		var environment, sessionID, commandID, jobID sql.NullString
		if err := rows.Scan(&record.ID, &principalType, &principalID, &ingress, &environment, &sessionID, &commandID, &jobID,
			&action, &outcome, &reasonCode, &occurredAt); err != nil {
			return nil, fmt.Errorf("scan audit record: %w", err)
		}
		controllerID, err := domain.NewControllerID(principalID)
		if err != nil {
			return nil, fmt.Errorf("decode audit principal ID: %w", err)
		}
		record.Principal, err = domain.NewControllerIdentity(domain.ControllerType(principalType), controllerID)
		if err != nil {
			return nil, fmt.Errorf("decode audit principal: %w", err)
		}
		record.Ingress = audit.Ingress(ingress)
		record.Action = audit.Action(action)
		record.Outcome = audit.Outcome(outcome)
		record.ReasonCode = reasonCode
		if environment.Valid {
			record.Environment = environment.String
		}
		if sessionID.Valid {
			record.SessionID, err = domain.NewSessionID(sessionID.String)
			if err != nil {
				return nil, fmt.Errorf("decode audit session ID: %w", err)
			}
		}
		if commandID.Valid {
			record.CommandID, err = domain.NewCommandID(commandID.String)
			if err != nil {
				return nil, fmt.Errorf("decode audit command ID: %w", err)
			}
		}
		if jobID.Valid {
			record.JobID, err = domain.NewJobID(jobID.String)
			if err != nil {
				return nil, fmt.Errorf("decode audit job ID: %w", err)
			}
		}
		record.OccurredAt, err = parseStoredTime(occurredAt)
		if err != nil {
			return nil, fmt.Errorf("decode audit timestamp: %w", err)
		}
		if err := record.Validate(); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		if IsSQLiteError(err) {
			s.storageErrors.Add(1)
		}
		return nil, fmt.Errorf("read audit records: %w", err)
	}
	for left, right := 0, len(records)-1; left < right; left, right = left+1, right-1 {
		records[left], records[right] = records[right], records[left]
	}
	return records, nil
}

func insertAuditOnConnection(ctx context.Context, connection *sql.Conn, record audit.Record) (audit.Record, error) {
	if err := record.Validate(); err != nil {
		return audit.Record{}, err
	}
	var environment, sessionID, commandID, jobID any
	if record.Environment != "" {
		environment = record.Environment
	}
	if record.SessionID != "" {
		sessionID = string(record.SessionID)
	}
	if record.CommandID != "" {
		commandID = string(record.CommandID)
	}
	if record.JobID != "" {
		jobID = string(record.JobID)
	}
	result, err := connection.ExecContext(ctx, `
INSERT INTO runner_audit_records (
    principal_type, principal_id, ingress, environment, session_id, command_id, job_id,
    action, outcome, reason_code, occurred_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, string(record.Principal.Type()), string(record.Principal.ID()), string(record.Ingress), environment,
		sessionID, commandID, jobID, string(record.Action), string(record.Outcome), record.ReasonCode, formatStoredTime(record.OccurredAt.UTC()))
	if err != nil {
		return audit.Record{}, fmt.Errorf("insert audit record: %w", err)
	}
	record.ID, err = result.LastInsertId()
	if err != nil {
		return audit.Record{}, fmt.Errorf("read audit row ID: %w", err)
	}
	return record, nil
}

func insertOptionalAuditOnConnection(ctx context.Context, connection *sql.Conn, record *audit.Record) error {
	if record == nil {
		return nil
	}
	saved, err := insertAuditOnConnection(ctx, connection, *record)
	if err != nil {
		return err
	}
	*record = saved
	return nil
}

func logOptionalAudit(ctx context.Context, record *audit.Record) {
	if record != nil && record.ID > 0 {
		audit.Log(ctx, *record)
	}
}
