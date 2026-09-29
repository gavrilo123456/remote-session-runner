//go:build p117twohost

package runnerd

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

const p132UbuntuServiceRoot = "/home/ubuntu/.local/share/remote-session-runner"

type p132HostEventRecord struct {
	Sequence int64  `json:"sequence"`
	Type     string `json:"type"`
}

type p132HostAuditRecord struct {
	ID          int64  `json:"id"`
	PrincipalID string `json:"principal_id"`
	Ingress     string `json:"ingress"`
	Action      string `json:"action"`
	Outcome     string `json:"outcome"`
	SessionID   string `json:"session_id"`
	CommandID   string `json:"command_id"`
}

type p132HostCommandSnapshot struct {
	SessionID          string                `json:"session_id"`
	SessionState       string                `json:"session_state"`
	CommandID          string                `json:"command_id"`
	CommandState       string                `json:"command_state"`
	FinalEventSequence *int64                `json:"final_event_sequence"`
	OutputComplete     bool                  `json:"output_complete"`
	Events             []p132HostEventRecord `json:"events"`
	Audits             []p132HostAuditRecord `json:"audits"`
}

func TestP132LinuxGracefulShutdownProcessHost(t *testing.T) {
	if os.Getenv("RSR_P132_HOST_GATE") != "1" {
		t.Skip("set RSR_P132_HOST_GATE=1 to run the Linux systemd graceful-shutdown gate")
	}
	identity := p127RequiredHostFixture(t, "RUNNER_P124_INSPECT_SSH_IDENTITY")
	knownHosts := p127RequiredHostFixture(t, "RUNNER_P124_SSH_KNOWN_HOSTS")
	expectedCommit := os.Getenv("RSR_P132_EXPECTED_COMMIT")
	if len(expectedCommit) != 40 {
		t.Fatalf("RSR_P132_EXPECTED_COMMIT must be the 40-character tested commit SHA")
	}
	for _, character := range expectedCommit {
		if !strings.ContainsRune("0123456789abcdef", character) {
			t.Fatalf("RSR_P132_EXPECTED_COMMIT is not a lowercase Git SHA")
		}
	}
	checkout := "/home/ubuntu/projects/remote-session-runner"
	checkCheckout := fmt.Sprintf("cd %s && test \"$(git branch --show-current)\" = dev && test \"$(git rev-parse HEAD)\" = %s && test \"$(git rev-parse origin/dev)\" = %s && test -z \"$(git status --porcelain)\" && sudo -n systemctl is-active --quiet runnerd.service",
		p127ShellQuote(checkout), p127ShellQuote(expectedCommit), p127ShellQuote(expectedCommit))
	p132RemoteMust(t, identity, knownHosts, 20*time.Second, checkCheckout)

	// Refuse to restart the selected authority if any user work is live.
	statusCommand := fmt.Sprintf("cd %s && GOCACHE=/home/ubuntu/.cache/go-build GOMODCACHE=/home/ubuntu/go/pkg/mod GOTOOLCHAIN=local RSR_P128_HOST_STATUS=1 make GO=%s test-p128-host-status",
		p127ShellQuote(checkout), p127ShellQuote("/home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go"))
	statusOutput := p132RemoteMust(t, identity, knownHosts, 45*time.Second, statusCommand)
	if !strings.Contains(string(statusOutput), `"active_sessions":0`) || !strings.Contains(string(statusOutput), `"running_commands":0`) || !strings.Contains(string(statusOutput), `"unreleased_slots":0`) || !strings.Contains(string(statusOutput), `"unfinished_jobs":0`) {
		t.Fatalf("Ubuntu P128 read-only guard did not confirm an idle authority: %s", statusOutput)
	}
	auditAfter := p127AuditHighWater(t, identity, knownHosts)

	backupSuffix := fmt.Sprintf("%d", time.Now().UnixNano())
	backupPath := fmt.Sprintf("%s/tmp/.runnerd-p132-before-%s", p132UbuntuServiceRoot, backupSuffix)
	unitBackupPath := fmt.Sprintf("%s/tmp/.runnerd-p132-unit-before-%s", p132UbuntuServiceRoot, backupSuffix)
	backupCommand := fmt.Sprintf("set -eu; test ! -e %s; test ! -e %s; test -f %s/bin/runnerd; test ! -L %s/bin/runnerd; test -f /etc/systemd/system/runnerd.service; test ! -L /etc/systemd/system/runnerd.service; cp -p %s/bin/runnerd %s; chmod 700 %s; sudo -n install -o ubuntu -g ubuntu -m 600 /etc/systemd/system/runnerd.service %s",
		p127ShellQuote(backupPath), p127ShellQuote(unitBackupPath), p127ShellQuote(p132UbuntuServiceRoot),
		p127ShellQuote(p132UbuntuServiceRoot), p127ShellQuote(p132UbuntuServiceRoot), p127ShellQuote(backupPath),
		p127ShellQuote(backupPath), p127ShellQuote(unitBackupPath))

	var owner *http.Client
	var sessionID, commandID string
	phasePassed := false
	backupReady := false
	stopVerified := false
	t.Cleanup(func() {
		if owner != nil {
			defer owner.CloseIdleConnections()
		}
		if !backupReady {
			cleanup := "rm -f " + p127ShellQuote(backupPath) + " " + p127ShellQuote(unitBackupPath)
			if _, err := p132RunRemote(identity, knownHosts, 20*time.Second, cleanup); err != nil {
				t.Errorf("remove partial P132 temporary backups: %v", err)
			}
			return
		}
		if !phasePassed {
			if stopVerified && sessionID != "" && commandID != "" {
				cleanup := fmt.Sprintf("cd %s && GOCACHE=/home/ubuntu/.cache/go-build GOMODCACHE=/home/ubuntu/go/pkg/mod RSR_P132_HOST_SESSION_ID=%s RSR_P132_HOST_COMMAND_ID=%s make GO=%s test-p132-host-cleanup",
					p127ShellQuote(checkout), p127ShellQuote(sessionID), p127ShellQuote(commandID),
					p127ShellQuote("/home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go"))
				if _, err := p132RunRemote(identity, knownHosts, 45*time.Second, cleanup); err != nil {
					t.Errorf("release only the P132 fixture's confirmed-dead lost capacity: %v", err)
				}
			} else {
				_, startErr := p132RunRemote(identity, knownHosts, 30*time.Second, "sudo -n systemctl start runnerd.service || sudo -n systemctl is-active --quiet runnerd.service")
				if startErr != nil {
					t.Errorf("restore active runnerd before P132 cleanup: %v", startErr)
				}
				ready := fmt.Sprintf("for attempt in $(seq 1 40); do if sudo -n systemctl is-active --quiet runnerd.service && test -S %s/run/runnerd.sock && ss -H -ltn 'sport = :8443' | grep -q '10\\.0\\.0\\.200:8443'; then exit 0; fi; sleep 0.25; done; exit 1", p127ShellQuote(p132UbuntuServiceRoot))
				if _, err := p132RunRemote(identity, knownHosts, 15*time.Second, ready); err != nil {
					t.Errorf("wait for runnerd cleanup API: %v", err)
				}
				if owner != nil && sessionID != "" {
					if err := p132CloseSession(owner, sessionID); err != nil {
						t.Errorf("close P132 fixture session during cleanup: %v", err)
					}
				}
			}
			restore := fmt.Sprintf("set -eu; sudo -n systemctl stop runnerd.service >/dev/null 2>&1 || true; install -m 700 %s %s/bin/runnerd; sudo -n install -o root -g root -m 644 %s /etc/systemd/system/runnerd.service; sudo -n systemctl daemon-reload; sudo -n systemctl start runnerd.service; rm -f %s %s",
				p127ShellQuote(backupPath), p127ShellQuote(p132UbuntuServiceRoot), p127ShellQuote(unitBackupPath),
				p127ShellQuote(backupPath), p127ShellQuote(unitBackupPath))
			if _, err := p132RunRemote(identity, knownHosts, 45*time.Second, restore); err != nil {
				t.Errorf("restore pre-P132 runnerd binary and service: %v", err)
			}
		} else {
			if _, err := p132RunRemote(identity, knownHosts, 20*time.Second, "sudo -n systemctl is-active --quiet runnerd.service"); err != nil {
				t.Errorf("P132 passed but runnerd.service is not active: %v", err)
			}
			cleanup := "rm -f " + p127ShellQuote(backupPath) + " " + p127ShellQuote(unitBackupPath)
			if _, err := p132RunRemote(identity, knownHosts, 20*time.Second, cleanup); err != nil {
				t.Errorf("remove P132 temporary backups: %v", err)
			}
		}
	})
	p132RemoteMust(t, identity, knownHosts, 20*time.Second, backupCommand)
	backupReady = true

	// Install the synchronized revision from the Ubuntu checkout after the
	// idle-state guard. This keeps project source changes Git-only.
	p132RemoteMust(t, identity, knownHosts, 45*time.Second, "sudo -n systemctl stop runnerd.service")
	installCommand := fmt.Sprintf("cd %s && deploy/linux/install-systemd-service.sh", p127ShellQuote(checkout))
	p132RemoteMust(t, identity, knownHosts, 90*time.Second, installCommand)
	readyCommand := fmt.Sprintf("sudo -n systemctl is-active --quiet runnerd.service && test \"$(sudo -n systemctl show -p TimeoutStopUSec --value runnerd.service)\" = 30s && test \"$(sudo -n systemctl show -p KillMode --value runnerd.service)\" = mixed && test \"$(stat -c '%%u:%%a' %s/run/runnerd.sock)\" = 1001:600 && ss -H -ltn 'sport = :8443' | grep -q '10\\.0\\.0\\.200:8443'",
		p127ShellQuote(p132UbuntuServiceRoot))
	p132RemoteMust(t, identity, knownHosts, 20*time.Second, readyCommand)

	roots := p117LoadServerRoots(t)
	certificate := p117LoadClientCertificate(t, "RUNNER_P117_CLIENT_CERT", "RUNNER_P117_CLIENT_KEY")
	owner = p117HTTPClient(roots, &certificate, tls.VersionTLS13, tls.VersionTLS13, "129.151.232.40")
	longClient := *owner
	longClient.Timeout = 0

	createStatus, createBody := p117Do(t, owner, http.MethodPost, p117PublicEndpoint+"/v1/sessions", []byte(p107CreateBody), p117Key(t), "")
	if createStatus != http.StatusAccepted {
		t.Fatalf("P132 create session status=%d body=%s", createStatus, createBody)
	}
	var created directSessionAcceptance
	if err := json.Unmarshal(createBody, &created); err != nil || created.SessionID == "" {
		t.Fatalf("decode P132 session acceptance=%+v err=%v", created, err)
	}
	sessionID = created.SessionID
	p117WaitForSessionState(t, owner, sessionID, "ready")

	script := "/bin/sh -c 'printf \"P132_CHILD_PID=%s\\nP132_RUNNING\\n\" \"$$\"; exec /bin/sleep 120'"
	commandBody, err := json.Marshal(directSubmitCommandRequest{Script: &script})
	if err != nil {
		t.Fatal(err)
	}
	commandStatus, commandResponse := p117Do(t, owner, http.MethodPost, p117PublicEndpoint+"/v1/sessions/"+sessionID+"/commands", commandBody, p117Key(t), "")
	if commandStatus != http.StatusAccepted {
		t.Fatalf("P132 command acceptance status=%d body=%s", commandStatus, commandResponse)
	}
	var accepted directCommandAcceptance
	if err := json.Unmarshal(commandResponse, &accepted); err != nil || accepted.CommandID == "" {
		t.Fatalf("decode P132 command acceptance=%+v err=%v", accepted, err)
	}
	commandID = accepted.CommandID

	followContext, cancelFollow := context.WithCancel(context.Background())
	defer cancelFollow()
	followRequest, err := http.NewRequestWithContext(followContext, http.MethodGet,
		p117PublicEndpoint+"/v1/commands/"+commandID+"/events?after=0&follow=true", nil)
	if err != nil {
		t.Fatal(err)
	}
	followResponse, err := longClient.Do(followRequest)
	if err != nil {
		t.Fatalf("open P132 live event follower: %v", err)
	}
	defer followResponse.Body.Close()
	if followResponse.StatusCode != http.StatusOK {
		body, _ := p132ReadLimited(followResponse)
		t.Fatalf("P132 live event follower status=%d body=%s", followResponse.StatusCode, body)
	}
	eventFrames := make(chan directCommandEvent, 64)
	streamDone := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(followResponse.Body)
		scanner.Buffer(make([]byte, 1024), 64*1024)
		for scanner.Scan() {
			var event directCommandEvent
			if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
				streamDone <- fmt.Errorf("decode P132 live event frame: %w", err)
				close(eventFrames)
				return
			}
			eventFrames <- event
		}
		streamDone <- scanner.Err()
		close(eventFrames)
	}()

	var stdout strings.Builder
	var markerCursor int64
	var childPID int
	markerDeadline := time.NewTimer(20 * time.Second)
	defer markerDeadline.Stop()
	for childPID == 0 {
		select {
		case event, ok := <-eventFrames:
			if !ok {
				select {
				case streamErr := <-streamDone:
					t.Fatalf("P132 event follower ended before its active-command marker: %v", streamErr)
				default:
					t.Fatal("P132 event follower ended before its active-command marker")
				}
			}
			if event.Type == "stdout" {
				payload, err := base64.StdEncoding.DecodeString(event.DataBase64)
				if err != nil {
					t.Fatalf("decode P132 stdout event: %v", err)
				}
				stdout.Write(payload)
				if strings.Contains(stdout.String(), "P132_RUNNING") {
					markerCursor = event.Sequence
					childPID = p132ChildPID(t, stdout.String())
				}
			}
		case <-markerDeadline.C:
			t.Fatalf("P132 active-command marker was not observed; stdout=%q", stdout.String())
		}
	}

	stopStarted := time.Now()
	p132RemoteMust(t, identity, knownHosts, 40*time.Second, "sudo -n systemctl stop runnerd.service")
	stopDuration := time.Since(stopStarted)
	if stopDuration >= 30*time.Second {
		t.Fatalf("P132 systemd stop took %s, exceeding TimeoutStopSec=30s", stopDuration)
	}
	t.Logf("P132 systemd stop with active command completed in %s", stopDuration)

	stoppedCommand := fmt.Sprintf("! sudo -n systemctl is-active --quiet runnerd.service && test ! -e %s/run/runnerd.sock && ! ss -H -ltn 'sport = :8443' | grep -q '10\\.0\\.0\\.200:8443' && ! kill -0 %d 2>/dev/null && sudo -n systemctl show -p Result --value runnerd.service | grep -qx success && sudo -n systemctl show -p ExecMainStatus --value runnerd.service | grep -qx 0",
		p127ShellQuote(p132UbuntuServiceRoot), childPID)
	p132RemoteMust(t, identity, knownHosts, 20*time.Second, stoppedCommand)
	stopVerified = true

	readCommand := fmt.Sprintf("cd %s && GOCACHE=/home/ubuntu/.cache/go-build GOMODCACHE=/home/ubuntu/go/pkg/mod RSR_P132_HOST_SESSION_ID=%s RSR_P132_HOST_COMMAND_ID=%s RSR_P132_HOST_AUDIT_AFTER_ID=%d make GO=%s test-p132-host-read",
		p127ShellQuote(checkout), p127ShellQuote(sessionID), p127ShellQuote(commandID), auditAfter,
		p127ShellQuote("/home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go"))
	hostSnapshot := p132ReadHostSnapshot(t, identity, knownHosts, readCommand)
	if hostSnapshot.SessionState != "closed" || hostSnapshot.CommandState != "cancelled" || !hostSnapshot.OutputComplete || hostSnapshot.FinalEventSequence == nil {
		t.Fatalf("Ubuntu shutdown state is not a confirmed cancellation: %+v", hostSnapshot)
	}
	if len(hostSnapshot.Events) == 0 || hostSnapshot.Events[len(hostSnapshot.Events)-1].Sequence != *hostSnapshot.FinalEventSequence || hostSnapshot.Events[len(hostSnapshot.Events)-1].Type != "command_cancelled" {
		t.Fatalf("Ubuntu shutdown event tail is not durably terminal: %+v", hostSnapshot.Events)
	}
	var shutdownAuditFound bool
	for _, record := range hostSnapshot.Audits {
		if record.PrincipalID == "tomasz.walczuk" && record.Ingress == "internal" && record.Action == "close" && record.Outcome == "allowed" && record.SessionID == sessionID {
			shutdownAuditFound = true
		}
	}
	if !shutdownAuditFound {
		t.Fatalf("Ubuntu shutdown close audit was not durable before restart: %+v", hostSnapshot.Audits)
	}

	select {
	case streamErr := <-streamDone:
		t.Logf("P132 live event stream ended during shutdown: %v", streamErr)
	case <-time.After(5 * time.Second):
		_ = followResponse.Body.Close()
		t.Fatal("P132 event follower remained open after runnerd stopped")
	}
	_ = followResponse.Body.Close()

	p132RemoteMust(t, identity, knownHosts, 30*time.Second, "sudo -n systemctl start runnerd.service")
	activeCheck := fmt.Sprintf("for attempt in $(seq 1 40); do if sudo -n systemctl is-active --quiet runnerd.service && test -S %s/run/runnerd.sock && ss -H -ltn 'sport = :8443' | grep -q '10\\.0\\.0\\.200:8443'; then break; fi; sleep 0.25; done; sudo -n systemctl is-active --quiet runnerd.service && test \"$(stat -c '%%u:%%a' %s/run/runnerd.sock)\" = 1001:600",
		p127ShellQuote(p132UbuntuServiceRoot), p127ShellQuote(p132UbuntuServiceRoot))
	p132RemoteMust(t, identity, knownHosts, 20*time.Second, activeCheck)

	status, body := p117Do(t, owner, http.MethodGet, p117PublicEndpoint+"/v1/sessions/"+sessionID, nil, "", "")
	p117RequireStatus(t, status, body, http.StatusOK)
	var sessionRead directSessionReadResponse
	if err := json.Unmarshal(body, &sessionRead); err != nil || sessionRead.Resource.SessionState != "closed" {
		t.Fatalf("session after Linux runnerd restart state=%q decode=%v", sessionRead.Resource.SessionState, err)
	}
	status, body = p117Do(t, owner, http.MethodGet, p117PublicEndpoint+"/v1/commands/"+commandID, nil, "", "")
	p117RequireStatus(t, status, body, http.StatusOK)
	var commandRead directCommandReadResponse
	if err := json.Unmarshal(body, &commandRead); err != nil {
		t.Fatalf("decode P132 command after restart: %v", err)
	}
	if commandRead.Resource.CommandState != "cancelled" || !commandRead.Resource.OutputComplete || commandRead.Resource.FinalEventSequence == nil || *commandRead.Resource.FinalEventSequence != *hostSnapshot.FinalEventSequence {
		t.Fatalf("command after Linux runnerd restart is not the same durable cancellation: %+v", commandRead.Resource)
	}
	status, body = p117Do(t, owner, http.MethodGet,
		fmt.Sprintf("%s/v1/commands/%s/events?after=%d", p117PublicEndpoint, commandID, markerCursor), nil, "", "")
	p117RequireStatus(t, status, body, http.StatusOK)
	replayed := p117DecodeEvents(t, body)
	if len(replayed) == 0 || replayed[len(replayed)-1].Type != "command_cancelled" || replayed[len(replayed)-1].Sequence != *hostSnapshot.FinalEventSequence {
		t.Fatalf("resumed command event stream omitted the durable terminal suffix: %+v", replayed)
	}
	for _, event := range replayed {
		if event.Sequence <= markerCursor {
			t.Fatalf("resumed event sequence=%d did not advance beyond cursor=%d", event.Sequence, markerCursor)
		}
	}
	if events := p127ReadAuditRecords(t, identity, knownHosts, auditAfter); len(events) == 0 {
		t.Fatal("read shutdown audit rows after restart returned no rows")
	}
	finalStatus := fmt.Sprintf("cd %s && GOCACHE=/home/ubuntu/.cache/go-build GOMODCACHE=/home/ubuntu/go/pkg/mod GOTOOLCHAIN=local RSR_P128_HOST_STATUS=1 make GO=%s test-p128-host-status",
		p127ShellQuote(checkout), p127ShellQuote("/home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go"))
	finalOutput := p132RemoteMust(t, identity, knownHosts, 45*time.Second, finalStatus)
	if !strings.Contains(string(finalOutput), `"active_sessions":0`) || !strings.Contains(string(finalOutput), `"running_commands":0`) || !strings.Contains(string(finalOutput), `"unreleased_slots":0`) || !strings.Contains(string(finalOutput), `"unfinished_jobs":0`) {
		t.Fatalf("Ubuntu post-gate read-only guard did not return to idle: %s", finalOutput)
	}

	t.Logf("P132 PASS: Linux systemd stop/restart cancelled command %s truthfully, removed child PID %d, persisted event/audit tails, and resumed events after cursor %d", commandID, childPID, markerCursor)
	phasePassed = true
}

func p132ReadHostSnapshot(t *testing.T, identity, knownHosts, command string) p132HostCommandSnapshot {
	t.Helper()
	output := p132RemoteMust(t, identity, knownHosts, 45*time.Second, command)
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, "P132_HOST_STATE=") {
			var result p132HostCommandSnapshot
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "P132_HOST_STATE=")), &result); err != nil {
				t.Fatalf("decode bounded P132 Ubuntu state: %v", err)
			}
			return result
		}
	}
	t.Fatalf("Ubuntu P132 state reader omitted its result marker: %s", output)
	return p132HostCommandSnapshot{}
}

func p132ChildPID(t *testing.T, output string) int {
	t.Helper()
	marker := "P132_CHILD_PID="
	start := strings.Index(output, marker)
	if start < 0 {
		t.Fatalf("P132 command output omitted child PID marker: %q", output)
	}
	start += len(marker)
	end := strings.IndexByte(output[start:], '\n')
	if end < 0 {
		t.Fatalf("P132 child PID marker was incomplete: %q", output)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(output[start : start+end]))
	if err != nil || pid < 2 {
		t.Fatalf("P132 child PID %q is invalid: %v", output[start:start+end], err)
	}
	return pid
}

func p132ReadLimited(response *http.Response) ([]byte, error) {
	defer response.Body.Close()
	return io.ReadAll(io.LimitReader(response.Body, 4096))
}

func p132CloseSession(client *http.Client, sessionID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, p117PublicEndpoint+"/v1/sessions/"+sessionID, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Idempotency-Key", fmt.Sprintf("p132-cleanup-%d", time.Now().UnixNano()))
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		body, _ := p132ReadLimited(response)
		return fmt.Errorf("close fixture session returned HTTP %d: %s", response.StatusCode, body)
	}
	return nil
}

func p132RemoteMust(t *testing.T, identity, knownHosts string, timeout time.Duration, command string) []byte {
	t.Helper()
	output, err := p132RunRemote(identity, knownHosts, timeout, command)
	if err != nil {
		t.Fatalf("Ubuntu command failed: %v; output=%s", err, output)
	}
	return output
}

func p132RunRemote(identity, knownHosts string, timeout time.Duration, command string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	args := []string{
		"-F", "/dev/null", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes",
		"-o", "StrictHostKeyChecking=yes", "-o", "UpdateHostKeys=no", "-o", "ConnectTimeout=10", "-o", "GlobalKnownHostsFile=/dev/null",
		"-o", p127SSHPathOption("UserKnownHostsFile", knownHosts), "-o", "ClearAllForwardings=yes",
		"-o", "RequestTTY=no", "-i", identity, "-l", "ubuntu", "-p", "22", "129.151.232.40", command,
	}
	process := exec.CommandContext(ctx, "ssh", args...)
	output, err := process.CombinedOutput()
	return output, err
}
