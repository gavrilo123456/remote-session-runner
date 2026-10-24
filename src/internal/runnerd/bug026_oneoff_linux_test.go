package runnerd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"remote-session-runner/src/internal/domain"
	hostruntime "remote-session-runner/src/internal/runtime"
	"remote-session-runner/src/internal/store"
)

// This fixture uses the real Ubuntu adapter and an isolated authority database;
// it never contacts an installed Runner, mailbox, or project checkout.
func TestBUG026LinuxOneOffExplicitPreflightExitClosesAndDoesNotReplay(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	workspaceRoot := filepath.Join(root, "workspaces")
	if err := os.Mkdir(workspaceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	adapter, err := hostruntime.NewLinuxProcessAdapter(hostruntime.LinuxRuntimeOptions{
		Account: hostruntime.LinuxHostAccount, WorkspaceRoot: workspaceRoot, ShellPath: "/usr/bin/bash",
	})
	if err != nil {
		t.Fatal(err)
	}
	runtimeAdapter, err := NewLinuxSessionRuntime(adapter)
	if err != nil {
		t.Fatal(err)
	}
	service, authority := newP046Service(t, runtimeAdapter)
	controller := p107DirectController(t, "tomasz.walczuk")
	const sessionID = "sess-bug026-linux-preflight"
	defer func() { _ = adapter.Cleanup(sessionID) }()
	oncePath := filepath.Join(root, "executions")
	quotedOncePath := "'" + strings.ReplaceAll(oncePath, "'", "'\\''") + "'"
	script := fmt.Sprintf("set -euo pipefail\nprintf 'once\\n' >>%s\nprintf 'PIN_OK\\n'\nprintf 'source_pin_mismatch file=fixture\\n' >&2\nexit 1\nprintf 'MUST_NOT_RUN\\n'\n", quotedOncePath)
	request := bug007DispatcherRunJobRequest(t, controller, "job-bug026-linux-preflight", sessionID, "cmd-bug026-linux-preflight", script)
	result, err := service.RunJob(ctx, request)
	if err != nil {
		t.Fatalf("one-off explicit preflight exit: %v", err)
	}
	if result.Job.Phase != store.JobPhaseComplete || result.Job.TeardownState != store.JobTeardownClosed || result.Session.State != domain.SessionStateClosed {
		t.Fatalf("one-off did not close normally: %+v", result)
	}
	if result.Command.State != domain.CommandStateFailed || result.Command.ExitCode == nil || *result.Command.ExitCode != 1 || !result.Command.OutputComplete || result.Command.OutputTruncated {
		t.Fatalf("explicit exit did not preserve the normal failed result: %+v", result.Command)
	}
	if string(result.Command.ScriptBytes) != script {
		t.Fatal("runtime isolation changed the durable original script")
	}
	events, err := authority.ListCommandEvents(ctx, request.Acceptance.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	for _, event := range events {
		switch event.Type {
		case "stdout":
			stdout.Write(event.Payload)
		case "stderr":
			stderr.Write(event.Payload)
		}
	}
	if stdout.String() != "PIN_OK\n" || stderr.String() != "source_pin_mismatch file=fixture\n" {
		t.Fatalf("captured output stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if slots, err := authority.CountLiveCommandSlots(ctx); err != nil || slots != 0 {
		t.Fatalf("live command slots=%d err=%v, want zero", slots, err)
	}
	if reservations, err := authority.CountLiveSessionReservations(ctx); err != nil || reservations != 0 {
		t.Fatalf("live session reservations=%d err=%v, want zero", reservations, err)
	}
	reservation, err := authority.GetSessionReservation(ctx, request.Acceptance.SessionID)
	if err != nil || reservation.CleanupConfirmedAt == nil || reservation.ReleasedAt == nil {
		t.Fatalf("session cleanup/release proof=%+v err=%v", reservation, err)
	}
	retry, err := service.RunJob(ctx, request)
	if err != nil {
		t.Fatalf("terminal retry: %v", err)
	}
	if retry.Job.JobID != result.Job.JobID || retry.Command.CommandID != result.Command.CommandID || retry.Command.State != domain.CommandStateFailed || retry.Job.TeardownState != store.JobTeardownClosed {
		t.Fatalf("retry changed the original terminal outcome: %+v", retry)
	}
	if after, err := authority.ListCommandEvents(ctx, request.Acceptance.CommandID); err != nil || !reflect.DeepEqual(after, events) {
		t.Fatalf("retry changed the retained event history: events=%+v err=%v", after, err)
	}
	if executions, err := os.ReadFile(oncePath); err != nil || string(executions) != "once\n" {
		t.Fatalf("one-off replayed or did not execute: executions=%q err=%v", executions, err)
	}
}
