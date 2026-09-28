//go:build p117twohost

package runnerd

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

type p127RemoteAuditRecord struct {
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

func TestP127UbuntuAuditRowsAndJournalForDirectIngress(t *testing.T) {
	if os.Getenv("RSR_P127_AUDIT_HOST_GATE") != "1" {
		t.Skip("set RSR_P127_AUDIT_HOST_GATE=1 to inspect live Ubuntu audit rows and service logs")
	}
	identity := p127RequiredHostFixture(t, "RUNNER_P124_SSH_IDENTITY")
	knownHosts := p127RequiredHostFixture(t, "RUNNER_P124_SSH_KNOWN_HOSTS")
	sinceID := p127AuditHighWater(t, identity, knownHosts)
	logSince := time.Now().Add(-time.Second).Unix()

	roots := p117LoadServerRoots(t)
	ownerCertificate := p117LoadClientCertificate(t, "RUNNER_P117_CLIENT_CERT", "RUNNER_P117_CLIENT_KEY")
	otherCertificate := p117LoadClientCertificate(t, "RUNNER_P117_OTHER_CLIENT_CERT", "RUNNER_P117_OTHER_CLIENT_KEY")
	owner := p117HTTPClient(roots, &ownerCertificate, tls.VersionTLS13, tls.VersionTLS13, "129.151.232.40")
	t.Cleanup(owner.CloseIdleConnections)
	p117ExerciseAuthorizedAPI(t, owner, roots, &otherCertificate)

	rows := p127ReadAuditRecords(t, identity, knownHosts, sinceID)
	counts := make(map[string]map[string]int)
	var sessionID, commandID string
	for _, row := range rows {
		if row.ID <= sinceID || row.Ingress != "direct_mtls" || row.OccurredAt == "" {
			t.Errorf("invalid or uncorrelated Ubuntu audit row: %+v", row)
		}
		if counts[row.Action] == nil {
			counts[row.Action] = make(map[string]int)
		}
		counts[row.Action][row.Outcome]++
		if row.Action == "create" && row.Outcome == "allowed" && row.PrincipalID == "tomasz.walczuk" {
			sessionID = row.SessionID
		}
		if row.Action == "submit" && row.Outcome == "allowed" && row.PrincipalID == "tomasz.walczuk" {
			commandID = row.CommandID
		}
		if row.Outcome == "denied" && row.ReasonCode == "controller_denied" && row.PrincipalID != "p117-cross-controller" {
			t.Errorf("controller denial principal=%q, want p117-cross-controller", row.PrincipalID)
		}
		if row.Outcome == "denied" && row.Action == "create" && (row.ReasonCode != "environment_denied" || row.Environment != "p117-unconfigured-environment") {
			t.Errorf("environment denial row=%+v", row)
		}
	}
	want := map[string]map[string]int{
		"create": {"allowed": 1, "denied": 1},
		"submit": {"allowed": 1, "denied": 1},
		"cancel": {"allowed": 1, "denied": 1},
		"close":  {"allowed": 1, "denied": 1},
	}
	for action, outcomes := range want {
		for outcome, count := range outcomes {
			if got := counts[action][outcome]; got != count {
				t.Errorf("Ubuntu %s %s audit rows=%d, want %d; rows=%+v", action, outcome, got, count, rows)
			}
		}
	}
	if sessionID == "" || commandID == "" {
		t.Fatalf("Ubuntu audit results did not identify the current allowed session/command: %+v", rows)
	}

	journal := p127RemoteCommand(t, identity, knownHosts, fmt.Sprintf("sudo -n journalctl -u runnerd.service --since=@%d -n 2000 --no-pager -o cat", logSince))
	var auditLines []string
	for _, line := range strings.Split(string(journal), "\n") {
		if strings.Contains(line, "runner authorization action") {
			auditLines = append(auditLines, line)
		}
	}
	logs := strings.Join(auditLines, "\n")
	for _, wantField := range []string{
		"action=create", "action=submit", "action=cancel", "action=close", "principal_id=tomasz.walczuk",
		"principal_id=p117-cross-controller", "ingress=direct_mtls", "outcome=allowed", "outcome=denied",
		"environment=p117-unconfigured-environment", "session_id=" + sessionID, "command_id=" + commandID,
	} {
		if !strings.Contains(logs, wantField) {
			t.Errorf("Ubuntu service audit logs omitted %q: %s", wantField, logs)
		}
	}
	for _, forbidden := range []string{"p117-public-mtls-ok", "printf denied", "BEGIN PRIVATE KEY", "credential_value_marker"} {
		if strings.Contains(logs, forbidden) {
			t.Errorf("Ubuntu service audit logs included sensitive script/output/credential marker %q", forbidden)
		}
	}
	t.Logf("P127 Ubuntu audit PASS: inspected %d safe audit rows and %d structured authorization log lines", len(rows), len(auditLines))
}

func p127ReadAuditRecords(t *testing.T, identity, knownHosts string, afterID int64) []p127RemoteAuditRecord {
	t.Helper()
	python := fmt.Sprintf(`import json,sqlite3; c=sqlite3.connect("file:/home/ubuntu/.local/share/remote-session-runner/state/remote.db?mode=ro",uri=True); q="SELECT id,principal_type,principal_id,ingress,COALESCE(environment,''),COALESCE(session_id,''),COALESCE(command_id,''),COALESCE(job_id,''),action,outcome,reason_code,occurred_at FROM runner_audit_records WHERE id > %d ORDER BY id LIMIT 10000"; cur=c.execute(q); keys=[d[0] for d in cur.description]; print(json.dumps([dict(zip(keys,r)) for r in cur.fetchall()]))`, afterID)
	contents := p127RemoteCommand(t, identity, knownHosts, "python3 -c "+p127ShellQuote(python))
	var records []p127RemoteAuditRecord
	if err := json.Unmarshal(contents, &records); err != nil {
		t.Fatalf("decode Ubuntu audit query JSON: %v", err)
	}
	return records
}

func p127AuditHighWater(t *testing.T, identity, knownHosts string) int64 {
	t.Helper()
	python := `import sqlite3; c=sqlite3.connect("file:/home/ubuntu/.local/share/remote-session-runner/state/remote.db?mode=ro",uri=True); print(c.execute("SELECT COALESCE(MAX(id),0) FROM runner_audit_records").fetchone()[0])`
	contents := p127RemoteCommand(t, identity, knownHosts, "python3 -c "+p127ShellQuote(python))
	value, err := strconv.ParseInt(strings.TrimSpace(string(contents)), 10, 64)
	if err != nil {
		t.Fatalf("decode Ubuntu audit high-water ID: %v", err)
	}
	return value
}

func p127RemoteCommand(t *testing.T, identity, knownHosts, command string) []byte {
	t.Helper()
	args := []string{
		"-F", "/dev/null", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes",
		"-o", "StrictHostKeyChecking=yes", "-o", "UpdateHostKeys=no", "-o", "ConnectTimeout=10", "-o", "GlobalKnownHostsFile=/dev/null",
		"-o", p127SSHPathOption("UserKnownHostsFile", knownHosts), "-o", "ClearAllForwardings=yes",
		"-o", "RequestTTY=no", "-i", identity, "-l", "ubuntu", "-p", "22", "129.151.232.40", command,
	}
	process := exec.Command("ssh", args...)
	process.Stderr = io.Discard
	contents, err := process.Output()
	if err != nil {
		t.Fatalf("read-only Ubuntu audit inspection command failed: %v", err)
	}
	return contents
}

func p127RequiredHostFixture(t *testing.T, name string) string {
	t.Helper()
	path := os.Getenv(name)
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		t.Fatalf("%s must name an absolute host fixture path", name)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("%s must name an existing regular file", name)
	}
	if name == "RUNNER_P124_SSH_IDENTITY" {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0o077 != 0 || info.Mode().Perm()&0o400 == 0 {
			t.Fatalf("%s must be owner-readable and owner-only", name)
		}
	}
	return path
}

func p127SSHPathOption(name, path string) string {
	path = strings.ReplaceAll(path, `\`, `\\`)
	path = strings.ReplaceAll(path, `"`, `\"`)
	return name + `="` + path + `"`
}

func p127ShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
