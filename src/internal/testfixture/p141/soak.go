package p141fixture

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	"remote-session-runner/src/internal/store"
)

const (
	activeSessionLimit  = 20
	runningCommandLimit = 4
	commandOutputBytes  = 1_310_720
	commandOutputScript = "sleep 8\n/usr/bin/head -c 1310720 /dev/zero\n"
	commandHoldScript   = "/bin/sleep 22\n"
)

type RuntimeController interface {
	CancelCommand(context.Context, execution.RuntimeCommandRequest) (execution.RuntimeCommandStopResult, error)
	StopSession(context.Context, store.SessionRecord) (bool, error)
}

type Options struct {
	Service       *execution.Service
	Authority     *store.AuthorityStore
	Runtime       RuntimeController
	Environment   domain.Environment
	Target        domain.ExecutionTarget
	Controller    domain.ControllerIdentity
	WorkspaceRoot string
	ExpectedOS    string
	ExpectedUser  string
	ExpectedUID   string
	ExpectedHost  string
}

type commandWorker struct {
	commandID domain.CommandID
	sessionID domain.SessionID
	done      chan error
	finished  bool
}

// RunReferenceHostSoak exercises real host process adapters with all fixture
// data under WorkspaceRoot and the caller's isolated SQLite authority.
func RunReferenceHostSoak(t *testing.T, options Options) {
	t.Helper()
	if options.Service == nil || options.Authority == nil || options.Runtime == nil || options.WorkspaceRoot == "" {
		t.Fatal("P141 requires a service, authority, runtime controller, and isolated workspace root")
	}
	if runtime.GOOS != options.ExpectedOS {
		t.Fatalf("P141 host OS=%s, want %s", runtime.GOOS, options.ExpectedOS)
	}
	current, err := osuserCurrent()
	if err != nil || current.Username != options.ExpectedUser || current.Uid != options.ExpectedUID {
		t.Fatalf("P141 host account=%+v err=%v, want %s uid %s", current, err, options.ExpectedUser, options.ExpectedUID)
	}
	host, err := os.Hostname()
	if err != nil || host != options.ExpectedHost {
		t.Fatalf("P141 host name=%q err=%v, want %s", host, err, options.ExpectedHost)
	}
	limits := options.Environment.ServiceLimits()
	if limits.ActiveSessionsPerHost != activeSessionLimit || limits.RunningCommandsPerHost != runningCommandLimit || limits.SubscriberBufferBytes != 1<<20 {
		t.Fatalf("P141 environment limits=%+v, want 20 sessions, 4 commands, and 1 MiB subscriber payload", limits)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var sessions []domain.SessionID
	var workers []*commandWorker
	cleanedSessions := make(map[domain.SessionID]bool)
	cleanup := func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		cancelErrors := make(map[domain.SessionID][]error)
		for _, sessionID := range sessions {
			session, readErr := options.Authority.GetSession(cleanupCtx, sessionID)
			if readErr != nil {
				t.Errorf("read fixture session %s during cleanup: %v", sessionID, readErr)
				continue
			}
			commands, listErr := options.Authority.ListSessionCommands(cleanupCtx, sessionID)
			if listErr != nil {
				t.Errorf("list fixture commands for session %s during cleanup: %v", sessionID, listErr)
				continue
			}
			for _, command := range commands {
				if command.State != domain.CommandStateRunning && command.State != domain.CommandStateCancelling {
					continue
				}
				if _, cancelErr := options.Runtime.CancelCommand(cleanupCtx, execution.RuntimeCommandRequest{Session: session, Command: command}); cancelErr != nil {
					cancelErrors[sessionID] = append(cancelErrors[sessionID], fmt.Errorf("command %s: %w", command.CommandID, cancelErr))
				}
			}
		}
		for _, worker := range workers {
			if worker.finished {
				continue
			}
			select {
			case <-worker.done:
				worker.finished = true
			case <-cleanupCtx.Done():
				t.Errorf("P141 command worker %s did not exit before cleanup: %v", worker.commandID, cleanupCtx.Err())
			}
		}
		for _, sessionID := range sessions {
			if cleanedSessions[sessionID] {
				continue
			}
			session, readErr := options.Authority.GetSession(cleanupCtx, sessionID)
			if readErr != nil {
				continue
			}
			confirmed, stopErr := options.Runtime.StopSession(cleanupCtx, session)
			if stopErr != nil || !confirmed {
				t.Errorf("stop fixture session %s during cleanup: cancel errors=%v; process-group confirmed=%t err=%v", sessionID, cancelErrors[sessionID], confirmed, stopErr)
			} else if len(cancelErrors[sessionID]) != 0 {
				t.Logf("fixture session %s reported command cancellation errors %v; StopSession confirmed process-group cleanup", sessionID, cancelErrors[sessionID])
			}
		}
		ownershipDirectory := filepath.Join(options.WorkspaceRoot, ".runner-runtime-ownership")
		markers, markerErr := os.ReadDir(ownershipDirectory)
		if markerErr != nil && !errors.Is(markerErr, os.ErrNotExist) {
			t.Errorf("read fixture ownership markers after cleanup: %v", markerErr)
		} else if len(markers) != 0 {
			t.Errorf("P141 cleanup left %d runtime ownership markers", len(markers))
		} else if len(sessions) != 0 {
			t.Logf("P141 cleanup confirmed runtime stop for %d sessions and zero ownership markers", len(sessions))
		}
	}
	defer cleanup()

	sampler, err := startMemorySampler(options.WorkspaceRoot)
	if err != nil {
		t.Fatalf("start P141 fixture memory sampler: %v", err)
	}
	samplerStopped := false
	defer func() {
		if !samplerStopped {
			_, _ = sampler.Stop()
		}
	}()

	stamp := strconv.FormatInt(time.Now().UnixNano(), 10)
	prefix := "p141-" + stamp
	for index := 0; index < activeSessionLimit; index++ {
		sessionID := domain.SessionID(fmt.Sprintf("%s-s%02d", prefix, index))
		created, createErr := options.Service.CreateSession(ctx, createRequest(t, options, sessionID, "create-"+string(sessionID)))
		if createErr != nil || created.Session.State != domain.SessionStateReady {
			t.Fatalf("create P141 session %s = %+v err=%v", sessionID, created.Session, createErr)
		}
		sessions = append(sessions, sessionID)
	}
	if reservations, countErr := options.Authority.CountLiveSessionReservations(ctx); countErr != nil || reservations != activeSessionLimit {
		t.Fatalf("P141 live session reservations=%d err=%v, want %d", reservations, countErr, activeSessionLimit)
	}
	overLimitID := domain.SessionID(prefix + "-s20")
	if _, createErr := options.Service.CreateSession(ctx, createRequest(t, options, overLimitID, "create-"+string(overLimitID))); !errors.Is(createErr, store.ErrSessionCapacityExceeded) {
		t.Fatalf("21st P141 session create error=%v, want ErrSessionCapacityExceeded", createErr)
	}
	if reservations, countErr := options.Authority.CountLiveSessionReservations(ctx); countErr != nil || reservations != activeSessionLimit {
		t.Fatalf("P141 reservations after rejected 21st session=%d err=%v, want unchanged at %d", reservations, countErr, activeSessionLimit)
	}

	for index := 0; index < runningCommandLimit; index++ {
		commandID := domain.CommandID(fmt.Sprintf("%s-c%02d", prefix, index))
		script := commandHoldScript
		if index == 0 {
			script = commandOutputScript
		}
		worker := &commandWorker{commandID: commandID, sessionID: sessions[index], done: make(chan error, 1)}
		workers = append(workers, worker)
		if _, submitErr := options.Service.AcceptCommand(ctx, submitRequest(t, options, worker, script)); submitErr != nil {
			t.Fatalf("accept P141 command %s: %v", commandID, submitErr)
		}
		go func(current *commandWorker) {
			_, runErr := options.Service.ResumeCommand(context.Background(), current.commandID, options.Controller)
			current.done <- runErr
		}(worker)
		waitCommandState(t, ctx, options.Authority, commandID, domain.CommandStateRunning)
	}
	if slots, countErr := options.Authority.CountLiveCommandSlots(ctx); countErr != nil || slots != runningCommandLimit {
		t.Fatalf("P141 occupied command slots=%d err=%v, want %d", slots, countErr, runningCommandLimit)
	}

	marker := filepath.Join(filepath.Dir(options.WorkspaceRoot), "p141-fifth-command-ran")
	fifth := &commandWorker{commandID: domain.CommandID(prefix + "-c04"), sessionID: sessions[4]}
	if _, submitErr := options.Service.AcceptCommand(ctx, submitRequest(t, options, fifth, "printf ran > "+shellQuote(marker)+"\n")); submitErr != nil {
		t.Fatalf("accept fifth P141 command: %v", submitErr)
	}
	queued, err := options.Service.ResumeCommand(ctx, fifth.commandID, options.Controller)
	if err != nil || queued.Command.State != domain.CommandStateQueued {
		t.Fatalf("fifth command while four slots are occupied=%+v err=%v, want queued", queued.Command, err)
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("fifth P141 command ran before a slot was free: stat error=%v", statErr)
	}

	commandID := workers[0].commandID
	slow, err := options.Service.SubscribeCommandEvents(ctx, commandID, options.Controller, 0, 256)
	if err != nil {
		t.Fatalf("subscribe P141 slow consumer: %v", err)
	}
	defer slow.Close()
	if slow.BufferedPayloadBytes() != 0 {
		t.Fatalf("P141 subscriber began with %d buffered output bytes, want zero", slow.BufferedPayloadBytes())
	}
	// Hold all four live commands briefly before the measured output starts.
	time.Sleep(3 * time.Second)
	if slots, countErr := options.Authority.CountLiveCommandSlots(ctx); countErr != nil || slots != runningCommandLimit {
		t.Fatalf("P141 command slots after bounded hold=%d err=%v, want %d", slots, countErr, runningCommandLimit)
	}

	select {
	case runErr := <-workers[0].done:
		workers[0].finished = true
		if runErr != nil {
			t.Fatalf("complete P141 slow-subscriber command: %v", runErr)
		}
	case <-ctx.Done():
		t.Fatalf("P141 slow-subscriber command did not complete: %v", ctx.Err())
	case <-time.After(45 * time.Second):
		t.Fatal("P141 slow-subscriber command exceeded 45 seconds")
	}
	completed, err := options.Authority.GetCommand(ctx, commandID)
	if err != nil || completed.State != domain.CommandStateSucceeded || !completed.OutputComplete || completed.OutputTruncated {
		t.Fatalf("P141 output command=%+v err=%v, want complete success without truncation", completed, err)
	}
	events, err := options.Authority.ListCommandEvents(ctx, commandID)
	if err != nil {
		t.Fatal(err)
	}
	var persistedOutput int64
	for _, event := range events {
		if event.Type == "stdout" || event.Type == "stderr" {
			persistedOutput += event.ByteCount
		}
	}
	if persistedOutput != commandOutputBytes {
		t.Fatalf("P141 persisted command output=%d bytes, want %d", persistedOutput, commandOutputBytes)
	}
	if buffered := slow.BufferedPayloadBytes(); buffered > limits.SubscriberBufferBytes {
		t.Fatalf("P141 slow subscriber retained %d payload bytes, limit %d", buffered, limits.SubscriberBufferBytes)
	}
	slowPeakBytes := slow.BufferedPayloadBytes()
	firstCursor := slow.LastSequence()
	if firstCursor <= 0 || completed.FinalEventSequence == nil || firstCursor >= *completed.FinalEventSequence {
		t.Fatalf("P141 overflow cursor=%d terminal=%v, want a resumable prefix before terminal", firstCursor, completed.FinalEventSequence)
	}
	var firstPrefix []store.CommandEventRecord
	for event := range slow.Events() {
		firstPrefix = append(firstPrefix, event)
		slow.Acknowledge(event)
	}
	if len(firstPrefix) == 0 || slow.BufferedPayloadBytes() != 0 {
		t.Fatalf("P141 slow prefix events=%d buffered after drain=%d, want a prefix and zero bytes", len(firstPrefix), slow.BufferedPayloadBytes())
	}
	select {
	case overflow, open := <-slow.Errors():
		if !open || !errors.Is(overflow, store.ErrSubscriberOverflow) {
			t.Fatalf("P141 slow subscriber error=%v open=%t, want ErrSubscriberOverflow", overflow, open)
		}
	default:
		t.Fatal("P141 slow subscriber did not report bounded overflow")
	}

	resumed, err := options.Service.SubscribeCommandEvents(ctx, commandID, options.Controller, firstCursor, 256)
	if err != nil {
		t.Fatalf("resume P141 event stream from cursor %d: %v", firstCursor, err)
	}
	var resumedCount int
	expectedSequence := firstCursor + 1
	for event := range resumed.Events() {
		if event.Sequence != expectedSequence {
			t.Fatalf("P141 resumed event sequence=%d after cursor=%d and prior count=%d", event.Sequence, firstCursor, resumedCount)
		}
		expectedSequence++
		resumedCount++
		resumed.Acknowledge(event)
		if event.Type == "command_succeeded" || event.Type == "command_failed" || event.Type == "command_cancelled" || event.Type == "command_timed_out" || event.Type == "command_rejected" || event.Type == "command_lost" {
			break
		}
	}
	resumed.Close()
	if resumedCount == 0 || firstCursor+int64(resumedCount) != *completed.FinalEventSequence {
		t.Fatalf("P141 resumed %d events from %d through %d, want terminal cursor %d", resumedCount, firstCursor, firstCursor+int64(resumedCount), *completed.FinalEventSequence)
	}

	// Keep the remaining three live command slots and all twenty sessions
	// occupied long enough to sample a stable bounded-load interval.
	time.Sleep(5 * time.Second)
	stats, err := sampler.Stop()
	samplerStopped = true
	if err != nil {
		t.Fatalf("sample P141 reference-host memory: %v", err)
	}
	if stats.Samples == 0 || stats.PeakFixtureRSSBytes <= 0 || stats.BaselineAvailableBytes <= 0 {
		t.Fatalf("incomplete P141 memory measurements: %+v", stats)
	}
	if stats.PeakFixtureRSSBytes > stats.BaselineAvailableBytes {
		t.Fatalf("P141 fixture peak RSS %d exceeds pre-test available memory %d", stats.PeakFixtureRSSBytes, stats.BaselineAvailableBytes)
	}
	t.Logf("machine=%s os=%s account=%s; sessions=%d; simultaneous_command_slots=%d; subscriber_payload_limit_bytes=%d; unread_subscriber_peak_bytes=%d; output_persisted_bytes=%d; cursor_before_overflow=%d; terminal_cursor=%d; memory_samples=%d; baseline_available_bytes=%d; peak_fixture_rss_bytes=%d; available_after_bytes=%d",
		host, runtime.GOOS, current.Username, activeSessionLimit, runningCommandLimit, limits.SubscriberBufferBytes,
		slowPeakBytes, persistedOutput, firstCursor, *completed.FinalEventSequence,
		stats.Samples, stats.BaselineAvailableBytes, stats.PeakFixtureRSSBytes, stats.AvailableAfterBytes)

	// Close each authority session through the service so runtime teardown and
	// both durable capacity reservations are confirmed before the fixture exits.
	if _, closeErr := options.Service.CloseSession(ctx, closeRequest(t, options, sessions[4], prefix)); closeErr != nil {
		t.Fatalf("close P141 session holding queued fifth command: %v", closeErr)
	}
	cleanedSessions[sessions[4]] = true
	for _, worker := range workers[1:] {
		if worker.finished {
			continue
		}
		select {
		case runErr := <-worker.done:
			worker.finished = true
			if runErr != nil {
				t.Errorf("P141 command %s worker result: %v", worker.commandID, runErr)
			}
		case <-ctx.Done():
			t.Fatalf("P141 command %s did not complete: %v", worker.commandID, ctx.Err())
		case <-time.After(15 * time.Second):
			t.Fatalf("P141 command %s did not complete within 15 seconds after the bounded-load interval", worker.commandID)
		}
	}
	for _, sessionID := range sessions {
		if cleanedSessions[sessionID] {
			continue
		}
		if _, closeErr := options.Service.CloseSession(ctx, closeRequest(t, options, sessionID, prefix)); closeErr != nil {
			t.Fatalf("close P141 session %s: %v", sessionID, closeErr)
		}
		cleanedSessions[sessionID] = true
	}
	if slots, countErr := options.Authority.CountLiveCommandSlots(ctx); countErr != nil || slots != 0 {
		t.Fatalf("P141 live command slots after cleanup=%d err=%v, want zero", slots, countErr)
	}
	if reservations, countErr := options.Authority.CountLiveSessionReservations(ctx); countErr != nil || reservations != 0 {
		t.Fatalf("P141 session reservations after cleanup=%d err=%v, want zero", reservations, countErr)
	}
	if markers, readErr := os.ReadDir(filepath.Join(options.WorkspaceRoot, ".runner-runtime-ownership")); readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		t.Fatalf("read P141 ownership markers after teardown: %v", readErr)
	} else if len(markers) != 0 {
		t.Fatalf("P141 left %d runtime ownership markers after teardown", len(markers))
	}
}

func createRequest(t testing.TB, options Options, sessionID domain.SessionID, key string) execution.CreateSessionRequest {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"environment":      options.Environment.Name(),
		"execution_target": map[string]string{"kind": string(options.Target.Kind()), "profile": options.Target.Profile()},
		"session_id":       string(sessionID), "source": map[string]string{"mode": "empty"},
	})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("create_session", encoded, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return execution.CreateSessionRequest{
		SessionID: sessionID, IdempotencyKey: key, RequestHash: hash,
		Environment: options.Environment.Name(), Target: options.Target, Controller: options.Controller,
		Source: domain.NewEmptySource(), MaxActiveSessions: activeSessionLimit,
		IdempotencyRetention: store.DefaultSessionIdempotencyRetention,
	}
}

func submitRequest(t testing.TB, options Options, worker *commandWorker, script string) execution.SubmitCommandRequest {
	t.Helper()
	encoded, err := json.Marshal(map[string]string{"script": script, "session_id": string(worker.sessionID)})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("submit_command", encoded, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return execution.SubmitCommandRequest{
		CommandID: worker.commandID, SessionID: worker.sessionID, Controller: options.Controller,
		IdempotencyKey: "submit-" + string(worker.commandID), RequestHash: hash, Script: script,
		IdempotencyRetention: store.DefaultSessionIdempotencyRetention,
	}
}

func closeRequest(t testing.TB, options Options, sessionID domain.SessionID, prefix string) execution.CloseSessionRequest {
	t.Helper()
	policy := "p141-cleanup"
	encoded, err := json.Marshal(map[string]string{"policy": policy, "session_id": string(sessionID)})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("close_session", encoded, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return execution.CloseSessionRequest{
		SessionID: sessionID, Controller: options.Controller, IdempotencyKey: prefix + "-close-" + string(sessionID),
		RequestHash: hash, Policy: policy, IdempotencyRetention: store.DefaultSessionIdempotencyRetention,
	}
}

func waitCommandState(t testing.TB, ctx context.Context, authority *store.AuthorityStore, id domain.CommandID, state domain.CommandState) {
	t.Helper()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		command, err := authority.GetCommand(ctx, id)
		if err == nil && command.State == state {
			return
		}
		if err != nil && !errors.Is(err, store.ErrCommandNotFound) {
			t.Fatalf("read P141 command %s: %v", id, err)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for P141 command %s state %s: %v", id, state, ctx.Err())
		case <-deadline.C:
			t.Fatalf("P141 command %s did not reach %s", id, state)
		case <-ticker.C:
		}
	}
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

type userIdentity struct {
	Username string
	Uid      string
}

func osuserCurrent() (userIdentity, error) {
	current, err := user.Current()
	if err != nil {
		return userIdentity{}, err
	}
	return userIdentity{Username: current.Username, Uid: current.Uid}, nil
}
