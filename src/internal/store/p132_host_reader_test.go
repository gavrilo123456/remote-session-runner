//go:build p132hostreader

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/user"
	"runtime"
	"strconv"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
)

const p132UbuntuDatabasePath = "/home/ubuntu/.local/share/remote-session-runner/state/remote.db"

type p132HostEvent struct {
	Sequence int64  `json:"sequence"`
	Type     string `json:"type"`
}

type p132HostAudit struct {
	ID          int64  `json:"id"`
	PrincipalID string `json:"principal_id"`
	Ingress     string `json:"ingress"`
	Action      string `json:"action"`
	Outcome     string `json:"outcome"`
	SessionID   string `json:"session_id"`
	CommandID   string `json:"command_id"`
}

type p132HostCommandState struct {
	SessionID          string          `json:"session_id"`
	SessionState       string          `json:"session_state"`
	CommandID          string          `json:"command_id"`
	CommandState       string          `json:"command_state"`
	FinalEventSequence *int64          `json:"final_event_sequence,omitempty"`
	OutputComplete     bool            `json:"output_complete"`
	Events             []p132HostEvent `json:"events"`
	Audits             []p132HostAudit `json:"audits"`
}

func TestP132UbuntuHostCommandReader(t *testing.T) {
	if os.Getenv("RSR_P132_HOST_READ") != "1" {
		t.Skip("set RSR_P132_HOST_READ=1 to read stopped Ubuntu Runner state")
	}
	if runtime.GOOS != "linux" {
		t.Fatalf("P132 host reader must run on Linux, got %s", runtime.GOOS)
	}
	current, err := user.Current()
	if err != nil || current.Username != "ubuntu" || current.Uid != "1001" {
		t.Fatalf("P132 host reader account=%v err=%v, want ubuntu uid 1001", current, err)
	}
	sessionID, err := domain.NewSessionID(os.Getenv("RSR_P132_HOST_SESSION_ID"))
	if err != nil {
		t.Fatalf("invalid P132 session ID: %v", err)
	}
	commandID, err := domain.NewCommandID(os.Getenv("RSR_P132_HOST_COMMAND_ID"))
	if err != nil {
		t.Fatalf("invalid P132 command ID: %v", err)
	}
	afterID, err := strconv.ParseInt(os.Getenv("RSR_P132_HOST_AUDIT_AFTER_ID"), 10, 64)
	if err != nil || afterID < 0 {
		t.Fatalf("invalid P132 audit high-water ID %q", os.Getenv("RSR_P132_HOST_AUDIT_AFTER_ID"))
	}
	db := p132OpenUbuntuDatabaseReadOnly(t)
	authority, err := NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := authority.GetSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("read P132 session after service stop: %v", err)
	}
	command, err := authority.GetCommand(ctx, commandID)
	if err != nil {
		t.Fatalf("read P132 command after service stop: %v", err)
	}
	if command.SessionID != sessionID {
		t.Fatalf("P132 command session=%q, want %q", command.SessionID, sessionID)
	}
	events, err := authority.ListCommandEvents(ctx, commandID)
	if err != nil {
		t.Fatalf("read P132 command events after service stop: %v", err)
	}
	records, err := authority.ListAuditRecords(ctx, 1000)
	if err != nil {
		t.Fatalf("read P132 audit records after service stop: %v", err)
	}
	result := p132HostCommandState{
		SessionID: string(sessionID), SessionState: string(session.State), CommandID: string(commandID),
		CommandState: string(command.State), FinalEventSequence: command.FinalEventSequence,
		OutputComplete: command.OutputComplete, Events: make([]p132HostEvent, 0, len(events)),
		Audits: make([]p132HostAudit, 0),
	}
	for _, event := range events {
		result.Events = append(result.Events, p132HostEvent{Sequence: event.Sequence, Type: event.Type})
	}
	for _, record := range records {
		if record.ID <= afterID || record.SessionID != sessionID {
			continue
		}
		result.Audits = append(result.Audits, p132HostAudit{
			ID: record.ID, PrincipalID: string(record.Principal.ID()), Ingress: string(record.Ingress),
			Action: string(record.Action), Outcome: string(record.Outcome),
			SessionID: string(record.SessionID), CommandID: string(record.CommandID),
		})
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("encode bounded P132 host result: %v", err)
	}
	fmt.Printf("P132_HOST_STATE=%s\n", encoded)
}

func p132OpenUbuntuDatabaseReadOnly(t *testing.T) *sql.DB {
	t.Helper()
	databaseURI := url.URL{Scheme: "file", Path: p132UbuntuDatabasePath}
	query := databaseURI.Query()
	query.Set("mode", "ro")
	query.Set("_busy_timeout", strconv.FormatInt(BusyTimeout.Milliseconds(), 10))
	databaseURI.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", databaseURI.String())
	if err != nil {
		t.Fatalf("open Ubuntu authority database read-only: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("connect to Ubuntu authority database read-only: %v", err)
	}
	return db
}
