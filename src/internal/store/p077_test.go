package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/testfixture"
)

func TestP077RemoteJobProjectionRoundTripsAndDoesNotBecomeAuthority(t *testing.T) {
	authority := p077Store(t)
	target, _ := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	controller, _ := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	when := time.Date(2026, 9, 27, 14, 0, 0, 0, time.UTC)
	state := domain.CommandStateSucceeded
	final := int64(3)
	exit := 0
	projection := RemoteJobProjection{JobID: "job-p077", SessionID: "session-p077", CommandID: "command-p077", Phase: JobPhaseAwaitingCommand, CommandState: &state, ExitCode: &exit, FinalEventSequence: &final, OutputComplete: true, TeardownState: JobTeardownPending, Target: target, Controller: controller, Environment: "linux-dev", Source: domain.NewEmptySource(), Capabilities: p076Capabilities(), ObservedAt: when}
	if _, err := authority.UpsertRemoteJobProjection(context.Background(), projection); err != nil {
		t.Fatal(err)
	}
	if err := authority.MarkRemoteJobProjectionStale(context.Background(), projection.JobID); err != nil {
		t.Fatal(err)
	}
	read, err := authority.GetRemoteJobProjection(context.Background(), projection.JobID)
	if err != nil || read.Phase != JobPhaseAwaitingCommand || read.CommandState == nil || *read.CommandState != state || !read.IsStale || read.TeardownState != JobTeardownPending {
		t.Fatalf("job projection = %+v, %v", read, err)
	}
	older := projection
	older.Phase = JobPhaseComplete
	older.ObservedAt = when.Add(-time.Second)
	ignored, err := authority.UpsertRemoteJobProjection(context.Background(), older)
	if err != nil || ignored.Phase != JobPhaseAwaitingCommand {
		t.Fatalf("older job projection = %+v, %v", ignored, err)
	}
	conflict := projection
	conflict.CommandID = "command-p077-other"
	if _, err := authority.UpsertRemoteJobProjection(context.Background(), conflict); !errors.Is(err, ErrRemoteProjectionConflict) {
		t.Fatalf("job identity conflict = %v", err)
	}
	if _, err := authority.GetRemoteJobProjection(context.Background(), "missing-p077-job"); !errors.Is(err, ErrRemoteProjectionNotFound) {
		t.Fatalf("missing job projection = %v", err)
	}
}

func p077Store(t *testing.T) *AuthorityStore {
	t.Helper()
	db, err := Open(context.Background(), testfixture.New(t).Path()+"/state/p077.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	authority, err := NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	return authority
}
