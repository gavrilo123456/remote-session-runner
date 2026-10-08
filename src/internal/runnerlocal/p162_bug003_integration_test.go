package runnerlocal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/dispatcher"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/lifecycle"
	"remote-session-runner/src/internal/localapi"
	"remote-session-runner/src/internal/mailbox"
	"remote-session-runner/src/internal/mailboxclient"
	"remote-session-runner/src/internal/sshbridge"
	"remote-session-runner/src/internal/store"
)

const (
	p162MailboxID     = "slidestud-io"
	p162TargetProfile = "sandbox-host"
	p162Endpoint      = "132.226.205.205:8443"
	p162Secret        = "BUG003_P162_SECRET_MUST_NOT_REACH_STDERR"
)

// TestBUG003ServiceCycleIsolatesRemoteMailboxFailureAcrossRestart is the
// service-level regression for the reported outage shape. It uses a real
// marker-last mailbox, local API, dispatcher, durable SQLite state, and a
// controlled store clock. The only fake is the in-memory remote bridge; no
// host, SSH connection, or network listener is contacted.
func TestBUG003ServiceCycleIsolatesRemoteMailboxFailureAcrossRestart(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	databasePath := filepath.Join(root, "state", "authority.db")
	now := time.Now().UTC().Truncate(time.Second)
	caller := newP162RemoteCaller(func() time.Time { return now })

	h := newP162Harness(t, root, databasePath, &now, caller)
	client := p162Client(t, h.mailboxRoot)
	const firstID = "req-p162-status-unavailable"
	const secondID = "req-p162-later-completes"
	p162WriteRunRequest(t, client, firstID, "key-p162-status-unavailable", "printf 'P162_STATUS_FAILURE\\n'")
	p162WriteRunRequest(t, client, secondID, "key-p162-later-completes", "printf 'P162_COMPLETE_OK\\n'")

	var firstCycleStderr bytes.Buffer
	h.service.runCycle(ctx, lifecycle.NewGate(), &firstCycleStderr)

	firstAccepted := p162ReadResponse(t, client, firstID)
	if firstAccepted.RequestState != "accepted" || firstAccepted.JobID == "" || firstAccepted.SessionID == "" || firstAccepted.CommandID == "" {
		t.Fatalf("first mailbox response=%+v, want accepted remote run receipt", firstAccepted)
	}
	secondComplete := p162ReadResponse(t, client, secondID)
	if secondComplete.RequestState != "complete" || secondComplete.CommandState != string(domain.CommandStateSucceeded) ||
		secondComplete.Stdout != "P162_COMPLETE_OK\\n" || secondComplete.OutputComplete == nil || !*secondComplete.OutputComplete ||
		secondComplete.ResolvedExecutionTarget == nil || secondComplete.ResolvedExecutionTarget.Kind != string(domain.TargetKindRemote) ||
		secondComplete.ResolvedExecutionTarget.Profile != p162TargetProfile || secondComplete.ResolvedEnvironment != "sandbox-dev" {
		t.Fatalf("later remote mailbox response=%+v, want complete sandbox result", secondComplete)
	}
	if events, err := client.ReadEventsThroughCursor(secondComplete); err != nil || len(events) != 4 || events[2].Text != "P162_COMPLETE_OK\\n" {
		t.Fatalf("later mailbox event projection events=%+v err=%v", events, err)
	}

	firstIntent := p162IntentForRequest(t, h.authority, h.owner, firstID)
	if firstIntent.DeliveryState != store.LocalIntentAccepted || firstIntent.RemoteStatusFailureAt == nil ||
		firstIntent.RemoteStatusFailureCode != store.RemoteStatusFailureCodeUnavailable || firstIntent.RemoteStatusFailureAttempts != 1 {
		t.Fatalf("first accepted remote intent=%+v, want durable first status failure", firstIntent)
	}
	if caller.mutationCount(string(firstIntent.JobID)) != 1 || caller.statusReadCount(string(firstIntent.JobID)) != 1 {
		t.Fatalf("first remote caller mutation=%d status_reads=%d, want exactly one each", caller.mutationCount(string(firstIntent.JobID)), caller.statusReadCount(string(firstIntent.JobID)))
	}
	secondIntent := p162IntentForRequest(t, h.authority, h.owner, secondID)
	if secondIntent.DeliveryState != store.LocalIntentReconciled || caller.mutationCount(string(secondIntent.JobID)) != 1 {
		t.Fatalf("later remote intent=%+v mutations=%d, want reconciled with one mutation", secondIntent, caller.mutationCount(string(secondIntent.JobID)))
	}

	firstLog := firstCycleStderr.String()
	for _, want := range []string{
		"remote_reconciliation", "mailbox=" + p162MailboxID, "request_id=" + firstID,
		"job_id=" + string(firstIntent.JobID), "target_profile=" + p162TargetProfile,
		"endpoint=" + p162Endpoint, "failure_class=remote_status_unavailable", "retry_count=1",
	} {
		if !strings.Contains(firstLog, want) {
			t.Fatalf("safe service diagnostic %q is missing %q", firstLog, want)
		}
	}
	if strings.Contains(firstLog, p162Secret) {
		t.Fatalf("service stderr leaked fake remote secret: %q", firstLog)
	}

	// Close the live SQLite/API composition before re-opening it, which models
	// a runner-local restart without touching the marker, outbox, or event tree.
	h.close()
	now = firstIntent.RemoteStatusFailureAt.Add(dispatcher.RemoteUncertaintyWindow + time.Second)
	h = newP162Harness(t, root, databasePath, &now, caller)
	client = p162Client(t, h.mailboxRoot)

	// runCycle normally rate-limits read-only recovery on wall time. A new
	// process begins with no prior cycle, so keep that restart fact explicit in
	// this deterministic in-process composition too.
	h.service.lastRemoteReconcile = time.Time{}
	var deadlineStderr bytes.Buffer
	h.service.runCycle(ctx, lifecycle.NewGate(), &deadlineStderr)
	firstTerminal := p162ReadResponse(t, client, firstID)
	if firstTerminal.RequestState != "indeterminate" || firstTerminal.JobID != firstAccepted.JobID ||
		firstTerminal.SessionID != firstAccepted.SessionID || firstTerminal.CommandID != firstAccepted.CommandID ||
		firstTerminal.Error == nil || firstTerminal.Error.Code != store.RemoteStatusFailureCodeUnavailable ||
		firstTerminal.ResponseRevision <= firstAccepted.ResponseRevision || firstTerminal.JobPhase != "" || firstTerminal.CommandState != "" ||
		firstTerminal.AvailableEventSequence != nil || firstTerminal.OutputComplete != nil {
		t.Fatalf("expired status result=%+v, want immutable truthful indeterminate response", firstTerminal)
	}
	if caller.mutationCount(string(firstIntent.JobID)) != 1 || caller.statusReadCount(string(firstIntent.JobID)) != 1 {
		t.Fatalf("deadline recovery replayed first remote work: mutations=%d status_reads=%d", caller.mutationCount(string(firstIntent.JobID)), caller.statusReadCount(string(firstIntent.JobID)))
	}
	if strings.Contains(deadlineStderr.String(), p162Secret) {
		t.Fatalf("deadline cycle leaked fake remote secret: %q", deadlineStderr.String())
	}

	firstTerminalBytes := p162ReadFile(t, filepath.Join(h.mailboxRoot, "outbox", firstID+mailbox.RequestSuffix))
	secondTerminalBytes := p162ReadFile(t, filepath.Join(h.mailboxRoot, "outbox", secondID+mailbox.RequestSuffix))

	// A second process start must only re-project the frozen terminal bytes;
	// it cannot replay either remote mutation or rewrite a terminal receipt.
	h.close()
	h = newP162Harness(t, root, databasePath, &now, caller)
	client = p162Client(t, h.mailboxRoot)
	h.service.lastRemoteReconcile = time.Time{}
	h.service.runCycle(ctx, lifecycle.NewGate(), io.Discard)
	if got := p162ReadFile(t, filepath.Join(h.mailboxRoot, "outbox", firstID+mailbox.RequestSuffix)); !bytes.Equal(got, firstTerminalBytes) {
		t.Fatalf("first terminal response changed after restart:\nwant=%s\n got=%s", firstTerminalBytes, got)
	}
	if got := p162ReadFile(t, filepath.Join(h.mailboxRoot, "outbox", secondID+mailbox.RequestSuffix)); !bytes.Equal(got, secondTerminalBytes) {
		t.Fatalf("later terminal response changed after restart:\nwant=%s\n got=%s", secondTerminalBytes, got)
	}
	if caller.totalMutations() != 2 || caller.statusReadCount(string(firstIntent.JobID)) != 1 {
		t.Fatalf("restart replayed remote work: mutations=%d first_status_reads=%d", caller.totalMutations(), caller.statusReadCount(string(firstIntent.JobID)))
	}

	// ACK exactly the two frozen responses through the native file-only client,
	// then advance the same controlled durable clock through ACK retention.
	if err := client.WriteAcknowledgment(firstID, firstTerminal); err != nil {
		t.Fatalf("write first native ACK: %v", err)
	}
	if err := client.WriteAcknowledgment(secondID, secondComplete); err != nil {
		t.Fatalf("write later native ACK: %v", err)
	}
	h.service.runMailboxCycles(ctx, io.Discard)
	for _, requestID := range []string{firstID, secondID} {
		record := p162Exchange(t, h.authority, requestID)
		if record.AcknowledgedAt == nil || record.ResponseCleanupAt == nil {
			t.Fatalf("ACK was not durable for %s: %+v", requestID, record)
		}
	}

	now = now.Add(store.MailboxAckedResponseLifetime + time.Second)
	h.service.runMailboxCycles(ctx, io.Discard)
	for _, path := range []string{
		filepath.Join(h.mailboxRoot, "outbox", firstID+mailbox.RequestSuffix),
		filepath.Join(h.mailboxRoot, "outbox", secondID+mailbox.RequestSuffix),
		filepath.Join(h.mailboxRoot, "events", secondComplete.CommandID+".ndjson"),
	} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("ACK-retention cleanup path=%s err=%v, want absent", path, err)
		}
	}
	for _, requestID := range []string{firstID, secondID} {
		record := p162Exchange(t, h.authority, requestID)
		if record.AcknowledgedAt == nil || record.ResponseFileRemovedAt == nil || len(record.TerminalResponseBytes) == 0 {
			t.Fatalf("cleanup lost durable mailbox receipt for %s: %+v", requestID, record)
		}
	}
	if caller.totalMutations() != 2 || caller.statusReadCount(string(firstIntent.JobID)) != 1 {
		t.Fatalf("ACK cleanup replayed remote work: mutations=%d first_status_reads=%d", caller.totalMutations(), caller.statusReadCount(string(firstIntent.JobID)))
	}
}

// TestBUG016CommandlessLostPendingPublishesBoundedIndeterminateWithoutReplay
// models the target shape found in BUG-016: a one-off job became lost before
// the job checkpoint recorded any command, and teardown remains unconfirmed.
// The Router must retain the accepted identity while it seeks a proof, then
// publish one truthful indeterminate receipt at the existing deadline without
// replaying the immutable run or inventing output/events.
func TestBUG016CommandlessLostPendingPublishesBoundedIndeterminateWithoutReplay(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	databasePath := filepath.Join(root, "state", "authority.db")
	now := time.Now().UTC().Truncate(time.Second)
	caller := newP162RemoteCaller(func() time.Time { return now })

	h := newP162Harness(t, root, databasePath, &now, caller)
	client := p162Client(t, h.mailboxRoot)
	const lostID = "req-bug016-pre-command-lost"
	const completeID = "req-bug016-independent-complete"
	p162WriteRunRequest(t, client, lostID, "key-bug016-pre-command-lost", "printf 'P162_PRECOMMAND_LOST\\n'")
	p162WriteRunRequest(t, client, completeID, "key-bug016-independent-complete", "printf 'P162_COMPLETE_OK\\n'")

	var firstCycleStderr bytes.Buffer
	h.service.runCycle(ctx, lifecycle.NewGate(), &firstCycleStderr)
	lostAccepted := p162ReadResponse(t, client, lostID)
	if lostAccepted.RequestState != string(store.MailboxExchangeAccepted) || lostAccepted.JobID == "" || lostAccepted.SessionID == "" || lostAccepted.CommandID == "" ||
		lostAccepted.JobPhase != "" || lostAccepted.CommandState != "" || lostAccepted.TeardownOutcome != "" || lostAccepted.Error != nil {
		t.Fatalf("initial BUG-016 response=%+v, want identity-only accepted receipt", lostAccepted)
	}
	completed := p162ReadResponse(t, client, completeID)
	if completed.RequestState != string(store.MailboxExchangeComplete) || completed.CommandState != string(domain.CommandStateSucceeded) ||
		completed.OutputComplete == nil || !*completed.OutputComplete {
		t.Fatalf("independent completed response=%+v, want normal terminal result", completed)
	}

	lostIntent := p162IntentForRequest(t, h.authority, h.owner, lostID)
	if lostIntent.DeliveryState != store.LocalIntentAccepted || lostIntent.RemoteStatusFailureAt == nil ||
		lostIntent.RemoteStatusFailureCode != store.RemoteStatusFailureCodeUnavailable || lostIntent.RemoteStatusFailureAttempts != 1 {
		t.Fatalf("BUG-016 intent=%+v, want durable accepted status uncertainty", lostIntent)
	}
	if caller.mutationCount(string(lostIntent.JobID)) != 1 || caller.statusReadCount(string(lostIntent.JobID)) != 1 ||
		caller.commandReads[string(lostIntent.CommandID)] != 0 || caller.streamReads[string(lostIntent.CommandID)] != 0 {
		t.Fatalf("initial BUG-016 calls mutation=%d status=%d command=%d stream=%d, want one RUN/status and no command/event reads",
			caller.mutationCount(string(lostIntent.JobID)), caller.statusReadCount(string(lostIntent.JobID)), caller.commandReads[string(lostIntent.CommandID)], caller.streamReads[string(lostIntent.CommandID)])
	}
	if !strings.Contains(firstCycleStderr.String(), "failure_class=remote_status_unavailable") || !strings.Contains(firstCycleStderr.String(), "request_id="+lostID) {
		t.Fatalf("BUG-016 initial cycle lacked sanitized reconciliation diagnostic: %s", firstCycleStderr.String())
	}
	if _, err := os.Stat(filepath.Join(h.mailboxRoot, "events", string(lostIntent.CommandID)+".ndjson")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("BUG-016 initial accepted response created event artifact err=%v", err)
	}
	h.close()

	now = lostIntent.RemoteStatusFailureAt.Add(dispatcher.RemoteUncertaintyWindow + time.Second)
	h = newP162Harness(t, root, databasePath, &now, caller)
	defer h.close()
	client = p162Client(t, h.mailboxRoot)
	h.service.lastRemoteReconcile = time.Time{}
	h.service.runCycle(ctx, lifecycle.NewGate(), io.Discard)
	terminal := p162ReadResponse(t, client, lostID)
	if terminal.RequestState != string(store.MailboxExchangeIndeterminate) || terminal.ResponseRevision <= lostAccepted.ResponseRevision ||
		terminal.JobID != lostAccepted.JobID || terminal.SessionID != lostAccepted.SessionID || terminal.CommandID != lostAccepted.CommandID ||
		terminal.DeliveryState != string(store.LocalIntentAccepted) || terminal.Error == nil || terminal.Error.Code != store.RemoteStatusFailureCodeUnavailable || terminal.Error.Retryable ||
		terminal.JobPhase != "" || terminal.CommandState != "" || terminal.TeardownOutcome != "" || terminal.ExitCode != nil ||
		terminal.FinalEventSequence != nil || terminal.AvailableEventSequence != nil || terminal.OutputComplete != nil || terminal.OutputTruncated != nil ||
		terminal.OutputUnavailableReason != "" || terminal.EventsFile != "" || terminal.Stdout != "" || terminal.Stderr != "" {
		t.Fatalf("BUG-016 terminal response=%+v, want truthful bounded indeterminate result", terminal)
	}
	terminalBytes := p162ReadFile(t, filepath.Join(h.mailboxRoot, "outbox", lostID+mailbox.RequestSuffix))
	h.service.runCycle(ctx, lifecycle.NewGate(), io.Discard)
	if got := p162ReadFile(t, filepath.Join(h.mailboxRoot, "outbox", lostID+mailbox.RequestSuffix)); !bytes.Equal(got, terminalBytes) {
		t.Fatalf("BUG-016 terminal response changed after repeat reconciliation")
	}
	if caller.mutationCount(string(lostIntent.JobID)) != 1 || caller.statusReadCount(string(lostIntent.JobID)) != 1 ||
		caller.commandReads[string(lostIntent.CommandID)] != 0 || caller.streamReads[string(lostIntent.CommandID)] != 0 {
		t.Fatalf("BUG-016 deadline replayed work mutation=%d status=%d command=%d stream=%d",
			caller.mutationCount(string(lostIntent.JobID)), caller.statusReadCount(string(lostIntent.JobID)), caller.commandReads[string(lostIntent.CommandID)], caller.streamReads[string(lostIntent.CommandID)])
	}
}

type p162Harness struct {
	authority   *store.AuthorityStore
	owner       domain.ControllerIdentity
	api         *localapi.Server
	database    interface{ Close() error }
	service     *Service
	mailboxRoot string
	closed      bool
}

func newP162Harness(t *testing.T, root, databasePath string, now *time.Time, caller *p162RemoteCaller) *p162Harness {
	t.Helper()
	if now == nil || caller == nil {
		t.Fatal("P162 harness requires clock and remote caller")
	}
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o700); err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := store.NewAuthorityStoreWithClock(database, func() time.Time { return now.UTC() })
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	ownerID, err := domain.NewControllerID(config.MacAccount)
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	owner, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, ownerID)
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "run"), 0o700); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	api, err := localapi.NewServer(localapi.ServerOptions{
		Authority: authority, Owner: owner, SocketPath: filepath.Join(root, "run", "local-api.sock"),
	})
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}

	mailboxRoot := filepath.Join(root, "mailbox")
	runtime := p162MailboxRuntime(t, mailboxRoot, authority, owner, api, now)
	localDriver, err := dispatcher.NewLocalDriver(authority, p162NoopAcceptor{}, "router-p162-local", time.Minute)
	if err != nil {
		_ = api.Close(context.Background())
		_ = database.Close()
		t.Fatal(err)
	}
	resolver, err := dispatcher.NewRemoteCallerResolver(map[string]dispatcher.RemoteCaller{p162TargetProfile: caller})
	if err != nil {
		_ = api.Close(context.Background())
		_ = database.Close()
		t.Fatal(err)
	}
	remoteDriver, err := dispatcher.NewRemoteDriverWithResolverAndClock(authority, resolver, "router-p162-remote", time.Minute, func() time.Time { return now.UTC() })
	if err != nil {
		_ = api.Close(context.Background())
		_ = database.Close()
		t.Fatal(err)
	}
	h := &p162Harness{
		authority: authority, owner: owner, api: api, database: database, mailboxRoot: mailboxRoot,
		service: &Service{
			database: authority, dbCloser: database, api: api, localDriver: localDriver, remoteDriver: remoteDriver,
			remoteEndpoints: map[string]string{p162TargetProfile: p162Endpoint}, mailboxes: []mailboxRuntime{runtime},
		},
	}
	t.Cleanup(h.close)
	return h
}

func (h *p162Harness) close() {
	if h == nil || h.closed {
		return
	}
	h.closed = true
	if h.api != nil {
		_ = h.api.Close(context.Background())
	}
	if h.database != nil {
		_ = h.database.Close()
	}
}

func p162MailboxRuntime(t *testing.T, root string, authority *store.AuthorityStore, owner domain.ControllerIdentity, operations mailbox.SessionOperations, now *time.Time) mailboxRuntime {
	t.Helper()
	importer, err := mailbox.New(mailbox.Options{MailboxID: p162MailboxID, Root: root, Clock: func() time.Time { return now.UTC() }})
	if err != nil {
		t.Fatal(err)
	}
	outbox, err := mailbox.NewOutbox(root)
	if err != nil {
		t.Fatal(err)
	}
	eventFiles, err := mailbox.NewEventFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	processor, err := mailbox.NewSessionProcessor(mailbox.SessionProcessorOptions{
		MailboxID: p162MailboxID, Importer: importer, Authority: authority, Controller: owner, Operations: operations,
		Outbox: outbox, EventFiles: eventFiles, ExecutionResolver: p162MailboxResolver{},
		Now: func() time.Time { return now.UTC() }, RemoteUncertaintyWindow: dispatcher.RemoteUncertaintyWindow,
		DeferTerminalArtifactRecovery: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ackImporter, err := mailbox.NewAckImporter(mailbox.AckImporterOptions{
		MailboxID: p162MailboxID, Root: root, Authority: authority, Clock: func() time.Time { return now.UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	return mailboxRuntime{
		id: p162MailboxID, importer: importer, processor: processor, ackImporter: ackImporter,
		artifactCleaner: mailbox.ArtifactCleaner{MailboxID: p162MailboxID, Authority: authority, Outbox: outbox, EventFiles: eventFiles},
	}
}

type p162MailboxResolver struct{}

func (p162MailboxResolver) ResolveMailboxExecution(mailboxID string, environmentPresent bool, environment string, targetPresent bool, target domain.ExecutionTarget, repositoryAlias string) (config.MailboxExecutionSelection, error) {
	if mailboxID != p162MailboxID {
		return config.MailboxExecutionSelection{}, config.ErrMailboxNotConfigured
	}
	if repositoryAlias != "" && repositoryAlias != p162MailboxID {
		return config.MailboxExecutionSelection{}, config.ErrMailboxRepositoryAliasNotAllowed
	}
	if environmentPresent != targetPresent {
		return config.MailboxExecutionSelection{}, config.ErrMailboxExecutionPairRequired
	}
	remote, err := domain.NewExecutionTarget(domain.TargetKindRemote, p162TargetProfile)
	if err != nil {
		return config.MailboxExecutionSelection{}, err
	}
	if environmentPresent {
		if environment != "sandbox-dev" || target != remote {
			return config.MailboxExecutionSelection{}, config.ErrMailboxExecutionContextNotFound
		}
		return config.MailboxExecutionSelection{
			MailboxID: mailboxID, ContextName: "ubuntu-sandbox", Environment: environment, Target: target,
			Source: config.MailboxExecutionSelectionSourceRequestOverride, RepositoryAlias: repositoryAlias,
			RepositoryAliases: []string{p162MailboxID},
		}, nil
	}
	return config.MailboxExecutionSelection{
		MailboxID: mailboxID, ContextName: "ubuntu-sandbox", Environment: "sandbox-dev", Target: remote,
		Source: config.MailboxExecutionSelectionSourceInboxDefault, RepositoryAlias: repositoryAlias,
		RepositoryAliases: []string{p162MailboxID},
	}, nil
}

type p162NoopAcceptor struct{}

func (p162NoopAcceptor) AcceptIntent(context.Context, dispatcher.AcceptIntentRequest) (dispatcher.IntentAcceptance, error) {
	return dispatcher.IntentAcceptance{}, errors.New("P162 local dispatcher should not receive remote work")
}

type p162RemoteCaller struct {
	now          func() time.Time
	jobs         map[string]*p162RemoteJob
	mutations    map[string]int
	statusReads  map[string]int
	commandReads map[string]int
	streamReads  map[string]int
	observations int
}

type p162RemoteJob struct {
	jobID, sessionID, commandID, environment, profile, script string
	statusFailure                                             bool
	preCommandLost                                            bool
	activeStatus                                              bool
	queueBlockedStatus                                        bool
	runningStatus                                             bool
}

func newP162RemoteCaller(now func() time.Time) *p162RemoteCaller {
	return &p162RemoteCaller{
		now: now, jobs: make(map[string]*p162RemoteJob), mutations: make(map[string]int),
		statusReads: make(map[string]int), commandReads: make(map[string]int), streamReads: make(map[string]int),
	}
}

func (c *p162RemoteCaller) Call(_ context.Context, frame sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
	switch frame.Operation {
	case sshbridge.OperationRunOrResumeJob:
		var payload struct {
			SessionID       string `json:"session_id"`
			CommandID       string `json:"command_id"`
			Environment     string `json:"environment"`
			Script          string `json:"script"`
			ExecutionTarget struct {
				Profile string `json:"profile"`
			} `json:"execution_target"`
		}
		if err := json.Unmarshal(frame.Payload, &payload); err != nil {
			return sshbridge.ReplyFrame{}, err
		}
		if frame.ResourceID == "" || payload.SessionID == "" || payload.CommandID == "" || payload.Environment == "" || payload.ExecutionTarget.Profile == "" {
			return sshbridge.ReplyFrame{}, errors.New("P162 invalid remote run mutation frame")
		}
		if _, exists := c.jobs[frame.ResourceID]; exists {
			return sshbridge.ReplyFrame{}, errors.New("P162 duplicate remote run mutation")
		}
		job := &p162RemoteJob{
			jobID: frame.ResourceID, sessionID: payload.SessionID, commandID: payload.CommandID,
			environment: payload.Environment, profile: payload.ExecutionTarget.Profile, script: payload.Script,
			statusFailure:  strings.Contains(payload.Script, "P162_STATUS_FAILURE"),
			preCommandLost: strings.Contains(payload.Script, "P162_PRECOMMAND_LOST"),
			activeStatus: strings.Contains(payload.Script, "P162_ACTIVE_STATUS") ||
				strings.Contains(payload.Script, "P3_QUEUE_BLOCKED_STATUS"),
			queueBlockedStatus: strings.Contains(payload.Script, "P3_QUEUE_BLOCKED_STATUS"),
		}
		c.jobs[job.jobID] = job
		c.mutations[job.jobID]++
		return p162Reply(frame.RequestID, "result", map[string]string{"job_id": job.jobID, "session_id": job.sessionID, "command_id": job.commandID}), nil
	case sshbridge.OperationGetJob:
		var payload struct {
			JobID string `json:"job_id"`
		}
		if err := json.Unmarshal(frame.Payload, &payload); err != nil {
			return sshbridge.ReplyFrame{}, err
		}
		job := c.jobs[payload.JobID]
		if job == nil {
			return sshbridge.ReplyFrame{}, errors.New("P162 unknown remote job status")
		}
		c.statusReads[job.jobID]++
		if job.statusFailure {
			return sshbridge.ReplyFrame{}, errors.New(p162Secret)
		}
		return p162Reply(frame.RequestID, "result", c.jobPayload(job)), nil
	case sshbridge.OperationGetCommand:
		var payload struct {
			CommandID string `json:"command_id"`
		}
		if err := json.Unmarshal(frame.Payload, &payload); err != nil {
			return sshbridge.ReplyFrame{}, err
		}
		job := c.jobForCommand(payload.CommandID)
		if job == nil || job.statusFailure || job.preCommandLost {
			return sshbridge.ReplyFrame{}, errors.New("P162 unknown remote command status")
		}
		c.commandReads[job.commandID]++
		return p162Reply(frame.RequestID, "result", c.commandPayload(job)), nil
	default:
		return sshbridge.ReplyFrame{}, errors.New("P162 unexpected remote bridge operation")
	}
}

func (c *p162RemoteCaller) Stream(_ context.Context, frame sshbridge.RequestFrame, receive func(sshbridge.ReplyFrame) error) error {
	var payload struct {
		CommandID     string `json:"command_id"`
		AfterSequence int64  `json:"after_sequence"`
	}
	if err := json.Unmarshal(frame.Payload, &payload); err != nil {
		return err
	}
	job := c.jobForCommand(payload.CommandID)
	if job == nil || job.statusFailure || job.preCommandLost {
		return errors.New("P162 unexpected remote event stream")
	}
	c.streamReads[job.commandID]++
	events := []sshbridge.ReplyFrame{
		p162EventReply(frame.RequestID, job.commandID, 1, "command_queued", c.observedAt(), nil),
		p162EventReply(frame.RequestID, job.commandID, 2, "command_started", c.observedAt(), nil),
		p162EventReply(frame.RequestID, job.commandID, 3, "stdout", c.observedAt(), []byte("P162_COMPLETE_OK\\n")),
		p162EventReply(frame.RequestID, job.commandID, 4, "command_succeeded", c.observedAt(), nil),
	}
	for _, event := range events {
		var envelope struct {
			Sequence int64 `json:"sequence"`
		}
		if err := json.Unmarshal(event.Payload, &envelope); err != nil {
			return err
		}
		if envelope.Sequence > payload.AfterSequence {
			if err := receive(event); err != nil {
				return err
			}
		}
	}
	return receive(p162Reply(frame.RequestID, "stream_end", map[string]int64{"last_sequence": 4}))
}

func (c *p162RemoteCaller) jobForCommand(commandID string) *p162RemoteJob {
	for _, job := range c.jobs {
		if job.commandID == commandID {
			return job
		}
	}
	return nil
}

func (c *p162RemoteCaller) jobPayload(job *p162RemoteJob) map[string]any {
	if job.preCommandLost {
		return map[string]any{
			"job_id": job.jobID, "session_id": job.sessionID, "command_id": job.commandID,
			"job_phase": string(store.JobPhaseLost), "output_complete": false, "output_truncated": false,
			"teardown_state": string(store.JobTeardownPending), "teardown_reason": "runtime_cleanup_unconfirmed",
			"execution_target": map[string]string{"kind": "remote", "profile": job.profile}, "authority": "remote",
			"controller":  map[string]string{"controller_type": "queued_mac", "controller_id": config.MacAccount},
			"environment": job.environment, "source": map[string]string{"mode": "empty"},
			"capabilities": map[string]any{"host_class": "Ubuntu Linux host", "isolation": "os-user", "effective_account": "ubuntu", "service_limits": map[string]any{"running_commands": 4}},
			"observed_at":  c.observedAt(),
		}
	}
	if job.activeStatus {
		state := domain.CommandStateQueued
		if job.runningStatus {
			state = domain.CommandStateRunning
		}
		payload := map[string]any{
			"job_id": job.jobID, "session_id": job.sessionID, "command_id": job.commandID,
			"job_phase": string(store.JobPhaseAwaitingCommand), "command_state": string(state),
			"output_complete": false, "output_truncated": false, "teardown_state": string(store.JobTeardownPending),
			"execution_target": map[string]string{"kind": "remote", "profile": job.profile}, "authority": "remote",
			"controller":  map[string]string{"controller_type": "queued_mac", "controller_id": config.MacAccount},
			"environment": job.environment, "source": map[string]string{"mode": "empty"},
			"capabilities": map[string]any{"host_class": "Ubuntu Linux host", "isolation": "os-user", "effective_account": "ubuntu", "service_limits": map[string]any{"running_commands": 4}},
			"observed_at":  c.observedAt(),
		}
		if job.queueBlockedStatus {
			payload["queue_blocked_reason"] = store.QueueBlockedReasonLostCapacityRecoveryPending
		}
		return payload
	}
	return map[string]any{
		"job_id": job.jobID, "session_id": job.sessionID, "command_id": job.commandID,
		"job_phase": string(store.JobPhaseComplete), "command_state": string(domain.CommandStateSucceeded),
		"exit_code": 0, "final_event_sequence": int64(4), "output_complete": true, "output_truncated": false,
		"teardown_state":   string(store.JobTeardownClosed),
		"execution_target": map[string]string{"kind": "remote", "profile": job.profile}, "authority": "remote",
		"controller":  map[string]string{"controller_type": "queued_mac", "controller_id": config.MacAccount},
		"environment": job.environment, "source": map[string]string{"mode": "empty"},
		"capabilities": map[string]any{"host_class": "Ubuntu Linux host", "isolation": "os-user", "effective_account": "ubuntu", "service_limits": map[string]any{"running_commands": 4}},
		"observed_at":  c.observedAt(),
	}
}

func (c *p162RemoteCaller) commandPayload(job *p162RemoteJob) map[string]any {
	digest := sha256.Sum256([]byte(job.script))
	return map[string]any{
		"command_id": job.commandID, "session_id": job.sessionID, "ordinal": 1,
		"command_state": string(domain.CommandStateSucceeded), "exit_code": 0, "final_event_sequence": int64(4),
		"output_complete": true, "output_truncated": false,
		"script_sha256": hex.EncodeToString(digest[:]), "script_byte_count": len(job.script),
		"execution_target": map[string]string{"kind": "remote", "profile": job.profile}, "authority": "remote",
		"controller":  map[string]string{"controller_type": "queued_mac", "controller_id": config.MacAccount},
		"environment": job.environment, "source": map[string]string{"mode": "empty"},
		"capabilities": map[string]any{"host_class": "Ubuntu Linux host", "isolation": "os-user", "effective_account": "ubuntu", "service_limits": map[string]any{"running_commands": 4}},
		"observed_at":  c.observedAt(),
	}
}

func (c *p162RemoteCaller) observedAt() time.Time {
	c.observations++
	return c.now().UTC().Add(time.Duration(c.observations) * time.Nanosecond)
}

func (c *p162RemoteCaller) mutationCount(jobID string) int   { return c.mutations[jobID] }
func (c *p162RemoteCaller) statusReadCount(jobID string) int { return c.statusReads[jobID] }

func (c *p162RemoteCaller) totalMutations() int {
	total := 0
	for _, count := range c.mutations {
		total += count
	}
	return total
}

func p162Reply(requestID, responseType string, value any) sshbridge.ReplyFrame {
	payload, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return sshbridge.ReplyFrame{ProtocolVersion: sshbridge.ProtocolVersion, RequestID: requestID, ResponseType: responseType, Payload: payload}
}

func p162EventReply(requestID, commandID string, sequence int64, eventType string, observedAt time.Time, output []byte) sshbridge.ReplyFrame {
	payload := map[string]any{"command_id": commandID, "sequence": sequence, "type": eventType, "timestamp": observedAt}
	if len(output) != 0 {
		payload["encoding"] = "base64"
		payload["data_base64"] = base64.StdEncoding.EncodeToString(output)
		payload["byte_count"] = len(output)
	}
	return p162Reply(requestID, "event", payload)
}

func p162Client(t *testing.T, root string) *mailboxclient.Client {
	t.Helper()
	client, err := mailboxclient.New(root)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func p162WriteRunRequest(t *testing.T, client *mailboxclient.Client, requestID, key, script string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"request_id": requestID, "idempotency_key": key, "operation": "run", "repository_alias": p162MailboxID, "script": script,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.WriteRequest(requestID, raw); err != nil {
		t.Fatalf("native marker-last request %s: %v", requestID, err)
	}
}

func p162ReadResponse(t *testing.T, client *mailboxclient.Client, requestID string) mailboxclient.Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	response, err := client.WaitResponse(ctx, requestID)
	if err != nil {
		t.Fatalf("read mailbox response %s: %v", requestID, err)
	}
	return response
}

func p162IntentForRequest(t *testing.T, authority *store.AuthorityStore, owner domain.ControllerIdentity, requestID string) store.LocalIntentRecord {
	t.Helper()
	record := p162Exchange(t, authority, requestID)
	var response mailboxclient.Response
	if err := json.Unmarshal(record.ResponseBytes, &response); err != nil || response.JobID == "" {
		t.Fatalf("mailbox request %s has no readable accepted run response: response=%q err=%v", requestID, record.ResponseBytes, err)
	}
	intent, err := authority.GetLocalIntentByResource(context.Background(), "run", response.JobID, owner)
	if err != nil {
		t.Fatalf("read local run intent for %s: %v", requestID, err)
	}
	return intent
}

func p162Exchange(t *testing.T, authority *store.AuthorityStore, requestID string) store.MailboxExchangeRecord {
	t.Helper()
	ref, err := store.NewMailboxExchangeRef(p162MailboxID, requestID)
	if err != nil {
		t.Fatal(err)
	}
	record, err := authority.GetMailboxExchangeInMailbox(context.Background(), ref)
	if err != nil {
		t.Fatalf("read mailbox exchange %s: %v", requestID, err)
	}
	return record
}

func p162ReadFile(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return contents
}

var _ dispatcher.RemoteCaller = (*p162RemoteCaller)(nil)
var _ dispatcher.RemoteEventStreamer = (*p162RemoteCaller)(nil)
