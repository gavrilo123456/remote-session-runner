package execution

import (
	"context"
	"errors"
	"testing"

	"remote-session-runner/src/internal/domain"
	hostruntime "remote-session-runner/src/internal/runtime"
	"remote-session-runner/src/internal/store"
)

// TestBUG011PersistentBoundaryErrorsRecordLostWithoutReplay proves that a
// persistent-shell boundary is made durably truthful and cannot be replayed
// by later scheduler resume attempts.
func TestBUG011PersistentBoundaryErrorsRecordLostWithoutReplay(t *testing.T) {
	for _, test := range []struct {
		name    string
		failure error
	}{
		{name: "persistent shell exited", failure: hostruntime.ErrPersistentShellExited},
		{name: "output boundary unconfirmed", failure: hostruntime.ErrOutputBoundary},
		{name: "output callback failed", failure: hostruntime.ErrOutputCallback},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := &p020FakeRuntime{generation: "generation-bug011-boundary", commandErr: test.failure}
			service, authority, _ := newP020Service(t, runtime)
			session := p021ReadySession(t, service, "session-bug011-"+test.name, "key-bug011-"+test.name)
			request := p021SubmitRequest(t, session.SessionID, "command-bug011-"+test.name, "submit-bug011-"+test.name, "printf harmless")

			first, err := service.SubmitCommand(context.Background(), request)
			if !errors.Is(err, ErrCommandTransport) {
				t.Fatalf("boundary submit error = %v, want ErrCommandTransport", err)
			}
			if first.Command.State != domain.CommandStateLost || first.Command.OutputComplete || first.Command.OutputUnavailableReason != "capture_boundary_unconfirmed" {
				t.Fatalf("lost command = %+v", first.Command)
			}
			storedSession, err := authority.GetSession(context.Background(), session.SessionID)
			if err != nil || storedSession.State != domain.SessionStateLost {
				t.Fatalf("lost session = %+v, err = %v", storedSession, err)
			}
			events, err := authority.ListCommandEvents(context.Background(), first.Command.CommandID)
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 3 || events[0].Type != "command_queued" || events[1].Type != "command_started" || events[2].Type != "command_lost" {
				t.Fatalf("lost events = %+v", events)
			}
			if slots, err := authority.CountLiveCommandSlots(context.Background()); err != nil || slots != 1 {
				t.Fatalf("live command slots = %d, err = %v, want 1 retained", slots, err)
			}
			if reservations, err := authority.CountLiveSessionReservations(context.Background()); err != nil || reservations != 1 {
				t.Fatalf("live session reservations = %d, err = %v, want 1 retained", reservations, err)
			}

			retry, err := service.ResumeCommand(context.Background(), first.Command.CommandID, request.Controller)
			if err != nil || retry.Command.CommandID != first.Command.CommandID || retry.Command.State != domain.CommandStateLost {
				t.Fatalf("lost command resume = %+v, err = %v", retry, err)
			}
			if runtime.commandCall != 1 {
				t.Fatalf("runtime command calls = %d, want exactly one; lost script was replayed", runtime.commandCall)
			}
		})
	}
}

// TestBUG011QueuePreservingRecoveryRejectsUnsafeInventoryBeforeRuntimeOrReplay
// joins the retained-loss fixture with the existing queue-preserving recovery
// contract.  Every unsafe inventory must stop before process proof, release,
// or execution can occur.
func TestBUG011QueuePreservingRecoveryRejectsUnsafeInventoryBeforeRuntimeOrReplay(t *testing.T) {
	for _, name := range []string{
		"partial selected retained set",
		"extra direct queued session",
		"missing terminal lost event",
		"mismatched session command pair",
	} {
		t.Run(name, func(t *testing.T) {
			runtime := &p027Runtime{
				p020FakeRuntime: p020FakeRuntime{generation: "generation-bug011-inventory"},
				lostRecoveryResult: RuntimeReconcileResult{
					RuntimeGeneration: "generation-bug011-inventory",
					CleanupConfirmed:  true,
				},
				lostFinalizeResult: RuntimeReconcileResult{
					RuntimeGeneration: "generation-bug011-inventory",
					CleanupConfirmed:  true,
				},
			}
			service, authority, database := newBUG008QueuePreservingServiceWithDatabase(t, runtime)
			firstSession, firstCommand := pRecoveryLostPair(t, service, authority, "bug011-first")
			secondSession, secondCommand := pRecoveryLostPair(t, service, authority, "bug011-second")
			thirdSession, thirdCommand := pRecoveryLostPair(t, service, authority, "bug011-third")
			fourthSession, fourthCommand := pRecoveryLostPair(t, service, authority, "bug011-fourth")
			queued, before := pBUG008ExecutionQueuedOneOff(t, service, authority, "bug011-unsafe")
			requests := []LostRuntimeRecoveryRequest{
				{SessionID: firstSession.SessionID, CommandID: firstCommand.CommandID},
				{SessionID: secondSession.SessionID, CommandID: secondCommand.CommandID},
				{SessionID: thirdSession.SessionID, CommandID: thirdCommand.CommandID},
				{SessionID: fourthSession.SessionID, CommandID: fourthCommand.CommandID},
			}

			switch name {
			case "partial selected retained set":
				requests = requests[:3]
			case "extra direct queued session":
				pBUG008ExecutionDirectQueued(t, service, authority, "bug011-unsafe")
			case "missing terminal lost event":
				if _, err := database.Exec("DELETE FROM exec_command_events WHERE command_id = ? AND event_type = 'command_lost'", string(firstCommand.CommandID)); err != nil {
					t.Fatalf("delete terminal lost event: %v", err)
				}
			case "mismatched session command pair":
				requests[0].CommandID = secondCommand.CommandID
			}

			_, err := service.RecoverLostRuntimeBatchPreservingQueuedOneOffs(context.Background(), requests)
			switch name {
			case "partial selected retained set", "extra direct queued session":
				if !errors.Is(err, store.ErrLostRuntimeRecoveryNotReleasable) {
					t.Fatalf("unsafe recovery error = %v, want ErrLostRuntimeRecoveryNotReleasable", err)
				}
			case "missing terminal lost event", "mismatched session command pair":
				if !errors.Is(err, ErrLostRuntimeRecoveryIneligible) {
					t.Fatalf("unsafe recovery error = %v, want ErrLostRuntimeRecoveryIneligible", err)
				}
			}
			if runtime.lostRecoveryCall != 0 || runtime.lostFinalizeCall != 0 || runtime.commandCall != 0 {
				t.Fatalf("unsafe inventory called runtime proof=%d finalization=%d command=%d", runtime.lostRecoveryCall, runtime.lostFinalizeCall, runtime.commandCall)
			}
			for _, pair := range []LostRuntimeRecoveryRequest{
				{SessionID: firstSession.SessionID, CommandID: firstCommand.CommandID},
				{SessionID: secondSession.SessionID, CommandID: secondCommand.CommandID},
				{SessionID: thirdSession.SessionID, CommandID: thirdCommand.CommandID},
				{SessionID: fourthSession.SessionID, CommandID: fourthCommand.CommandID},
			} {
				pRecoveryAssertRetained(t, authority, pair.SessionID, pair.CommandID)
			}
			pBUG008AssertExecutionQueuedOneOffUnchanged(t, authority, queued, before)
		})
	}
}
