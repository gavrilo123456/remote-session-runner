//go:build p131host

package runnerlocal

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/audit"
	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestP131MacGracefulShutdownProcessHost(t *testing.T) {
	if os.Getenv("RSR_P131_HOST_GATE") != "1" {
		t.Skip("set RSR_P131_HOST_GATE=1 to run the real macOS LaunchAgent shutdown gate")
	}
	if runtime.GOOS != "darwin" {
		t.Fatalf("P131 process gate must run on macOS, got %s", runtime.GOOS)
	}
	current, err := user.Current()
	if err != nil || current.Username != config.MacAccount {
		t.Fatalf("P131 Mac account=%v err=%v, want %s", current, err, config.MacAccount)
	}
	configPath := strings.TrimSpace(os.Getenv("RUNNER_P131_MAC_CONFIG"))
	if configPath == "" || !filepath.IsAbs(configPath) {
		t.Fatal("RUNNER_P131_MAC_CONFIG must name the isolated owner-only Mac config")
	}
	loaded, err := config.LoadFile(configPath)
	if err != nil {
		t.Fatalf("load isolated Mac config: %v", err)
	}
	settings, ok := loaded.MacSettings()
	if !ok || settings.Account != config.MacAccount {
		t.Fatal("P131 host config does not select the Mac account")
	}

	launchDomain := fmt.Sprintf("gui/%d", os.Getuid())
	launchAgents := filepath.Join(current.HomeDir, "Library", "LaunchAgents")
	localPlist := filepath.Join(launchAgents, "com.remote-session-runner.local.plist")
	localdPlist := filepath.Join(launchAgents, "com.remote-session-runner.locald.plist")
	waitP131Socket(t, settings.APISocket, true, 10*time.Second)
	waitP131Socket(t, settings.LocalDSocket, true, 10*time.Second)
	client, transport := p131UnixClient(settings.APISocket)
	defer transport.CloseIdleConnections()

	database, authority := p131OpenAuthority(t, configPath)
	closeDatabase := func() {
		if database != nil {
			if err := database.Close(); err != nil {
				t.Errorf("close host-gate database handle: %v", err)
			}
			database = nil
			authority = nil
		}
	}
	defer closeDatabase()

	baseContext, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	createBody, err := json.Marshal(map[string]any{
		"environment":      "mac-dev",
		"execution_target": map[string]string{"kind": "local", "profile": "mac-workstation"},
		"source":           map[string]string{"mode": "empty"},
	})
	if err != nil {
		t.Fatal(err)
	}
	createResponse := p131PostJSON(t, baseContext, client, "http://runner/v1/sessions", "p131-create-"+strconv.FormatInt(time.Now().UnixNano(), 10), createBody)
	var acceptedSession struct {
		SessionID       string `json:"session_id"`
		IntentID        string `json:"intent_id"`
		AcceptanceScope string `json:"acceptance_scope"`
	}
	p131Decode(t, createResponse, &acceptedSession)
	if createResponse.StatusCode != http.StatusAccepted || acceptedSession.SessionID == "" || acceptedSession.IntentID == "" || acceptedSession.AcceptanceScope != "local_intent" {
		t.Fatalf("create acceptance status=%d body=%+v", createResponse.StatusCode, acceptedSession)
	}
	p131CloseResponse(t, createResponse)
	if err := p131WaitForSession(baseContext, authority, domain.SessionID(acceptedSession.SessionID)); err != nil {
		t.Fatal(err)
	}

	commandBody, err := json.Marshal(map[string]any{"script": "printf 'P131_BEFORE\\n'; sleep 4; printf 'P131_AFTER\\n'"})
	if err != nil {
		t.Fatal(err)
	}
	commandURL := "http://runner/v1/sessions/" + url.PathEscape(acceptedSession.SessionID) + "/commands"
	commandResponse := p131PostJSON(t, baseContext, client, commandURL, "p131-submit-"+strconv.FormatInt(time.Now().UnixNano(), 10), commandBody)
	var acceptedCommand struct {
		CommandID       string `json:"command_id"`
		SessionID       string `json:"session_id"`
		IntentID        string `json:"intent_id"`
		AcceptanceScope string `json:"acceptance_scope"`
	}
	p131Decode(t, commandResponse, &acceptedCommand)
	if commandResponse.StatusCode != http.StatusAccepted || acceptedCommand.CommandID == "" || acceptedCommand.SessionID != acceptedSession.SessionID || acceptedCommand.IntentID == "" || acceptedCommand.AcceptanceScope != "local_intent" {
		t.Fatalf("command acceptance status=%d body=%+v", commandResponse.StatusCode, acceptedCommand)
	}
	p131CloseResponse(t, commandResponse)
	commandID := domain.CommandID(acceptedCommand.CommandID)
	cursor, err := p131WaitForRunningOutput(baseContext, authority, commandID, "P131_BEFORE")
	if err != nil {
		t.Fatal(err)
	}

	streamContext, cancelStream := context.WithTimeout(baseContext, 20*time.Second)
	defer cancelStream()
	streamURL := fmt.Sprintf("http://runner/v1/commands/%s/events?after=%d&follow=true", url.PathEscape(acceptedCommand.CommandID), cursor)
	streamRequest, err := http.NewRequestWithContext(streamContext, http.MethodGet, streamURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	streamResponse, err := client.Do(streamRequest)
	if err != nil {
		t.Fatalf("open resumable event follower at cursor %d: %v", cursor, err)
	}
	if streamResponse.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(streamResponse.Body)
		_ = streamResponse.Body.Close()
		t.Fatalf("event follower status=%d body=%s", streamResponse.StatusCode, body)
	}
	streamDone := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(streamResponse.Body)
		for scanner.Scan() {
		}
		streamDone <- scanner.Err()
	}()

	// Close ingress first so no new intent can reach locald, then stop the
	// executor while its accepted command is still within the drain budget.
	p131Launchctl(t, "bootout", launchDomain, localPlist)
	waitP131Socket(t, settings.APISocket, false, 10*time.Second)
	p131Launchctl(t, "bootout", launchDomain, localdPlist)
	waitP131Socket(t, settings.LocalDSocket, false, 12*time.Second)
	select {
	case streamErr := <-streamDone:
		if streamErr != nil {
			t.Logf("event follower closed for cursor-based resume: %v", streamErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("event follower remained open after launchd stopped runner-local")
	}
	closeDatabase()

	p131Launchctl(t, "bootstrap", launchDomain, localdPlist)
	p131Launchctl(t, "bootstrap", launchDomain, localPlist)
	waitP131Socket(t, settings.LocalDSocket, true, 10*time.Second)
	waitP131Socket(t, settings.APISocket, true, 10*time.Second)

	database, authority = p131OpenAuthority(t, configPath)
	command, err := authority.GetCommand(baseContext, commandID)
	if err != nil {
		t.Fatalf("read command after LaunchAgent restart: %v", err)
	}
	if command.State != domain.CommandStateSucceeded || !command.OutputComplete || command.FinalEventSequence == nil {
		t.Fatalf("accepted command did not drain to a complete truthful result: state=%s output_complete=%v final_sequence=%v", command.State, command.OutputComplete, command.FinalEventSequence)
	}
	session, err := authority.GetSession(baseContext, domain.SessionID(acceptedSession.SessionID))
	if err != nil {
		t.Fatalf("read session after LaunchAgent restart: %v", err)
	}
	if session.State != domain.SessionStateClosed {
		t.Fatalf("Mac runtime session after graceful service stop = %s, want closed after confirmed teardown", session.State)
	}

	resumeURL := fmt.Sprintf("http://runner/v1/commands/%s/events?after=%d", url.PathEscape(acceptedCommand.CommandID), cursor)
	resumeRequest, err := http.NewRequestWithContext(baseContext, http.MethodGet, resumeURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resumeResponse, err := client.Do(resumeRequest)
	if err != nil {
		t.Fatalf("resume events after restart at cursor %d: %v", cursor, err)
	}
	if resumeResponse.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resumeResponse.Body)
		_ = resumeResponse.Body.Close()
		t.Fatalf("resumed event status=%d body=%s", resumeResponse.StatusCode, body)
	}
	resumedEvents, err := p131ReadEventFrames(resumeResponse.Body)
	closeErr := resumeResponse.Body.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("read resumed event frames: scan=%v close=%v", err, closeErr)
	}
	if len(resumedEvents) == 0 {
		t.Fatalf("event replay after cursor %d returned no remaining events", cursor)
	}
	expectedSequence := cursor + 1
	terminal := false
	foundAfter := false
	for _, event := range resumedEvents {
		if event.Sequence != expectedSequence {
			t.Fatalf("resumed event sequence=%d, want contiguous sequence %d", event.Sequence, expectedSequence)
		}
		expectedSequence++
		if event.Type == "command_succeeded" {
			terminal = true
		}
		payload, decodeErr := base64.StdEncoding.DecodeString(event.DataBase64)
		if decodeErr == nil && strings.Contains(string(payload), "P131_AFTER") {
			foundAfter = true
		}
	}
	if !terminal || !foundAfter {
		t.Fatalf("resumed events omitted the terminal outcome or post-drain output: terminal=%v output=%v events=%+v", terminal, foundAfter, resumedEvents)
	}

	auditRows, err := authority.ListAuditRecords(baseContext, 256)
	if err != nil {
		t.Fatalf("read audit rows after restart: %v", err)
	}
	foundSubmitAudit := false
	for _, record := range auditRows {
		if record.CommandID == commandID && record.Action == audit.ActionSubmit && record.Outcome == audit.OutcomeAllowed {
			foundSubmitAudit = true
			break
		}
	}
	if !foundSubmitAudit {
		t.Fatalf("durable allowed submit audit row for command %s was not present after restart", commandID)
	}
	t.Logf("machine=Mac account=%s uid=%s os=%s go=%s command=%s drain_state=%s cursor_before=%d resumed_through=%d audit=present", current.Username, current.Uid, runtime.GOOS, runtime.Version(), commandID, command.State, cursor, expectedSequence-1)
}

type p131EventFrame struct {
	Sequence   int64  `json:"sequence"`
	Type       string `json:"type"`
	DataBase64 string `json:"data_base64"`
}

func p131OpenAuthority(t *testing.T, configPath string) (*sql.DB, *store.AuthorityStore) {
	t.Helper()
	loaded, err := config.LoadFile(configPath)
	if err != nil {
		t.Fatalf("reload P131 Mac config: %v", err)
	}
	settings, ok := loaded.MacSettings()
	if !ok {
		t.Fatal("P131 Mac settings are missing")
	}
	database, err := store.Open(context.Background(), settings.Database)
	if err != nil {
		t.Fatalf("open P131 authority database: %v", err)
	}
	authority, err := store.NewAuthorityStore(database)
	if err != nil {
		_ = database.Close()
		t.Fatalf("construct P131 authority store: %v", err)
	}
	return database, authority
}

func p131UnixClient(socketPath string) (*http.Client, *http.Transport) {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	}}
	return &http.Client{Transport: transport}, transport
}

func p131PostJSON(t *testing.T, ctx context.Context, client *http.Client, endpoint, key string, body []byte) *http.Response {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", key)
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("POST %s: %v", endpoint, err)
	}
	return response
}

func p131Decode(t *testing.T, response *http.Response, destination any) {
	t.Helper()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read API response: %v", err)
	}
	if err := json.Unmarshal(body, destination); err != nil {
		t.Fatalf("decode API response status=%d body=%s: %v", response.StatusCode, body, err)
	}
}

func p131CloseResponse(t *testing.T, response *http.Response) {
	t.Helper()
	if err := response.Body.Close(); err != nil {
		t.Fatalf("close API response body: %v", err)
	}
}

func p131WaitForSession(ctx context.Context, authority *store.AuthorityStore, id domain.SessionID) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		session, err := authority.GetSession(ctx, id)
		if err == nil && session.State == domain.SessionStateReady {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("session %s did not become ready: last state=%s err=%v", id, session.State, err)
		case <-ticker.C:
		}
	}
}

func p131WaitForRunningOutput(ctx context.Context, authority *store.AuthorityStore, id domain.CommandID, marker string) (int64, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		command, commandErr := authority.GetCommand(ctx, id)
		if commandErr == nil && command.State == domain.CommandStateRunning {
			events, eventsErr := authority.ListCommandEvents(ctx, id)
			if eventsErr == nil {
				lastSequence := int64(0)
				for _, event := range events {
					lastSequence = event.Sequence
					if strings.Contains(string(event.Payload), marker) {
						return lastSequence, nil
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return 0, fmt.Errorf("command %s did not publish %q while running: state=%s err=%v", id, marker, command.State, commandErr)
		case <-ticker.C:
		}
	}
}

func p131Launchctl(t *testing.T, verb, launchDomain, plist string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "launchctl", verb, launchDomain, plist)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("launchctl %s %s %s failed: %v: %s", verb, launchDomain, filepath.Base(plist), err, strings.TrimSpace(string(output)))
	}
}

func waitP131Socket(t *testing.T, path string, exists bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		info, err := os.Lstat(path)
		present := err == nil && info.Mode()&os.ModeSocket != 0
		if present == exists {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	if exists {
		t.Fatalf("timed out waiting for socket: %s", path)
	}
	t.Fatalf("socket remained after launchd stop: %s", path)
}

func p131ReadEventFrames(reader io.Reader) ([]p131EventFrame, error) {
	var events []p131EventFrame
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		var event p131EventFrame
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, fmt.Errorf("decode event frame: %w", err)
		}
		events = append(events, event)
	}
	return events, scanner.Err()
}
