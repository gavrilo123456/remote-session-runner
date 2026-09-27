package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/sshbridge"
	"remote-session-runner/src/internal/sshclient"
	"remote-session-runner/src/internal/store"
	"remote-session-runner/src/internal/testfixture"
)

func TestP093D09RemoteRetryUsesSameMutationOnlyBeforeKeyExpiry(t *testing.T) {
	for _, test := range []struct {
		name             string
		dispatchLeadTime time.Duration
		reconcileDelay   time.Duration
		wantRetry        bool
	}{
		{name: "before_expiry", dispatchLeadTime: 2 * time.Hour, reconcileDelay: time.Hour, wantRetry: true},
		{name: "at_expiry", dispatchLeadTime: time.Hour, reconcileDelay: time.Hour, wantRetry: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			base := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
			now := base
			authority := p093Authority(t, &now)
			intent := p068SubmitIntent(t, "intent-p093-"+test.name, "session-p093-"+test.name, "command-p093-"+test.name, domain.TargetKindRemote, "printf 'retry once\\n'")
			if _, err := authority.CreateLocalIntent(ctx, intent); err != nil {
				t.Fatal(err)
			}
			persistedIntent, err := authority.GetLocalIntent(ctx, intent.IntentID)
			if err != nil {
				t.Fatal(err)
			}
			deadline := base.Add(store.DefaultSessionIdempotencyRetention)
			firstSendAt := deadline.Add(-test.dispatchLeadTime)
			now = firstSendAt

			mutationFrame, err := frameForRemoteIntent(persistedIntent)
			if err != nil {
				t.Fatal(err)
			}
			missingPayload, _ := json.Marshal(sshbridge.ErrorPayload{Code: "resource_not_found", Message: "unknown command"})
			caller := &p070Caller{
				errors: []error{&sshclient.TransportError{Phase: sshclient.PhaseAfterSend, Err: errors.New("response lost")}},
				responses: []sshbridge.ReplyFrame{
					{},
					{ProtocolVersion: sshbridge.ProtocolVersion, RequestID: string(intent.IntentID) + "/reconcile", ResponseType: "error", Payload: missingPayload},
					p069AcceptedReply(mutationFrame),
				},
			}
			driver, err := NewRemoteDriverWithClock(authority, caller, "router-p093", time.Minute, func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := driver.DispatchIntent(ctx, intent.IntentID); err == nil {
				t.Fatal("after-send failure unexpectedly succeeded")
			}
			now = firstSendAt.Add(test.reconcileDelay)
			result, _, err := driver.ReconcileIntent(ctx, intent.IntentID)
			if test.wantRetry {
				if err != nil || result.DeliveryState != store.LocalIntentAccepted {
					t.Fatalf("pre-expiry retry result=%+v err=%v", result, err)
				}
				if len(caller.frames) != 3 || caller.frames[1].Operation != sshbridge.OperationGetCommand || !reflect.DeepEqual(caller.frames[0], caller.frames[2]) {
					t.Fatalf("retry did not reuse the original mutation frame: %+v", caller.frames)
				}
				return
			}
			if !errors.Is(err, ErrRemoteIdempotencyDeadline) {
				t.Fatalf("post-expiry reconciliation error=%v, want ErrRemoteIdempotencyDeadline", err)
			}
			current, readErr := authority.GetLocalIntent(ctx, intent.IntentID)
			if readErr != nil || current.DeliveryState != store.LocalIntentUncertain {
				t.Fatalf("expired uncertain intent=%+v err=%v", current, readErr)
			}
			if len(caller.frames) != 2 || caller.frames[1].Operation != sshbridge.OperationGetCommand {
				t.Fatalf("post-expiry path sent another mutation: %+v", caller.frames)
			}
		})
	}
}

func TestP093D09ExpiredRecordedIntentIsNotDispatched(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 9, 27, 13, 0, 0, 0, time.UTC)
	now := base
	authority := p093Authority(t, &now)
	intent := p068SubmitIntent(t, "intent-p093-expired-recorded", "session-p093-expired-recorded", "command-p093-expired-recorded", domain.TargetKindRemote, "echo expired")
	if _, err := authority.CreateLocalIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}
	now = base.Add(store.DefaultSessionIdempotencyRetention)
	caller := &p070Caller{}
	driver, err := NewRemoteDriverWithClock(authority, caller, "router-p093", time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	result, _, err := driver.DispatchIntent(ctx, intent.IntentID)
	if !errors.Is(err, ErrRemoteIdempotencyDeadline) || result.DeliveryState != store.LocalIntentNotDelivered {
		t.Fatalf("expired recorded dispatch=%+v err=%v", result, err)
	}
	if len(caller.frames) != 0 {
		t.Fatalf("expired recorded intent reached remote target: %+v", caller.frames)
	}
}

func p093Authority(t *testing.T, now *time.Time) *store.AuthorityStore {
	t.Helper()
	db, err := store.Open(context.Background(), testfixture.New(t).Path()+"/state/p093.db")
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
