//go:build p127hostreader

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"
)

const p127HostAuditDBPath = "/home/ubuntu/.local/share/remote-session-runner/state/remote.db"

type p127HostAuditReadRecord struct {
	ID            int64  `json:"id"`
	PrincipalType string `json:"principal_type"`
	PrincipalID   string `json:"principal_id"`
	Ingress       string `json:"ingress"`
	Environment   string `json:"environment"`
	SessionID     string `json:"session_id"`
	CommandID     string `json:"command_id"`
	JobID         string `json:"job_id"`
	Action        string `json:"action"`
	Outcome       string `json:"outcome"`
	ReasonCode    string `json:"reason_code"`
	OccurredAt    string `json:"occurred_at"`
}

// TestP127HostAuditReadHelper is invoked explicitly on Ubuntu through the
// separate host-inspection SSH identity. It opens the live authority database
// read-only and emits only bounded, non-sensitive audit columns.
func TestP127HostAuditReadHelper(t *testing.T) {
	if os.Getenv("RSR_P127_HOST_AUDIT_READ") != "1" {
		t.Skip("set RSR_P127_HOST_AUDIT_READ=1 for the read-only Ubuntu audit inspector")
	}

	mode := os.Getenv("RSR_P127_HOST_AUDIT_MODE")
	if mode != "highwater" && mode != "rows" {
		t.Fatalf("unsupported audit read mode %q", mode)
	}
	afterID, err := strconv.ParseInt(os.Getenv("RSR_P127_HOST_AUDIT_AFTER_ID"), 10, 64)
	if err != nil || afterID < 0 {
		t.Fatalf("invalid audit after ID %q", os.Getenv("RSR_P127_HOST_AUDIT_AFTER_ID"))
	}

	databaseURI := url.URL{Scheme: "file", Path: p127HostAuditDBPath}
	query := databaseURI.Query()
	query.Set("mode", "ro")
	query.Set("_busy_timeout", strconv.FormatInt(BusyTimeout.Milliseconds(), 10))
	databaseURI.RawQuery = query.Encode()

	db, err := sql.Open("sqlite", databaseURI.String())
	if err != nil {
		t.Fatalf("open Ubuntu authority database read-only: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("connect to Ubuntu authority database read-only: %v", err)
	}

	authority, err := NewAuthorityStore(db)
	if err != nil {
		t.Fatalf("wrap read-only authority database: %v", err)
	}
	rows, err := authority.ListAuditRecords(ctx, maxAuditReadRows)
	if err != nil {
		t.Fatalf("read bounded audit records: %v", err)
	}
	if mode == "highwater" {
		var id int64
		if len(rows) > 0 {
			id = rows[len(rows)-1].ID
		}
		fmt.Printf("P127_HOST_AUDIT_HIGHWATER=%d\n", id)
		return
	}
	records := make([]p127HostAuditReadRecord, 0, len(rows))
	for _, row := range rows {
		if row.ID <= afterID {
			continue
		}
		records = append(records, p127HostAuditReadRecord{
			ID: row.ID, PrincipalType: string(row.Principal.Type()), PrincipalID: string(row.Principal.ID()),
			Ingress: string(row.Ingress), Environment: row.Environment, SessionID: string(row.SessionID),
			CommandID: string(row.CommandID), JobID: string(row.JobID), Action: string(row.Action),
			Outcome: string(row.Outcome), ReasonCode: row.ReasonCode, OccurredAt: row.OccurredAt.Format(time.RFC3339Nano),
		})
	}
	encoded, err := json.Marshal(records)
	if err != nil {
		t.Fatalf("encode safe audit records: %v", err)
	}
	fmt.Printf("P127_HOST_AUDIT_ROWS=%s\n", encoded)
}
