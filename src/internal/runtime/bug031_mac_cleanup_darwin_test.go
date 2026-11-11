//go:build darwin && cgo

package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
)

// The helper runs only as this test binary's task-owned child. The parent
// fixture releases it through a private file; its natural deadline is a final
// cleanup bound and no unrelated PID is ever signalled.
func TestBUG031MacCleanupHelperProcess(t *testing.T) {
	mode := os.Getenv("RUNNER_BUG031_MAC_CLEANUP_HELPER")
	if mode == "" {
		t.Skip("disposable subprocess helper")
	}
	if mode == "child" {
		os.Exit(0)
	}
	control := os.Getenv("RUNNER_BUG031_MAC_CLEANUP_CONTROL")
	var child *exec.Cmd
	if mode == "zombie" || mode == "mixed" {
		originalGroup := syscall.Getpgrp()
		executable, err := os.Executable()
		if err != nil {
			os.Exit(2)
		}
		child = exec.Command(executable, "-test.run=^TestBUG031MacCleanupHelperProcess$")
		child.Env = append(os.Environ(), "RUNNER_BUG031_MAC_CLEANUP_HELPER=child")
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		if mode == "zombie" {
			if _, err := syscall.Setsid(); err != nil {
				_ = child.Wait()
				os.Exit(2)
			}
		}
		// Do not race a slow or race-instrumented child against the cleanup
		// window. Establish its positive native zombie state before Ready.
		deadline := time.Now().Add(5 * time.Second)
		for {
			member, found, inspectErr := readMacCleanupGroupMember(child.Process.Pid)
			if inspectErr == nil && found && member.Status == macProcessStatusZombie &&
				member.UID == os.Getuid() && member.ProcessGroupID == originalGroup {
				break
			}
			if inspectErr != nil || time.Now().After(deadline) {
				_ = child.Wait()
				_ = os.WriteFile(filepath.Join(control, "exited"), nil, 0o600)
				os.Exit(2)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	ready, _ := json.Marshal(map[string]int{"pid": os.Getpid(), "pgid": syscall.Getpgrp()})
	if err := os.WriteFile(filepath.Join(control, "ready.json"), ready, 0o600); err != nil {
		if child != nil {
			_ = child.Wait()
		}
		os.Exit(2)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(control, "release")); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if child != nil {
		_ = child.Wait()
	}
	_ = os.WriteFile(filepath.Join(control, "exited"), nil, 0o600)
	os.Exit(0)
}

type bug031MacFixture struct {
	adapter  *MacProcessAdapter
	prepared MacPrepared
	owner    RuntimeOwnershipRecord
	control  string
	result   PersistentShellResult
}

func newBUG031MacFixture(t *testing.T, mode string) *bug031MacFixture {
	t.Helper()
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	root, control := t.TempDir(), t.TempDir()
	adapter, err := NewMacProcessAdapter(MacRuntimeOptions{Account: current.Username, WorkspaceRoot: root, ShellPath: "/bin/bash"})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := adapter.Prepare(context.Background(), "session-bug031-"+mode, "generation-bug031-"+mode)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.StartAgent(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	shell, err := adapter.Shell(prepared.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fixture := &bug031MacFixture{adapter: adapter, prepared: prepared, control: control}
	fixture.owner, err = readRuntimeOwnership(root, prepared.SessionID)
	if err != nil {
		_ = shell.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(control, "release"), nil, 0o600)
		_ = shell.Close()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			_, doneErr := os.Stat(filepath.Join(control, "exited"))
			if doneErr == nil && waitProcessGroupGone(context.Background(), fixture.owner.ProcessGroupID, 20*time.Millisecond) {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Errorf("BUG031 task-owned helper did not exit and release its original group")
	})
	script := "RUNNER_BUG031_MAC_CLEANUP_HELPER=" + shellQuote(mode) + " RUNNER_BUG031_MAC_CLEANUP_CONTROL=" + shellQuote(control) +
		" " + shellQuote(executable) + " -test.run='^TestBUG031MacCleanupHelperProcess$' </dev/null >/dev/null 2>&1 3<&- 4>&- &\n" +
		"printf BUG031_FAILED_OUTPUT\n/bin/sh -c 'exit 2'\n"
	fixture.result, err = shell.RunScript(context.Background(), "command-bug031-"+mode, []byte(script))
	if err != nil || fixture.result.CommandComplete.ExitCode == nil || *fixture.result.CommandComplete.ExitCode != 2 || string(fixture.result.Stdout) != "BUG031_FAILED_OUTPUT" {
		t.Fatalf("ordinary failed command was not preserved: result=%+v error=%v", fixture.result, err)
	}
	deadline := time.Now().Add(6 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(control, "ready.json")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("BUG031 task-owned helper was not ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fixture
}

func TestBUG031MacCleanupVerifiedZombieGroupSucceeds(t *testing.T) {
	fixture := newBUG031MacFixture(t, "zombie")
	if err := fixture.adapter.CleanupContext(context.Background(), fixture.prepared.SessionID); err != nil {
		var carrier domain.RuntimeCleanupDiagnosticCarrier
		if errors.As(err, &carrier) {
			t.Fatalf("verified zombie group retained capacity: %+v", carrier.RuntimeCleanupDiagnostic())
		}
		t.Fatal(err)
	}
	members, err := inspectMacCleanupGroupMembers(fixture.owner.ProcessGroupID)
	if err != nil || len(members) != 1 || members[0].Status != macProcessStatusZombie || members[0].UID != os.Getuid() || members[0].ProcessStartIdentity == "" {
		t.Fatalf("positive native zombie evidence=%+v error=%v", members, err)
	}
	if _, err := readRuntimeOwnership(fixture.adapter.options.WorkspaceRoot, fixture.prepared.SessionID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("confirmed cleanup retained owner marker: %v", err)
	}
	if _, err := os.Stat(fixture.prepared.Workspace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("confirmed cleanup retained owned workspace: %v", err)
	}
	if *fixture.result.CommandComplete.ExitCode != 2 || string(fixture.result.Stdout) != "BUG031_FAILED_OUTPUT" {
		t.Fatal("cleanup altered the original failed command")
	}
}

func TestBUG031MacCleanupLiveAndMixedNativeGroupsRemainRetained(t *testing.T) {
	for _, mode := range []string{"live", "mixed"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newBUG031MacFixture(t, mode)
			err := fixture.adapter.CleanupContext(context.Background(), fixture.prepared.SessionID)
			d := bug031Diagnostic(t, err)
			if d.Code != "group_still_runnable" || d.RunnableCount != 1 || (mode == "mixed" && d.ZombieCount != 1) {
				t.Fatalf("live native group proof did not remain blocked: %+v error=%v", d, err)
			}
			owner, ownerErr := readRuntimeOwnership(fixture.adapter.options.WorkspaceRoot, fixture.prepared.SessionID)
			if ownerErr != nil || owner != fixture.owner {
				t.Fatalf("unconfirmed group changed exact ownership: %+v error=%v", owner, ownerErr)
			}
			if _, err := os.Stat(fixture.prepared.Workspace); err != nil {
				t.Fatalf("unconfirmed group lost owned workspace: %v", err)
			}
			if err := os.WriteFile(filepath.Join(fixture.control, "release"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if !waitProcessGroupGone(context.Background(), fixture.owner.ProcessGroupID, 3*time.Second) {
				t.Fatal("task-owned live group did not exit after release")
			}
			if err := fixture.adapter.CleanupContext(context.Background(), fixture.prepared.SessionID); err != nil {
				t.Fatalf("retry after actual group exit: %v", err)
			}
			if *fixture.result.CommandComplete.ExitCode != 2 || string(fixture.result.Stdout) != "BUG031_FAILED_OUTPUT" {
				t.Fatal("cleanup altered the original failed command")
			}
		})
	}
}
