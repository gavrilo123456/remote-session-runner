package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/testfixture"
)

func TestP012_D01CreatesAndReopensTargetControllerPreservingSessions(t *testing.T) {
	root := testfixture.New(t)
	databasePath := root.Path() + "/state/authority.db"
	db, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fixedNow := time.Date(2026, 9, 26, 10, 11, 12, 123456789, time.UTC)
	store, err := NewAuthorityStoreWithClock(db, func() time.Time { return fixedNow })
	if err != nil {
		t.Fatal(err)
	}

	localTarget, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	if err != nil {
		t.Fatal(err)
	}
	localController, err := newController(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	localSource, err := domain.NewLocalWorktreeSource(root.Path())
	if err != nil {
		t.Fatal(err)
	}
	localInput := SessionCreate{
		SessionID:   domain.SessionID("session-local-1"),
		Target:      localTarget,
		Environment: "mac-dev",
		Controller:  localController,
		Source:      localSource,
		Limits:      testSessionLimits(),
		Reason:      "accepted by local authority",
	}
	local, err := store.CreateSession(context.Background(), localInput)
	if err != nil {
		t.Fatal(err)
	}
	assertSessionMatchesInput(t, local, localInput, fixedNow)

	lifecycle, err := store.ListSessionLifecycle(context.Background(), local.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(lifecycle) != 1 || lifecycle[0].PreviousState != nil || lifecycle[0].NewState != domain.SessionStateCreating || lifecycle[0].Reason != localInput.Reason {
		t.Fatalf("initial lifecycle = %+v", lifecycle)
	}

	ready, err := store.TransitionSession(context.Background(), local.SessionID, domain.SessionStateReady, "agent handshake confirmed")
	if err != nil {
		t.Fatal(err)
	}
	if ready.State != domain.SessionStateReady || ready.Target != local.Target || ready.Controller != local.Controller {
		t.Fatalf("transition changed immutable identity or state: %+v", ready)
	}

	remoteTarget, err := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	remoteController, err := newController(domain.ControllerTypeQueuedMac, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	remoteInput := SessionCreate{
		SessionID:   domain.SessionID("session-remote-1"),
		Target:      remoteTarget,
		Environment: "linux-dev",
		Controller:  remoteController,
		Source:      domain.NewEmptySource(),
		Limits:      testSessionLimits(),
	}
	remote, err := store.CreateSession(context.Background(), remoteInput)
	if err != nil {
		t.Fatal(err)
	}
	assertSessionMatchesInput(t, remote, remoteInput, fixedNow)

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedDB, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedDB.Close()
	reopened, err := NewAuthorityStoreWithClock(reopenedDB, func() time.Time { return fixedNow })
	if err != nil {
		t.Fatal(err)
	}
	gotLocal, err := reopened.GetSession(context.Background(), local.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if gotLocal.State != domain.SessionStateReady || gotLocal.Target != local.Target || gotLocal.Controller != local.Controller || !reflect.DeepEqual(gotLocal.Source, local.Source) {
		t.Fatalf("reopened local session = %+v, want ready session preserving identity", gotLocal)
	}
	gotRemote, err := reopened.GetSession(context.Background(), remote.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if gotRemote.Target != remote.Target || gotRemote.Controller != remote.Controller || gotRemote.State != domain.SessionStateCreating {
		t.Fatalf("reopened remote session = %+v, want creating session preserving identity", gotRemote)
	}
	reopenedLifecycle, err := reopened.ListSessionLifecycle(context.Background(), local.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reopenedLifecycle) != 2 || reopenedLifecycle[1].PreviousState == nil || *reopenedLifecycle[1].PreviousState != domain.SessionStateCreating || reopenedLifecycle[1].NewState != domain.SessionStateReady {
		t.Fatalf("reopened lifecycle = %+v", reopenedLifecycle)
	}
}

func TestP012_D01LifecycleStateAndHistoryAreAtomic(t *testing.T) {
	root := testfixture.New(t)
	db, err := Open(context.Background(), root.Path()+"/state/atomic.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewAuthorityStoreWithClock(db, func() time.Time { return time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	target, err := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := newController(domain.ControllerTypeDirectMTLS, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateSession(context.Background(), SessionCreate{
		SessionID:   domain.SessionID("session-atomic-1"),
		Target:      target,
		Environment: "linux-dev",
		Controller:  controller,
		Source:      domain.NewEmptySource(),
		Limits:      testSessionLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionSession(context.Background(), created.SessionID, domain.SessionStateReady, "ready"); err != nil {
		t.Fatal(err)
	}

	if _, err := store.TransitionSession(context.Background(), created.SessionID, domain.SessionStateCreating, "illegal backwards edge"); !errors.Is(err, domain.ErrIllegalSessionTransition) {
		t.Fatalf("backwards transition error = %v, want ErrIllegalSessionTransition", err)
	}
	assertStateAndLifecycleCount(t, store, created.SessionID, domain.SessionStateReady, 2)

	if _, err := db.Exec(`
CREATE TRIGGER p012_fail_lifecycle
BEFORE INSERT ON exec_session_lifecycle
WHEN NEW.reason = 'inject-failure'
BEGIN
    SELECT RAISE(ABORT, 'injected lifecycle failure');
END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionSession(context.Background(), created.SessionID, domain.SessionStateBusy, "inject-failure"); err == nil {
		t.Fatal("injected lifecycle failure unexpectedly committed")
	}
	assertStateAndLifecycleCount(t, store, created.SessionID, domain.SessionStateReady, 2)

	if _, err := db.Exec("UPDATE exec_sessions SET target_kind = 'local' WHERE session_id = ?", string(created.SessionID)); err == nil {
		t.Fatal("immutable target update unexpectedly succeeded")
	}
	assertStateAndLifecycleCount(t, store, created.SessionID, domain.SessionStateReady, 2)
}

func TestP012_RejectsInvalidSessionStoreInputs(t *testing.T) {
	if _, err := NewAuthorityStore(nil); !errors.Is(err, ErrNilDatabase) {
		t.Fatalf("NewAuthorityStore(nil) error = %v, want ErrNilDatabase", err)
	}
	root := testfixture.New(t)
	db, err := Open(context.Background(), root.Path()+"/state/invalid.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	target, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := newController(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	base := SessionCreate{
		SessionID:   domain.SessionID("session-invalid-base"),
		Target:      target,
		Environment: "mac-dev",
		Controller:  controller,
		Source:      domain.NewEmptySource(),
		Limits:      testSessionLimits(),
	}
	for name, input := range map[string]SessionCreate{
		"empty ID":          func() SessionCreate { value := base; value.SessionID = ""; return value }(),
		"empty environment": func() SessionCreate { value := base; value.Environment = " "; return value }(),
		"zero limits":       func() SessionCreate { value := base; value.Limits = domain.EffectiveSessionLimits{}; return value }(),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.CreateSession(context.Background(), input); !errors.Is(err, ErrInvalidSession) {
				t.Fatalf("CreateSession error = %v, want ErrInvalidSession", err)
			}
		})
	}
	if _, err := store.GetSession(context.Background(), domain.SessionID("missing")); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("GetSession(missing) error = %v, want ErrSessionNotFound", err)
	}
}

func newController(kind domain.ControllerType, id string) (domain.ControllerIdentity, error) {
	controllerID, err := domain.NewControllerID(id)
	if err != nil {
		return domain.ControllerIdentity{}, err
	}
	return domain.NewControllerIdentity(kind, controllerID)
}

func testSessionLimits() domain.EffectiveSessionLimits {
	return domain.EffectiveSessionLimits{
		CommandTimeout:        30 * time.Minute,
		IdleTimeout:           30 * time.Minute,
		SessionMaxLifetime:    4 * time.Hour,
		OutputBytesPerCommand: 100 << 20,
	}
}

func assertSessionMatchesInput(t *testing.T, got SessionRecord, want SessionCreate, now time.Time) {
	t.Helper()
	if got.SessionID != want.SessionID || got.Target != want.Target || got.Environment != want.Environment || got.Controller != want.Controller || !reflect.DeepEqual(got.Source, want.Source) || got.ResolvedRevision != want.ResolvedRevision || got.RuntimeGeneration != want.RuntimeGeneration || got.State != domain.SessionStateCreating || got.Limits != want.Limits {
		t.Fatalf("session = %+v, want input identity/limits and creating state", got)
	}
	if !got.CreatedAt.Equal(now) || !got.UpdatedAt.Equal(now) || !got.ExpiresAt.Equal(now.Add(want.Limits.SessionMaxLifetime)) {
		t.Fatalf("session times = created %s updated %s expires %s", got.CreatedAt, got.UpdatedAt, got.ExpiresAt)
	}
}

func assertStateAndLifecycleCount(t *testing.T, store *AuthorityStore, id domain.SessionID, state domain.SessionState, wantCount int) {
	t.Helper()
	got, err := store.GetSession(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != state {
		t.Fatalf("session state = %q, want %q", got.State, state)
	}
	history, err := store.ListSessionLifecycle(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != wantCount {
		t.Fatalf("lifecycle count = %d, want %d (%+v)", len(history), wantCount, history)
	}
}
