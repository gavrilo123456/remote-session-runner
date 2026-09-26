package runnerlocald

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	hostruntime "remote-session-runner/src/internal/runtime"
	"remote-session-runner/src/internal/store"
	"remote-session-runner/src/internal/testfixture"
)

func TestP062DaemonMacAccountAndSourceProvenance(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("P062 is a real Mac host gate")
	}
	if got := mustCommandOutput(t, "id", "-un"); got != "tomasz.walczuk\n" {
		t.Fatalf("Mac account = %q, want tomasz.walczuk", got)
	}
	uidText := strings.TrimSpace(mustCommandOutput(t, "id", "-u"))
	if uidText != "501" {
		t.Fatalf("Mac UID = %q, want 501", uidText)
	}
	t.Logf("machine=%s os=%s account=tomasz.walczuk uid=%s", mustCommandOutput(t, "hostname"), runtime.GOOS, uidText)

	harness := newP062Harness(t)
	defer harness.stop(t)

	root := harness.root.Path()
	empty := harness.acceptCreate(t, "intent-p062-empty", "session-p062-empty", domain.NewEmptySource(), "create-p062-empty")
	emptyOutput := harness.acceptCommand(t, empty.SessionID, "intent-p062-empty-command", "command-p062-empty", "printf '%s|%s|%s' \"$(id -un)\" \"$(id -u)\" \"$PWD\"", "submit-p062-empty")
	wantPrefix := "tomasz.walczuk|501|" + filepath.Join(root, "workspaces")
	if !strings.HasPrefix(emptyOutput, wantPrefix) {
		t.Fatalf("empty source identity/workspace = %q, want prefix %q", emptyOutput, wantPrefix)
	}
	t.Logf("empty source command identity/workspace=%q", emptyOutput)
	parts := strings.Split(emptyOutput, "|")
	if len(parts) != 3 {
		t.Fatalf("empty source output fields = %q", emptyOutput)
	}
	workspaceInfo, err := os.Stat(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	if got := workspaceInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("empty session workspace mode = %o, want 700", got)
	}
	emptyRecord, err := harness.authority.GetSession(context.Background(), empty.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if emptyRecord.Controller.ID() != "tomasz.walczuk" || emptyRecord.Source.Mode() != domain.SourceModeEmpty || emptyRecord.Target.Kind() != domain.TargetKindLocal {
		t.Fatalf("empty session record = %+v", emptyRecord)
	}
	harness.closeSession(t, empty.SessionID, "close-p062-empty")

	worktree := filepath.Join(root, "local-worktree")
	if err := os.MkdirAll(worktree, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(worktree, "uncommitted-marker.txt")
	if err := os.WriteFile(marker, []byte("visible-from-local-worktree"), 0o600); err != nil {
		t.Fatal(err)
	}
	localSource, err := domain.NewLocalWorktreeSource(worktree)
	if err != nil {
		t.Fatal(err)
	}
	local := harness.acceptCreate(t, "intent-p062-local", "session-p062-local", localSource, "create-p062-local")
	localOutput := harness.acceptCommand(t, local.SessionID, "intent-p062-local-command", "command-p062-local", "cat uncommitted-marker.txt", "submit-p062-local")
	if localOutput != "visible-from-local-worktree" {
		t.Fatalf("local worktree output = %q", localOutput)
	}
	t.Logf("local_worktree marker output=%q path=%s", localOutput, worktree)
	localRecord, err := harness.authority.GetSession(context.Background(), local.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if localRecord.Source.Mode() != domain.SourceModeLocalWorktree || localRecord.Source.Portable() {
		t.Fatalf("local worktree source = %+v", localRecord.Source)
	}
	harness.closeSession(t, local.SessionID, "close-p062-local")
	if got, err := os.ReadFile(marker); err != nil || string(got) != "visible-from-local-worktree" {
		t.Fatalf("local worktree marker after cleanup = %q err=%v", got, err)
	}

	repoRoot := strings.TrimSpace(mustCommandOutput(t, "git", "rev-parse", "--show-toplevel"))
	head := strings.TrimSpace(mustCommandOutput(t, "git", "-C", repoRoot, "rev-parse", "HEAD"))
	gitSource, err := domain.NewGitRevisionSource("runner", head)
	if err != nil {
		t.Fatal(err)
	}
	gitSession := harness.acceptCreate(t, "intent-p062-git", "session-p062-git", gitSource, "create-p062-git")
	gitOutput := harness.acceptCommand(t, gitSession.SessionID, "intent-p062-git-command", "command-p062-git", "git rev-parse HEAD", "submit-p062-git")
	if gitOutput != head {
		t.Fatalf("git revision output = %q, want %q", gitOutput, head)
	}
	t.Logf("git_revision requested/resolved=%s command_output=%q", head, gitOutput)
	gitRecord, err := harness.authority.GetSession(context.Background(), gitSession.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if gitRecord.Source.Mode() != domain.SourceModeGitRevision || !gitRecord.Source.Portable() || gitRecord.ResolvedRevision != head {
		t.Fatalf("git session record = %+v", gitRecord)
	}
	harness.closeSession(t, gitSession.SessionID, "close-p062-git")
	if entries, err := os.ReadDir(filepath.Join(root, "workspaces")); err != nil || len(entries) != 0 {
		t.Fatalf("owned workspace cleanup entries=%v err=%v", entries, err)
	}
}

type p062Harness struct {
	root      *testfixture.Root
	authority *store.AuthorityStore
	service   *execution.Service
	server    *PrivateServer
	serveErr  <-chan error
	client    *http.Client
}

func newP062Harness(t *testing.T) *p062Harness {
	t.Helper()
	root := testfixture.New(t)
	db, err := store.Open(context.Background(), filepath.Join(root.Path(), "state", "p062.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	authority, err := store.NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	target, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	environment, err := domain.NewEnvironment(domain.EnvironmentSpec{Name: "mac-dev", HostClass: "mac", EffectiveAccount: "tomasz.walczuk", AllowedTargets: []domain.ExecutionTarget{target}, AllowedSourceModes: []domain.SourceMode{domain.SourceModeEmpty, domain.SourceModeGitRevision, domain.SourceModeLocalWorktree}, AllowedRepositoryAliases: []string{"runner"}, AllowedControllers: []domain.ControllerIdentity{controller}, ServiceLimits: domain.DefaultServiceLimits()})
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := strings.TrimSpace(mustCommandOutput(t, "git", "rev-parse", "--show-toplevel"))
	service, _, err := NewMacExecutionService(authority, hostruntime.MacRuntimeOptions{Account: "tomasz.walczuk", WorkspaceRoot: filepath.Join(root.Path(), "workspaces"), ShellPath: "/bin/bash", RepositoryAliases: map[string]string{"runner": repoRoot}}, environment)
	if err != nil {
		t.Fatal(err)
	}
	socketPath := p060SocketPath(t)
	server, err := NewPrivateServer(PrivateServerOptions{Authority: authority, Service: service, Owner: controller, SocketPath: socketPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Listen(); err != nil {
		t.Fatal(err)
	}
	actualServeErr := make(chan error, 1)
	go func() { actualServeErr <- server.Serve() }()
	return &p062Harness{root: root, authority: authority, service: service, server: server, serveErr: actualServeErr, client: p060UnixClient(socketPath)}
}

func (h *p062Harness) acceptCreate(t *testing.T, intentID, sessionID string, source domain.Source, key string) store.SessionRecord {
	t.Helper()
	target, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	payloadObject := map[string]any{"environment": "mac-dev", "operation": "create_session", "session_id": sessionID, "source": p062SourcePayload(source)}
	payload, err := json.Marshal(payloadObject)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := domain.CanonicalizeMutationRequestJSON("create_session", payload, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("create_session", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	intent, err := h.authority.CreateLocalIntent(context.Background(), store.LocalIntentCreate{IntentID: domain.IntentID(intentID), Operation: "create_session", ResourceID: sessionID, SessionID: domain.SessionID(sessionID), Target: target, Environment: "mac-dev", Controller: controller, Source: source, RequestHash: hash, IdempotencyKey: key, PayloadJSON: canonical})
	if err != nil {
		t.Fatal(err)
	}
	response := p060DoJSON(t, h.client, http.MethodPost, "http://locald/internal/v1/accept-intent", []byte(fmt.Sprintf(`{"intent_id":%q,"request_hash":%q}`, intent.IntentID, intent.RequestHash.String())))
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("create %s status = %d body=%s", sessionID, response.StatusCode, p060ReadBody(t, response))
	}
	if _, err := h.authority.GetSession(context.Background(), intent.SessionID); err != nil {
		t.Fatal(err)
	}
	return store.SessionRecord{SessionID: intent.SessionID}
}

func p062SourcePayload(source domain.Source) map[string]string {
	payload := map[string]string{"mode": string(source.Mode())}
	if source.RepositoryAlias() != "" {
		payload["repository_alias"] = source.RepositoryAlias()
	}
	if source.RequestedRevision() != "" {
		payload["requested_revision"] = source.RequestedRevision()
	}
	if source.Path() != "" {
		payload["path"] = source.Path()
	}
	return payload
}

func (h *p062Harness) acceptCommand(t *testing.T, sessionID domain.SessionID, intentID, commandID, script, key string) string {
	t.Helper()
	payload := []byte(fmt.Sprintf(`{"environment":"mac-dev","operation":"submit_command","session_id":%q,"command_id":%q,"script":%q}`, sessionID, commandID, script))
	canonical, err := domain.CanonicalizeMutationRequestJSON("submit_command", payload, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("submit_command", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	target, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	intent, err := h.authority.CreateLocalIntent(context.Background(), store.LocalIntentCreate{IntentID: domain.IntentID(intentID), Operation: "submit_command", ResourceID: commandID, SessionID: sessionID, CommandID: domain.CommandID(commandID), Target: target, Environment: "mac-dev", Controller: controller, Source: domain.NewEmptySource(), RequestHash: hash, IdempotencyKey: key, PayloadJSON: canonical, ScriptBytes: []byte(script)})
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"intent_id":%q,"request_hash":%q,"intent_ordinal":%d}`, intent.IntentID, intent.RequestHash.String(), *intent.IntentOrdinal)
	response := p060DoJSON(t, h.client, http.MethodPost, "http://locald/internal/v1/accept-intent", []byte(body))
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("command %s status = %d body=%s", commandID, response.StatusCode, p060ReadBody(t, response))
	}
	events := p060DoJSON(t, h.client, http.MethodGet, "http://locald/internal/v1/commands/"+commandID+"/events?after=0", nil)
	if events.StatusCode != http.StatusOK {
		t.Fatalf("command %s events status = %d body=%s", commandID, events.StatusCode, p060ReadBody(t, events))
	}
	for _, line := range nonEmptyLines(p060ReadBody(t, events)) {
		var event localCommandEventResponse
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event.Type == "stdout" {
			decoded, err := base64.StdEncoding.DecodeString(event.DataBase64)
			if err != nil {
				t.Fatal(err)
			}
			return strings.TrimSuffix(string(decoded), "\n")
		}
	}
	t.Fatalf("command %s did not produce stdout", commandID)
	return ""
}

func (h *p062Harness) closeSession(t *testing.T, sessionID domain.SessionID, key string) {
	t.Helper()
	response := p060DoJSON(t, h.client, http.MethodDelete, "http://locald/internal/v1/sessions/"+string(sessionID), []byte(fmt.Sprintf(`{"idempotency_key":%q,"policy":"graceful"}`, key)))
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("close %s status = %d body=%s", sessionID, response.StatusCode, p060ReadBody(t, response))
	}
	var closed localSessionResponse
	p060DecodeJSON(t, response, &closed)
	if closed.SessionState != string(domain.SessionStateClosed) {
		t.Fatalf("close %s response = %+v", sessionID, closed)
	}
}

func (h *p062Harness) stop(t *testing.T) {
	t.Helper()
	if h == nil || h.server == nil {
		return
	}
	if err := h.server.Close(context.Background()); err != nil {
		t.Error(err)
	}
	if err := <-h.serveErr; err != nil {
		t.Error(err)
	}
}

func mustCommandOutput(t *testing.T, name string, args ...string) string {
	t.Helper()
	output, err := exec.Command(name, args...).Output()
	if err != nil {
		t.Fatalf("%s %v: %v", name, args, err)
	}
	return string(output)
}
