//go:build p140twohost

package localapi

import (
	"context"
	"crypto/rand"
	"database/sql"
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

const p140MacHostGate = "RSR_P140_MAC_HOST_GATE"

func TestP140MacLinuxOrdinalGapSurvivesRestoreAndReconcilesBeforeNextDispatch(t *testing.T) {
	if os.Getenv(p140MacHostGate) != "1" {
		t.Skip("set RSR_P140_MAC_HOST_GATE=1 to run the actual Mac/Ubuntu backup, restore, and reconciliation gate")
	}
	if runtime.GOOS != "darwin" {
		t.Fatalf("P140 two-host gate must run on macOS, got %s", runtime.GOOS)
	}
	current, err := user.Current()
	if err != nil || current.Username != "tomasz.walczuk" || current.Uid != "501" {
		t.Fatalf("P140 Mac account=%v err=%v, want tomasz.walczuk uid 501", current, err)
	}
	hostname, err := os.Hostname()
	if err != nil || hostname != "AMAK2KJ6X9JJJ" {
		t.Fatalf("P140 Mac host=%q err=%v, want AMAK2KJ6X9JJJ", hostname, err)
	}
	identity := strings.TrimSpace(os.Getenv("RUNNER_P140_SSH_IDENTITY"))
	knownHosts := strings.TrimSpace(os.Getenv("RUNNER_P140_SSH_KNOWN_HOSTS"))
	for name, path := range map[string]string{"RUNNER_P140_SSH_IDENTITY": identity, "RUNNER_P140_SSH_KNOWN_HOSTS": knownHosts} {
		p140CheckOwnerOnlyFile(t, name, path)
	}
	ssh, err := sshclient.New(sshclient.Config{User: "ubuntu", Host: "129.151.232.40", IdentityFile: identity, KnownHostsFile: knownHosts})
	if err != nil {
		t.Fatal(err)
	}
	caller := &p140AfterSendCaller{client: ssh}

	root, err := os.MkdirTemp("/private/tmp", "p140m-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("remove isolated P140 Mac fixture %s: %v", root, err)
		}
	})
	databasePath := filepath.Join(root, "authority.db")
	backupPath := filepath.Join(root, "backups", "authority.snapshot.db")
	if err := os.Mkdir(filepath.Dir(backupPath), 0o700); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(root, "local-api.sock")
	database, authority, server := p140OpenMacAuthority(t, databasePath, socketPath)
	t.Cleanup(func() {
		if server != nil {
			_ = server.Close(context.Background())
		}
		if database != nil {
			_ = database.Close()
		}
	})
	remote, err := dispatcher.NewRemoteDriver(authority, caller, "router-p140-host", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	mailboxOwner := p063Owner(t)
	queuedController, err := domain.NewControllerIdentity(domain.ControllerTypeQueuedMac, domain.ControllerID("tomasz.walczuk"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	createRequestID, createKey := p140RequestIDs(t, "create")
	createBody, err := json.Marshal(map[string]any{
		"request_id": createRequestID, "idempotency_key": createKey, "operation": "create_session",
		"environment": "linux-dev", "execution_target": map[string]string{"kind": "remote", "profile": "linux-host"},
		"source": map[string]string{"mode": "empty"},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.CreateSessionIntent(ctx, mailbox.Request{
		RequestID: createRequestID, IdempotencyKey: createKey, Operation: "create_session", RawJSON: createBody,
	})
	if err != nil {
		t.Fatalf("record test-owned Mac create intent: %v", err)
	}
	sessionID, err := domain.NewSessionID(created.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	sessionClosed := false
	t.Cleanup(func() {
		if sessionClosed {
			return
		}
		p140CleanupRemoteSession(t, context.WithoutCancel(ctx), server, authority, remote, ssh, sessionID, mailboxOwner)
	})
	createIntent, err := authority.GetLocalIntentByResource(ctx, "create_session", created.SessionID, mailboxOwner)
	if err != nil {
		t.Fatal(err)
	}
	createIntent, _, err = remote.DispatchIntent(ctx, createIntent.IntentID)
	if err != nil || createIntent.DeliveryState != store.LocalIntentAccepted {
		t.Fatalf("remote create delivery=%s err=%v", createIntent.DeliveryState, err)
	}
	if err := p104WaitRemoteSessionReady(ctx, ssh, createIntent); err != nil {
		t.Fatalf("remote session readiness: %v", err)
	}

	firstCommand, firstIntent := p140SubmitMacIntent(t, ctx, server, authority, sessionID, mailboxOwner, "cancelled before target dispatch")
	if firstIntent.IntentOrdinal == nil || *firstIntent.IntentOrdinal != 1 {
		t.Fatalf("cancelled Mac command ordinal=%v, want 1", firstIntent.IntentOrdinal)
	}
	cancelRequestID, cancelKey := p140RequestIDs(t, "cancel")
	if _, err := server.CancelCommandIntent(ctx, mailbox.Request{
		RequestID: cancelRequestID, IdempotencyKey: cancelKey, Operation: "cancel_command",
		CommandID: firstCommand.CommandID, SessionID: string(sessionID),
	}); err != nil {
		t.Fatalf("record pre-dispatch cancel intent: %v", err)
	}
	cancelIntent, err := authority.GetLocalIntentByResource(ctx, "cancel_command", firstCommand.CommandID, mailboxOwner)
	if err != nil {
		t.Fatal(err)
	}
	callsBeforeCancel := caller.callCount()
	_, _, err = remote.DispatchIntent(ctx, cancelIntent.IntentID)
	if !errors.Is(err, dispatcher.ErrRemoteCancelledBeforeDelivery) {
		t.Fatalf("pre-dispatch cancel result=%v, want ErrRemoteCancelledBeforeDelivery", err)
	}
	if got := caller.callCount(); got != callsBeforeCancel {
		t.Fatalf("pre-dispatch cancellation made %d bridge calls, want none", got-callsBeforeCancel)
	}
	firstIntent, err = authority.GetLocalIntent(ctx, firstIntent.IntentID)
	if err != nil || firstIntent.DeliveryState != store.LocalIntentNotDelivered {
		t.Fatalf("cancelled Mac command delivery=%s err=%v, want not_delivered", firstIntent.DeliveryState, err)
	}
	cancelIntent, err = authority.GetLocalIntent(ctx, cancelIntent.IntentID)
	if err != nil || cancelIntent.DeliveryState != store.LocalIntentNotDelivered {
		t.Fatalf("pre-dispatch cancel delivery=%s err=%v, want not_delivered", cancelIntent.DeliveryState, err)
	}

	secondCommand, secondIntent := p140SubmitMacIntent(t, ctx, server, authority, sessionID, mailboxOwner,
		"printf x >> .p140-run-count; printf 'p140-command-ran\\n'")
	if secondIntent.IntentOrdinal == nil || *secondIntent.IntentOrdinal != 2 {
		t.Fatalf("second Mac command ordinal=%v, want 2", secondIntent.IntentOrdinal)
	}
	caller.failAfterSendFor(secondCommand.CommandID)
	_, _, dispatchErr := remote.DispatchIntent(ctx, secondIntent.IntentID)
	var transportErr *sshclient.TransportError
	if !errors.As(dispatchErr, &transportErr) || transportErr.Phase != sshclient.PhaseAfterSend {
		t.Fatalf("second command dispatch error=%v, want a simulated after-send lost reply", dispatchErr)
	}
	secondIntent, err = authority.GetLocalIntent(ctx, secondIntent.IntentID)
	if err != nil || secondIntent.DeliveryState != store.LocalIntentUncertain {
		t.Fatalf("second Mac command state=%s err=%v, want uncertain after target accepted it", secondIntent.DeliveryState, err)
	}
	var acceptedReply map[string]json.RawMessage
	if err := json.Unmarshal(caller.failedReply.Payload, &acceptedReply); err != nil {
		t.Fatalf("decode actual Linux mutation reply %s: %v", caller.failedReply.Payload, err)
	}
	if ordinal, ok := positiveRemoteAuthorityOrdinalForP140(acceptedReply); !ok || ordinal != 1 {
		t.Fatalf("actual Linux ordinal=%d present=%v, want first contiguous authority ordinal 1", ordinal, ok)
	}
	secondCommandID, err := domain.NewCommandID(secondCommand.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if err := p140WaitTargetCommandSucceeded(ctx, ssh, secondCommandID, sessionID, 1); err != nil {
		t.Fatalf("first real Linux command execution after lost reply: %v", err)
	}

	thirdCommand, thirdIntent := p140SubmitMacIntent(t, ctx, server, authority, sessionID, mailboxOwner,
		"printf 'p140-counter:'; cat .p140-run-count; printf '\\n'")
	if thirdIntent.IntentOrdinal == nil || *thirdIntent.IntentOrdinal != 3 {
		t.Fatalf("third Mac command ordinal=%v, want 3", thirdIntent.IntentOrdinal)
	}

	if _, err := database.ExecContext(ctx, "CREATE TABLE p140_backup_probe (value TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	reader, err := database.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	readSnapshot, err := reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	var localIntentCount int
	if err := readSnapshot.QueryRowContext(ctx, "SELECT COUNT(*) FROM local_intents WHERE session_id = ?", sessionID).Scan(&localIntentCount); err != nil {
		t.Fatal(err)
	}
	if localIntentCount < 5 {
		t.Fatalf("held Mac backup snapshot sees %d session intents, want create, cancelled submit/cancel, uncertain submit, and next submit", localIntentCount)
	}
	if _, err := database.ExecContext(ctx, "INSERT INTO p140_backup_probe(value) VALUES ('committed-in-wal')"); err != nil {
		t.Fatal(err)
	}
	var busy, logFrames, checkpointedFrames int
	if err := database.QueryRowContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)").Scan(&busy, &logFrames, &checkpointedFrames); err != nil {
		t.Fatal(err)
	}
	if logFrames <= checkpointedFrames {
		t.Fatalf("P140 Mac host WAL checkpoint log=%d checkpointed=%d busy=%d; want committed frames retained", logFrames, checkpointedFrames, busy)
	}
	if err := store.CreateOnlineBackup(ctx, database, backupPath); err != nil {
		t.Fatalf("create online Mac backup with committed WAL frames: %v", err)
	}
	p140AssertMacBackup(t, backupPath, sessionID, secondIntent.IntentID, thirdIntent.IntentID)

	if err := readSnapshot.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if err := server.Close(ctx); err != nil {
		t.Fatalf("stop test-owned Mac API before restore: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.RestoreOnlineBackup(ctx, databasePath, backupPath); err != nil {
		t.Fatalf("restore test-owned Mac authority while all old handles are closed: %v", err)
	}

	database, authority, server = p140OpenMacAuthority(t, databasePath, socketPath)
	remote, err = dispatcher.NewRemoteDriver(authority, caller, "router-p140-host-after-restore", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	secondIntent, err = authority.GetLocalIntent(ctx, secondIntent.IntentID)
	if err != nil || secondIntent.DeliveryState != store.LocalIntentUncertain {
		t.Fatalf("restored uncertain intent=%s err=%v, want uncertain", secondIntent.DeliveryState, err)
	}
	thirdIntent, err = authority.GetLocalIntent(ctx, thirdIntent.IntentID)
	if err != nil || thirdIntent.DeliveryState != store.LocalIntentRecorded {
		t.Fatalf("restored next intent=%s err=%v, want recorded", thirdIntent.DeliveryState, err)
	}
	callsBeforeBlockedDispatch := caller.callCount()
	if _, _, err := remote.DispatchNext(ctx); !errors.Is(err, dispatcher.ErrNoRemoteDispatchWork) {
		t.Fatalf("post-restore next dispatch=%v, want no work until uncertain predecessor reconciliation", err)
	}
	if got := caller.callCount(); got != callsBeforeBlockedDispatch {
		t.Fatalf("post-restore later command dispatch made %d target reads or mutations before reconciliation", got-callsBeforeBlockedDispatch)
	}

	frameCountBeforeReconcile := caller.callCount()
	accepted, reconcileReply, err := remote.ReconcileIntent(ctx, secondIntent.IntentID)
	if err != nil || accepted.DeliveryState != store.LocalIntentAccepted {
		t.Fatalf("reconcile stable command ID after restore delivery=%s err=%v", accepted.DeliveryState, err)
	}
	if caller.callCount() != frameCountBeforeReconcile+1 || caller.lastOperation() != sshbridge.OperationGetCommand {
		t.Fatalf("restore recovery did not perform exactly one GET command; calls=%d last=%s", caller.callCount()-frameCountBeforeReconcile, caller.lastOperation())
	}
	var reconciledReply map[string]json.RawMessage
	if err := json.Unmarshal(reconcileReply.Payload, &reconciledReply); err != nil {
		t.Fatal(err)
	}
	if ordinal, ok := positiveRemoteAuthorityOrdinalForP140(reconciledReply); !ok || ordinal != 1 {
		t.Fatalf("reconciled Linux ordinal=%d present=%v, want 1 preserved across Mac restore", ordinal, ok)
	}
	secondProjection, err := authority.GetRemoteCommandProjection(ctx, secondCommandID)
	if err != nil || secondProjection.Ordinal != 1 || secondProjection.CommandID != secondIntent.CommandID || secondProjection.SessionID != sessionID {
		t.Fatalf("reconciled target projection=%+v err=%v, want stable IDs and Linux ordinal 1", secondProjection, err)
	}
	secondProjection = p140WaitRemoteCommand(t, ctx, authority, remote, secondCommandID, queuedController, mailboxOwner)
	if secondProjection.State != domain.CommandStateSucceeded {
		t.Fatalf("reconciled first command state=%s, want succeeded", secondProjection.State)
	}
	completedSecond, err := authority.GetLocalIntent(ctx, secondIntent.IntentID)
	if err != nil || completedSecond.DeliveryState != store.LocalIntentReconciled {
		t.Fatalf("first command after target output reconciliation=%s err=%v, want reconciled", completedSecond.DeliveryState, err)
	}

	thirdIntent, _, err = remote.DispatchIntent(ctx, thirdIntent.IntentID)
	if err != nil || thirdIntent.DeliveryState != store.LocalIntentAccepted {
		t.Fatalf("next command dispatch after reconciliation=%s err=%v", thirdIntent.DeliveryState, err)
	}
	var submittedReply map[string]json.RawMessage
	if err := json.Unmarshal(caller.lastSubmitReply.Payload, &submittedReply); err != nil {
		t.Fatalf("decode next real Linux submit reply: %v", err)
	}
	if ordinal, ok := positiveRemoteAuthorityOrdinalForP140(submittedReply); !ok || ordinal != 2 {
		t.Fatalf("next Linux authority ordinal=%d present=%v, want contiguous 2 after cancelled Mac ordinal 1", ordinal, ok)
	}
	thirdCommandID, err := domain.NewCommandID(thirdCommand.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	thirdProjection := p140WaitRemoteCommand(t, ctx, authority, remote, thirdCommandID, queuedController, mailboxOwner)
	if thirdProjection.State != domain.CommandStateSucceeded {
		t.Fatalf("next command terminal state=%s, want succeeded", thirdProjection.State)
	}
	_, thirdEvents, err := authority.GetRemoteCommandWithEvents(ctx, thirdCommandID)
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	for _, event := range thirdEvents {
		if event.Type == "stdout" {
			output.Write(event.Payload)
		}
	}
	if output.String() != "p140-counter:x\n" {
		t.Fatalf("counter command output=%q, want exactly one execution of the uncertain command", output.String())
	}
	if thirdProjection.Ordinal != 2 {
		t.Fatalf("third Mac intent projection ordinal=%d, want Linux authority ordinal 2", thirdProjection.Ordinal)
	}

	if p140CloseRemoteSession(t, ctx, server, authority, remote, ssh, sessionID, mailboxOwner) {
		sessionClosed = true
	}
	if err := server.Close(ctx); err != nil {
		t.Errorf("close test-owned Mac API after P140: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Errorf("close test-owned Mac authority after P140: %v", err)
	}
	t.Logf("machine=Mac account=%s OS=%s remote=ubuntu@129.151.232.40: cancelled Mac ordinal 1 was not delivered; Linux accepted Mac ordinal 2 as authority ordinal 1; restored Mac backup blocked ordinal 3 until stable-ID GET reconciliation; Linux then accepted it as authority ordinal 2; output side effect occurred once", current.Username, runtime.GOOS)
}

type p140AfterSendCaller struct {
	client          *sshclient.Client
	failCommandID   string
	failureConsumed bool
	failedReply     sshbridge.ReplyFrame
	lastSubmitReply sshbridge.ReplyFrame
	frames          []sshbridge.RequestFrame
}

func (c *p140AfterSendCaller) Call(ctx context.Context, frame sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
	c.frames = append(c.frames, frame)
	reply, err := c.client.Call(ctx, frame)
	if err != nil {
		return reply, err
	}
	if frame.Operation == sshbridge.OperationSubmitOrResumeCommand {
		c.lastSubmitReply = reply
		if frame.ResourceID == c.failCommandID && !c.failureConsumed {
			c.failureConsumed = true
			c.failedReply = reply
			return sshbridge.ReplyFrame{}, &sshclient.TransportError{Phase: sshclient.PhaseAfterSend, Err: errors.New("P140 dropped actual post-commit Linux response")}
		}
	}
	return reply, nil
}

func (c *p140AfterSendCaller) Stream(ctx context.Context, frame sshbridge.RequestFrame, receive func(sshbridge.ReplyFrame) error) error {
	return c.client.Stream(ctx, frame, receive)
}

func (c *p140AfterSendCaller) failAfterSendFor(commandID string) {
	c.failCommandID = commandID
}

func (c *p140AfterSendCaller) callCount() int { return len(c.frames) }

func (c *p140AfterSendCaller) lastOperation() sshbridge.Operation {
	if len(c.frames) == 0 {
		return ""
	}
	return c.frames[len(c.frames)-1].Operation
}

func p140OpenMacAuthority(t *testing.T, databasePath, socketPath string) (*sql.DB, *store.AuthorityStore, *Server) {
	t.Helper()
	database, err := store.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := store.NewAuthorityStore(database)
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	server, err := NewServer(ServerOptions{Authority: authority, Owner: p063Owner(t), SocketPath: socketPath})
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	return database, authority, server
}

func p140SubmitMacIntent(t *testing.T, ctx context.Context, server *Server, authority *store.AuthorityStore, sessionID domain.SessionID, owner domain.ControllerIdentity, script string) (mailbox.CommandIntent, store.LocalIntentRecord) {
	t.Helper()
	requestID, key := p140RequestIDs(t, "submit")
	body, err := json.Marshal(map[string]any{
		"request_id": requestID, "idempotency_key": key, "operation": "submit_command",
		"session_id": string(sessionID), "script": script, "timeout_seconds": 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := server.SubmitCommandIntent(ctx, mailbox.Request{
		RequestID: requestID, IdempotencyKey: key, Operation: "submit_command", SessionID: string(sessionID), RawJSON: body,
	})
	if err != nil {
		t.Fatalf("record test-owned Mac command intent: %v", err)
	}
	intent, err := authority.GetLocalIntentByResource(ctx, "submit_command", accepted.CommandID, owner)
	if err != nil {
		t.Fatal(err)
	}
	return accepted, intent
}

func p140RequestIDs(t *testing.T, suffix string) (string, string) {
	t.Helper()
	var random [16]byte
	if _, err := io.ReadFull(rand.Reader, random[:]); err != nil {
		t.Fatal(err)
	}
	key := "p140-" + hex.EncodeToString(random[:])
	return "req-" + key + "-" + suffix, key
}

func p140WaitRemoteCommand(t *testing.T, ctx context.Context, authority *store.AuthorityStore, remote *dispatcher.RemoteDriver, commandID domain.CommandID, targetController, mailboxOwner domain.ControllerIdentity) store.RemoteCommandProjection {
	t.Helper()
	deadline, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	for {
		if _, err := remote.MirrorCommandEvents(deadline, commandID, targetController); err != nil {
			t.Fatalf("mirror real Ubuntu command %s events: %v", commandID, err)
		}
		projection, err := remote.RefreshCommandProjection(deadline, commandID, mailboxOwner)
		if err == nil && projection.State.IsTerminal() {
			return projection
		}
		select {
		case <-deadline.Done():
			t.Fatalf("real Ubuntu command %s did not reach terminal state: %v", commandID, deadline.Err())
		case <-time.After(120 * time.Millisecond):
		}
	}
}

func p140WaitTargetCommandSucceeded(ctx context.Context, caller *sshclient.Client, commandID domain.CommandID, sessionID domain.SessionID, ordinal int64) error {
	deadline, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	payload, err := json.Marshal(map[string]string{"command_id": string(commandID)})
	if err != nil {
		return err
	}
	frame := sshbridge.RequestFrame{
		ProtocolVersion: sshbridge.ProtocolVersion, RequestID: "p140-target-command-" + string(commandID),
		Operation: sshbridge.OperationGetCommand, Payload: payload,
	}
	ticker := time.NewTicker(120 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		reply, callErr := caller.Call(deadline, frame)
		if callErr != nil {
			lastErr = callErr
		} else if reply.ProtocolVersion != sshbridge.ProtocolVersion || reply.RequestID != frame.RequestID || reply.ResponseType != "result" {
			lastErr = fmt.Errorf("target command reply identity/type mismatch: %+v", reply)
		} else {
			var result struct {
				CommandID    string `json:"command_id"`
				SessionID    string `json:"session_id"`
				Ordinal      int64  `json:"ordinal"`
				CommandState string `json:"command_state"`
			}
			if err := json.Unmarshal(reply.Payload, &result); err != nil {
				return fmt.Errorf("decode target command result: %w", err)
			}
			if result.CommandID != string(commandID) || result.SessionID != string(sessionID) || result.Ordinal != ordinal {
				return fmt.Errorf("target command identity/ordinal mismatch: %+v", result)
			}
			if result.CommandState == string(domain.CommandStateSucceeded) {
				return nil
			}
			if state := domain.CommandState(result.CommandState); !state.Valid() || state.IsTerminal() {
				return fmt.Errorf("target command reached state %q before succeeded", result.CommandState)
			}
			lastErr = fmt.Errorf("target command remains %s", result.CommandState)
		}
		select {
		case <-deadline.Done():
			return fmt.Errorf("wait for target command: %w (last observation: %v)", deadline.Err(), lastErr)
		case <-ticker.C:
		}
	}
}

func p140AssertMacBackup(t *testing.T, backupPath string, sessionID domain.SessionID, uncertainID, recordedID domain.IntentID) {
	t.Helper()
	database, err := sql.Open("sqlite", backupPath)
	if err != nil {
		t.Fatalf("open P140 Mac online backup: %v", err)
	}
	defer database.Close()
	var probe, uncertainState, recordedState string
	if err := database.QueryRow("SELECT value FROM p140_backup_probe WHERE value = 'committed-in-wal'").Scan(&probe); err != nil || probe != "committed-in-wal" {
		t.Fatalf("P140 Mac snapshot WAL probe=%q err=%v", probe, err)
	}
	if err := database.QueryRow("SELECT delivery_state FROM local_intents WHERE intent_id = ? AND session_id = ?", uncertainID, sessionID).Scan(&uncertainState); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow("SELECT delivery_state FROM local_intents WHERE intent_id = ? AND session_id = ?", recordedID, sessionID).Scan(&recordedState); err != nil {
		t.Fatal(err)
	}
	if uncertainState != string(store.LocalIntentUncertain) || recordedState != string(store.LocalIntentRecorded) {
		t.Fatalf("P140 Mac snapshot intent states uncertain=%q recorded=%q", uncertainState, recordedState)
	}
}

func p140CheckOwnerOnlyFile(t *testing.T, name, path string) {
	t.Helper()
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		t.Fatalf("%s must name an absolute, clean fixture path", name)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		t.Fatalf("%s fixture must be a small regular file: info=%v err=%v", name, info, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint32(stat.Uid) != uint32(os.Geteuid()) || info.Mode().Perm()&0o400 == 0 || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("%s fixture must be owned by the current account and have no group/other permissions", name)
	}
}

func positiveRemoteAuthorityOrdinalForP140(object map[string]json.RawMessage) (int64, bool) {
	raw, present := object["ordinal"]
	if !present {
		return 0, false
	}
	var ordinal int64
	if json.Unmarshal(raw, &ordinal) != nil || ordinal <= 0 {
		return 0, false
	}
	return ordinal, true
}

func p140CleanupRemoteSession(t *testing.T, ctx context.Context, server *Server, authority *store.AuthorityStore, remote *dispatcher.RemoteDriver, ssh *sshclient.Client, sessionID domain.SessionID, owner domain.ControllerIdentity) {
	t.Helper()
	cleanupCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if !p140CloseRemoteSession(t, cleanupCtx, server, authority, remote, ssh, sessionID, owner) {
		t.Errorf("could not confirm cleanup of test-owned P140 remote session %s", sessionID)
	}
}

func p140CloseRemoteSession(t *testing.T, ctx context.Context, server *Server, authority *store.AuthorityStore, remote *dispatcher.RemoteDriver, ssh *sshclient.Client, sessionID domain.SessionID, owner domain.ControllerIdentity) bool {
	t.Helper()
	requestID, key := p140RequestIDs(t, "close")
	if _, err := server.CloseSessionIntent(ctx, mailbox.Request{
		RequestID: requestID, IdempotencyKey: key, Operation: "close_session", SessionID: string(sessionID), ClosePolicy: "graceful",
	}); err != nil {
		t.Errorf("accept test-owned P140 remote session cleanup: %v", err)
		return false
	}
	intent, err := authority.GetLocalIntentByResource(ctx, "close_session", string(sessionID), owner)
	if err != nil {
		t.Errorf("read test-owned P140 close intent: %v", err)
		return false
	}
	if _, _, err := remote.DispatchIntent(ctx, intent.IntentID); err != nil {
		t.Errorf("dispatch test-owned P140 remote session cleanup: %v", err)
		return false
	}
	requestID = "p140-session-state-" + string(sessionID)
	payload, _ := json.Marshal(map[string]string{"session_id": string(sessionID)})
	frame := sshbridge.RequestFrame{ProtocolVersion: sshbridge.ProtocolVersion, RequestID: requestID, Operation: sshbridge.OperationGetSession, Payload: payload}
	for {
		reply, callErr := ssh.Call(ctx, frame)
		if callErr == nil && reply.ResponseType == "result" && reply.RequestID == requestID {
			var result struct {
				SessionState string `json:"session_state"`
			}
			if json.Unmarshal(reply.Payload, &result) == nil && result.SessionState == string(domain.SessionStateClosed) {
				return true
			}
		}
		select {
		case <-ctx.Done():
			t.Errorf("test-owned P140 remote session %s did not close: %v", sessionID, ctx.Err())
			return false
		case <-time.After(150 * time.Millisecond):
		}
	}
}

var _ dispatcher.RemoteCaller = (*p140AfterSendCaller)(nil)
var _ interface {
	Stream(context.Context, sshbridge.RequestFrame, func(sshbridge.ReplyFrame) error) error
} = (*p140AfterSendCaller)(nil)
