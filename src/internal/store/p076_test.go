package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/testfixture"
)

func TestP076RemoteSessionProjectionIsMonotonicAndStale(t *testing.T) {
	authority := p076Store(t)
	target, _ := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	controller, _ := domain.NewControllerIdentity(domain.ControllerTypeQueuedMac, "tomasz.walczuk")
	when := time.Date(2026, 9, 27, 13, 0, 0, 0, time.UTC)
	base := RemoteSessionProjection{SessionID: "session-p076", Target: target, Controller: controller, State: domain.SessionStateReady, Environment: "linux-dev", Source: domain.NewEmptySource(), Capabilities: p076Capabilities(), ObservedAt: when}
	if _, err := authority.UpsertRemoteSessionProjection(context.Background(), base); err != nil {
		t.Fatal(err)
	}
	older := base
	older.State = domain.SessionStateFailed
	older.ObservedAt = when.Add(-time.Second)
	read, err := authority.UpsertRemoteSessionProjection(context.Background(), older)
	if err != nil || read.State != domain.SessionStateReady || !read.ObservedAt.Equal(when) {
		t.Fatalf("older projection = %+v, %v", read, err)
	}
	if err := authority.MarkRemoteSessionProjectionStale(context.Background(), base.SessionID); err != nil {
		t.Fatal(err)
	}
	stale, err := authority.GetRemoteSessionProjection(context.Background(), base.SessionID)
	if err != nil || !stale.IsStale || stale.State != domain.SessionStateReady {
		t.Fatalf("stale projection = %+v, %v", stale, err)
	}
	newer := base
	newer.State = domain.SessionStateBusy
	newer.ObservedAt = when.Add(time.Second)
	newer.IsStale = false
	fresh, err := authority.UpsertRemoteSessionProjection(context.Background(), newer)
	if err != nil || fresh.State != domain.SessionStateBusy || fresh.IsStale {
		t.Fatalf("newer projection = %+v, %v", fresh, err)
	}
	conflict := newer
	conflict.Target, _ = domain.NewExecutionTarget(domain.TargetKindRemote, "other-profile")
	if _, err := authority.UpsertRemoteSessionProjection(context.Background(), conflict); !errors.Is(err, ErrRemoteProjectionConflict) {
		t.Fatalf("identity conflict = %v", err)
	}
}

func TestP076RemoteCommandProjectionRoundTripsAndRequiresAuthorityFields(t *testing.T) {
	authority := p076Store(t)
	target, _ := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	controller, _ := domain.NewControllerIdentity(domain.ControllerTypeQueuedMac, "tomasz.walczuk")
	when := time.Date(2026, 9, 27, 13, 5, 0, 0, time.UTC)
	projection := RemoteCommandProjection{CommandID: "command-p076", SessionID: "session-p076", Ordinal: 1, State: domain.CommandStateSucceeded, OutputComplete: true, Target: target, Controller: controller, Environment: "linux-dev", Source: domain.NewEmptySource(), Capabilities: p076Capabilities(), ObservedAt: when}
	final := int64(3)
	projection.FinalEventSequence = &final
	exit := 0
	projection.ExitCode = &exit
	stored, err := authority.UpsertRemoteCommandProjection(context.Background(), projection)
	if err != nil {
		t.Fatal(err)
	}
	read, err := authority.GetRemoteCommandProjection(context.Background(), projection.CommandID)
	if err != nil || read.State != domain.CommandStateSucceeded || read.FinalEventSequence == nil || *read.FinalEventSequence != 3 || read.ExitCode == nil || *read.ExitCode != 0 {
		t.Fatalf("command projection = %+v, %v", read, err)
	}
	if stored.Capabilities.EffectiveAccount != "ubuntu" || read.Capabilities.Isolation != string(domain.IsolationOSUser) {
		t.Fatalf("capabilities = %+v", read.Capabilities)
	}
	if _, err := authority.GetRemoteCommandProjection(context.Background(), "missing-p076-command"); !errors.Is(err, ErrRemoteProjectionNotFound) {
		t.Fatalf("missing projection error = %v", err)
	}
}

func p076Capabilities() RemoteCapabilities {
	return RemoteCapabilities{HostClass: "linux-host", Isolation: string(domain.IsolationOSUser), EffectiveAccount: "ubuntu", ServiceLimits: map[string]any{"running_commands_per_host": float64(4)}}
}

func p076Store(t *testing.T) *AuthorityStore {
	t.Helper()
	db, err := Open(context.Background(), testfixture.New(t).Path()+"/state/p076.db")
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
