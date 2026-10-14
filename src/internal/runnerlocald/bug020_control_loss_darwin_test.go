//go:build darwin && cgo

package runnerlocald

import (
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/lifecycle"
	hostruntime "remote-session-runner/src/internal/runtime"
	"remote-session-runner/src/internal/store"
)

// TestBUG020MacShellExitWithInheritedControlWriterSettlesOnce reproduces a
// real executor disappearance without pipe EOF: Bash exits while a live,
// test-owned descendant keeps fd 4 open. It uses the same Mac adapter, durable
// intent acceptance, and queue worker as locald, with no installed-service state.
func TestBUG020MacShellExitWithInheritedControlWriterSettlesOnce(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	workspaceRoot := filepath.Join(root, "workspaces")
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(ctx, filepath.Join(root, "authority.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close BUG020 fixture authority: %v", err)
		}
	})
	authority, err := store.NewAuthorityStore(database)
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
	environment, err := domain.NewEnvironment(domain.EnvironmentSpec{
		Name: "mac-dev", HostClass: "macOS host", EffectiveAccount: current.Username,
		AllowedTargets: []domain.ExecutionTarget{target}, AllowedSourceModes: []domain.SourceMode{domain.SourceModeEmpty},
		AllowedControllers: []domain.ControllerIdentity{controller}, ServiceLimits: domain.DefaultServiceLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	service, adapter, err := NewMacExecutionService(authority, hostruntime.MacRuntimeOptions{
		Account: current.Username, WorkspaceRoot: workspaceRoot, ShellPath: "/bin/bash",
	}, environment)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReconcileStartup(ctx); err != nil {
		t.Fatal(err)
	}
	gate := lifecycle.NewGate()
	worker := newBUG011P4Worker(t, service, authority, gate, 5*time.Millisecond)
	server, err := NewPrivateServer(PrivateServerOptions{
		Authority: authority, Service: service, SocketPath: p060SocketPath(t),
		Owner: controller, DispatchGate: gate, QueueWake: worker.Wake,
	})
	if err != nil {
		t.Fatal(err)
	}
	create := p060CreateIntent(t, authority, "intent-bug020-create", "session-bug020-control-loss", "key-bug020-create")
	if _, err := server.acceptIntent(ctx, create); err != nil {
		t.Fatal(err)
	}
	owner := bug011MacReadOwnershipRecord(t, workspaceRoot, create.SessionID).record
	pidPath := filepath.Join(root, "control-writer.pid")
	countPath := filepath.Join(root, "body-count")
	t.Cleanup(func() {
		gate.Stop()
		worker.Stop()
		// The PID comes only from this command's private fixture file. Check its
		// original process group before signaling; never kill by command name.
		if encoded, readErr := os.ReadFile(pidPath); readErr == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(encoded)))
			if parseErr != nil {
				t.Errorf("parse BUG020 fixture child PID: %v", parseErr)
			} else if stopErr := bug011MacStopKnownPID(pid, owner.ProcessGroupID); stopErr != nil {
				t.Errorf("stop BUG020 fixture child: %v", stopErr)
			} else if waitErr := bug011MacWaitForPIDsGone(map[int]struct{}{pid: {}}, 3*time.Second); waitErr != nil {
				t.Errorf("wait BUG020 fixture child: %v", waitErr)
			}
		} else if !errors.Is(readErr, os.ErrNotExist) {
			t.Errorf("read BUG020 fixture child PID: %v", readErr)
		}
		cleanupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := worker.Wait(cleanupCtx); err != nil {
			t.Errorf("wait BUG020 queue worker: %v", err)
		}
		if err := gate.Wait(cleanupCtx); err != nil {
			t.Errorf("drain BUG020 command worker: %v", err)
		}
		session, readErr := authority.GetSession(cleanupCtx, create.SessionID)
		if readErr != nil {
			t.Errorf("read BUG020 fixture session: %v", readErr)
		} else if _, stopErr := adapter.StopSession(cleanupCtx, session); stopErr != nil {
			t.Errorf("clean BUG020 fixture runtime: %v", stopErr)
		}
	})
	worker.Start(ctx)
	// Redirect the child's output so only control-writer inheritance prevents
	// EOF. Closing fd 4 here would remove the boundary this regression covers.
	script := "printf 'body\\n' >> " + p136MacShellQuote(countPath) + "\n" +
		"( exec /bin/sleep 30 ) </dev/null >/dev/null 2>&1 &\n" +
		"printf '%s\\n' \"$!\" > " + p136MacShellQuote(pidPath) + "\nexit 0\n"
	input := p060SubmitIntent(t, "intent-bug020-command", string(create.SessionID), "command-bug020-control-loss", "key-bug020-command", script)
	intent, err := authority.CreateLocalIntent(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, err := server.acceptIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}
	worker.Wake()
	childPID := bug011MacReadPIDFile(t, pidPath)
	bug011MacAssertRunnableProcess(t, childPID, owner.ProcessGroupID, 100*time.Millisecond)
	deadline := started.Add(3 * time.Second)
	for {
		command, readErr := authority.GetCommand(ctx, input.CommandID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if command.State == domain.CommandStateLost {
			if time.Now().After(deadline) {
				t.Fatalf("Bash disappearance reached lost too late: elapsed=%s", time.Since(started))
			}
			break
		}
		if command.State.IsTerminal() || time.Now().After(deadline) {
			t.Fatalf("Bash disappearance did not produce bounded lost result: state=%s elapsed=%s", command.State, time.Since(started))
		}
		time.Sleep(10 * time.Millisecond)
	}
	bug011MacAssertRunnableProcess(t, childPID, owner.ProcessGroupID, 100*time.Millisecond)
	bug020AssertLostIdentity(t, authority, input)

	// Repeated policy/worker recovery and terminal resume must observe the same
	// result. A stale execution claim must never source the command body again.
	for range 3 {
		if _, err := service.EnforcePolicies(ctx); err != nil {
			t.Fatal(err)
		}
		if err := worker.RecoverNonterminalJobs(ctx); err != nil {
			t.Fatal(err)
		}
		result, err := service.ResumeCommand(ctx, input.CommandID, controller)
		if err != nil || result.Command.State != domain.CommandStateLost {
			t.Fatalf("terminal resume state=%s err=%v", result.Command.State, err)
		}
		worker.Wake()
		time.Sleep(20 * time.Millisecond)
		bug020AssertLostIdentity(t, authority, input)
	}
	count, err := os.ReadFile(countPath)
	if err != nil || string(count) != "body\n" {
		t.Fatalf("command body execution count=%q err=%v, want exactly once", count, err)
	}
}

func bug020AssertLostIdentity(t *testing.T, authority *store.AuthorityStore, input store.LocalIntentCreate) {
	t.Helper()
	ctx := context.Background()
	command, err := authority.GetCommand(ctx, input.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if command.CommandID != input.CommandID || command.SessionID != input.SessionID || command.RequestHash != input.RequestHash ||
		command.State != domain.CommandStateLost || command.ExitCode != nil || command.OutputComplete ||
		command.OutputUnavailableReason != "capture_boundary_unconfirmed" || command.FinalEventSequence == nil {
		t.Fatalf("BUG020 command identity/output boundary: command=%s session=%s state=%s exit=%v output_complete=%v reason=%q final_sequence=%v",
			command.CommandID, command.SessionID, command.State, command.ExitCode, command.OutputComplete, command.OutputUnavailableReason, command.FinalEventSequence)
	}
	intent, err := authority.GetLocalIntent(ctx, input.IntentID)
	if err != nil || intent.CommandID != input.CommandID || intent.SessionID != input.SessionID ||
		intent.RequestHash != input.RequestHash || intent.IdempotencyKey != input.IdempotencyKey {
		t.Fatalf("BUG020 durable intent identity changed: intent=%s command=%s session=%s err=%v", intent.IntentID, intent.CommandID, intent.SessionID, err)
	}
	session, err := authority.GetSession(ctx, input.SessionID)
	if err != nil || session.State != domain.SessionStateLost {
		t.Fatalf("BUG020 session state=%s err=%v, want lost", session.State, err)
	}
	p136MacAssertLostOutcome(t, authority, input.CommandID)
	allEvents, err := authority.ListCommandEvents(ctx, input.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	started := 0
	for _, event := range allEvents {
		if event.Type == "command_started" {
			started++
		}
	}
	if started != 1 {
		t.Fatalf("BUG020 command started events=%d, want exactly one", started)
	}
	events, err := authority.ReplayCommandEvents(ctx, input.CommandID, *command.FinalEventSequence)
	if err != nil || len(events) != 0 {
		t.Fatalf("BUG020 events continued beyond final cursor: events=%+v err=%v", events, err)
	}
}
