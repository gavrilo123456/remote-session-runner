//go:build p123twohost

package runnerd

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const p123PublicEndpoint = "https://129.151.232.40:8443"

func TestP123DirectDisconnectReplaysWithoutRerun(t *testing.T) {
	if os.Getenv("RSR_P123_HOST_GATE") != "1" {
		t.Skip("set RSR_P123_HOST_GATE=1 to run the real Mac/Ubuntu P-NET-03 gate")
	}
	if runtime.GOOS != "darwin" {
		t.Fatalf("P123 direct client must run on the selected Mac, got %s", runtime.GOOS)
	}
	current, err := user.Current()
	if err != nil || current.Username != "tomasz.walczuk" {
		t.Fatalf("P123 direct client account=%v err=%v, want tomasz.walczuk", current, err)
	}
	t.Logf("machine=Mac account=%s uid=%s os=%s go=%s endpoint=%s", current.Username, current.Uid, runtime.GOOS, runtime.Version(), p123PublicEndpoint)

	client, closeClient := p123NewHTTPClient(t)
	t.Cleanup(closeClient)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	createBody, err := json.Marshal(map[string]any{
		"environment":      "linux-dev",
		"execution_target": map[string]string{"kind": "remote", "profile": "linux-host"},
		"source":           map[string]string{"mode": "empty"},
	})
	if err != nil {
		t.Fatal(err)
	}
	status, body := p123DoJSON(t, ctx, client, http.MethodPost, "/v1/sessions", createBody, p123IdempotencyKey(t))
	if status != http.StatusAccepted {
		t.Fatalf("create session status=%d body=%s", status, body)
	}
	var sessionAcceptance directSessionAcceptance
	if err := json.Unmarshal(body, &sessionAcceptance); err != nil || sessionAcceptance.SessionID == "" {
		t.Fatalf("decode session acceptance: %v", err)
	}
	sessionID := sessionAcceptance.SessionID
	t.Cleanup(func() { p123CleanupDirectSession(t, client, sessionID) })
	p123WaitDirectSession(t, ctx, client, sessionID, "ready")

	commandBody, err := json.Marshal(map[string]any{
		"script": "printf x >> .p123-run-count; printf 'p123-before-disconnect\\n'; sleep 4; printf 'p123-after-disconnect\\n'",
	})
	if err != nil {
		t.Fatal(err)
	}
	status, body = p123DoJSON(t, ctx, client, http.MethodPost, "/v1/sessions/"+sessionID+"/commands", commandBody, p123IdempotencyKey(t))
	if status != http.StatusAccepted {
		t.Fatalf("submit first command status=%d body=%s", status, body)
	}
	var firstAcceptance directCommandAcceptance
	if err := json.Unmarshal(body, &firstAcceptance); err != nil || firstAcceptance.CommandID == "" || firstAcceptance.SessionID != sessionID {
		t.Fatalf("decode first command acceptance: %v", err)
	}

	initialEvents, cursor := p123DisconnectAfterOutput(t, ctx, client, firstAcceptance.CommandID, "p123-before-disconnect\n")
	if cursor <= 0 || len(initialEvents) == 0 {
		t.Fatalf("disconnect cursor=%d initial events=%d", cursor, len(initialEvents))
	}
	command := p123WaitDirectCommand(t, ctx, client, firstAcceptance.CommandID, "succeeded")
	if command.Resource.FinalEventSequence == nil || !command.Resource.OutputComplete {
		t.Fatalf("detached command did not reach a complete terminal result: %+v", command.Resource)
	}

	replayed := p123ReadDirectEvents(t, ctx, client, firstAcceptance.CommandID, cursor)
	combined := append(append([]directCommandEvent(nil), initialEvents...), replayed...)
	if int64(len(combined)) != *command.Resource.FinalEventSequence {
		t.Fatalf("combined replay has %d events, terminal cursor is %d", len(combined), *command.Resource.FinalEventSequence)
	}
	for index, event := range combined {
		if event.CommandID != firstAcceptance.CommandID || event.Sequence != int64(index+1) {
			t.Fatalf("combined event[%d] breaks contiguous replay: %+v", index, event)
		}
	}
	if len(replayed) == 0 || replayed[0].Sequence != cursor+1 {
		t.Fatalf("replay begins at sequence %d after disconnect cursor %d", firstEventSequence(replayed), cursor)
	}
	firstOutput, replayedOutput := p123EventStdout(t, initialEvents), p123EventStdout(t, replayed)
	if !strings.Contains(firstOutput, "p123-before-disconnect\n") || !strings.Contains(replayedOutput, "p123-after-disconnect\n") {
		t.Fatalf("stdout split across disconnect/replay is incomplete: first=%q replay=%q", firstOutput, replayedOutput)
	}

	// A separate target-authoritative read proves the accepted command's
	// side-effect ran once even though its event stream client disconnected.
	verifyBody, err := json.Marshal(map[string]any{"script": "cat .p123-run-count; printf '\\n'"})
	if err != nil {
		t.Fatal(err)
	}
	status, body = p123DoJSON(t, ctx, client, http.MethodPost, "/v1/sessions/"+sessionID+"/commands", verifyBody, p123IdempotencyKey(t))
	if status != http.StatusAccepted {
		t.Fatalf("submit execution-count read status=%d body=%s", status, body)
	}
	var verifyAcceptance directCommandAcceptance
	if err := json.Unmarshal(body, &verifyAcceptance); err != nil || verifyAcceptance.CommandID == "" {
		t.Fatalf("decode execution-count command acceptance: %v", err)
	}
	verifyCommand := p123WaitDirectCommand(t, ctx, client, verifyAcceptance.CommandID, "succeeded")
	if verifyCommand.Resource.FinalEventSequence == nil {
		t.Fatalf("execution-count read has no final cursor: %+v", verifyCommand.Resource)
	}
	verifyEvents := p123ReadDirectEvents(t, ctx, client, verifyAcceptance.CommandID, 0)
	if output := p123EventStdout(t, verifyEvents); output != "x\n" {
		t.Fatalf("remote command side effect occurred more than once: counter=%q", output)
	}
}

type p123CommandRead struct {
	View     string `json:"view"`
	IsStale  bool   `json:"is_stale"`
	Resource struct {
		CommandID          string `json:"command_id"`
		CommandState       string `json:"command_state"`
		FinalEventSequence *int64 `json:"final_event_sequence"`
		OutputComplete     bool   `json:"output_complete"`
	} `json:"resource"`
}

func p123NewHTTPClient(t *testing.T) (*http.Client, func()) {
	t.Helper()
	caPEM := p123ReadOwnerFixture(t, "RUNNER_P123_SERVER_CA", false)
	clientKeyPath := p123RequiredFixturePath(t, "RUNNER_P123_CLIENT_KEY")
	_ = p123ReadOwnerFixture(t, "RUNNER_P123_CLIENT_KEY", true)
	_ = p123ReadOwnerFixture(t, "RUNNER_P123_CLIENT_CERT", false)
	certificate, err := tls.LoadX509KeyPair(p123RequiredFixturePath(t, "RUNNER_P123_CLIENT_CERT"), clientKeyPath)
	if err != nil {
		t.Fatalf("load P123 direct mTLS identity: %v", err)
	}
	if len(certificate.Certificate) == 0 {
		t.Fatal("P123 direct client certificate chain is empty")
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil || len(leaf.URIs) != 1 || leaf.URIs[0].String() != "urn:remote-session-runner:controller:runner-tomasz-direct" {
		t.Fatalf("P123 direct client identity URI SAN=%v parseErr=%v", leaf.URIs, err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		t.Fatal("P123 server CA fixture contains no certificate")
	}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{
		MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, ServerName: "129.151.232.40",
		RootCAs: roots, Certificates: []tls.Certificate{certificate},
	}}
	return &http.Client{Transport: transport}, transport.CloseIdleConnections
}

func p123RequiredFixturePath(t *testing.T, name string) string {
	t.Helper()
	path := strings.TrimSpace(os.Getenv(name))
	if path == "" || !filepath.IsAbs(path) {
		t.Fatalf("%s must name an absolute owner-only fixture path", name)
	}
	return path
}

func p123ReadOwnerFixture(t *testing.T, name string, private bool) []byte {
	t.Helper()
	path := p123RequiredFixturePath(t, name)
	info, err := os.Lstat(path)
	if err != nil || filepath.Clean(path) != path || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		t.Fatalf("%s fixture must be an absolute, clean, small regular file", name)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint32(stat.Uid) != uint32(os.Geteuid()) || info.Mode().Perm()&0o400 == 0 {
		t.Fatalf("%s fixture must be owned by the current account and owner-readable", name)
	}
	if private && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("%s private fixture permissions=%#o, require owner-only", name, info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s fixture: %v", name, err)
	}
	return data
}

func p123DoJSON(t *testing.T, ctx context.Context, client *http.Client, method, path string, body []byte, idempotencyKey string) (int, []byte) {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, method, p123PublicEndpoint+path, strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("build P123 %s request: %v", method, err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("P123 %s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	if response.TLS == nil || response.TLS.Version != tls.VersionTLS13 || len(response.TLS.VerifiedChains) == 0 {
		t.Fatalf("P123 endpoint did not complete verified TLS 1.3: %+v", response.TLS)
	}
	contents, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	if err != nil || len(contents) > 1<<20 {
		t.Fatalf("read bounded P123 response body: bytes=%d err=%v", len(contents), err)
	}
	return response.StatusCode, contents
}

func p123DisconnectAfterOutput(t *testing.T, ctx context.Context, client *http.Client, commandID, marker string) ([]directCommandEvent, int64) {
	t.Helper()
	streamCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(streamCtx, http.MethodGet,
		p123PublicEndpoint+"/v1/commands/"+commandID+"/events?after=0&follow=true", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("open P123 live event stream: %v", err)
	}
	if response.StatusCode != http.StatusOK || response.TLS == nil || response.TLS.Version != tls.VersionTLS13 {
		contents, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		_ = response.Body.Close()
		t.Fatalf("P123 live event stream status=%d body=%s", response.StatusCode, contents)
	}
	var events []directCommandEvent
	var output strings.Builder
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 32<<10), 1<<20)
	var cursor int64
	for scanner.Scan() {
		var event directCommandEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			_ = response.Body.Close()
			t.Fatalf("decode P123 live event: %v", err)
		}
		events = append(events, event)
		if event.Type == "stdout" {
			chunk, err := base64.StdEncoding.DecodeString(event.DataBase64)
			if err != nil || int64(len(chunk)) != event.ByteCount {
				_ = response.Body.Close()
				t.Fatalf("decode P123 live stdout byte_count=%d err=%v", event.ByteCount, err)
			}
			output.Write(chunk)
			if strings.Contains(output.String(), marker) {
				cursor = event.Sequence
				break
			}
		}
	}
	if err := scanner.Err(); err != nil {
		_ = response.Body.Close()
		t.Fatalf("read P123 live event stream: %v", err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatalf("close P123 live event stream: %v", err)
	}
	if cursor == 0 {
		t.Fatalf("P123 live stream ended before output marker %q; events=%d output=%q", marker, len(events), output.String())
	}
	for index, event := range events {
		if event.Sequence != int64(index+1) {
			t.Fatalf("P123 pre-disconnect sequence[%d]=%d", index, event.Sequence)
		}
	}
	return events, cursor
}

func p123WaitDirectSession(t *testing.T, ctx context.Context, client *http.Client, sessionID, expected string) {
	t.Helper()
	deadline, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	for {
		status, body := p123DoJSON(t, deadline, client, http.MethodGet, "/v1/sessions/"+sessionID, nil, "")
		if status != http.StatusOK {
			t.Fatalf("P123 session read status=%d body=%s", status, body)
		}
		var snapshot directSessionReadResponse
		if err := json.Unmarshal(body, &snapshot); err != nil {
			t.Fatalf("decode P123 session snapshot: %v", err)
		}
		if snapshot.Resource.SessionState == expected {
			return
		}
		if snapshot.Resource.SessionState == "failed" || snapshot.Resource.SessionState == "lost" {
			t.Fatalf("P123 session reached terminal state %q before %q", snapshot.Resource.SessionState, expected)
		}
		select {
		case <-deadline.Done():
			t.Fatalf("P123 session %s did not reach %q: %v", sessionID, expected, deadline.Err())
		case <-time.After(150 * time.Millisecond):
		}
	}
}

func p123WaitDirectCommand(t *testing.T, ctx context.Context, client *http.Client, commandID, expected string) p123CommandRead {
	t.Helper()
	deadline, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	for {
		status, body := p123DoJSON(t, deadline, client, http.MethodGet, "/v1/commands/"+commandID, nil, "")
		if status != http.StatusOK {
			t.Fatalf("P123 command read status=%d body=%s", status, body)
		}
		var snapshot p123CommandRead
		if err := json.Unmarshal(body, &snapshot); err != nil {
			t.Fatalf("decode P123 command snapshot: %v", err)
		}
		if snapshot.Resource.CommandState == expected {
			return snapshot
		}
		if snapshot.Resource.CommandState == "failed" || snapshot.Resource.CommandState == "lost" || snapshot.Resource.CommandState == "cancelled" {
			t.Fatalf("P123 command reached %q before %q", snapshot.Resource.CommandState, expected)
		}
		select {
		case <-deadline.Done():
			t.Fatalf("P123 command %s did not reach %q: %v", commandID, expected, deadline.Err())
		case <-time.After(150 * time.Millisecond):
		}
	}
}

func p123ReadDirectEvents(t *testing.T, ctx context.Context, client *http.Client, commandID string, after int64) []directCommandEvent {
	t.Helper()
	path := "/v1/commands/" + commandID + "/events?after=" + strconv.FormatInt(after, 10)
	status, body := p123DoJSON(t, ctx, client, http.MethodGet, path, nil, "")
	if status != http.StatusOK {
		t.Fatalf("P123 event replay after=%d status=%d body=%s", after, status, body)
	}
	var events []directCommandEvent
	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	scanner.Buffer(make([]byte, 32<<10), 1<<20)
	for scanner.Scan() {
		var event directCommandEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("decode P123 replay event: %v", err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read P123 replay event lines: %v", err)
	}
	for index, event := range events {
		if event.Sequence != after+int64(index+1) {
			t.Fatalf("P123 replay event[%d] sequence=%d after=%d", index, event.Sequence, after)
		}
	}
	return events
}

func p123EventStdout(t *testing.T, events []directCommandEvent) string {
	t.Helper()
	var output strings.Builder
	for _, event := range events {
		if event.Type != "stdout" {
			continue
		}
		chunk, err := base64.StdEncoding.DecodeString(event.DataBase64)
		if err != nil || int64(len(chunk)) != event.ByteCount {
			t.Fatalf("decode P123 replay stdout byte_count=%d err=%v", event.ByteCount, err)
		}
		output.Write(chunk)
	}
	return output.String()
}

func p123IdempotencyKey(t *testing.T) string {
	t.Helper()
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	return "p123-" + hex.EncodeToString(random[:])
}

func p123CleanupDirectSession(t *testing.T, client *http.Client, sessionID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	status, body := p123DoJSON(t, ctx, client, http.MethodDelete, "/v1/sessions/"+sessionID, nil, p123IdempotencyKey(t))
	if status != http.StatusAccepted && status != http.StatusOK {
		t.Errorf("P123 cleanup close status=%d body=%s", status, body)
		return
	}
	for {
		status, body = p123DoJSON(t, ctx, client, http.MethodGet, "/v1/sessions/"+sessionID, nil, "")
		if status != http.StatusOK {
			t.Errorf("P123 cleanup session read status=%d body=%s", status, body)
			return
		}
		var snapshot directSessionReadResponse
		if err := json.Unmarshal(body, &snapshot); err == nil && snapshot.Resource.SessionState == "closed" {
			return
		}
		select {
		case <-ctx.Done():
			t.Errorf("P123 session %s did not confirm cleanup: %v", sessionID, ctx.Err())
			return
		case <-time.After(150 * time.Millisecond):
		}
	}
}

func firstEventSequence(events []directCommandEvent) int64 {
	if len(events) == 0 {
		return 0
	}
	return events[0].Sequence
}
