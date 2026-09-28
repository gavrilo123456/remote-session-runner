package store

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/audit"
	"remote-session-runner/src/internal/domain"
)

func TestP127AuditRowsAreAppendOnlyAndLogsContainOnlySafeFields(t *testing.T) {
	ctx := context.Background()
	authority := newP019Store(t, &p019Clock{value: time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)})
	principal, err := domain.NewControllerIdentity(domain.ControllerTypeDirectMTLS, domain.ControllerID("p127-auditor"))
	if err != nil {
		t.Fatal(err)
	}
	sessionID, err := domain.NewSessionID("session-p127-audit")
	if err != nil {
		t.Fatal(err)
	}
	commandID, err := domain.NewCommandID("command-p127-audit")
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	first := audit.NewRecord(principal, audit.IngressDirectMTLS, audit.ActionSubmit, audit.OutcomeAllowed)
	first.Environment = "linux-dev"
	first.SessionID = sessionID
	first.CommandID = commandID
	if err := authority.RecordAudit(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := audit.NewRecord(principal, audit.IngressMailbox, audit.ActionClose, audit.OutcomeDenied)
	second.SessionID = sessionID
	second.ReasonCode = audit.ReasonControllerDenied
	if err := authority.RecordAudit(ctx, second); err != nil {
		t.Fatal(err)
	}
	records, err := authority.ListAuditRecords(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].ID != 1 || records[1].ID != 2 {
		t.Fatalf("audit rows = %+v, want monotonic IDs 1 and 2", records)
	}
	if records[0].Action != audit.ActionSubmit || records[0].Outcome != audit.OutcomeAllowed || records[0].CommandID != commandID || records[0].Ingress != audit.IngressDirectMTLS {
		t.Fatalf("allowed audit row lost correlation fields: %+v", records[0])
	}
	if records[1].Action != audit.ActionClose || records[1].Outcome != audit.OutcomeDenied || records[1].ReasonCode != audit.ReasonControllerDenied || records[1].Ingress != audit.IngressMailbox {
		t.Fatalf("denied audit row lost correlation fields: %+v", records[1])
	}
	serialized, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	text := string(serialized) + logs.String()
	for _, forbidden := range []string{"raw-script-marker", "private-key-marker", "credential-marker", "stdout-marker", "stderr-marker", "script_bytes", "payload"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("audit output included forbidden field/value %q: %s", forbidden, text)
		}
	}
	for _, want := range []string{`"action":"submit"`, `"principal_id":"p127-auditor"`, `"ingress":"direct_mtls"`, `"outcome":"allowed"`, `"occurred_at"`} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("structured log omitted %s: %s", want, logs.String())
		}
	}

	if _, err := authority.db.ExecContext(ctx, "UPDATE runner_audit_records SET action='close' WHERE id=1"); err == nil {
		t.Fatal("audit UPDATE unexpectedly succeeded")
	}
	if _, err := authority.db.ExecContext(ctx, "DELETE FROM runner_audit_records WHERE id=1"); err == nil {
		t.Fatal("audit DELETE unexpectedly succeeded")
	}
	columns, err := authority.db.QueryContext(ctx, "PRAGMA table_info(runner_audit_records)")
	if err != nil {
		t.Fatal(err)
	}
	defer columns.Close()
	forbiddenColumn := map[string]bool{"script": false, "script_bytes": false, "output": false, "stdout": false, "stderr": false, "credential": false, "private_key": false, "request_body": false}
	for columns.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := columns.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if _, exists := forbiddenColumn[strings.ToLower(name)]; exists {
			t.Fatalf("audit schema contains sensitive column %q", name)
		}
	}
	if err := columns.Err(); err != nil {
		t.Fatal(err)
	}
}
