package localapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/dispatcher"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/mailbox"
	"remote-session-runner/src/internal/mailboxclient"
	"remote-session-runner/src/internal/runnerlocald"
	hostruntime "remote-session-runner/src/internal/runtime"
	"remote-session-runner/src/internal/sshbridge"
	"remote-session-runner/src/internal/sshclient"
	"remote-session-runner/src/internal/store"
)

const p104HostServiceRoot = "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner"

func TestP104TwoHostFileOnlyMailboxClient(t *testing.T) {
	if os.Getenv("RSR_P104_HOST_GATE") != "1" {
		t.Skip("set RSR_P104_HOST_GATE=1 to run the real Mac/Ubuntu mailbox gate")
	}
	if runtime.GOOS != "darwin" {
		t.Fatalf("P104 client must run on the selected Mac, got %s", runtime.GOOS)
	}
	current, err := user.Current()
	if err != nil || current.Username != "tomasz.walczuk" {
		t.Fatalf("P104 client account=%v err=%v, want tomasz.walczuk", current, err)
	}
	t.Logf("machine=Mac account=%s uid=%s os=%s go=%s", current.Username, current.Uid, runtime.GOOS, runtime.Version())

	h := newP095Harness(t)
	fileClient, err := mailboxclient.New(filepath.Dir(h.importer.InboxPath()))
	if err != nil {
		t.Fatal(err)
	}
	ackImporter, err := mailbox.NewAckImporter(mailbox.AckImporterOptions{
		Root: filepath.Dir(h.importer.InboxPath()), Authority: h.authority,
	})
	if err != nil {
		t.Fatal(err)
	}

	localServiceRoot := t.TempDir()
	localWorkspaceRoot := filepath.Join(localServiceRoot, "workspaces")
	if err := os.Mkdir(localWorkspaceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	localTarget, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	if err != nil {
		t.Fatal(err)
	}
	owner := p063Owner(t)
	macEnvironment, err := domain.NewEnvironment(domain.EnvironmentSpec{
		Name: "mac-dev", HostClass: "mac", EffectiveAccount: "tomasz.walczuk",
		AllowedTargets:     []domain.ExecutionTarget{localTarget},
		AllowedSourceModes: []domain.SourceMode{domain.SourceModeEmpty},
		AllowedControllers: []domain.ControllerIdentity{owner},
		ServiceLimits:      domain.DefaultServiceLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	service, _, err := runnerlocald.NewMacExecutionService(h.authority, hostruntime.MacRuntimeOptions{
		Account: "tomasz.walczuk", WorkspaceRoot: localWorkspaceRoot, ShellPath: "/bin/bash",
	}, macEnvironment)
	if err != nil {
		t.Fatal(err)
	}
	socketDir, err := os.MkdirTemp("/tmp", "rsr-p104-locald-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(socketDir, 0o700); err != nil {
		t.Fatal(err)
	}
	localdSocket := filepath.Join(socketDir, "locald.sock")
	localdServer, err := runnerlocald.NewPrivateServer(runnerlocald.PrivateServerOptions{
		Authority: h.authority, Service: service, Owner: owner, SocketPath: localdSocket,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := localdServer.Listen(); err != nil {
		t.Fatal(err)
	}
	localdServeErr := make(chan error, 1)
	go func() { localdServeErr <- localdServer.Serve() }()
	t.Cleanup(func() {
		if err := localdServer.Close(context.Background()); err != nil {
			t.Error(err)
		}
		if err := <-localdServeErr; err != nil {
			t.Error(err)
		}
		_ = os.RemoveAll(socketDir)
	})
	localAcceptor, err := dispatcher.NewLocaldClient(localdSocket)
	if err != nil {
		t.Fatal(err)
	}
	localDriver, err := dispatcher.NewLocalDriver(h.authority, localAcceptor, "router-p104-local", time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	dispatcherIdentity := filepath.Join(p104HostServiceRoot, "secrets", "dispatcher_ed25519")
	knownHosts := filepath.Join(p104HostServiceRoot, "secrets", "ssh_known_hosts")
	for _, path := range []string{dispatcherIdentity, knownHosts} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("required pinned SSH file %s is unavailable: %v", path, err)
		}
	}
	ssh, err := sshclient.New(sshclient.Config{
		User: "ubuntu", Host: "129.151.232.40", IdentityFile: dispatcherIdentity, KnownHostsFile: knownHosts,
	})
	if err != nil {
		t.Fatal(err)
	}
	remoteDriver, err := dispatcher.NewRemoteDriver(h.authority, ssh, "router-p104-remote", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	queuedController, err := domain.NewControllerIdentity(domain.ControllerTypeQueuedMac, domain.ControllerID("tomasz.walczuk"))
	if err != nil {
		t.Fatal(err)
	}

	for _, target := range []struct {
		name, kind, profile, environment string
		remote                           bool
	}{
		{name: "local", kind: "local", profile: "mac-workstation", environment: "mac-dev"},
		{name: "queued-remote", kind: "remote", profile: "linux-host", environment: "linux-dev", remote: true},
	} {
		t.Run(target.name, func(t *testing.T) {
			p104RunHostMailboxTarget(t, h, fileClient, ackImporter, localDriver, remoteDriver, ssh, queuedController, target.kind, target.profile, target.environment, target.remote)
		})
	}
}

func p104RunHostMailboxTarget(t *testing.T, h *p095Harness, client *mailboxclient.Client, ackImporter *mailbox.AckImporter, local *dispatcher.LocalDriver, remote *dispatcher.RemoteDriver, caller *sshclient.Client, queuedController domain.ControllerIdentity, kind, profile, environment string, isRemote bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	targetName := "local"
	if isRemote {
		targetName = "remote"
	}
	// The Ubuntu authority keeps idempotency records between host-gate runs.
	// Give each run fresh request and key identifiers while keeping retries
	// within this run on the same key.
	targetName += fmt.Sprintf("-%x", time.Now().UnixNano())
	createID := "req-p104-" + targetName + "-create"
	create := p104FileImport(t, ctx, h, client, createID, map[string]any{
		"request_id": createID, "idempotency_key": "key-p104-" + targetName + "-create", "operation": "create_session",
		"environment": environment, "execution_target": map[string]string{"kind": kind, "profile": profile},
		"source": map[string]string{"mode": "empty"},
	})
	if create.RequestID != createID || create.Operation != "create_session" || create.RequestState != "accepted" || create.SessionID == "" {
		t.Fatalf("create response=%+v", create)
	}
	createIntent, err := h.authority.GetLocalIntentByResource(ctx, "create_session", create.SessionID, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		p104BestEffortCloseFailedSession(t, h, client, local, remote, caller, createIntent, targetName, isRemote)
	})
	if isRemote {
		if _, _, err := remote.DispatchIntent(ctx, createIntent.IntentID); err != nil {
			t.Fatalf("queued remote create dispatch: %v", err)
		}
	} else if _, _, err := local.DispatchIntent(ctx, createIntent.IntentID); err != nil {
		t.Fatalf("local create dispatch: %v", err)
	}
	if isRemote {
		if err := p104WaitRemoteSessionReady(ctx, caller, createIntent); err != nil {
			t.Fatal(err)
		}
	} else if err := p104WaitSessionReady(ctx, h, create.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := h.processor.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}

	countScript := "printf x >> .p104-run-count; printf 'complete-output\\n'"
	firstRequestID := "req-p104-" + targetName + "-same-key-first"
	first := p104SubmitAndDispatch(t, ctx, h, client, local, remote, firstRequestID, "key-p104-"+targetName+"-same-key", create.SessionID, countScript, isRemote)
	if first.SessionID != create.SessionID || first.CommandID == "" || first.RequestID != firstRequestID {
		t.Fatalf("first submit did not correlate IDs: %+v", first)
	}
	if err := p104WaitCommand(ctx, h, remote, queuedController, p063Owner(t), first.CommandID, isRemote, true); err != nil {
		t.Fatalf("wait first command: %v", err)
	}
	retryID := "req-p104-" + targetName + "-same-key-retry"
	retry := p104FileImport(t, ctx, h, client, retryID, map[string]any{
		"request_id": retryID, "idempotency_key": "key-p104-" + targetName + "-same-key",
		"operation": "submit_command", "session_id": create.SessionID, "script": countScript, "timeout_seconds": 30,
	})
	if retry.RequestID != retryID || retry.CommandID != first.CommandID || retry.SessionID != create.SessionID || (retry.RequestState != "accepted" && retry.RequestState != "complete") {
		t.Fatalf("same-key retry response=%+v; original=%+v", retry, first)
	}
	if err := p104WaitCommand(ctx, h, remote, queuedController, p063Owner(t), first.CommandID, isRemote, true); err != nil {
		t.Fatalf("wait retried command: %v", err)
	}
	countReadID := "req-p104-" + targetName + "-count-read"
	countRead := p104SubmitAndDispatch(t, ctx, h, client, local, remote, countReadID, "key-p104-"+targetName+"-count-read", create.SessionID, "cat .p104-run-count", isRemote)
	if err := p104WaitCommand(ctx, h, remote, queuedController, p063Owner(t), countRead.CommandID, isRemote, true); err != nil {
		t.Fatalf("wait side-effect check: %v", err)
	}
	countResponse := p104GetCommand(t, ctx, h, client, countRead.CommandID, "req-p104-"+targetName+"-count-result")
	countEvents, err := client.ReadEventsThroughCursor(countResponse)
	if err != nil || p104EventOutput(countEvents, "stdout") != "x" {
		t.Fatalf("same-key retry side effect output=%q events=%+v err=%v, want exactly one x", p104EventOutput(countEvents, "stdout"), countEvents, err)
	}

	partialID := "req-p104-" + targetName + "-incomplete"
	partial := p104SubmitAndDispatch(t, ctx, h, client, local, remote, partialID, "key-p104-"+targetName+"-incomplete", create.SessionID, "printf 'prefix\\n'; sleep 4; printf 'tail\\n'", isRemote)
	if err := p104WaitForOutputPrefix(ctx, h, remote, queuedController, partial.CommandID, isRemote); err != nil {
		t.Fatalf("wait for active output prefix: %v", err)
	}
	incompleteID := "req-p104-" + targetName + "-incomplete-read"
	incomplete := p104GetCommand(t, ctx, h, client, partial.CommandID, incompleteID)
	if incomplete.RequestID != incompleteID || incomplete.SessionID != create.SessionID || incomplete.CommandID != partial.CommandID || incomplete.OutputComplete == nil || *incomplete.OutputComplete || incomplete.AvailableEventSequence == nil || *incomplete.AvailableEventSequence < 3 {
		t.Fatalf("active frozen response did not distinguish incomplete output: %+v", incomplete)
	}
	incompleteEvents, err := client.ReadEventsThroughCursor(incomplete)
	if err != nil || !strings.Contains(p104EventOutput(incompleteEvents, "stdout"), "prefix\n") {
		t.Fatalf("incomplete output prefix=%q events=%+v err=%v", p104EventOutput(incompleteEvents, "stdout"), incompleteEvents, err)
	}
	p104AcknowledgeExact(t, ctx, h, client, ackImporter, incomplete)

	if err := p104WaitCommand(ctx, h, remote, queuedController, p063Owner(t), partial.CommandID, isRemote, true); err != nil {
		t.Fatalf("wait partial command terminal: %v", err)
	}
	completeID := "req-p104-" + targetName + "-complete-read"
	complete := p104GetCommand(t, ctx, h, client, partial.CommandID, completeID)
	if complete.RequestID != completeID || complete.SessionID != create.SessionID || complete.CommandID != partial.CommandID || complete.OutputComplete == nil || !*complete.OutputComplete {
		t.Fatalf("terminal response did not distinguish complete output: %+v", complete)
	}
	completeEvents, err := client.ReadEventsThroughCursor(complete)
	if err != nil || p104EventOutput(completeEvents, "stdout") != "prefix\ntail\n" {
		t.Fatalf("complete output=%q events=%+v err=%v", p104EventOutput(completeEvents, "stdout"), completeEvents, err)
	}
	p104AcknowledgeExact(t, ctx, h, client, ackImporter, complete)

	closeID := "req-p104-" + targetName + "-close"
	closed := p104FileImport(t, ctx, h, client, closeID, map[string]any{
		"request_id": closeID, "idempotency_key": "key-p104-" + targetName + "-close",
		"operation": "close_session", "session_id": create.SessionID, "close_policy": map[string]string{"policy": "graceful"},
	})
	if closed.RequestState != "accepted" || closed.SessionID != create.SessionID {
		t.Fatalf("close acceptance=%+v", closed)
	}
	closeIntent, err := h.authority.GetLocalIntentByIdempotency(ctx, "close_session", "key-p104-"+targetName+"-close", p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	if isRemote {
		if _, _, err := remote.DispatchIntent(ctx, closeIntent.IntentID); err != nil {
			t.Fatalf("queued remote close dispatch: %v", err)
		}
	} else if _, _, err := local.DispatchIntent(ctx, closeIntent.IntentID); err != nil {
		t.Fatalf("local close dispatch: %v", err)
	}
	if err := h.processor.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
}

func p104BestEffortCloseFailedSession(t *testing.T, h *p095Harness, client *mailboxclient.Client, local *dispatcher.LocalDriver, remote *dispatcher.RemoteDriver, caller *sshclient.Client, createIntent store.LocalIntentRecord, targetName string, isRemote bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	session, err := h.server.GetSession(ctx, string(createIntent.ResourceID))
	if err != nil {
		t.Logf("P104 cleanup could not inspect session %s: %v", createIntent.ResourceID, err)
		return
	}
	if !domain.SessionState(session.SessionState).IsTerminal() && session.SessionState != string(domain.SessionStateReady) {
		if isRemote {
			_, _, err = remote.DispatchIntent(ctx, createIntent.IntentID)
		} else {
			_, _, err = local.DispatchIntent(ctx, createIntent.IntentID)
		}
		if err != nil {
			t.Logf("P104 cleanup create dispatch for session %s: %v", createIntent.ResourceID, err)
		}
		readyCtx, readyCancel := context.WithTimeout(ctx, 20*time.Second)
		if isRemote {
			err = p104WaitRemoteSessionReady(readyCtx, caller, createIntent)
		} else {
			err = p104WaitSessionReady(readyCtx, h, string(createIntent.ResourceID))
		}
		readyCancel()
		if err != nil {
			t.Logf("P104 cleanup could not ready session %s: %v", createIntent.ResourceID, err)
			return
		}
	}
	if domain.SessionState(session.SessionState).IsTerminal() {
		return
	}
	requestID := "req-p104-" + targetName + "-failure-cleanup"
	request := map[string]any{
		"request_id": requestID, "idempotency_key": "key-p104-" + targetName + "-failure-cleanup",
		"operation": "close_session", "session_id": string(createIntent.ResourceID),
		"close_policy": map[string]string{"policy": "graceful"},
	}
	raw, err := json.Marshal(request)
	if err == nil {
		err = client.WriteRequest(requestID, raw)
	}
	if err != nil {
		t.Logf("P104 cleanup could not publish close request for session %s: %v", createIntent.ResourceID, err)
		return
	}
	imports, err := h.processor.Import(ctx)
	if err != nil || len(imports) != 1 || !imports[0].Durable || !imports[0].PairRemoved {
		t.Logf("P104 cleanup could not import close request for session %s: results=%+v err=%v", createIntent.ResourceID, imports, err)
		return
	}
	response, err := client.WaitResponse(ctx, requestID)
	if err != nil || response.RequestState != "accepted" {
		t.Logf("P104 cleanup close response for session %s: response=%+v err=%v", createIntent.ResourceID, response, err)
		return
	}
	owner, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, domain.ControllerID("tomasz.walczuk"))
	if err != nil {
		t.Logf("P104 cleanup could not construct controller: %v", err)
		return
	}
	closeIntent, err := h.authority.GetLocalIntentByIdempotency(ctx, "close_session", "key-p104-"+targetName+"-failure-cleanup", owner)
	if err != nil {
		t.Logf("P104 cleanup could not load close intent for session %s: %v", createIntent.ResourceID, err)
		return
	}
	if isRemote {
		_, _, err = remote.DispatchIntent(ctx, closeIntent.IntentID)
	} else {
		_, _, err = local.DispatchIntent(ctx, closeIntent.IntentID)
	}
	if err != nil {
		t.Logf("P104 cleanup could not dispatch close for session %s: %v", createIntent.ResourceID, err)
		return
	}
	if err := h.processor.Reconcile(ctx); err != nil {
		t.Logf("P104 cleanup reconcile for session %s: %v", createIntent.ResourceID, err)
	}
}

func p104FileImport(t *testing.T, ctx context.Context, h *p095Harness, client *mailboxclient.Client, requestID string, request map[string]any) mailboxclient.Response {
	t.Helper()
	request["request_id"] = requestID
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.WriteRequest(requestID, raw); err != nil {
		t.Fatalf("file client write request %s: %v", requestID, err)
	}
	results, err := h.processor.Import(ctx)
	if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("server import %s results=%+v err=%v", requestID, results, err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	response, err := client.WaitResponse(waitCtx, requestID)
	if err != nil {
		t.Fatalf("file client poll outbox %s: %v", requestID, err)
	}
	return response
}

func p104SubmitAndDispatch(t *testing.T, ctx context.Context, h *p095Harness, client *mailboxclient.Client, local *dispatcher.LocalDriver, remote *dispatcher.RemoteDriver, requestID, key, sessionID, script string, isRemote bool) mailboxclient.Response {
	t.Helper()
	response := p104FileImport(t, ctx, h, client, requestID, map[string]any{
		"request_id": requestID, "idempotency_key": key, "operation": "submit_command",
		"session_id": sessionID, "script": script, "timeout_seconds": 30,
	})
	if response.RequestState != "accepted" || response.RequestID != requestID || response.SessionID != sessionID || response.CommandID == "" {
		t.Fatalf("submit response=%+v", response)
	}
	intent, err := h.authority.GetLocalIntentByResource(ctx, "submit_command", response.CommandID, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	if isRemote {
		if _, _, err := remote.DispatchIntent(ctx, intent.IntentID); err != nil {
			t.Fatalf("queued remote submit dispatch %s: %v", requestID, err)
		}
	} else if _, _, err := local.DispatchIntent(ctx, intent.IntentID); err != nil {
		command, commandErr := h.authority.GetCommand(ctx, intent.CommandID)
		session, sessionErr := h.authority.GetSession(ctx, intent.SessionID)
		diagnostic := ""
		var rejection *dispatcher.LocaldRejectionError
		if errors.As(err, &rejection) {
			diagnostic = rejection.Message
		}
		t.Fatalf("local submit dispatch %s: %v (diagnostic=%q; target command state=%q lookup_err=%v; session state=%q lookup_err=%v)", requestID, err, diagnostic, command.State, commandErr, session.State, sessionErr)
	}
	if err := h.processor.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	return response
}

func p104GetCommand(t *testing.T, ctx context.Context, h *p095Harness, client *mailboxclient.Client, commandID, requestID string) mailboxclient.Response {
	t.Helper()
	response := p104FileImport(t, ctx, h, client, requestID, map[string]any{
		"request_id": requestID, "operation": "get_command", "command_id": commandID,
	})
	if response.RequestState != "complete" || response.CommandID != commandID {
		t.Fatalf("get_command %s response=%+v", requestID, response)
	}
	return response
}

func p104WaitSessionReady(ctx context.Context, h *p095Harness, sessionID string) error {
	deadline := time.NewTicker(100 * time.Millisecond)
	defer deadline.Stop()
	for {
		if err := h.processor.Reconcile(ctx); err != nil {
			return err
		}
		snapshot, err := h.server.GetSession(ctx, sessionID)
		if err == nil && snapshot.SessionState == string(domain.SessionStateReady) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("session %s did not become ready: %w", sessionID, ctx.Err())
		case <-deadline.C:
		}
	}
}

func p104WaitRemoteSessionReady(ctx context.Context, caller *sshclient.Client, create store.LocalIntentRecord) error {
	requestID := "req-p104-remote-readiness"
	payload, err := json.Marshal(map[string]string{"session_id": string(create.SessionID)})
	if err != nil {
		return err
	}
	frame := sshbridge.RequestFrame{
		ProtocolVersion: sshbridge.ProtocolVersion,
		RequestID:       requestID,
		Operation:       sshbridge.OperationGetSession,
		Payload:         payload,
	}
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		reply, callErr := caller.Call(ctx, frame)
		if callErr != nil {
			lastErr = callErr
		} else if reply.ProtocolVersion != sshbridge.ProtocolVersion || reply.RequestID != requestID {
			return fmt.Errorf("remote readiness reply identity mismatch: %+v", reply)
		} else if reply.ResponseType == "error" {
			var remoteErr sshbridge.ErrorPayload
			if err := json.Unmarshal(reply.Payload, &remoteErr); err != nil {
				return fmt.Errorf("decode remote readiness error: %w", err)
			}
			if remoteErr.Code != "resource_not_found" && !remoteErr.Retryable {
				return fmt.Errorf("remote readiness failed: %s: %s", remoteErr.Code, remoteErr.Message)
			}
			lastErr = fmt.Errorf("remote session not ready: %s: %s", remoteErr.Code, remoteErr.Message)
		} else if reply.ResponseType != "result" {
			return fmt.Errorf("unexpected remote readiness response type %q", reply.ResponseType)
		} else {
			var result struct {
				SessionID       string `json:"session_id"`
				SessionState    string `json:"session_state"`
				Environment     string `json:"environment"`
				ExecutionTarget struct {
					Kind    string `json:"kind"`
					Profile string `json:"profile"`
				} `json:"execution_target"`
			}
			if err := json.Unmarshal(reply.Payload, &result); err != nil {
				return fmt.Errorf("decode remote readiness result: %w", err)
			}
			if result.SessionID != string(create.SessionID) || result.Environment != create.Environment ||
				result.ExecutionTarget.Kind != string(domain.TargetKindRemote) || result.ExecutionTarget.Profile != "linux-host" {
				return fmt.Errorf("remote readiness resource identity mismatch: %+v", result)
			}
			state := domain.SessionState(result.SessionState)
			if !state.Valid() {
				return fmt.Errorf("remote readiness returned invalid session state %q", result.SessionState)
			}
			if state == domain.SessionStateReady {
				return nil
			}
			if state.IsTerminal() {
				return fmt.Errorf("remote session %s reached terminal state %s before ready", create.SessionID, state)
			}
			lastErr = fmt.Errorf("remote session state is %s", state)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("remote session %s did not become ready (last result %v): %w", create.SessionID, lastErr, ctx.Err())
		case <-ticker.C:
		}
	}
}

func p104WaitForOutputPrefix(ctx context.Context, h *p095Harness, remote *dispatcher.RemoteDriver, controller domain.ControllerIdentity, commandID string, isRemote bool) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if isRemote {
			if _, err := remote.MirrorCommandEvents(ctx, domain.CommandID(commandID), controller); err != nil {
				return err
			}
		}
		if err := h.processor.Reconcile(ctx); err != nil {
			return err
		}
		snapshot, err := h.server.GetCommandSnapshot(ctx, commandID)
		if err == nil && !snapshot.State.IsTerminal() && snapshot.AvailableEventSequence >= 3 && strings.Contains(snapshot.StdoutPreview, "prefix\n") {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("command %s produced no active output prefix: %w", commandID, ctx.Err())
		case <-ticker.C:
		}
	}
}

func p104WaitCommand(ctx context.Context, h *p095Harness, remote *dispatcher.RemoteDriver, controller, mailboxOwner domain.ControllerIdentity, commandID string, isRemote, terminal bool) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		var operationErr error
		if isRemote {
			if _, err := remote.MirrorCommandEvents(ctx, domain.CommandID(commandID), controller); err != nil {
				operationErr = err
			}
			if _, err := remote.RefreshCommandProjection(ctx, domain.CommandID(commandID), mailboxOwner); err != nil {
				operationErr = err
			}
		}
		if err := h.processor.Reconcile(ctx); err != nil {
			operationErr = err
		}
		snapshot, err := h.server.GetCommandSnapshot(ctx, commandID)
		if err == nil && operationErr == nil && (!terminal || snapshot.State.IsTerminal()) {
			return nil
		}
		if err != nil {
			operationErr = err
		}
		lastErr = operationErr
		select {
		case <-ctx.Done():
			return fmt.Errorf("command %s did not reach required state (last error %v): %w", commandID, lastErr, ctx.Err())
		case <-ticker.C:
		}
	}
}

func p104AcknowledgeExact(t *testing.T, ctx context.Context, h *p095Harness, client *mailboxclient.Client, importer *mailbox.AckImporter, response mailboxclient.Response) {
	t.Helper()
	if err := client.WriteAcknowledgment(response.RequestID, response); err != nil {
		t.Fatal(err)
	}
	ackPath := filepath.Join(importer.AcksPath(), response.RequestID+mailbox.RequestSuffix)
	raw, err := os.ReadFile(ackPath)
	if err != nil {
		t.Fatal(err)
	}
	var ack mailboxclient.Acknowledgment
	if err := json.Unmarshal(raw, &ack); err != nil {
		t.Fatal(err)
	}
	if ack.RequestID != response.RequestID || ack.ResponseRevision != response.ResponseRevision || !p104SameCursor(ack.AvailableEventSequence, response.AvailableEventSequence) {
		t.Fatalf("file client ACK=%+v does not exactly match response=%+v", ack, response)
	}
	results, err := importer.Import(ctx)
	if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("server ACK import results=%+v err=%v", results, err)
	}
	record, err := h.authority.GetMailboxExchange(ctx, response.RequestID)
	if err != nil || record.AcknowledgedAt == nil || record.ResponseRevision != response.ResponseRevision || !p104SameCursor(record.AvailableEventSequence, response.AvailableEventSequence) {
		t.Fatalf("durable ACK receipt=%+v err=%v", record, err)
	}
}

func p104EventOutput(events []mailboxclient.Event, eventType string) string {
	var output strings.Builder
	for _, event := range events {
		if event.Type != eventType {
			continue
		}
		if event.Encoding == "utf8" {
			output.WriteString(event.Text)
		} else if event.Encoding == "base64" {
			decoded, err := base64.StdEncoding.DecodeString(event.DataBase64)
			if err != nil {
				return "<invalid-base64>"
			}
			output.Write(decoded)
		}
	}
	return output.String()
}

func p104SameCursor(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
