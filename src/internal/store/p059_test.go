package store

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"remote-session-runner/src/internal/testfixture"
)

func TestP059ConcurrentSubmitIntentsAllocateUniqueOrdinals(t *testing.T) {
	root := testfixture.New(t)
	db, err := Open(context.Background(), root.Path()+"/state/p059-ordinals.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	first := p059SubmitWithoutOrdinal(t, "intent-p059-ordinal-1", "session-p059-ordinal", "command-p059-ordinal-1", "key-p059-ordinal-1", "echo one")
	second := p059SubmitWithoutOrdinal(t, "intent-p059-ordinal-2", "session-p059-ordinal", "command-p059-ordinal-2", "key-p059-ordinal-2", "echo two")
	start := make(chan struct{})
	results := make(chan LocalIntentRecord, 2)
	errorsCh := make(chan error, 2)
	var wait sync.WaitGroup
	for _, input := range []LocalIntentCreate{first, second} {
		input := input
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			record, err := store.CreateLocalIntent(context.Background(), input)
			if err != nil {
				errorsCh <- err
				return
			}
			results <- record
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(errorsCh)
	for err := range errorsCh {
		t.Fatal(err)
	}
	ordinals := make([]int64, 0, 2)
	for record := range results {
		if record.IntentOrdinal == nil {
			t.Fatalf("allocated record has no intent ordinal: %+v", record)
		}
		ordinals = append(ordinals, *record.IntentOrdinal)
	}
	sort.Slice(ordinals, func(i, j int) bool { return ordinals[i] < ordinals[j] })
	if len(ordinals) != 2 || ordinals[0] != 1 || ordinals[1] != 2 {
		t.Fatalf("allocated ordinals = %v, want [1 2]", ordinals)
	}
}

func TestP059OrderedEligibilityBlocksUnsettledPredecessor(t *testing.T) {
	root := testfixture.New(t)
	db, err := Open(context.Background(), root.Path()+"/state/p059-order.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.CreateLocalIntent(context.Background(), p059SubmitWithoutOrdinal(t, "intent-p059-order-1", "session-p059-order", "command-p059-order-1", "key-p059-order-1", "echo one"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateLocalIntent(context.Background(), p059SubmitWithoutOrdinal(t, "intent-p059-order-2", "session-p059-order", "command-p059-order-2", "key-p059-order-2", "echo two"))
	if err != nil {
		t.Fatal(err)
	}
	eligible, err := store.ListEligibleLocalIntents(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(eligible) != 1 || eligible[0].IntentID != first.IntentID {
		t.Fatalf("initial eligible = %+v, want first only", eligible)
	}
	claimed, err := store.ClaimNextLocalIntent(context.Background(), "router-a", time.Minute)
	if err != nil || claimed.IntentID != first.IntentID {
		t.Fatalf("first claim = %+v err=%v", claimed, err)
	}
	eligible, err = store.ListEligibleLocalIntents(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(eligible) != 0 {
		t.Fatalf("eligible while predecessor dispatching = %+v, want empty", eligible)
	}
	if _, err := store.TransitionLocalIntent(context.Background(), first.IntentID, LocalIntentAccepted, "target_accepted"); err != nil {
		t.Fatal(err)
	}
	eligible, err = store.ListEligibleLocalIntents(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(eligible) != 0 {
		t.Fatalf("eligible while predecessor accepted but unreconciled = %+v, want empty", eligible)
	}
	if _, err := store.TransitionLocalIntent(context.Background(), first.IntentID, LocalIntentReconciled, "events_reconciled"); err != nil {
		t.Fatal(err)
	}
	eligible, err = store.ListEligibleLocalIntents(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(eligible) != 1 || eligible[0].IntentID != second.IntentID {
		t.Fatalf("eligible after reconciliation = %+v, want second", eligible)
	}
}

func TestP059LeaseClaimRenewalRaceAndRecoveryQuery(t *testing.T) {
	root := testfixture.New(t)
	db, err := Open(context.Background(), root.Path()+"/state/p059-lease.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	store, err := NewAuthorityStoreWithClock(db, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	input := p059SubmitWithoutOrdinal(t, "intent-p059-lease", "session-p059-lease", "command-p059-lease", "key-p059-lease", "echo lease")
	if _, err := store.CreateLocalIntent(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	type claimResult struct {
		record LocalIntentRecord
		err    error
		owner  string
	}
	results := make(chan claimResult, 2)
	var wait sync.WaitGroup
	for _, owner := range []string{"router-a", "router-b"} {
		owner := owner
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			record, err := store.ClaimLocalIntent(context.Background(), input.IntentID, owner, time.Minute)
			results <- claimResult{record: record, err: err, owner: owner}
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	winner := claimResult{}
	losers := 0
	for result := range results {
		if result.err == nil {
			winner = result
			continue
		}
		if errors.Is(result.err, ErrNoEligibleLocalIntent) || errors.Is(result.err, ErrLocalIntentLeaseHeld) {
			losers++
			continue
		}
		t.Fatalf("lease race owner %s error = %v", result.owner, result.err)
	}
	if winner.err != nil || winner.record.LeaseOwner != winner.owner || losers != 1 || winner.record.AttemptCount != 1 {
		t.Fatalf("lease race winner=%+v losers=%d", winner, losers)
	}
	if _, err := store.RenewLocalIntentLease(context.Background(), input.IntentID, "wrong-owner", time.Minute); !errors.Is(err, ErrLocalIntentLeaseLost) {
		t.Fatalf("wrong-owner renewal = %v, want lease lost", err)
	}
	renewed, err := store.RenewLocalIntentLease(context.Background(), input.IntentID, winner.owner, 2*time.Minute)
	if err != nil || renewed.LeaseOwner != winner.owner || renewed.LeaseExpiresAt == nil || !renewed.LeaseExpiresAt.After(*winner.record.LeaseExpiresAt) {
		t.Fatalf("renewed lease = %+v err=%v", renewed, err)
	}
	now = now.Add(3 * time.Minute)
	// The lease is expired, but the intent remains dispatching until recovery
	// explicitly reconciles it; the query must report it without resubmitting.
	recoverable, err := store.ListRecoverableLocalIntents(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recoverable) != 1 || recoverable[0].IntentID != input.IntentID || recoverable[0].DeliveryState != LocalIntentDispatching {
		t.Fatalf("recoverable = %+v, want expired dispatching intent", recoverable)
	}
}

func p059SubmitWithoutOrdinal(t *testing.T, intentID, sessionID, commandID, key, script string) LocalIntentCreate {
	t.Helper()
	input := p057SubmitIntent(t, intentID, sessionID, commandID, key, 1, script)
	input.IntentOrdinal = nil
	return input
}
