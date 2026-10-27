package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	DefaultRetainedWorkInventoryRows = 10000
	MaxRetainedWorkInventoryRows     = 50000
	MaxRetainedWorkInventoryBytes    = 4 << 20
)

var ErrRetainedWorkInventoryLimit = errors.New("retained-work metadata inventory exceeds its bound")

// RetainedWorkInventory describes one read cut, never a recovery authorization
// or process-cleanup proof. An error always returns Complete=false without
// partial identity rows. RemoteIntents are routing metadata, not local jobs.
type RetainedWorkInventory struct {
	Complete              bool                         `json:"complete"`
	SchemaVersion         int                          `json:"schema_version"`
	SnapshotStartedAt     string                       `json:"snapshot_started_at"`
	ObservedAt            string                       `json:"observed_at"`
	SnapshotCompletedAt   string                       `json:"snapshot_completed_at"`
	Sessions              []RetainedWorkSession        `json:"sessions"`
	Commands              []RetainedWorkCommand        `json:"commands"`
	Jobs                  []RetainedWorkJob            `json:"jobs"`
	SessionReservations   []RetainedWorkReservation    `json:"session_reservations"`
	CommandSlots          []RetainedWorkSlot           `json:"command_slots"`
	RetainedPairs         []RetainedWorkPair           `json:"retained_pairs"`
	RecoveryFinalizations []RetainedWorkFinalization   `json:"recovery_finalizations"`
	LocalIntents          []RetainedWorkIntent         `json:"local_intents"`
	RemoteIntents         []RetainedWorkIntent         `json:"remote_intents"`
	MailboxBindings       []RetainedWorkMailboxBinding `json:"mailbox_bindings"`
}

type RetainedWorkSession struct {
	SessionID         string `json:"session_id"`
	State             string `json:"state"`
	TargetKind        string `json:"target_kind"`
	TargetProfile     string `json:"target_profile"`
	EnvironmentID     string `json:"environment_id"`
	ControllerType    string `json:"controller_type"`
	ControllerID      string `json:"controller_id"`
	RuntimeGeneration string `json:"runtime_generation"`
}

type RetainedWorkCommand struct {
	CommandID       string `json:"command_id"`
	SessionID       string `json:"session_id"`
	State           string `json:"state"`
	OutputComplete  bool   `json:"output_complete"`
	OutputTruncated bool   `json:"output_truncated"`
}

type RetainedWorkJob struct {
	JobID            string `json:"job_id"`
	SessionID        string `json:"session_id"`
	PlannedCommandID string `json:"planned_command_id"`
	CommandPresent   bool   `json:"command_present"`
	Phase            string `json:"phase"`
	TeardownState    string `json:"teardown_state"`
	Ingress          string `json:"ingress"`
}

type RetainedWorkReservation struct {
	SessionID          string  `json:"session_id"`
	SessionPresent     bool    `json:"session_present"`
	HostKey            string  `json:"host_key"`
	ReservedAt         string  `json:"reserved_at"`
	CleanupConfirmedAt *string `json:"cleanup_confirmed_at"`
	ReleasedAt         *string `json:"released_at"`
}

type RetainedWorkSlot struct {
	CommandID       string  `json:"command_id"`
	CommandPresent  bool    `json:"command_present"`
	SessionID       string  `json:"session_id"`
	HostKey         string  `json:"host_key"`
	ReservedAt      string  `json:"reserved_at"`
	StopConfirmedAt *string `json:"stop_confirmed_at"`
	ReleasedAt      *string `json:"released_at"`
}

type RetainedWorkPair struct {
	SessionID              string `json:"session_id"`
	CommandID              string `json:"command_id"`
	JobID                  string `json:"job_id"`
	CommandPresent         bool   `json:"command_present"`
	IdentityConsistent     bool   `json:"identity_consistent"`
	SessionState           string `json:"session_state"`
	CommandState           string `json:"command_state"`
	JobPhase               string `json:"job_phase"`
	TeardownState          string `json:"teardown_state"`
	SessionReservationHeld bool   `json:"session_reservation_held"`
	CommandSlotHeld        bool   `json:"command_slot_held"`
	Classification         string `json:"classification"`
}

type RetainedWorkFinalization struct {
	Kind               string `json:"kind"`
	SessionID          string `json:"session_id"`
	CommandID          string `json:"command_id"`
	JobID              string `json:"job_id"`
	CapacityReleasedAt string `json:"capacity_released_at"`
}

type RetainedWorkIntent struct {
	IntentID      string `json:"intent_id"`
	Operation     string `json:"operation"`
	ResourceID    string `json:"resource_id"`
	SessionID     string `json:"session_id"`
	CommandID     string `json:"command_id"`
	JobID         string `json:"job_id"`
	DeliveryState string `json:"delivery_state"`
	TargetProfile string `json:"target_profile"`
}

type RetainedWorkMailboxBinding struct {
	MailboxID       string `json:"mailbox_id"`
	ClientRequestID string `json:"client_request_id"`
	ResourceID      string `json:"resource_id"`
	RequestState    string `json:"request_state"`
	Acknowledged    bool   `json:"acknowledged"`
}

// ReadRetainedWorkInventory executes only SELECTs in a deferred read
// transaction: it does not reserve SQLite's writer slot. The caller should use
// OpenExistingCurrentReadOnly to enforce file/owner/schema and read-only open
// guards. Schema 40 is deliberately explicit; newer schemas require review.
func ReadRetainedWorkInventory(ctx context.Context, database *sql.DB, maxRows int) (RetainedWorkInventory, error) {
	if database == nil {
		return RetainedWorkInventory{}, ErrNilDatabase
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if maxRows == 0 {
		maxRows = DefaultRetainedWorkInventoryRows
	}
	if maxRows < 1 || maxRows > MaxRetainedWorkInventoryRows {
		return RetainedWorkInventory{}, ErrRetainedWorkInventoryLimit
	}
	started := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := withReadTransaction(ctx, database, func(ctx context.Context, conn *sql.Conn) (RetainedWorkInventory, error) {
		return readRetainedWorkInventorySnapshot(ctx, conn, maxRows)
	})
	if err != nil {
		return RetainedWorkInventory{}, err
	}
	result.SnapshotStartedAt = started
	result.SnapshotCompletedAt = time.Now().UTC().Format(time.RFC3339Nano)
	result.Complete = true
	raw, err := json.Marshal(result)
	if err != nil {
		return RetainedWorkInventory{}, errors.New("encode retained-work metadata inventory")
	}
	if len(raw) > MaxRetainedWorkInventoryBytes {
		return RetainedWorkInventory{}, ErrRetainedWorkInventoryLimit
	}
	return result, nil
}

// UNIONs include unfamiliar states by excluding only known terminal states;
// incomplete or orphaned capacity rows are exported separately as well.
const retainedWorkCTE = `WITH relevant_sessions AS (
 SELECT session_id FROM exec_sessions WHERE state NOT IN ('closed','expired','failed','lost')
 UNION SELECT session_id FROM exec_capacity_reservations WHERE cleanup_confirmed_at IS NULL OR released_at IS NULL
 UNION SELECT session_id FROM exec_commands WHERE state NOT IN ('succeeded','failed','cancelled','timed_out','rejected','lost')
 UNION SELECT c.session_id FROM exec_command_slots x JOIN exec_commands c ON c.command_id=x.command_id WHERE x.stop_confirmed_at IS NULL OR x.released_at IS NULL
 UNION SELECT session_id FROM exec_jobs WHERE phase NOT IN ('complete','failed','lost')
 UNION SELECT session_id FROM exec_lost_runtime_recovery_finalizations
 UNION SELECT session_id FROM exec_commandless_lost_runtime_recovery_finalizations
 UNION SELECT session_id FROM exec_completed_teardown_recovery_finalizations
), relevant_commands AS (
 SELECT command_id FROM exec_commands WHERE session_id IN (SELECT session_id FROM relevant_sessions)
 UNION SELECT command_id FROM exec_command_slots WHERE stop_confirmed_at IS NULL OR released_at IS NULL
 UNION SELECT command_id FROM exec_lost_runtime_recovery_finalizations
 UNION SELECT command_id FROM exec_completed_teardown_recovery_finalizations
), relevant_jobs AS (
 SELECT job_id FROM exec_jobs WHERE phase NOT IN ('complete','failed','lost') OR session_id IN (SELECT session_id FROM relevant_sessions)
 UNION SELECT job_id FROM exec_commandless_lost_runtime_recovery_finalizations
 UNION SELECT job_id FROM exec_completed_teardown_recovery_finalizations
) `

func readRetainedWorkInventorySnapshot(ctx context.Context, conn *sql.Conn, maxRows int) (RetainedWorkInventory, error) {
	var result RetainedWorkInventory
	if err := verifyCurrentSchemaConnection(ctx, conn); err != nil {
		return result, err
	}
	if CurrentSchemaVersion != 40 {
		return result, ErrSchemaVersion
	}
	result.SchemaVersion = 40
	// The migration-ledger read above establishes this connection's WAL snapshot.
	result.ObservedAt = time.Now().UTC().Format(time.RFC3339Nano)
	remaining := maxRows
	queries := []struct {
		name, query string
		appendRow   func(*sql.Rows) error
	}{
		{"sessions", `SELECT session_id,state,target_kind,target_profile,environment,controller_type,controller_id,runtime_generation FROM exec_sessions WHERE session_id IN (SELECT session_id FROM relevant_sessions) ORDER BY session_id`, func(rows *sql.Rows) error {
			var row RetainedWorkSession
			if err := rows.Scan(&row.SessionID, &row.State, &row.TargetKind, &row.TargetProfile, &row.EnvironmentID, &row.ControllerType, &row.ControllerID, &row.RuntimeGeneration); err != nil {
				return err
			}
			result.Sessions = append(result.Sessions, row)
			return nil
		}},
		{"commands", `SELECT command_id,session_id,state,output_complete,output_truncated FROM exec_commands WHERE command_id IN (SELECT command_id FROM relevant_commands) ORDER BY command_id`, func(rows *sql.Rows) error {
			var row RetainedWorkCommand
			if err := rows.Scan(&row.CommandID, &row.SessionID, &row.State, &row.OutputComplete, &row.OutputTruncated); err != nil {
				return err
			}
			result.Commands = append(result.Commands, row)
			return nil
		}},
		{"jobs", `SELECT j.job_id,j.session_id,j.command_id,EXISTS(SELECT 1 FROM exec_commands c WHERE c.command_id=j.command_id),j.phase,j.teardown_state,j.ingress FROM exec_jobs j WHERE j.job_id IN (SELECT job_id FROM relevant_jobs) ORDER BY j.job_id`, func(rows *sql.Rows) error {
			var row RetainedWorkJob
			if err := rows.Scan(&row.JobID, &row.SessionID, &row.PlannedCommandID, &row.CommandPresent, &row.Phase, &row.TeardownState, &row.Ingress); err != nil {
				return err
			}
			result.Jobs = append(result.Jobs, row)
			return nil
		}},
		{"session_reservations", `SELECT r.session_id,s.session_id IS NOT NULL,r.host_key,r.reserved_at,r.cleanup_confirmed_at,r.released_at FROM exec_capacity_reservations r LEFT JOIN exec_sessions s ON s.session_id=r.session_id WHERE r.cleanup_confirmed_at IS NULL OR r.released_at IS NULL ORDER BY r.session_id`, func(rows *sql.Rows) error {
			var row RetainedWorkReservation
			if err := rows.Scan(&row.SessionID, &row.SessionPresent, &row.HostKey, &row.ReservedAt, &row.CleanupConfirmedAt, &row.ReleasedAt); err != nil {
				return err
			}
			result.SessionReservations = append(result.SessionReservations, row)
			return nil
		}},
		{"command_slots", `SELECT x.command_id,c.command_id IS NOT NULL,COALESCE(c.session_id,''),x.host_key,x.reserved_at,x.stop_confirmed_at,x.released_at FROM exec_command_slots x LEFT JOIN exec_commands c ON c.command_id=x.command_id WHERE x.stop_confirmed_at IS NULL OR x.released_at IS NULL ORDER BY x.command_id`, func(rows *sql.Rows) error {
			var row RetainedWorkSlot
			if err := rows.Scan(&row.CommandID, &row.CommandPresent, &row.SessionID, &row.HostKey, &row.ReservedAt, &row.StopConfirmedAt, &row.ReleasedAt); err != nil {
				return err
			}
			result.CommandSlots = append(result.CommandSlots, row)
			return nil
		}},
		{"retained_pairs", `SELECT DISTINCT selected.session_id,COALESCE(c.command_id,j.command_id,''),COALESCE(j.job_id,''),c.command_id IS NOT NULL OR EXISTS(SELECT 1 FROM exec_commands actual WHERE actual.command_id=j.command_id),NOT EXISTS(SELECT 1 FROM exec_jobs linked JOIN exec_commands actual ON actual.command_id=linked.command_id WHERE linked.session_id=selected.session_id AND actual.session_id<>selected.session_id),COALESCE(s.state,''),COALESCE(c.state,''),COALESCE(j.phase,''),COALESCE(j.teardown_state,''),r.session_id IS NOT NULL AND (r.cleanup_confirmed_at IS NULL OR r.released_at IS NULL),x.command_id IS NOT NULL AND (x.stop_confirmed_at IS NULL OR x.released_at IS NULL)
 FROM relevant_sessions selected
 LEFT JOIN exec_sessions s ON s.session_id=selected.session_id
 LEFT JOIN exec_capacity_reservations r ON r.session_id=selected.session_id
 LEFT JOIN exec_commands c ON c.session_id=selected.session_id AND (
  EXISTS(SELECT 1 FROM exec_command_slots held WHERE held.command_id=c.command_id AND (held.stop_confirmed_at IS NULL OR held.released_at IS NULL))
  OR EXISTS(SELECT 1 FROM exec_jobs linked WHERE linked.session_id=c.session_id AND linked.command_id=c.command_id)
  OR EXISTS(SELECT 1 FROM exec_lost_runtime_recovery_finalizations f WHERE f.command_id=c.command_id)
  OR EXISTS(SELECT 1 FROM exec_completed_teardown_recovery_finalizations f WHERE f.command_id=c.command_id))
 LEFT JOIN exec_command_slots x ON x.command_id=c.command_id
 LEFT JOIN exec_jobs j ON j.session_id=selected.session_id AND (j.command_id=c.command_id OR c.command_id IS NULL)
 WHERE (r.session_id IS NOT NULL AND (r.cleanup_confirmed_at IS NULL OR r.released_at IS NULL)) OR (x.command_id IS NOT NULL AND (x.stop_confirmed_at IS NULL OR x.released_at IS NULL))
 ORDER BY selected.session_id,2`, func(rows *sql.Rows) error {
			var row RetainedWorkPair
			if err := rows.Scan(&row.SessionID, &row.CommandID, &row.JobID, &row.CommandPresent, &row.IdentityConsistent, &row.SessionState, &row.CommandState, &row.JobPhase, &row.TeardownState, &row.SessionReservationHeld, &row.CommandSlotHeld); err != nil {
				return err
			}
			row.Classification = "unclassified_metadata"
			if row.IdentityConsistent && row.SessionState == "lost" && row.CommandPresent && row.CommandState == "lost" && row.SessionReservationHeld && row.CommandSlotHeld {
				row.Classification = "lost_pair"
			}
			if row.IdentityConsistent && row.SessionState == "lost" && !row.CommandPresent && row.JobPhase == "lost" && row.SessionReservationHeld && row.JobID != "" && row.CommandID != "" {
				row.Classification = "commandless_lost"
			}
			if row.IdentityConsistent && row.SessionState == "lost" && row.CommandPresent && (row.CommandState == "succeeded" || row.CommandState == "failed" || row.CommandState == "cancelled" || row.CommandState == "timed_out" || row.CommandState == "rejected") && row.TeardownState == "lost" && row.SessionReservationHeld && !row.CommandSlotHeld {
				row.Classification = "completed_teardown"
			}
			result.RetainedPairs = append(result.RetainedPairs, row)
			return nil
		}},
		{"recovery_finalizations", `SELECT 'lost_pair',f.session_id,f.command_id,COALESCE(j.job_id,''),f.capacity_released_at FROM exec_lost_runtime_recovery_finalizations f LEFT JOIN exec_jobs j ON j.session_id=f.session_id
 UNION ALL SELECT 'commandless',session_id,'',job_id,capacity_released_at FROM exec_commandless_lost_runtime_recovery_finalizations
 UNION ALL SELECT 'completed_teardown',session_id,command_id,job_id,capacity_released_at FROM exec_completed_teardown_recovery_finalizations ORDER BY 1,2,3`, func(rows *sql.Rows) error {
			var row RetainedWorkFinalization
			if err := rows.Scan(&row.Kind, &row.SessionID, &row.CommandID, &row.JobID, &row.CapacityReleasedAt); err != nil {
				return err
			}
			result.RecoveryFinalizations = append(result.RecoveryFinalizations, row)
			return nil
		}},
		{"intents", `SELECT intent_id,operation,resource_id,session_id,command_id,job_id,delivery_state,target_profile,target_kind FROM local_intents WHERE delivery_state NOT IN ('reconciled','not_delivered') ORDER BY intent_id`, func(rows *sql.Rows) error {
			var row RetainedWorkIntent
			var kind string
			if err := rows.Scan(&row.IntentID, &row.Operation, &row.ResourceID, &row.SessionID, &row.CommandID, &row.JobID, &row.DeliveryState, &row.TargetProfile, &kind); err != nil {
				return err
			}
			if kind == "remote" {
				result.RemoteIntents = append(result.RemoteIntents, row)
			} else if kind == "local" {
				result.LocalIntents = append(result.LocalIntents, row)
			} else {
				return errors.New("unrecognized intent target kind")
			}
			return nil
		}},
		{"mailbox_bindings", `SELECT mailbox_id,client_request_id,resource_id,request_state,acknowledged_at IS NOT NULL FROM mailbox_exchanges WHERE resource_id IN (SELECT session_id FROM relevant_sessions) OR resource_id IN (SELECT command_id FROM relevant_commands) OR resource_id IN (SELECT job_id FROM relevant_jobs) OR resource_id IN (SELECT resource_id FROM local_intents WHERE delivery_state NOT IN ('reconciled','not_delivered')) ORDER BY mailbox_id,client_request_id`, func(rows *sql.Rows) error {
			var row RetainedWorkMailboxBinding
			if err := rows.Scan(&row.MailboxID, &row.ClientRequestID, &row.ResourceID, &row.RequestState, &row.Acknowledged); err != nil {
				return err
			}
			result.MailboxBindings = append(result.MailboxBindings, row)
			return nil
		}},
	}
	for _, query := range queries {
		rows, err := conn.QueryContext(ctx, retainedWorkCTE+query.query+` LIMIT ?`, remaining+1)
		if err != nil {
			return RetainedWorkInventory{}, fmt.Errorf("read retained-work %s: %w", query.name, err)
		}
		for rows.Next() {
			if remaining == 0 {
				rows.Close()
				return RetainedWorkInventory{}, ErrRetainedWorkInventoryLimit
			}
			if err := query.appendRow(rows); err != nil {
				rows.Close()
				return RetainedWorkInventory{}, fmt.Errorf("decode retained-work %s: %w", query.name, err)
			}
			remaining--
		}
		iterationErr := rows.Err()
		closeErr := rows.Close()
		if iterationErr != nil || closeErr != nil {
			return RetainedWorkInventory{}, fmt.Errorf("finish retained-work %s: %w", query.name, errors.Join(iterationErr, closeErr))
		}
	}
	return result, nil
}
