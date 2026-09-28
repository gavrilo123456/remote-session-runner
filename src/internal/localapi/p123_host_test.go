//go:build p123twohost

package localapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"remote-session-runner/src/internal/dispatcher"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/mailbox"
	"remote-session-runner/src/internal/sshbridge"
	"remote-session-runner/src/internal/sshclient"
	"remote-session-runner/src/internal/store"
)

func TestP123QueuedProjectionReconcilesAfterSSHOutage(t *testing.T) {
	if os.Getenv("RSR_P123_HOST_GATE") != "1" {
		t.Skip("set RSR_P123_HOST_GATE=1 to run the real Mac/Ubuntu P-NET-03 gate")
	}
	if runtime.GOOS != "darwin" {
		t.Fatalf("P123 queued client must run on the selected Mac, got %s", runtime.GOOS)
	}
	current, err := user.Current()
	if err != nil || current.Username != "tomasz.walczuk" {
		t.Fatalf("P123 queued client account=%v err=%v, want tomasz.walczuk", current, err)
	}
	t.Logf("machine=Mac account=%s uid=%s os=%s go=%s remote=ubuntu@129.151.232.40", current.Username, current.Uid, runtime.GOOS, runtime.Version())

	identity := strings.TrimSpace(os.Getenv("RUNNER_P123_SSH_IDENTITY"))
	knownHosts := strings.TrimSpace(os.Getenv("RUNNER_P123_SSH_KNOWN_HOSTS"))
	for name, path := range map[string]string{"RUNNER_P123_SSH_IDENTITY": identity, "RUNNER_P123_SSH_KNOWN_HOSTS": knownHosts} {
		if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
			t.Fatalf("%s must name an absolute pinned SSH fixture path", name)
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
			t.Fatalf("%s fixture must be a small regular file", name)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || uint32(stat.Uid) != uint32(os.Geteuid()) || info.Mode().Perm()&0o400 == 0 || info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("%s fixture must be owner-readable and restricted to the current account", name)
		}
	}

	ssh, err := sshclient.New(sshclient.Config{
		User: "ubuntu", Host: "129.151.232.40", IdentityFile: identity, KnownHostsFile: knownHosts,
	})
	if err != nil {
		t.Fatal(err)
	}
	caller := &p123SwitchableSSHCaller{client: ssh}
	h := newP095Harness(t)
	remote, err := dispatcher.NewRemoteDriver(h.authority, caller, "router-p123-real", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	queuedController, err := domain.NewControllerIdentity(domain.ControllerTypeQueuedMac, domain.ControllerID("tomasz.walczuk"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	createKey := p123LocalKey(t)
	createRequestID := "req-p123-" + createKey[len("p123-"):] + "-create"
	createBody, err := json.Marshal(map[string]any{
		"request_id":       createRequestID,
		"idempotency_key":  createKey,
		"operation":        "create_session",
		"environment":      "linux-dev",
		"execution_target": map[string]string{"kind": "remote", "profile": "linux-host"},
		"source":           map[string]string{"mode": "empty"},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := h.server.CreateSessionIntent(ctx, mailbox.Request{
		RequestID: createRequestID, IdempotencyKey: createKey, Operation: "create_session", RawJSON: createBody,
	})
	if err != nil || created.SessionID == "" {
		t.Fatalf("record queued session intent: acceptance=%+v err=%v", created, err)
	}
	sessionID, err := domain.NewSessionID(created.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	createIntent, err := h.authority.GetLocalIntentByResource(ctx, "create_session", created.SessionID, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	createIntent, _, err = remote.DispatchIntent(ctx, createIntent.IntentID)
	if err != nil || createIntent.DeliveryState != store.LocalIntentAccepted {
		t.Fatalf("remote create delivery=%s err=%v", createIntent.DeliveryState, err)
	}
	sessionClosed := false
	t.Cleanup(func() {
		caller.setUnavailable(false)
		if !sessionClosed {
			_ = p123CleanupQueuedSession(t, ctx, h, remote, ssh, sessionID)
		}
	})
	if err := p104WaitRemoteSessionReady(ctx, ssh, createIntent); err != nil {
		t.Fatalf("remote session readiness: %v", err)
	}

	commandKey := p123LocalKey(t)
	commandRequestID := "req-p123-" + commandKey[len("p123-"):] + "-submit"
	script := "printf x >> .p123-run-count; printf 'p123-before-ssh-outage\\n'; sleep 6; printf 'p123-after-ssh-outage\\n'"
	command, commandIntent := p123AcceptQueuedCommand(t, ctx, h, remote, commandRequestID, commandKey, sessionID, script)
	if command.CommandID == "" || command.SessionID != string(sessionID) {
		t.Fatalf("queued command acceptance=%+v", command)
	}
	commandID, err := domain.NewCommandID(command.CommandID)
	if err != nil {
		t.Fatal(err)
	}

	initialCursor := p123MirrorUntilOutput(t, ctx, h.authority, remote, commandID, queuedController, "p123-before-ssh-outage\n")
	initialProjection, err := remote.RefreshCommandProjection(ctx, commandID, p063Owner(t))
	if err != nil || initialProjection.IsStale {
		t.Fatalf("pre-outage projection=%+v err=%v", initialProjection, err)
	}
	if initialCursor <= 0 {
		t.Fatalf("pre-outage durable event cursor=%d", initialCursor)
	}

	// Only the P123 test's client wrapper is disabled. The healthy path before
	// and after this interval delegates to the real pinned OpenSSH client.
	caller.setUnavailable(true)
	if _, err := remote.MirrorCommandEvents(ctx, commandID, queuedController); err == nil {
		t.Fatal("event mirror unexpectedly succeeded while the Mac SSH client was unavailable")
	}
	staleProjection, mirroredEvents, err := h.authority.GetRemoteCommandWithEvents(ctx, commandID)
	if err != nil || !staleProjection.IsStale {
		t.Fatalf("projection during SSH outage=%+v err=%v", staleProjection, err)
	}
	outageCursor, err := h.authority.GetRemoteEventCursor(ctx, commandID)
	if err != nil || outageCursor != initialCursor || int64(len(mirroredEvents)) != initialCursor {
		t.Fatalf("durable cursor changed during SSH outage: cursor=%d events=%d initial=%d err=%v", outageCursor, len(mirroredEvents), initialCursor, err)
	}
	t.Logf("Mac bridge unavailable with stale projection at durable cursor %d", outageCursor)
	time.Sleep(7 * time.Second)
	caller.setUnavailable(false)

	recovered, err := remote.MirrorCommandEvents(ctx, commandID, queuedController)
	if err != nil || recovered.LastSequence <= initialCursor || recovered.Duplicates != 0 {
		t.Fatalf("recovered mirror=%+v err=%v; expected only the missing suffix after cursor %d", recovered, err, initialCursor)
	}
	projection, err := remote.RefreshCommandProjection(ctx, commandID, p063Owner(t))
	if err != nil || projection.IsStale || projection.State != domain.CommandStateSucceeded || !projection.OutputComplete || projection.FinalEventSequence == nil || *projection.FinalEventSequence != recovered.LastSequence {
		t.Fatalf("reconciled projection=%+v cursor=%d err=%v", projection, recovered.LastSequence, err)
	}
	completedIntent, err := h.authority.GetLocalIntent(ctx, commandIntent.IntentID)
	if err != nil || completedIntent.DeliveryState != store.LocalIntentReconciled {
		t.Fatalf("completed local delivery=%s err=%v, want reconciled", completedIntent.DeliveryState, err)
	}
	_, allEvents, err := h.authority.GetRemoteCommandWithEvents(ctx, commandID)
	if err != nil || int64(len(allEvents)) != *projection.FinalEventSequence {
		t.Fatalf("reconciled event history count=%d final=%v err=%v", len(allEvents), projection.FinalEventSequence, err)
	}
	var output strings.Builder
	for index, event := range allEvents {
		if event.Sequence != int64(index+1) {
			t.Fatalf("reconciled event[%d] sequence=%d", index, event.Sequence)
		}
		if event.Type == "stdout" {
			output.Write(event.Payload)
		}
	}
	if text := output.String(); !strings.Contains(text, "p123-before-ssh-outage\n") || !strings.Contains(text, "p123-after-ssh-outage\n") {
		t.Fatalf("reconciled remote output is incomplete: %q", text)
	}

	// Read the command-owned counter through a second real queued command. If
	// recovery had resubmitted the accepted mutation, it would contain "xx".
	verifyKey := p123LocalKey(t)
	verifyID := "req-p123-" + verifyKey[len("p123-"):] + "-verify"
	verify, verifyIntent := p123AcceptQueuedCommand(t, ctx, h, remote, verifyID, verifyKey, sessionID, "cat .p123-run-count; printf '\\n'")
	verifyCommandID, err := domain.NewCommandID(verify.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	verifyProjection := p123WaitQueuedCommand(t, ctx, h.authority, remote, verifyCommandID, queuedController, p063Owner(t))
	if verifyProjection.State != domain.CommandStateSucceeded {
		t.Fatalf("execution-count command projection=%+v", verifyProjection)
	}
	_, verifyEvents, err := h.authority.GetRemoteCommandWithEvents(ctx, verifyCommandID)
	if err != nil {
		t.Fatal(err)
	}
	var counter strings.Builder
	for _, event := range verifyEvents {
		if event.Type == "stdout" {
			counter.Write(event.Payload)
		}
	}
	if counter.String() != "x\n" {
		t.Fatalf("queued command side effect occurred more than once: counter=%q", counter.String())
	}
	verifiedIntent, err := h.authority.GetLocalIntent(ctx, verifyIntent.IntentID)
	if err != nil || verifiedIntent.DeliveryState != store.LocalIntentReconciled {
		t.Fatalf("verification command delivery=%s err=%v", verifiedIntent.DeliveryState, err)
	}

	if p123CleanupQueuedSession(t, ctx, h, remote, ssh, sessionID) {
		sessionClosed = true
	}
}

type p123SwitchableSSHCaller struct {
	mu          sync.RWMutex
	client      *sshclient.Client
	unavailable bool
}

func (c *p123SwitchableSSHCaller) setUnavailable(value bool) {
	c.mu.Lock()
	c.unavailable = value
	c.mu.Unlock()
}

func (c *p123SwitchableSSHCaller) isUnavailable() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.unavailable
}

func (c *p123SwitchableSSHCaller) Call(ctx context.Context, request sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
	if c.isUnavailable() {
		return sshbridge.ReplyFrame{}, &sshclient.TransportError{Phase: sshclient.PhaseBeforeSend, Err: errors.New("P123 injected Mac SSH outage")}
	}
	return c.client.Call(ctx, request)
}

func (c *p123SwitchableSSHCaller) Stream(ctx context.Context, request sshbridge.RequestFrame, receive func(sshbridge.ReplyFrame) error) error {
	if c.isUnavailable() {
		return &sshclient.TransportError{Phase: sshclient.PhaseBeforeSend, Err: errors.New("P123 injected Mac SSH outage")}
	}
	return c.client.Stream(ctx, request, receive)
}

func p123AcceptQueuedCommand(t *testing.T, ctx context.Context, h *p095Harness, remote *dispatcher.RemoteDriver, requestID, key string, sessionID domain.SessionID, script string) (mailbox.CommandIntent, store.LocalIntentRecord) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"request_id": requestID, "idempotency_key": key, "operation": "submit_command",
		"session_id": string(sessionID), "script": script, "timeout_seconds": 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := h.server.SubmitCommandIntent(ctx, mailbox.Request{
		RequestID: requestID, IdempotencyKey: key, Operation: "submit_command", SessionID: string(sessionID), RawJSON: body,
	})
	if err != nil {
		t.Fatalf("record queued command intent: %v", err)
	}
	intent, err := h.authority.GetLocalIntentByResource(ctx, "submit_command", string(accepted.CommandID), p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	intent, _, err = remote.DispatchIntent(ctx, intent.IntentID)
	if err != nil || intent.DeliveryState != store.LocalIntentAccepted {
		t.Fatalf("remote command delivery=%s err=%v", intent.DeliveryState, err)
	}
	stored, err := h.authority.GetLocalIntent(ctx, intent.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	return accepted, stored
}

func p123MirrorUntilOutput(t *testing.T, ctx context.Context, authority *store.AuthorityStore, remote *dispatcher.RemoteDriver, commandID domain.CommandID, controller domain.ControllerIdentity, marker string) int64 {
	t.Helper()
	deadline, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		if err := deadline.Err(); err != nil {
			cursor, cursorErr := authority.GetRemoteEventCursor(context.Background(), commandID)
			t.Fatalf("remote command %s did not publish %q before timeout: %v (mirrored cursor=%d, cursor error=%v)", commandID, marker, err, cursor, cursorErr)
		}
		if _, err := remote.MirrorCommandEvents(deadline, commandID, controller); err != nil {
			t.Fatalf("mirror events before outage: %v", err)
		}
		// Event mirroring can precede creation of the read-only command
		// projection. Inspect the durable event mirror directly while waiting
		// for output; GetRemoteCommandWithEvents intentionally requires both.
		events, err := authority.ListRemoteEvents(deadline, commandID, 0)
		if err != nil {
			t.Fatalf("read mirrored events before outage: %v", err)
		}
		var output strings.Builder
		for _, event := range events {
			if event.Type == "stdout" {
				output.Write(event.Payload)
			}
		}
		if strings.Contains(output.String(), marker) {
			return int64(len(events))
		}
		select {
		case <-deadline.Done():
			// The next loop reports the current durable cursor without starting
			// another SSH request on an expired context.
		case <-time.After(120 * time.Millisecond):
		}
	}
}

func p123WaitQueuedCommand(t *testing.T, ctx context.Context, authority *store.AuthorityStore, remote *dispatcher.RemoteDriver, commandID domain.CommandID, targetController, mailboxOwner domain.ControllerIdentity) store.RemoteCommandProjection {
	t.Helper()
	deadline, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	for {
		if _, err := remote.MirrorCommandEvents(deadline, commandID, targetController); err != nil {
			t.Fatalf("mirror queued command %s: %v", commandID, err)
		}
		projection, err := remote.RefreshCommandProjection(deadline, commandID, mailboxOwner)
		if err == nil && projection.State.IsTerminal() {
			return projection
		}
		select {
		case <-deadline.Done():
			t.Fatalf("queued command %s did not reach a terminal result: %v", commandID, deadline.Err())
		case <-time.After(120 * time.Millisecond):
		}
	}
}

func p123CleanupQueuedSession(t *testing.T, ctx context.Context, h *p095Harness, remote *dispatcher.RemoteDriver, ssh *sshclient.Client, sessionID domain.SessionID) bool {
	t.Helper()
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	key := p123LocalKey(t)
	requestID := "req-p123-" + key[len("p123-"):] + "-close"
	_, err := h.server.CloseSessionIntent(cleanupCtx, mailbox.Request{
		RequestID: requestID, IdempotencyKey: key, Operation: "close_session", SessionID: string(sessionID), ClosePolicy: "graceful",
	})
	if err != nil {
		t.Errorf("accept queued session cleanup: %v", err)
		return false
	}
	intent, err := h.authority.GetLocalIntentByResource(cleanupCtx, "close_session", string(sessionID), p063Owner(t))
	if err != nil {
		t.Errorf("read queued session cleanup intent: %v", err)
		return false
	}
	if _, _, err := remote.DispatchIntent(cleanupCtx, intent.IntentID); err != nil {
		t.Errorf("dispatch queued session cleanup: %v", err)
		return false
	}
	requestID = "p123-session-state-" + string(sessionID)
	frame := sshbridge.RequestFrame{
		ProtocolVersion: sshbridge.ProtocolVersion, RequestID: requestID,
		Operation: sshbridge.OperationGetSession,
		Payload:   []byte(fmt.Sprintf(`{"session_id":%q}`, string(sessionID))),
	}
	for {
		reply, err := ssh.Call(cleanupCtx, frame)
		if err == nil && reply.ResponseType == "result" && reply.RequestID == requestID {
			var result struct {
				SessionState string `json:"session_state"`
			}
			if json.Unmarshal(reply.Payload, &result) == nil && result.SessionState == string(domain.SessionStateClosed) {
				return true
			}
		}
		select {
		case <-cleanupCtx.Done():
			t.Errorf("queued session %s did not confirm cleanup: %v", sessionID, cleanupCtx.Err())
			return false
		case <-time.After(150 * time.Millisecond):
		}
	}
}

func p123LocalKey(t *testing.T) string {
	t.Helper()
	var random [16]byte
	if _, err := io.ReadFull(rand.Reader, random[:]); err != nil {
		t.Fatal(err)
	}
	return "p123-" + hex.EncodeToString(random[:])
}
