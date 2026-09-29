package runnerd

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

const p136LinuxHostGate = "RSR_P136_LINUX_HOST_GATE"

type p136LinuxRunResult struct {
	result execution.SubmitCommandResult
	err    error
}

type p136LinuxStopResult struct {
	result execution.RuntimeCommandStopResult
	err    error
}

func TestP136UbuntuFourSlotsHoldAcrossDelayedStopEOF(t *testing.T) {
	if os.Getenv(p136LinuxHostGate) != "1" {
		t.Skip("set RSR_P136_LINUX_HOST_GATE=1 to run the actual Ubuntu four-slot/delayed-EOF gate")
	}
	if runtime.GOOS != "linux" {
		t.Fatalf("P136 Ubuntu gate must run on Linux, got %s", runtime.GOOS)
	}
	current, err := user.Current()
	if err != nil || current.Username != "ubuntu" || current.Uid != "1001" {
		t.Fatalf("P136 Linux account=%v err=%v, want ubuntu uid 1001", current, err)
	}
	hostname, err := os.Hostname()
	if err != nil || hostname != "oracle-yuta-konopka-ubuntu-micro-02" {
		t.Fatalf("P136 Linux host=%q err=%v, want oracle-yuta-konopka-ubuntu-micro-02", hostname, err)
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
	target, err := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	controllerID, err := domain.NewControllerID("tomasz.walczuk")
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeDirectMTLS, controllerID)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	environment, err := domain.NewEnvironment(domain.EnvironmentSpec{
		Name: "linux-dev", HostClass: "Ubuntu Linux host", EffectiveAccount: hostruntime.LinuxHostAccount,
		AllowedTargets: []domain.ExecutionTarget{target}, AllowedSourceModes: []domain.SourceMode{domain.SourceModeEmpty},
		AllowedControllers: []domain.ControllerIdentity{controller}, ServiceLimits: domain.DefaultServiceLimits(),
	})
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	service, runtimeAdapter, err := NewLinuxExecutionService(authority, hostruntime.LinuxRuntimeOptions{
		Account: hostruntime.LinuxHostAccount, WorkspaceRoot: workspaceRoot, ShellPath: "/usr/bin/bash",
	}, environment)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	ctx := context.Background()
	var sessions []domain.SessionID
	var runDone []chan p136LinuxRunResult
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
				t.Errorf("P136 Ubuntu command worker did not exit before fixture cleanup: %v", cleanupCtx.Err())
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
				t.Errorf("clean P136 Ubuntu runtime for %s: %v", sessionID, stopErr)
			}
		}
		if err := db.Close(); err != nil {
			t.Errorf("close P136 Ubuntu test database: %v", err)
		}
	})

	const sessionCount = 5
	for i := 0; i < sessionCount; i++ {
		sessionID := domain.SessionID(fmt.Sprintf("p136-linux-session-%d", i))
		hash := p136LinuxHash(t, "create_session", map[string]any{
			"environment": "linux-dev", "execution_target": map[string]string{"kind": "remote", "profile": "linux-host"},
			"session_id": string(sessionID), "source": map[string]string{"mode": "empty"},
		})
		created, err := service.CreateSession(ctx, execution.CreateSessionRequest{
			SessionID: sessionID, IdempotencyKey: "p136-linux-create-" + strconv.Itoa(i), RequestHash: hash,
			Environment: "linux-dev", Target: target, Controller: controller, Source: domain.NewEmptySource(),
			IdempotencyRetention: store.DefaultSessionIdempotencyRetention,
		})
		if err != nil || created.Session.State != domain.SessionStateReady {
			t.Fatalf("create P136 Ubuntu session %s = %+v err=%v", sessionID, created.Session, err)
		}
		sessions = append(sessions, sessionID)
	}

	commandIDs := make([]domain.CommandID, 4)
	childPIDFile := filepath.Join(fixture.Path(), "delayed-child.pid")
	for i := 0; i < 4; i++ {
		commandID := domain.CommandID(fmt.Sprintf("p136-linux-command-%d", i))
		commandIDs[i] = commandID
		script := "/bin/sleep 10\n"
		if i == 0 {
			script = "trap '' INT\n( exec /bin/sleep 30 ) &\nprintf '%s\\n' \"$!\" > " + p136LinuxShellQuote(childPIDFile) + "\nwait\n"
		}
		accepted, err := service.AcceptCommand(ctx, p136LinuxSubmitRequest(t, sessions[i], commandID, controller, script))
		if err != nil || accepted.Command.State != domain.CommandStateQueued {
			t.Fatalf("accept P136 Ubuntu command %s = %+v err=%v", commandID, accepted.Command, err)
		}
		done := make(chan p136LinuxRunResult, 1)
		runDone = append(runDone, done)
		runFinished = append(runFinished, false)
		go func(id domain.CommandID, resultChannel chan p136LinuxRunResult) {
			result, runErr := service.ResumeCommand(context.Background(), id, controller)
			resultChannel <- p136LinuxRunResult{result: result, err: runErr}
		}(commandID, done)
		p135LinuxWaitCommandState(t, authority, commandID, domain.CommandStateRunning)
		if i == 0 {
			p136LinuxWaitFile(t, childPIDFile)
		}
	}
	if live, err := authority.CountLiveCommandSlots(ctx); err != nil || live != 4 {
		t.Fatalf("P136 Ubuntu live command slots=%d err=%v, want four", live, err)
	}
	childPID := p136LinuxReadPID(t, childPIDFile)
	childOwnership := p135LinuxReadOwnership(t, workspaceRoot, string(sessions[0]))
	if childPID == childOwnership.PID || syscall.Kill(childPID, 0) != nil {
		t.Fatalf("delayed Ubuntu output-pipe child pid=%d is not live and distinct from Bash pid=%d", childPID, childOwnership.PID)
	}

	fifthID := domain.CommandID("p136-linux-command-4")
	fifthMarker := filepath.Join(fixture.Path(), "fifth-started")
	fifth, err := service.AcceptCommand(ctx, p136LinuxSubmitRequest(t, sessions[4], fifthID, controller, "printf started > "+p136LinuxShellQuote(fifthMarker)+"\n"))
	if err != nil || fifth.Command.State != domain.CommandStateQueued {
		t.Fatalf("accept fifth P136 Ubuntu command = %+v err=%v, want queued", fifth.Command, err)
	}
	queued, err := service.ResumeCommand(ctx, fifthID, controller)
	if err != nil || queued.Command.State != domain.CommandStateQueued {
		t.Fatalf("fifth P136 Ubuntu start = %+v err=%v, want it to wait for a free host slot", queued.Command, err)
	}
	if _, err := os.Stat(fifthMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fifth P136 Ubuntu command ran while four slots were occupied: stat error=%v", err)
	}

	firstSession, err := authority.GetSession(ctx, sessions[0])
	if err != nil {
		t.Fatal(err)
	}
	firstCommand, err := authority.GetCommand(ctx, commandIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionCommand(ctx, store.CommandTransition{CommandID: commandIDs[0], NextState: domain.CommandStateCancelling}); err != nil {
		t.Fatalf("persist P136 Ubuntu cancellation request: %v", err)
	}
	stopDone := make(chan p136LinuxStopResult, 1)
	go func() {
		stopped, stopErr := runtimeAdapter.CancelCommand(context.Background(), execution.RuntimeCommandRequest{Session: firstSession, Command: firstCommand})
		stopDone <- p136LinuxStopResult{result: stopped, err: stopErr}
	}()
	p135LinuxWaitCommandState(t, authority, commandIDs[0], domain.CommandStateCancelling)
	time.Sleep(150 * time.Millisecond)
	if syscall.Kill(childPID, 0) != nil {
		t.Fatalf("delayed Ubuntu child %d exited before stop grace elapsed", childPID)
	}
	if live, err := authority.CountLiveCommandSlots(ctx); err != nil || live != 4 {
		t.Fatalf("P136 Ubuntu slots during delayed stop=%d err=%v, want all four retained", live, err)
	}
	slot, err := authority.GetCommandSlot(ctx, commandIDs[0])
	if err != nil || slot.StopConfirmedAt != nil || slot.ReleasedAt != nil {
		t.Fatalf("P136 Ubuntu interrupted command slot=%+v err=%v, want unreleased until stop/EOF confirmation", slot, err)
	}
	select {
	case result := <-runDone[0]:
		t.Fatalf("P136 Ubuntu command crossed its delayed EOF boundary early: %+v", result)
	default:
	}
	if record, err := authority.GetCommand(ctx, fifthID); err != nil || record.State != domain.CommandStateQueued {
		t.Fatalf("fifth P136 Ubuntu command changed while stop was delayed: %+v err=%v", record, err)
	}
	if _, err := os.Stat(fifthMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fifth P136 Ubuntu command ran during delayed stop: stat error=%v", err)
	}

	select {
	case stopped := <-stopDone:
		if stopped.result.Confirmed || stopped.err == nil {
			t.Fatalf("P136 Ubuntu stop=%+v err=%v, want an unconfirmed stop after the bounded grace", stopped.result, stopped.err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("P136 Ubuntu stop did not finish after its bounded grace")
	}
	select {
	case result := <-runDone[0]:
		runFinished[0] = true
		if result.err == nil {
			t.Fatalf("P136 Ubuntu interrupted command result=%+v, want shell/output-boundary loss", result.result)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("P136 Ubuntu command worker did not observe delayed control/EOF loss")
	}
	p135LinuxWaitCommandState(t, authority, commandIDs[0], domain.CommandStateLost)
	if live, err := authority.CountLiveCommandSlots(ctx); err != nil || live != 4 {
		t.Fatalf("P136 Ubuntu live slots after unconfirmed stop=%d err=%v, want four including residual reservation", live, err)
	}
	slot, err = authority.GetCommandSlot(ctx, commandIDs[0])
	if err != nil || slot.StopConfirmedAt != nil || slot.ReleasedAt != nil {
		t.Fatalf("P136 Ubuntu residual command slot=%+v err=%v, want durably retained", slot, err)
	}

	for i := 1; i < 4; i++ {
		p135LinuxWaitCommandState(t, authority, commandIDs[i], domain.CommandStateSucceeded)
		select {
		case <-runDone[i]:
			runFinished[i] = true
		case <-time.After(3 * time.Second):
			t.Fatalf("P136 Ubuntu command %s did not finish", commandIDs[i])
		}
	}
	if live, err := authority.CountLiveCommandSlots(ctx); err != nil || live != 1 {
		t.Fatalf("P136 Ubuntu live slots after clean completions=%d err=%v, want only the unresolved residual slot", live, err)
	}
	if record, err := authority.GetCommand(ctx, fifthID); err != nil || record.State != domain.CommandStateQueued {
		t.Fatalf("fifth P136 Ubuntu command state=%+v err=%v, want queued behind retained capacity", record, err)
	}
	if _, err := os.Stat(fifthMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fifth P136 Ubuntu command ran in residual capacity test: stat error=%v", err)
	}

	if _, err := service.CancelCommand(ctx, p136LinuxCancelRequest(t, fifthID, controller)); err != nil {
		t.Fatalf("cancel queued fifth P136 Ubuntu command: %v", err)
	}
	for i := 1; i < 5; i++ {
		session, err := authority.GetSession(ctx, sessions[i])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.CloseSession(ctx, p136LinuxCloseRequest(t, session.SessionID, controller)); err != nil {
			t.Fatalf("close P136 Ubuntu session %s: %v", session.SessionID, err)
		}
		cleanedSessions[session.SessionID] = true
	}
	lostSession, err := authority.GetSession(ctx, sessions[0])
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := runtimeAdapter.StopSession(ctx, lostSession)
	if err != nil || !confirmed {
		t.Fatalf("confirm P136 Ubuntu residual runtime cleanup=%t err=%v", confirmed, err)
	}
	cleanedSessions[lostSession.SessionID] = true
	if err := authority.ConfirmCommandSlotRelease(ctx, commandIDs[0]); err != nil {
		t.Fatalf("release P136 Ubuntu slot after confirmed runtime cleanup: %v", err)
	}
	if err := authority.ConfirmSessionCleanup(ctx, sessions[0]); err != nil {
		t.Fatalf("release P136 Ubuntu session reservation after confirmed cleanup: %v", err)
	}
	if live, err := authority.CountLiveCommandSlots(ctx); err != nil || live != 0 {
		t.Fatalf("P136 Ubuntu live slots after fixture cleanup=%d err=%v, want zero", live, err)
	}
	if reservations, err := authority.CountLiveSessionReservations(ctx); err != nil || reservations != 0 {
		t.Fatalf("P136 Ubuntu session reservations after fixture cleanup=%d err=%v, want zero", reservations, err)
	}
	t.Logf("machine=%s os=Linux account=%s: four live commands occupied all slots; fifth stayed queued; ignored-SIGINT child held output EOF through stop grace; lost command retained its slot until process-group cleanup was confirmed", hostname, current.Username)
}

func p136LinuxSubmitRequest(t *testing.T, sessionID domain.SessionID, commandID domain.CommandID, controller domain.ControllerIdentity, script string) execution.SubmitCommandRequest {
	t.Helper()
	hash := p136LinuxHash(t, "submit_command", map[string]string{"script": script})
	return execution.SubmitCommandRequest{
		CommandID: commandID, SessionID: sessionID, Controller: controller,
		IdempotencyKey: "key-" + string(commandID), RequestHash: hash, Script: script,
		IdempotencyRetention: store.DefaultSessionIdempotencyRetention,
	}
}

func p136LinuxCancelRequest(t *testing.T, commandID domain.CommandID, controller domain.ControllerIdentity) execution.CancelCommandRequest {
	t.Helper()
	hash := p136LinuxHash(t, "cancel_command", map[string]string{"command_id": string(commandID)})
	return execution.CancelCommandRequest{CommandID: commandID, Controller: controller, IdempotencyKey: "cancel-" + string(commandID), RequestHash: hash, IdempotencyRetention: store.DefaultSessionIdempotencyRetention}
}

func p136LinuxCloseRequest(t *testing.T, sessionID domain.SessionID, controller domain.ControllerIdentity) execution.CloseSessionRequest {
	t.Helper()
	hash := p136LinuxHash(t, "close_session", map[string]string{"policy": "p136-cleanup", "session_id": string(sessionID)})
	return execution.CloseSessionRequest{SessionID: sessionID, Controller: controller, IdempotencyKey: "close-" + string(sessionID), RequestHash: hash, Policy: "p136-cleanup", IdempotencyRetention: store.DefaultSessionIdempotencyRetention}
}

func p136LinuxHash(t *testing.T, operation string, payload any) domain.CanonicalHash {
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

func p136LinuxWaitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("P136 Ubuntu child did not publish PID marker %s", path)
}

func p136LinuxReadPID(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		t.Fatalf("invalid P136 Ubuntu child PID %q: %v", data, err)
	}
	return pid
}

func p136LinuxShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
