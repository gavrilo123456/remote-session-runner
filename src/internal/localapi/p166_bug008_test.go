package localapi

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/mailbox"
	"remote-session-runner/src/internal/store"
)

func TestBUG008AcceptedRemoteRunProjectsNonterminalStatusOnceWithoutOutput(t *testing.T) {
	ctx := context.Background()
	h, _ := newP096Harness(t)
	requestID := "req-bug008-active-projection"
	p101Import(t, h, requestID, p100RunRequest(requestID, "key-bug008-active-projection", "remote", "linux-host", "linux-dev", "printf bug008"))
	initial := p101ReadResponse(t, h, requestID)
	intent := bug008RunIntent(t, h, initial.JobID)
	p101SetIntentDelivery(t, h, intent, store.LocalIntentAccepted)

	projection := bug008ActiveRemoteJobProjection(t, intent, time.Date(2026, 9, 27, 11, 0, 0, 0, time.UTC))
	projection.Phase = store.JobPhaseCreatingSession
	projection.CommandState = nil
	if _, err := h.authority.UpsertRemoteJobProjection(ctx, projection); err != nil {
		t.Fatal(err)
	}
	if err := h.processor.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	active := p101ReadResponse(t, h, requestID)
	if active.RequestState != "accepted" || active.ResponseRevision != initial.ResponseRevision+1 ||
		active.DeliveryState != string(store.LocalIntentAccepted) || active.JobID != initial.JobID ||
		active.SessionID != initial.SessionID || active.CommandID != initial.CommandID ||
		active.JobPhase != string(store.JobPhaseCreatingSession) || active.CommandState != "" {
		t.Fatalf("accepted active projection=%+v, initial=%+v", active, initial)
	}
	bug008AssertAcceptedRunHasNoResultFields(t, h, requestID, true)
	firstBytes, err := h.outbox.Read(requestID)
	if err != nil {
		t.Fatal(err)
	}

	// A later target observation with the same phase/state must not replace the
	// accepted response. The active wire projection deliberately omits the
	// volatile observed_at value.
	projection.ObservedAt = projection.ObservedAt.Add(time.Minute)
	if _, err := h.authority.UpsertRemoteJobProjection(ctx, projection); err != nil {
		t.Fatal(err)
	}
	if err := h.processor.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	secondBytes, err := h.outbox.Read(requestID)
	if err != nil || !bytes.Equal(firstBytes, secondBytes) {
		t.Fatalf("unchanged active projection churned response: equal=%v err=%v", bytes.Equal(firstBytes, secondBytes), err)
	}
	if got := p101ReadResponse(t, h, requestID); got.ResponseRevision != active.ResponseRevision {
		t.Fatalf("unchanged active projection revision=%d, want %d", got.ResponseRevision, active.ResponseRevision)
	}

	// Each actual remote lifecycle change gets exactly one new accepted
	// revision. The wire response retains only nonterminal progress labels.
	projection.Phase = store.JobPhaseAcceptingCommand
	projection.ObservedAt = projection.ObservedAt.Add(time.Minute)
	if _, err := h.authority.UpsertRemoteJobProjection(ctx, projection); err != nil {
		t.Fatal(err)
	}
	if err := h.processor.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	acceptingResponse := p101ReadResponse(t, h, requestID)
	if acceptingResponse.ResponseRevision != active.ResponseRevision+1 || acceptingResponse.JobPhase != string(store.JobPhaseAcceptingCommand) ||
		acceptingResponse.CommandState != "" {
		t.Fatalf("accepting active projection=%+v, previous=%+v", acceptingResponse, active)
	}
	bug008AssertAcceptedRunHasNoResultFields(t, h, requestID, true)

	queued := domain.CommandStateQueued
	projection.Phase = store.JobPhaseAwaitingCommand
	projection.CommandState = &queued
	projection.ObservedAt = projection.ObservedAt.Add(time.Minute)
	if _, err := h.authority.UpsertRemoteJobProjection(ctx, projection); err != nil {
		t.Fatal(err)
	}
	if err := h.processor.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	awaitingResponse := p101ReadResponse(t, h, requestID)
	if awaitingResponse.ResponseRevision != acceptingResponse.ResponseRevision+1 || awaitingResponse.JobPhase != string(store.JobPhaseAwaitingCommand) ||
		awaitingResponse.CommandState != string(domain.CommandStateQueued) {
		t.Fatalf("awaiting active projection=%+v, previous=%+v", awaitingResponse, acceptingResponse)
	}
	bug008AssertAcceptedRunHasNoResultFields(t, h, requestID, true)

	running := domain.CommandStateRunning
	projection.CommandState = &running
	projection.ObservedAt = projection.ObservedAt.Add(time.Minute)
	if _, err := h.authority.UpsertRemoteJobProjection(ctx, projection); err != nil {
		t.Fatal(err)
	}
	if err := h.processor.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	runningResponse := p101ReadResponse(t, h, requestID)
	if runningResponse.ResponseRevision != awaitingResponse.ResponseRevision+1 || runningResponse.JobPhase != string(store.JobPhaseAwaitingCommand) ||
		runningResponse.CommandState != string(domain.CommandStateRunning) {
		t.Fatalf("running active projection=%+v, previous=%+v", runningResponse, active)
	}
	bug008AssertAcceptedRunHasNoResultFields(t, h, requestID, true)

	cancelling := domain.CommandStateCancelling
	projection.CommandState = &cancelling
	projection.ObservedAt = projection.ObservedAt.Add(time.Minute)
	if _, err := h.authority.UpsertRemoteJobProjection(ctx, projection); err != nil {
		t.Fatal(err)
	}
	if err := h.processor.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	cancellingResponse := p101ReadResponse(t, h, requestID)
	if cancellingResponse.ResponseRevision != runningResponse.ResponseRevision+1 || cancellingResponse.JobPhase != string(store.JobPhaseAwaitingCommand) ||
		cancellingResponse.CommandState != string(domain.CommandStateCancelling) {
		t.Fatalf("cancelling active projection=%+v, previous=%+v", cancellingResponse, runningResponse)
	}
	bug008AssertAcceptedRunHasNoResultFields(t, h, requestID, true)

	// The remote coordinator may be closing a one-off session after the command
	// has a terminal state. Before strict terminal proof, expose the nonterminal
	// cleanup phase only; never leak the terminal command result.
	succeeded := domain.CommandStateSucceeded
	projection.Phase = store.JobPhaseClosingSession
	projection.CommandState = &succeeded
	projection.ObservedAt = projection.ObservedAt.Add(time.Minute)
	if _, err := h.authority.UpsertRemoteJobProjection(ctx, projection); err != nil {
		t.Fatal(err)
	}
	if err := h.processor.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	closingResponse := p101ReadResponse(t, h, requestID)
	if closingResponse.ResponseRevision != cancellingResponse.ResponseRevision+1 || closingResponse.JobPhase != string(store.JobPhaseClosingSession) ||
		closingResponse.CommandState != "" {
		t.Fatalf("closing active projection=%+v, previous=%+v", closingResponse, cancellingResponse)
	}
	bug008AssertAcceptedRunHasNoResultFields(t, h, requestID, true)
	closingBytes, err := h.outbox.Read(requestID)
	if err != nil {
		t.Fatal(err)
	}

	projection.ObservedAt = projection.ObservedAt.Add(time.Minute)
	if _, err := h.authority.UpsertRemoteJobProjection(ctx, projection); err != nil {
		t.Fatal(err)
	}
	if err := h.processor.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if repeatBytes, repeatErr := h.outbox.Read(requestID); repeatErr != nil || !bytes.Equal(closingBytes, repeatBytes) {
		t.Fatalf("unchanged closing projection churned response: equal=%v err=%v", bytes.Equal(closingBytes, repeatBytes), repeatErr)
	}

	// A terminal remote row remains hidden until the existing strict terminal
	// proof is recorded. The active receipt is withdrawn to the IDs-only
	// accepted shape, rather than leaving an old cleanup phase that could be
	// mistaken for a current target status.
	p101CompleteRemoteRun(t, h, intent, "bug008 terminal\n")
	if err := h.processor.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	beforeProofResponse := p101ReadResponse(t, h, requestID)
	if beforeProofResponse.RequestState != "accepted" || beforeProofResponse.DeliveryState != string(store.LocalIntentAccepted) ||
		beforeProofResponse.ResponseRevision != closingResponse.ResponseRevision+1 || beforeProofResponse.JobPhase != "" ||
		beforeProofResponse.CommandState != "" || beforeProofResponse.Error != nil {
		t.Fatalf("unproven terminal remote row was not withdrawn: %+v", beforeProofResponse)
	}
	bug008AssertAcceptedRunHasNoResultFields(t, h, requestID, true)
	beforeProofBytes, err := h.outbox.Read(requestID)
	if err != nil || bytes.Equal(closingBytes, beforeProofBytes) {
		t.Fatalf("unproven terminal remote row did not replace active receipt: equal=%v err=%v", bytes.Equal(closingBytes, beforeProofBytes), err)
	}
	if err := h.processor.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if repeatBytes, repeatErr := h.outbox.Read(requestID); repeatErr != nil || !bytes.Equal(beforeProofBytes, repeatBytes) {
		t.Fatalf("unproven terminal receipt churned after withdrawal: equal=%v err=%v", bytes.Equal(beforeProofBytes, repeatBytes), repeatErr)
	}
	p101SetIntentDelivery(t, h, intent, store.LocalIntentReconciled)
	p101MarkRemoteTerminalProof(t, h, intent)
	if err := h.processor.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	terminal := p101ReadResponse(t, h, requestID)
	if terminal.RequestState != "complete" || terminal.ResponseRevision <= beforeProofResponse.ResponseRevision ||
		terminal.JobPhase != string(store.JobPhaseComplete) || terminal.CommandState != string(domain.CommandStateSucceeded) ||
		terminal.TeardownOutcome != "closed" || terminal.ExitCode == nil || *terminal.ExitCode != 0 ||
		terminal.OutputComplete == nil || !*terminal.OutputComplete || terminal.FinalEventSequence == nil ||
		terminal.AvailableEventSequence == nil || *terminal.FinalEventSequence != *terminal.AvailableEventSequence ||
		terminal.EventsFile != "events/"+initial.CommandID+".ndjson" {
		t.Fatalf("strict terminal response changed=%+v", terminal)
	}
}

func TestBUG008ActiveRemoteRunProjectionHidesMismatchedOrStaleRows(t *testing.T) {
	for _, fixture := range []struct {
		name   string
		mutate func(*testing.T, *store.RemoteJobProjection)
	}{
		{
			name: "job",
			mutate: func(t *testing.T, projection *store.RemoteJobProjection) {
				t.Helper()
				value, err := domain.NewJobID("job-bug008-wrong-owner")
				if err != nil {
					t.Fatal(err)
				}
				projection.JobID = value
			},
		},
		{
			name: "session",
			mutate: func(t *testing.T, projection *store.RemoteJobProjection) {
				t.Helper()
				value, err := domain.NewSessionID("sess-bug008-wrong-owner")
				if err != nil {
					t.Fatal(err)
				}
				projection.SessionID = value
			},
		},
		{
			name: "command",
			mutate: func(t *testing.T, projection *store.RemoteJobProjection) {
				t.Helper()
				value, err := domain.NewCommandID("cmd-bug008-wrong-owner")
				if err != nil {
					t.Fatal(err)
				}
				projection.CommandID = value
			},
		},
		{
			name: "target",
			mutate: func(t *testing.T, projection *store.RemoteJobProjection) {
				t.Helper()
				value, err := domain.NewExecutionTarget(domain.TargetKindRemote, "other-host")
				if err != nil {
					t.Fatal(err)
				}
				projection.Target = value
			},
		},
		{
			name: "controller-id",
			mutate: func(t *testing.T, projection *store.RemoteJobProjection) {
				t.Helper()
				id, err := domain.NewControllerID("other-user")
				if err != nil {
					t.Fatal(err)
				}
				value, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, id)
				if err != nil {
					t.Fatal(err)
				}
				projection.Controller = value
			},
		},
		{
			name: "controller-type",
			mutate: func(t *testing.T, projection *store.RemoteJobProjection) {
				t.Helper()
				value, err := domain.NewControllerIdentity(domain.ControllerTypeQueuedMac, projection.Controller.ID())
				if err != nil {
					t.Fatal(err)
				}
				projection.Controller = value
			},
		},
		{
			name: "environment",
			mutate: func(_ *testing.T, projection *store.RemoteJobProjection) {
				projection.Environment = "other-environment"
			},
		},
		{
			name: "source",
			mutate: func(t *testing.T, projection *store.RemoteJobProjection) {
				t.Helper()
				value, err := domain.NewGitRevisionSource("other-repository", "0123456789abcdef")
				if err != nil {
					t.Fatal(err)
				}
				projection.Source = value
			},
		},
	} {
		fixture := fixture
		t.Run("mismatched/"+fixture.name, func(t *testing.T) {
			ctx := context.Background()
			h, _ := newP096Harness(t)
			requestID := "req-bug008-mismatched-" + fixture.name
			p101Import(t, h, requestID, p100RunRequest(requestID, "key-bug008-mismatched-"+fixture.name, "remote", "linux-host", "linux-dev", "printf bug008"))
			initial := p101ReadResponse(t, h, requestID)
			intent := bug008RunIntent(t, h, initial.JobID)
			p101SetIntentDelivery(t, h, intent, store.LocalIntentAccepted)

			projection := bug008ActiveRemoteJobProjection(t, intent, time.Date(2026, 9, 27, 11, 1, 0, 0, time.UTC))
			fixture.mutate(t, &projection)
			if _, err := h.authority.UpsertRemoteJobProjection(ctx, projection); err != nil {
				t.Fatal(err)
			}
			if err := h.processor.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			response := p101ReadResponse(t, h, requestID)
			if response.RequestState != "accepted" || response.DeliveryState != string(store.LocalIntentAccepted) ||
				response.JobPhase != "" || response.CommandState != "" {
				t.Fatalf("mismatched %s projection was exposed: %+v", fixture.name, response)
			}
			bug008AssertAcceptedRunHasNoResultFields(t, h, requestID, false)
			firstBytes, err := h.outbox.Read(requestID)
			if err != nil {
				t.Fatal(err)
			}
			if err := h.processor.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			secondBytes, err := h.outbox.Read(requestID)
			if err != nil || !bytes.Equal(firstBytes, secondBytes) {
				t.Fatalf("mismatched %s projection changed accepted response: equal=%v err=%v", fixture.name, bytes.Equal(firstBytes, secondBytes), err)
			}
		})
	}

	t.Run("stale", func(t *testing.T) {
		ctx := context.Background()
		h, _ := newP096Harness(t)
		requestID := "req-bug008-stale-projection"
		p101Import(t, h, requestID, p100RunRequest(requestID, "key-bug008-stale-projection", "remote", "linux-host", "linux-dev", "printf bug008"))
		initial := p101ReadResponse(t, h, requestID)
		intent := bug008RunIntent(t, h, initial.JobID)
		p101SetIntentDelivery(t, h, intent, store.LocalIntentAccepted)

		projection := bug008ActiveRemoteJobProjection(t, intent, time.Date(2026, 9, 27, 11, 2, 0, 0, time.UTC))
		if _, err := h.authority.UpsertRemoteJobProjection(ctx, projection); err != nil {
			t.Fatal(err)
		}
		if err := h.processor.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		active := p101ReadResponse(t, h, requestID)
		if active.JobPhase != string(store.JobPhaseAwaitingCommand) || active.CommandState != string(domain.CommandStateQueued) {
			t.Fatalf("fresh active projection was not published: %+v", active)
		}
		if err := h.authority.MarkRemoteJobProjectionStale(ctx, intent.JobID); err != nil {
			t.Fatal(err)
		}
		if err := h.processor.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		response := p101ReadResponse(t, h, requestID)
		if response.RequestState != "accepted" || response.DeliveryState != string(store.LocalIntentAccepted) ||
			response.JobPhase != "" || response.CommandState != "" || response.ResponseRevision != active.ResponseRevision+1 {
			t.Fatalf("stale projection was not withdrawn: %+v", response)
		}
		bug008AssertAcceptedRunHasNoResultFields(t, h, requestID, true)
		firstBytes, err := h.outbox.Read(requestID)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.processor.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		secondBytes, err := h.outbox.Read(requestID)
		if err != nil || !bytes.Equal(firstBytes, secondBytes) {
			t.Fatalf("stale projection withdrawal churned response: equal=%v err=%v", bytes.Equal(firstBytes, secondBytes), err)
		}
	})

	t.Run("status unavailable", func(t *testing.T) {
		ctx := context.Background()
		h, _ := newP096Harness(t)
		requestID := "req-bug008-unavailable-projection"
		p101Import(t, h, requestID, p100RunRequest(requestID, "key-bug008-unavailable-projection", "remote", "linux-host", "linux-dev", "printf bug008"))
		initial := p101ReadResponse(t, h, requestID)
		intent := bug008RunIntent(t, h, initial.JobID)
		p101SetIntentDelivery(t, h, intent, store.LocalIntentAccepted)
		projection := bug008ActiveRemoteJobProjection(t, intent, time.Date(2026, 9, 27, 11, 3, 0, 0, time.UTC))
		if _, err := h.authority.UpsertRemoteJobProjection(ctx, projection); err != nil {
			t.Fatal(err)
		}
		if err := h.processor.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		active := p101ReadResponse(t, h, requestID)
		if active.JobPhase == "" || active.CommandState == "" {
			t.Fatalf("fresh active projection was not published: %+v", active)
		}
		if _, err := h.authority.MarkAcceptedRemoteRunStatusFailure(ctx, intent.IntentID, store.RemoteStatusFailureCodeUnavailable); err != nil {
			t.Fatal(err)
		}
		if err := h.processor.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		withdrawn := p101ReadResponse(t, h, requestID)
		if withdrawn.RequestState != "accepted" || withdrawn.DeliveryState != string(store.LocalIntentAccepted) ||
			withdrawn.JobPhase != "" || withdrawn.CommandState != "" || withdrawn.ResponseRevision != active.ResponseRevision+1 || withdrawn.Error != nil {
			t.Fatalf("unavailable active projection was not withdrawn: %+v", withdrawn)
		}
		bug008AssertAcceptedRunHasNoResultFields(t, h, requestID, true)
		firstBytes, err := h.outbox.Read(requestID)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.processor.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		secondBytes, err := h.outbox.Read(requestID)
		if err != nil || !bytes.Equal(firstBytes, secondBytes) {
			t.Fatalf("unavailable projection withdrawal churned response: equal=%v err=%v", bytes.Equal(firstBytes, secondBytes), err)
		}
	})

	t.Run("retryable snapshot read", func(t *testing.T) {
		ctx := context.Background()
		h, _ := newP096Harness(t)
		operations := &bug008RetryableSnapshotOperations{SessionOperations: h.server}
		h.processor = p099NewProcessor(t, h, operations)
		requestID := "req-bug008-retryable-snapshot"
		p101Import(t, h, requestID, p100RunRequest(requestID, "key-bug008-retryable-snapshot", "remote", "linux-host", "linux-dev", "printf bug008"))
		initial := p101ReadResponse(t, h, requestID)
		intent := bug008RunIntent(t, h, initial.JobID)
		p101SetIntentDelivery(t, h, intent, store.LocalIntentAccepted)
		projection := bug008ActiveRemoteJobProjection(t, intent, time.Date(2026, 9, 27, 11, 4, 0, 0, time.UTC))
		if _, err := h.authority.UpsertRemoteJobProjection(ctx, projection); err != nil {
			t.Fatal(err)
		}
		if err := h.processor.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		active := p101ReadResponse(t, h, requestID)
		if active.JobPhase == "" || active.CommandState == "" {
			t.Fatalf("fresh active projection was not published: %+v", active)
		}

		operations.failGetRunSnapshot = true
		if err := h.processor.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		withdrawn := p101ReadResponse(t, h, requestID)
		if withdrawn.RequestState != "accepted" || withdrawn.DeliveryState != string(store.LocalIntentAccepted) ||
			withdrawn.JobPhase != "" || withdrawn.CommandState != "" || withdrawn.Error != nil ||
			withdrawn.ResponseRevision != active.ResponseRevision+1 {
			t.Fatalf("retryable snapshot failure was not withdrawn: %+v", withdrawn)
		}
		bug008AssertAcceptedRunHasNoResultFields(t, h, requestID, true)
		firstBytes, err := h.outbox.Read(requestID)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.processor.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		secondBytes, err := h.outbox.Read(requestID)
		if err != nil || !bytes.Equal(firstBytes, secondBytes) {
			t.Fatalf("retryable snapshot withdrawal churned response: equal=%v err=%v", bytes.Equal(firstBytes, secondBytes), err)
		}

		operations.failGetRunSnapshot = false
		if err := h.processor.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		refreshed := p101ReadResponse(t, h, requestID)
		if refreshed.JobPhase != string(store.JobPhaseAwaitingCommand) || refreshed.CommandState != string(domain.CommandStateQueued) ||
			refreshed.ResponseRevision != withdrawn.ResponseRevision+1 {
			t.Fatalf("fresh snapshot did not restore active projection: %+v", refreshed)
		}
		bug008AssertAcceptedRunHasNoResultFields(t, h, requestID, true)
	})
}

func bug008RunIntent(t *testing.T, h *p095Harness, jobID string) store.LocalIntentRecord {
	t.Helper()
	intent, err := h.authority.GetLocalIntentByResource(context.Background(), "run", jobID, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	return intent
}

func bug008ActiveRemoteJobProjection(t *testing.T, intent store.LocalIntentRecord, observedAt time.Time) store.RemoteJobProjection {
	t.Helper()
	target, err := domain.NewExecutionTarget(domain.TargetKindRemote, intent.Target.Profile())
	if err != nil {
		t.Fatal(err)
	}
	state := domain.CommandStateQueued
	return store.RemoteJobProjection{
		JobID: intent.JobID, SessionID: intent.SessionID, CommandID: intent.CommandID,
		Phase: store.JobPhaseAwaitingCommand, CommandState: &state, TeardownState: store.JobTeardownPending,
		Target: target, Controller: intent.Controller, Environment: intent.Environment, Source: intent.Source,
		Capabilities: p076APICapabilities(), ObservedAt: observedAt,
	}
}

func bug008AssertAcceptedRunHasNoResultFields(t *testing.T, h *p095Harness, requestID string, requireNoObservedAt bool) {
	t.Helper()
	raw, err := h.outbox.Read(requestID)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		"exit_code", "stdout", "stderr", "final_event_sequence", "available_event_sequence",
		"output_complete", "output_truncated", "output_unavailable_reason", "events_file", "teardown_outcome", "error",
	} {
		if _, present := wire[field]; present {
			t.Fatalf("accepted active response exposes %q: %s", field, raw)
		}
	}
	if requireNoObservedAt {
		if _, present := wire["observed_at"]; present {
			t.Fatalf("accepted active response exposes observed_at: %s", raw)
		}
	}
}

type bug008RetryableSnapshotOperations struct {
	mailbox.SessionOperations
	failGetRunSnapshot bool
}

func (o *bug008RetryableSnapshotOperations) GetRunSnapshot(ctx context.Context, jobID string) (mailbox.RunSnapshot, error) {
	if o.failGetRunSnapshot {
		return mailbox.RunSnapshot{}, &mailbox.SessionOperationError{
			Code: "runtime_unavailable", Message: "test snapshot unavailable", Retryable: true,
		}
	}
	return o.SessionOperations.GetRunSnapshot(ctx, jobID)
}
