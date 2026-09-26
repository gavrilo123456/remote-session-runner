package dispatcher

import (
	"context"
	"errors"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/sshclient"
	"remote-session-runner/src/internal/store"
	"remote-session-runner/src/internal/testfixture"
)

func TestP071RemoteUncertaintyDeadlineStopsReconciliationWithoutRetry(t *testing.T) {
	now := time.Date(2026, 9, 26, 23, 0, 0, 0, time.UTC)
	authority := p071Authority(t, &now)
	intent := p068SubmitIntent(t, "intent-p071-expire", "session-p071-expire", "command-p071-expire", domain.TargetKindRemote, "echo expire")
	if _, err := authority.CreateLocalIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	caller := &p070Caller{errors: []error{&sshclient.TransportError{Phase: sshclient.PhaseAfterSend, Err: errors.New("response lost")}}}
	driver, err := NewRemoteDriverWithClock(authority, caller, "router-p071", time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = driver.DispatchIntent(context.Background(), intent.IntentID)
	if len(caller.frames) != 1 {
		t.Fatalf("initial calls = %d", len(caller.frames))
	}
	now = now.Add(RemoteUncertaintyWindow)
	result, _, err := driver.ReconcileIntent(context.Background(), intent.IntentID)
	if !errors.Is(err, ErrRemoteUncertaintyDeadline) {
		t.Fatalf("deadline error = %v, want ErrRemoteUncertaintyDeadline", err)
	}
	if result.DeliveryState != store.LocalIntentUncertain || len(caller.frames) != 1 {
		t.Fatalf("deadline result = %+v calls=%d", result, len(caller.frames))
	}
}

func TestP071RemoteUncertaintyDeadlineIsBasedOnUncertainTransition(t *testing.T) {
	now := time.Date(2026, 9, 26, 23, 0, 0, 0, time.UTC)
	authority := p071Authority(t, &now)
	intent := p068SubmitIntent(t, "intent-p071-transition", "session-p071-transition", "command-p071-transition", domain.TargetKindRemote, "echo transition")
	if _, err := authority.CreateLocalIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	caller := &p070Caller{errors: []error{&sshclient.TransportError{Phase: sshclient.PhaseAfterSend, Err: errors.New("response lost")}}}
	driver, err := NewRemoteDriverWithClock(authority, caller, "router-p071", time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = driver.DispatchIntent(context.Background(), intent.IntentID)
	// Lease renewal changes updated_at, but it must not extend the uncertainty
	// window measured from the durable uncertain lifecycle transition.
	if _, err := authority.RenewLocalIntentLease(context.Background(), intent.IntentID, "router-p071", time.Hour); err != nil {
		t.Fatal(err)
	}
	now = now.Add(RemoteUncertaintyWindow)
	if _, _, err := driver.ReconcileIntent(context.Background(), intent.IntentID); !errors.Is(err, ErrRemoteUncertaintyDeadline) {
		t.Fatalf("renewal extended uncertainty deadline: %v", err)
	}
}

func p071Authority(t *testing.T, now *time.Time) *store.AuthorityStore {
	t.Helper()
	db, err := store.Open(context.Background(), testfixture.New(t).Path()+"/state/p071.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	authority, err := store.NewAuthorityStoreWithClock(db, func() time.Time { return *now })
	if err != nil {
		t.Fatal(err)
	}
	return authority
}
