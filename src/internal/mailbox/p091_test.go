package mailbox

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
	"remote-session-runner/src/internal/testfixture"
)

func TestP091EarlyAckWaitsForSharedEventReferencesAndLateAckCannotRepublish(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	now := base
	root := testfixture.New(t)
	db, err := store.Open(ctx, filepath.Join(root.Path(), "state", "p091.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authority, err := store.NewAuthorityStoreWithClock(db, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	command := p091CreateTerminalCommand(t, authority)
	mailboxRoot := p081MailboxRoot(t)
	outbox, err := NewOutbox(mailboxRoot)
	if err != nil {
		t.Fatal(err)
	}
	eventFiles, err := NewEventFiles(mailboxRoot)
	if err != nil {
		t.Fatal(err)
	}
	projector := Projector{Authority: authority, Outbox: outbox, EventFiles: eventFiles}
	for _, requestID := range []string{"req-p091-early", "req-p091-late"} {
		p091PublishTerminalCommandResponse(t, authority, requestID, command.CommandID)
		if err := projector.PublishCommand(ctx, requestID, command.CommandID); err != nil {
			t.Fatalf("publish %s: %v", requestID, err)
		}
	}
	eventPath, err := eventFiles.Path(command.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	firstResponsePath, err := outbox.Path("req-p091-early")
	if err != nil {
		t.Fatal(err)
	}
	secondResponsePath, err := outbox.Path("req-p091-late")
	if err != nil {
		t.Fatal(err)
	}
	cursor := int64(4)
	now = base.Add(time.Hour)
	if _, err := authority.AcknowledgeMailboxExchange(ctx, store.MailboxAcknowledgement{RequestID: "req-p091-early", ResponseRevision: 1, AvailableEventSequence: &cursor}); err != nil {
		t.Fatal(err)
	}
	cleaner := ArtifactCleaner{Authority: authority, Outbox: outbox, EventFiles: eventFiles}

	// Simulate a crash after the durable response cleanup claim but before its
	// filesystem unlink. The projector must not recreate the expired response.
	now = base.Add(26 * time.Hour)
	claimed, err := authority.ClaimMailboxResponsesForCleanup(ctx)
	if err != nil || len(claimed) != 1 || claimed[0] != "req-p091-early" {
		t.Fatalf("early response cleanup claim=%v err=%v", claimed, err)
	}
	if err := projector.Publish(ctx, "req-p091-early"); !errors.Is(err, store.ErrMailboxResponseExpired) {
		t.Fatalf("expired response republish error=%v", err)
	}
	report, err := cleaner.Run(ctx)
	if err != nil || report.ResponsesRemoved != 1 || report.EventFilesRemoved != 0 {
		t.Fatalf("early cleanup report=%+v err=%v", report, err)
	}
	assertPathAbsent(t, firstResponsePath)
	assertPathPresent(t, secondResponsePath)
	assertPathPresent(t, eventPath)

	// A late exact ACK may be recorded while metadata remains, but it cannot
	// extend the seven-day deadline or recreate the response file.
	now = base.Add(8 * 24 * time.Hour)
	late, err := authority.AcknowledgeMailboxExchange(ctx, store.MailboxAcknowledgement{RequestID: "req-p091-late", ResponseRevision: 1, AvailableEventSequence: &cursor})
	if err != nil || late.ResponseCleanupAt == nil || !late.ResponseCleanupAt.Equal(base.Add(store.MailboxUnackedResponseLifetime)) {
		t.Fatalf("late ACK cleanup deadline=%v err=%v", late.ResponseCleanupAt, err)
	}
	if err := projector.PublishCommand(ctx, "req-p091-late", command.CommandID); !errors.Is(err, store.ErrMailboxResponseExpired) {
		t.Fatalf("late response republish error=%v", err)
	}
	report, err = cleaner.Run(ctx)
	if err != nil || report.ResponsesRemoved != 1 || report.EventFilesRemoved != 1 {
		t.Fatalf("final cleanup report=%+v err=%v", report, err)
	}
	assertPathAbsent(t, secondResponsePath)
	assertPathAbsent(t, eventPath)

	// A fresh status request during the command's normal output-retention
	// window can regenerate the shared file from durable command events.
	p091PublishTerminalCommandResponse(t, authority, "req-p091-new-status", command.CommandID)
	if err := projector.PublishCommand(ctx, "req-p091-new-status", command.CommandID); err != nil {
		t.Fatalf("republish for new status request: %v", err)
	}
	assertPathPresent(t, eventPath)
}

func p091CreateTerminalCommand(t *testing.T, authority *store.AuthorityStore) store.CommandRecord {
	t.Helper()
	ctx := context.Background()
	controllerID, err := domain.NewControllerID("tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, controllerID)
	if err != nil {
		t.Fatal(err)
	}
	target, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	if err != nil {
		t.Fatal(err)
	}
	limits := domain.DefaultServiceLimits()
	session, _, err := authority.AcceptSessionCreate(ctx, store.SessionCreateAcceptance{
		SessionCreate:  store.SessionCreate{SessionID: "session-p091", Target: target, Environment: "mac-dev", Controller: controller, Source: domain.NewEmptySource(), Limits: domain.EffectiveSessionLimits{CommandTimeout: limits.CommandTimeout, IdleTimeout: limits.IdleTimeout, SessionMaxLifetime: limits.SessionMaxLifetime, OutputBytesPerCommand: limits.OutputBytesPerCommand}},
		IdempotencyKey: "key-p091-session", RequestHash: p085Hash(t, "create_session", `{"operation":"create_session","session_id":"session-p091"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionSession(ctx, session.SessionID, domain.SessionStateReady, "agent_ready"); err != nil {
		t.Fatal(err)
	}
	command, _, err := authority.AcceptCommand(ctx, store.CommandAcceptance{CommandID: "command-p091", SessionID: session.SessionID, IdempotencyKey: "key-p091-command", RequestHash: p085Hash(t, "submit_command", `{"operation":"submit_command","session_id":"session-p091","script":"echo p091"}`), Script: "echo p091", Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionCommand(ctx, store.CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateRunning}); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.AppendCommandEvent(ctx, store.CommandEventAppend{CommandID: command.CommandID, Type: "stdout", Payload: []byte("p091\n"), ByteCount: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionCommand(ctx, store.CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateSucceeded, ExitCode: intPointerP085(0), OutputComplete: true}); err != nil {
		t.Fatal(err)
	}
	return command
}

func p091PublishTerminalCommandResponse(t *testing.T, authority *store.AuthorityStore, requestID string, commandID domain.CommandID) {
	t.Helper()
	ctx := context.Background()
	requestPayload := []byte(`{"operation":"get_command","command_id":"` + string(commandID) + `"}`)
	digest := sha256.Sum256(requestPayload)
	hash, err := domain.NewCanonicalHash(domain.CanonicalizationVersionV1, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := authority.AcceptMailboxExchange(ctx, store.MailboxExchangeCreate{RequestID: requestID, Operation: "get_command", Controller: controller, RequestHash: hash, CanonicalPayload: requestPayload, ResourceID: string(commandID)}); err != nil {
		t.Fatal(err)
	}
	const cursor = int64(4)
	response, err := json.Marshal(map[string]any{
		"request_id": requestID, "operation": "get_command", "request_state": "complete",
		"response_revision": 1, "command_id": string(commandID), "available_event_sequence": cursor,
		"events_file": "events/" + string(commandID) + ".ndjson",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.PublishMailboxResponse(ctx, requestID, store.MailboxResponsePublication{State: store.MailboxExchangeComplete, Bytes: response, AvailableEventSequence: int64Ptr(cursor)}); err != nil {
		t.Fatal(err)
	}
}
