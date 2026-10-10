package runnerlocald

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
	"syscall"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	hostruntime "remote-session-runner/src/internal/runtime"
	"remote-session-runner/src/internal/store"
	"remote-session-runner/src/internal/testfixture"
)

const p136MacHostGate = "RSR_P136_MAC_HOST_GATE"

type p136MacRunResult struct {
	result execution.SubmitCommandResult
	err    error
}

type p136MacCancelResult struct {
	result execution.CancelCommandResult
	err    error
}

func TestP136MacFourSlotsHoldAcrossDelayedStopEOF(t *testing.T) {
	if os.Getenv(p136MacHostGate) != "1" {
		t.Skip("set RSR_P136_MAC_HOST_GATE=1 to run the actual Mac four-slot/delayed-EOF gate")
	}
	if runtime.GOOS != "darwin" {
		t.Fatalf("P136 Mac gate must run on macOS, got %s", runtime.GOOS)
	}
	current, err := user.Current()
	if err != nil || current.Username != "tomasz.walczuk" || current.Uid != "502" {
		t.Fatalf("P136 Mac account=%v err=%v, want tomasz.walczuk uid 502", current, err)
	}
	hostname, err := os.Hostname()
	if err != nil || hostname != "AMAK2KJ6X9JJJ" {
		t.Fatalf("P136 Mac host=%q err=%v, want AMAK2KJ6X9JJJ", hostname, err)
	}

	fixture := testfixture.New(t)
	if err := os.Chmod(fixture.Path(), 0o700); err != nil {
		t.Fatal(err)
	}
	workspaceRoot := filepath.Join(fixture.Path(), "workspaces")
	if err := os.Mkdir(workspaceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(fixture.Path(), "authority.db")
	db, err := store.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := store.NewAuthorityStore(db)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	target, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, domain.ControllerID("tomasz.walczuk"))
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	environment, err := domain.NewEnvironment(domain.EnvironmentSpec{
		Name: "mac-dev", HostClass: "macOS host", EffectiveAccount: "tomasz.walczuk",
		AllowedTargets: []domain.ExecutionTarget{target}, AllowedSourceModes: []domain.SourceMode{domain.SourceModeEmpty},
		AllowedControllers: []domain.ControllerIdentity{controller}, ServiceLimits: domain.DefaultServiceLimits(),
	})
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	service, runtimeAdapter, err := NewMacExecutionService(authority, hostruntime.MacRuntimeOptions{
		Account: "tomasz.walczuk", WorkspaceRoot: workspaceRoot, ShellPath: "/bin/bash",
	}, environment)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	ctx := context.Background()
	var sessions []domain.SessionID
	var runDone []chan p136MacRunResult
	var runFinished []bool
	cleanedSessions := make(map[domain.SessionID]bool)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		for _, sessionID := range sessions {
			if cleanedSessions[sessionID] {
				continue
			}
			session, readErr := authority.GetSession(cleanupCtx, sessionID)
			if readErr != nil {
				continue
			}
			commands, listErr := authority.ListSessionCommands(cleanupCtx, sessionID)
			if listErr == nil {
				for _, command := range commands {
					if command.State == domain.CommandStateRunning || command.State == domain.CommandStateCancelling {
						_, _ = runtimeAdapter.CancelCommand(cleanupCtx, execution.RuntimeCommandRequest{Session: session, Command: command})
					}
				}
			}
		}
		for index, done := range runDone {
			if runFinished[index] {
				continue
			}
			select {
			case <-done:
				runFinished[index] = true
			case <-cleanupCtx.Done():
				t.Errorf("P136 Mac command worker did not exit before fixture cleanup: %v", cleanupCtx.Err())
			}
		}
		for _, sessionID := range sessions {
			if cleanedSessions[sessionID] {
				continue
			}
			session, readErr := authority.GetSession(cleanupCtx, sessionID)
			if readErr != nil {
				continue
			}
			if _, stopErr := runtimeAdapter.StopSession(cleanupCtx, session); stopErr != nil {
				t.Errorf("clean P136 Mac runtime for %s: %v", sessionID, stopErr)
			}
		}
		if err := db.Close(); err != nil {
			t.Errorf("close P136 Mac test database: %v", err)
		}
	})

	const sessionCount = 5
	for i := 0; i < sessionCount; i++ {
		sessionID := domain.SessionID(fmt.Sprintf("p136-mac-session-%d", i))
		hash := p136MacHash(t, "create_session", map[string]any{
			"environment": "mac-dev", "execution_target": map[string]string{"kind": "local", "profile": "mac-workstation"},
			"session_id": string(sessionID), "source": map[string]string{"mode": "empty"},
		})
		created, err := service.CreateSession(ctx, execution.CreateSessionRequest{
			SessionID: sessionID, IdempotencyKey: "p136-mac-create-" + strconv.Itoa(i), RequestHash: hash,
			Environment: "mac-dev", Target: target, Controller: controller, Source: domain.NewEmptySource(),
			IdempotencyRetention: store.DefaultSessionIdempotencyRetention,
		})
		if err != nil || created.Session.State != domain.SessionStateReady {
			t.Fatalf("create P136 Mac session %s = %+v err=%v", sessionID, created.Session, err)
		}
		sessions = append(sessions, sessionID)
	}

	commandIDs := make([]domain.CommandID, 4)
	childPIDFile := filepath.Join(fixture.Path(), "delayed-child.pid")
	for i := 0; i < 4; i++ {
		commandID := domain.CommandID(fmt.Sprintf("p136-mac-command-%d", i))
		commandIDs[i] = commandID
		script := "/bin/sleep 10\n"
		if i == 0 {
			script = "trap '' INT\n( exec /bin/sleep 30 ) &\nprintf '%s\\n' \"$!\" > " + p136MacShellQuote(childPIDFile) + "\nwait\n"
		}
		accepted, err := service.AcceptCommand(ctx, p136MacSubmitRequest(t, sessions[i], commandID, controller, script))
		if err != nil || accepted.Command.State != domain.CommandStateQueued {
			t.Fatalf("accept P136 Mac command %s = %+v err=%v", commandID, accepted.Command, err)
		}
		done := make(chan p136MacRunResult, 1)
		runDone = append(runDone, done)
		runFinished = append(runFinished, false)
		go func(id domain.CommandID, resultChannel chan p136MacRunResult) {
			result, runErr := service.ResumeCommand(context.Background(), id, controller)
			resultChannel <- p136MacRunResult{result: result, err: runErr}
		}(commandID, done)
		p135MacWaitCommandState(t, authority, commandID, domain.CommandStateRunning)
		if i == 0 {
			p136MacWaitFile(t, childPIDFile)
		}
	}
	if live, err := authority.CountLiveCommandSlots(ctx); err != nil || live != 4 {
		t.Fatalf("P136 Mac live command slots=%d err=%v, want four", live, err)
	}
	childPID := p136MacReadPID(t, childPIDFile)
	childOwnership := p135MacReadOwnership(t, workspaceRoot, string(sessions[0]))
	if childPID == childOwnership.PID || syscall.Kill(childPID, 0) != nil {
		t.Fatalf("delayed Mac output-pipe child pid=%d is not live and distinct from Bash pid=%d", childPID, childOwnership.PID)
	}

	fifthID := domain.CommandID("p136-mac-command-4")
	fifthMarker := filepath.Join(fixture.Path(), "fifth-started")
	fifth, err := service.AcceptCommand(ctx, p136MacSubmitRequest(t, sessions[4], fifthID, controller, "printf started > "+p136MacShellQuote(fifthMarker)+"\n"))
	if err != nil || fifth.Command.State != domain.CommandStateQueued {
		t.Fatalf("accept fifth P136 Mac command = %+v err=%v, want queued", fifth.Command, err)
	}
	queued, err := service.ResumeCommand(ctx, fifthID, controller)
	if err != nil || queued.Command.State != domain.CommandStateQueued {
		t.Fatalf("fifth P136 Mac start = %+v err=%v, want it to wait for a free host slot", queued.Command, err)
	}
	if _, err := os.Stat(fifthMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fifth P136 Mac command ran while four slots were occupied: stat error=%v", err)
	}

	cancelRequest := p136MacCancelRequest(t, commandIDs[0], controller)
	stopDone := make(chan p136MacCancelResult, 1)
	go func() {
		cancelled, cancelErr := service.CancelCommand(context.Background(), cancelRequest)
		stopDone <- p136MacCancelResult{result: cancelled, err: cancelErr}
	}()
	p135MacWaitCommandState(t, authority, commandIDs[0], domain.CommandStateCancelling)
	time.Sleep(150 * time.Millisecond)
	if syscall.Kill(childPID, 0) != nil {
		t.Fatalf("delayed Mac child %d exited before stop grace elapsed", childPID)
	}
	if live, err := authority.CountLiveCommandSlots(ctx); err != nil || live != 4 {
		t.Fatalf("P136 Mac slots during delayed stop=%d err=%v, want all four retained", live, err)
	}
	slot, err := authority.GetCommandSlot(ctx, commandIDs[0])
	if err != nil || slot.StopConfirmedAt != nil || slot.ReleasedAt != nil {
		t.Fatalf("P136 Mac interrupted command slot=%+v err=%v, want unreleased until stop/EOF confirmation", slot, err)
	}
	select {
	case result := <-runDone[0]:
		t.Fatalf("P136 Mac command crossed its delayed EOF boundary early: %+v", result)
	default:
	}
	if record, err := authority.GetCommand(ctx, fifthID); err != nil || record.State != domain.CommandStateQueued {
		t.Fatalf("fifth P136 Mac command changed while stop was delayed: %+v err=%v", record, err)
	}
	if _, err := os.Stat(fifthMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fifth P136 Mac command ran during delayed stop: stat error=%v", err)
	}

	select {
	case cancelled := <-stopDone:
		if cancelled.result.Command.State != domain.CommandStateLost {
			t.Fatalf("P136 Mac cancellation=%+v err=%v, want a lost command after unconfirmed stop", cancelled.result, cancelled.err)
		}
		if cancelled.err != nil && !errors.Is(cancelled.err, execution.ErrStopUnconfirmed) {
			t.Fatalf("P136 Mac cancellation error=%v, want only the expected unconfirmed-stop outcome", cancelled.err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("P136 Mac cancellation did not finish after its bounded grace")
	}
	select {
	case result := <-runDone[0]:
		runFinished[0] = true
		if result.err == nil {
			t.Fatalf("P136 Mac interrupted command result=%+v, want shell/output-boundary loss", result.result)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("P136 Mac command worker did not observe delayed control/EOF loss")
	}
	p135MacWaitCommandState(t, authority, commandIDs[0], domain.CommandStateLost)
	p136MacAssertLostOutcome(t, authority, commandIDs[0])
	if live, err := authority.CountLiveCommandSlots(ctx); err != nil || live != 4 {
		t.Fatalf("P136 Mac live slots after unconfirmed stop=%d err=%v, want four including residual reservation", live, err)
	}
	slot, err = authority.GetCommandSlot(ctx, commandIDs[0])
	if err != nil || slot.StopConfirmedAt != nil || slot.ReleasedAt != nil {
		t.Fatalf("P136 Mac residual command slot=%+v err=%v, want durably retained", slot, err)
	}

	for i := 1; i < 4; i++ {
		p135MacWaitCommandState(t, authority, commandIDs[i], domain.CommandStateSucceeded)
		select {
		case <-runDone[i]:
			runFinished[i] = true
		case <-time.After(3 * time.Second):
			t.Fatalf("P136 Mac command %s did not finish", commandIDs[i])
		}
	}
	if live, err := authority.CountLiveCommandSlots(ctx); err != nil || live != 1 {
		t.Fatalf("P136 Mac live slots after clean completions=%d err=%v, want only the unresolved residual slot", live, err)
	}
	if record, err := authority.GetCommand(ctx, fifthID); err != nil || record.State != domain.CommandStateQueued {
		t.Fatalf("fifth P136 Mac command state=%+v err=%v, want queued behind retained capacity", record, err)
	}
	if _, err := os.Stat(fifthMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fifth P136 Mac command ran in residual capacity test: stat error=%v", err)
	}

	fifthSession, err := authority.GetSession(ctx, sessions[4])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CloseSession(ctx, p136MacCloseRequest(t, fifthSession.SessionID, controller)); err != nil {
		t.Fatalf("close session with queued fifth P136 Mac command: %v", err)
	}
	if record, err := authority.GetCommand(ctx, fifthID); err != nil || record.State != domain.CommandStateCancelled {
		t.Fatalf("queued fifth P136 Mac command after close=%+v err=%v, want cancelled", record, err)
	}
	_, _ = service.ResumeCommand(ctx, fifthID, controller)
	if record, err := authority.GetCommand(ctx, fifthID); err != nil || record.State != domain.CommandStateCancelled {
		t.Fatalf("closed fifth P136 Mac command changed after a later resume attempt=%+v err=%v", record, err)
	}
	if _, err := os.Stat(fifthMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("closed fifth P136 Mac command ran after capacity became available: stat error=%v", err)
	}
	cleanedSessions[fifthSession.SessionID] = true
	for i := 1; i < 4; i++ {
		session, err := authority.GetSession(ctx, sessions[i])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.CloseSession(ctx, p136MacCloseRequest(t, session.SessionID, controller)); err != nil {
			t.Fatalf("close P136 Mac session %s: %v", session.SessionID, err)
		}
		cleanedSessions[session.SessionID] = true
	}
	lostSession, err := authority.GetSession(ctx, sessions[0])
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := runtimeAdapter.StopSession(ctx, lostSession)
	if err != nil || !confirmed {
		t.Fatalf("confirm P136 Mac residual runtime cleanup=%t err=%v", confirmed, err)
	}
	cleanedSessions[lostSession.SessionID] = true
	if err := authority.ConfirmCommandSlotRelease(ctx, commandIDs[0]); err != nil {
		t.Fatalf("release P136 Mac slot after confirmed runtime cleanup: %v", err)
	}
	if err := authority.ConfirmSessionCleanup(ctx, sessions[0]); err != nil {
		t.Fatalf("release P136 Mac session reservation after confirmed cleanup: %v", err)
	}
	if live, err := authority.CountLiveCommandSlots(ctx); err != nil || live != 0 {
		t.Fatalf("P136 Mac live slots after fixture cleanup=%d err=%v, want zero", live, err)
	}
	if reservations, err := authority.CountLiveSessionReservations(ctx); err != nil || reservations != 0 {
		t.Fatalf("P136 Mac session reservations after fixture cleanup=%d err=%v, want zero", reservations, err)
	}
	t.Logf("machine=%s os=macOS account=%s: four live commands occupied all slots; fifth stayed queued; ignored-SIGINT child held output EOF through stop grace; lost command retained its slot until process-group cleanup was confirmed", hostname, current.Username)
}

func p136MacAssertLostOutcome(t *testing.T, authority *store.AuthorityStore, commandID domain.CommandID) {
	t.Helper()
	command, err := authority.GetCommand(context.Background(), commandID)
	if err != nil {
		t.Fatal(err)
	}
	if command.State != domain.CommandStateLost || command.OutputComplete || command.FinalEventSequence == nil {
		t.Fatalf("P136 Mac lost command=%+v, want incomplete output and a final event sequence", command)
	}
	events, err := authority.ListCommandEvents(context.Background(), commandID)
	if err != nil {
		t.Fatal(err)
	}
	terminal := 0
	for _, event := range events {
		switch event.Type {
		case "command_succeeded", "command_failed", "command_cancelled", "command_timed_out", "command_rejected", "command_lost":
			terminal++
		}
	}
	if terminal != 1 || len(events) == 0 || events[len(events)-1].Type != "command_lost" || events[len(events)-1].Sequence != *command.FinalEventSequence {
		t.Fatalf("P136 Mac terminal events=%+v final_sequence=%v, want exactly one terminal command_lost event", events, command.FinalEventSequence)
	}
}

func p136MacSubmitRequest(t *testing.T, sessionID domain.SessionID, commandID domain.CommandID, controller domain.ControllerIdentity, script string) execution.SubmitCommandRequest {
	t.Helper()
	hash := p136MacHash(t, "submit_command", map[string]string{"script": script})
	return execution.SubmitCommandRequest{
		CommandID: commandID, SessionID: sessionID, Controller: controller,
		IdempotencyKey: "key-" + string(commandID), RequestHash: hash, Script: script,
		IdempotencyRetention: store.DefaultSessionIdempotencyRetention,
	}
}

func p136MacCancelRequest(t *testing.T, commandID domain.CommandID, controller domain.ControllerIdentity) execution.CancelCommandRequest {
	t.Helper()
	hash := p136MacHash(t, "cancel_command", map[string]string{"command_id": string(commandID)})
	return execution.CancelCommandRequest{CommandID: commandID, Controller: controller, IdempotencyKey: "cancel-" + string(commandID), RequestHash: hash, IdempotencyRetention: store.DefaultSessionIdempotencyRetention}
}

func p136MacCloseRequest(t *testing.T, sessionID domain.SessionID, controller domain.ControllerIdentity) execution.CloseSessionRequest {
	t.Helper()
	hash := p136MacHash(t, "close_session", map[string]string{"policy": "p136-cleanup", "session_id": string(sessionID)})
	return execution.CloseSessionRequest{SessionID: sessionID, Controller: controller, IdempotencyKey: "close-" + string(sessionID), RequestHash: hash, Policy: "p136-cleanup", IdempotencyRetention: store.DefaultSessionIdempotencyRetention}
}

func p136MacHash(t *testing.T, operation string, payload any) domain.CanonicalHash {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON(operation, encoded, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func p136MacWaitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("P136 Mac child did not publish PID marker %s", path)
}

func p136MacReadPID(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		t.Fatalf("invalid P136 Mac child PID %q: %v", data, err)
	}
	return pid
}

func p136MacShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
