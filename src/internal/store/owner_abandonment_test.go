package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/testfixture"
)

func TestOwnerAbandonmentClosesOnlyExactExpiredUncertainRunWithoutClaimingOutcome(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 23, 22, 0, 0, 0, time.UTC)
	authority, clock := ownerAbandonmentFixture(t, &now, "req-owner-closeout")
	ref, intent := clock.ref, clock.intent

	// Expired uncertainty is still an unknown target outcome. Administrative
	// closeout records that fact and prevents this local intent being dispatched
	// again; it does not add target events or success data.
	*clock.now = clock.now.Add(2 * time.Minute)
	record, closed, err := authority.CloseUncertainRemoteRunAsIndeterminateInMailbox(
		ctx, ref, intent.IntentID, intent.JobID, intent.SessionID, intent.CommandID,
	)
	if err != nil || !closed {
		t.Fatalf("closeout record=%+v closed=%t err=%v", record, closed, err)
	}
	if record.State != MailboxExchangeIndeterminate || record.ResponseRevision != 3 || record.AvailableEventSequence != nil ||
		len(record.TerminalResponseBytes) == 0 || string(record.ResponseBytes) != string(record.TerminalResponseBytes) {
		t.Fatalf("terminal exchange=%+v; want immutable indeterminate response revision 3 without events", record)
	}
	var response struct {
		RequestID        string `json:"request_id"`
		RequestState     string `json:"request_state"`
		ResponseRevision int64  `json:"response_revision"`
		JobID            string `json:"job_id"`
		SessionID        string `json:"session_id"`
		CommandID        string `json:"command_id"`
		DeliveryState    string `json:"delivery_state"`
		ExitCode         *int   `json:"exit_code"`
		Stdout           string `json:"stdout"`
		Stderr           string `json:"stderr"`
		AvailableCursor  *int64 `json:"available_event_sequence"`
		Error            *struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			Retryable bool   `json:"retryable"`
		} `json:"error"`
	}
	if err := json.Unmarshal(record.ResponseBytes, &response); err != nil {
		t.Fatal(err)
	}
	if response.RequestID != ref.ClientRequestID || response.RequestState != string(MailboxExchangeIndeterminate) || response.ResponseRevision != 3 ||
		response.JobID != string(intent.JobID) || response.SessionID != string(intent.SessionID) || response.CommandID != string(intent.CommandID) ||
		response.DeliveryState != string(LocalIntentUncertain) || response.ExitCode != nil || response.Stdout != "" || response.Stderr != "" ||
		response.AvailableCursor != nil || response.Error == nil || response.Error.Code != MailboxOwnerAbandonedCode ||
		response.Error.Message != MailboxOwnerAbandonedMessage || response.Error.Retryable {
		t.Fatalf("administrative response invented target outcome: %+v bytes=%s", response, record.ResponseBytes)
	}

	closedIntent, err := authority.GetLocalIntent(ctx, intent.IntentID)
	if err != nil || closedIntent.DeliveryState != LocalIntentReconciled || closedIntent.Reason != MailboxOwnerAbandonedReason ||
		closedIntent.LeaseOwner != "" || closedIntent.LeaseExpiresAt != nil || closedIntent.AttemptCount != 1 || closedIntent.RemoteTerminalProofVersion != 0 {
		t.Fatalf("administratively closed intent=%+v err=%v", closedIntent, err)
	}
	lifecycle, err := authority.ListLocalIntentLifecycle(ctx, intent.IntentID)
	if err != nil || len(lifecycle) != 4 || lifecycle[3].PreviousState == nil || *lifecycle[3].PreviousState != LocalIntentUncertain ||
		lifecycle[3].NewState != LocalIntentReconciled || lifecycle[3].Reason != MailboxOwnerAbandonedReason {
		t.Fatalf("closeout lifecycle=%+v err=%v", lifecycle, err)
	}
	if eligible, err := authority.ListEligibleLocalIntents(ctx, 10); err != nil || len(eligible) != 0 {
		t.Fatalf("eligible after owner closeout=%+v err=%v; closed work must no longer dispatch", eligible, err)
	}
	retry, closed, err := authority.CloseUncertainRemoteRunAsIndeterminateInMailbox(
		ctx, ref, intent.IntentID, intent.JobID, intent.SessionID, intent.CommandID,
	)
	if err != nil || closed || string(retry.TerminalResponseBytes) != string(record.TerminalResponseBytes) || retry.ResponseRevision != record.ResponseRevision {
		t.Fatalf("idempotent closeout changed terminal result: record=%+v closed=%t err=%v", retry, closed, err)
	}
}

func TestOwnerAbandonmentRejectsActiveLeaseAndInconsistentEvidence(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *AuthorityStore, *ownerAbandonmentTestClock)
	}{
		{
			name: "lease still active",
			mutate: func(t *testing.T, _ *AuthorityStore, clock *ownerAbandonmentTestClock) {
				*clock.now = clock.leaseExpiry.Add(-time.Second)
			},
		},
		{
			name: "accepted response identity mismatch",
			mutate: func(t *testing.T, authority *AuthorityStore, clock *ownerAbandonmentTestClock) {
				wrong := []byte(`{"request_id":"req-other","operation":"run","request_state":"accepted","response_revision":3,"job_id":"job-other","session_id":"sess-other","command_id":"cmd-other","delivery_state":"uncertain"}`)
				if _, err := authority.PublishMailboxResponseInMailbox(context.Background(), clock.ref, MailboxResponsePublication{State: MailboxExchangeAccepted, Bytes: wrong}); err != nil {
					t.Fatal(err)
				}
				*clock.now = clock.leaseExpiry.Add(time.Second)
			},
		},
		{
			name: "target proof appeared",
			mutate: func(t *testing.T, authority *AuthorityStore, clock *ownerAbandonmentTestClock) {
				if _, err := authority.db.Exec(`UPDATE local_intents SET remote_terminal_proof_version = ? WHERE intent_id = ?`, RemoteTerminalProofP149, string(clock.intent.IntentID)); err != nil {
					t.Fatal(err)
				}
				*clock.now = clock.leaseExpiry.Add(time.Second)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 10, 23, 22, 0, 0, 0, time.UTC)
			authority, clock := ownerAbandonmentFixture(t, &now, "req-owner-closeout-unsafe")
			test.mutate(t, authority, clock)
			record, closed, err := authority.CloseUncertainRemoteRunAsIndeterminateInMailbox(
				context.Background(), clock.ref, clock.intent.IntentID, clock.intent.JobID, clock.intent.SessionID, clock.intent.CommandID,
			)
			if !errors.Is(err, ErrMailboxOwnerAbandonmentNotEligible) || closed || record.ExchangeID != "" {
				t.Fatalf("unsafe closeout record=%+v closed=%t err=%v", record, closed, err)
			}
		})
	}
}

type ownerAbandonmentTestClock struct {
	now         *time.Time
	leaseExpiry time.Time
	ref         MailboxExchangeRef
	intent      LocalIntentRecord
}

func ownerAbandonmentFixture(t *testing.T, now *time.Time, requestID string) (*AuthorityStore, *ownerAbandonmentTestClock) {
	t.Helper()
	ctx := context.Background()
	root := testfixture.New(t)
	db, err := Open(ctx, root.Path()+"/state/owner-abandonment.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	authority, err := NewAuthorityStoreWithClock(db, func() time.Time { return *now })
	if err != nil {
		t.Fatal(err)
	}
	ref, err := NewMailboxExchangeRef("slidestud-io", requestID)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	target, err := domain.NewExecutionTarget(domain.TargetKindRemote, "sandbox-host")
	if err != nil {
		t.Fatal(err)
	}
	jobID, sessionID, commandID := domain.JobID("job-p168-owner"), domain.SessionID("sess-p168-owner"), domain.CommandID("cmd-p168-owner")
	clientKey := "key-" + requestID
	executionKey := mailboxExecutionIdempotencyKey(ref.MailboxID, clientKey)
	request := []byte(`{"operation":"run","environment":"sandbox-host-qas","execution_target":{"kind":"remote","profile":"sandbox-host"},"source":{"mode":"empty"},"script":"printf owner-closeout","timeout_seconds":30}`)
	canonical, err := domain.CanonicalizeMutationRequestJSON("run", request, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("run", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	selection := &MailboxExecutionSelection{
		ContextName: "sandbox-qas", Environment: "sandbox-host-qas", Target: target,
		Source: MailboxExecutionSelectionInboxDefault,
	}
	if _, _, err := authority.AcceptMailboxExchangeInMailbox(ctx, ref, MailboxExchangeCreate{
		RequestID: requestID, Operation: "run", Controller: controller, IdempotencyKey: clientKey,
		ExecutionIdempotencyKey: executionKey, RequestHash: hash, CanonicalPayload: canonical,
		ResourceID: string(jobID), Selection: selection, SelectionState: MailboxExecutionSelectionResolved,
	}); err != nil {
		t.Fatal(err)
	}
	intent, err := authority.CreateLocalIntent(ctx, LocalIntentCreate{
		IntentID: domain.IntentID("intent-p168-owner"), Operation: "run", ResourceID: string(jobID), JobID: jobID,
		SessionID: sessionID, CommandID: commandID, Target: target, Environment: "sandbox-host-qas", Controller: controller,
		Source: domain.NewEmptySource(), RequestHash: hash, IdempotencyKey: executionKey, PayloadJSON: canonical,
		ScriptBytes: []byte("printf owner-closeout"),
	})
	if err != nil {
		t.Fatal(err)
	}
	initial := []byte(`{"request_id":"` + requestID + `","operation":"run","request_state":"accepted","response_revision":1,"job_id":"` + string(jobID) + `","session_id":"` + string(sessionID) + `","command_id":"` + string(commandID) + `","delivery_state":"recorded","execution_selection_source":"inbox_default","resolved_environment":"sandbox-host-qas","resolved_execution_target":{"kind":"remote","profile":"sandbox-host"}}`)
	if _, err := authority.PublishMailboxResponseInMailbox(ctx, ref, MailboxResponsePublication{State: MailboxExchangeAccepted, Bytes: initial}); err != nil {
		t.Fatal(err)
	}
	lease, err := authority.ClaimLocalIntent(ctx, intent.IntentID, "mac-router-test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	uncertain, err := authority.TransitionLocalIntent(ctx, lease.IntentID, LocalIntentUncertain, "remote_transport_uncertain")
	if err != nil {
		t.Fatal(err)
	}
	uncertainResponse := []byte(`{"request_id":"` + requestID + `","operation":"run","request_state":"accepted","response_revision":2,"job_id":"` + string(jobID) + `","session_id":"` + string(sessionID) + `","command_id":"` + string(commandID) + `","delivery_state":"uncertain","execution_selection_source":"inbox_default","resolved_environment":"sandbox-host-qas","resolved_execution_target":{"kind":"remote","profile":"sandbox-host"}}`)
	if _, err := authority.PublishMailboxResponseInMailbox(ctx, ref, MailboxResponsePublication{State: MailboxExchangeAccepted, Bytes: uncertainResponse}); err != nil {
		t.Fatal(err)
	}
	return authority, &ownerAbandonmentTestClock{now: now, leaseExpiry: *uncertain.LeaseExpiresAt, ref: ref, intent: intent}
}
