package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"strconv"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
)

func TestP092M07PinnedLocalEventsSurviveOutputExpiryUntilEveryResponseDeadline(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	clock := &p019Clock{value: base}
	authority := newP019Store(t, clock)
	sessionID := p019ReadySession(t, authority, "session-p092-local", "key-p092-local-session")
	command := p019Command(t, authority, sessionID, "command-p092-local", "key-p092-local-command")
	if _, err := authority.StartNextEligibleCommand(ctx, DefaultRunningCommandLimit); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.AppendCommandEvent(ctx, CommandEventAppend{CommandID: command.CommandID, Type: "stdout", Payload: []byte("p092\n"), ByteCount: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.CompleteRunningCommand(ctx, CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateSucceeded, ExitCode: intPointer(0), OutputComplete: true}, domain.SessionStateReady, "command_complete", true); err != nil {
		t.Fatal(err)
	}
	for _, requestID := range []string{"req-p092-local-early", "req-p092-local-late"} {
		p092PublishEventResponse(t, authority, requestID, command.CommandID, 4)
	}
	cursor := int64(4)
	clock.Advance(time.Hour)
	if _, err := authority.AcknowledgeMailboxExchange(ctx, MailboxAcknowledgement{RequestID: "req-p092-local-early", ResponseRevision: 1, AvailableEventSequence: &cursor}); err != nil {
		t.Fatal(err)
	}

	clock.Advance(47 * time.Hour)
	report, err := authority.CollectGarbage(ctx, GarbageCollectionOptions{OutputRetention: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if report.CommandsOutputExpired != 1 || report.CommandEventsDeleted != 0 {
		t.Fatalf("pinned local retention report = %+v", report)
	}
	commandAfterExpiry, err := authority.GetCommand(ctx, command.CommandID)
	if err != nil || commandAfterExpiry.OutputComplete || commandAfterExpiry.OutputUnavailableReason != "retention_expired" {
		t.Fatalf("fresh command read = %+v err=%v", commandAfterExpiry, err)
	}
	if events, err := authority.ListCommandEvents(ctx, command.CommandID); err != nil || len(events) != 0 {
		t.Fatalf("ordinary expired event read = %+v err=%v", events, err)
	}
	if _, _, err := authority.MailboxResponseCommandEvents(ctx, "req-p092-local-early", command.CommandID); !errors.Is(err, ErrMailboxResponseExpired) {
		t.Fatalf("early response read error = %v, want expired response", err)
	}
	_, pinned, err := authority.MailboxResponseCommandEvents(ctx, "req-p092-local-late", command.CommandID)
	if err != nil || len(pinned) != 4 || string(pinned[2].Payload) != "p092\n" || pinned[3].Type != "command_succeeded" {
		t.Fatalf("later live response prefix = %+v err=%v", pinned, err)
	}

	clock.Advance(6 * 24 * time.Hour)
	report, err = authority.CollectGarbage(ctx, GarbageCollectionOptions{OutputRetention: 24 * time.Hour})
	if err != nil || report.CommandEventsDeleted != 4 {
		t.Fatalf("post-deadline local GC = %+v err=%v", report, err)
	}
	if _, _, err := authority.MailboxResponseCommandEvents(ctx, "req-p092-local-late", command.CommandID); !errors.Is(err, ErrMailboxResponseExpired) {
		t.Fatalf("expired response read error = %v, want expired response", err)
	}
}

func TestP092M07PinnedMirroredEventsExpireForNewReadsButRemainForFrozenResponse(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 9, 27, 13, 0, 0, 0, time.UTC)
	clock := &p019Clock{value: base}
	authority := newP019Store(t, clock)
	target, _ := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	controller, _ := domain.NewControllerIdentity(domain.ControllerTypeQueuedMac, "tomasz.walczuk")
	final := int64(4)
	exitCode := 0
	projection := RemoteCommandProjection{
		CommandID: "command-p092-remote", SessionID: "session-p092-remote", Ordinal: 1,
		State: domain.CommandStateSucceeded, ExitCode: &exitCode, FinalEventSequence: &final,
		OutputComplete: true, Target: target, Controller: controller, Environment: "linux-dev",
		Source: domain.NewEmptySource(), Capabilities: p076Capabilities(), ObservedAt: base,
	}
	if _, err := authority.UpsertRemoteCommandProjection(ctx, projection); err != nil {
		t.Fatal(err)
	}
	events := []RemoteEventRecord{
		{CommandID: projection.CommandID, Sequence: 1, Type: "command_queued", OccurredAt: base},
		{CommandID: projection.CommandID, Sequence: 2, Type: "command_started", OccurredAt: base.Add(time.Second)},
		{CommandID: projection.CommandID, Sequence: 3, Type: "stdout", Payload: []byte("remote\n"), ByteCount: 7, OccurredAt: base.Add(2 * time.Second)},
		{CommandID: projection.CommandID, Sequence: 4, Type: "command_succeeded", OccurredAt: base.Add(3 * time.Second)},
	}
	if _, err := authority.MirrorRemoteEvents(ctx, events); err != nil {
		t.Fatal(err)
	}
	p092PublishEventResponse(t, authority, "req-p092-remote", projection.CommandID, 4)
	record, err := authority.GetMailboxExchange(ctx, "req-p092-remote")
	if err != nil || record.EventFileCommandID != string(projection.CommandID) {
		t.Fatalf("atomic remote event reference = %+v err=%v", record, err)
	}

	clock.Advance(48 * time.Hour)
	report, err := authority.CollectGarbage(ctx, GarbageCollectionOptions{OutputRetention: 24 * time.Hour})
	if err != nil || report.RemoteCommandsOutputExpired != 1 || report.RemoteCommandEventsDeleted != 0 {
		t.Fatalf("pinned remote retention report = %+v err=%v", report, err)
	}
	remoteView, err := authority.GetRemoteCommandProjection(ctx, projection.CommandID)
	if err != nil || remoteView.OutputComplete || remoteView.OutputUnavailableReason != "retention_expired" {
		t.Fatalf("fresh remote command read = %+v err=%v", remoteView, err)
	}
	if _, err := authority.ListRemoteEvents(ctx, projection.CommandID, 0); !errors.Is(err, ErrRemoteEventRetentionExpired) {
		t.Fatalf("ordinary mirrored event read error = %v, want retention expired", err)
	}
	_, pinned, err := authority.MailboxResponseCommandEvents(ctx, "req-p092-remote", projection.CommandID)
	if err != nil || len(pinned) != 4 || string(pinned[2].Payload) != "remote\n" || pinned[3].Type != "command_succeeded" {
		t.Fatalf("frozen remote response prefix = %+v err=%v", pinned, err)
	}

	projection.ObservedAt = base.Add(3 * 24 * time.Hour)
	projection.OutputComplete = true
	projection.OutputUnavailableReason = ""
	if refreshed, err := authority.UpsertRemoteCommandProjection(ctx, projection); err != nil || refreshed.OutputComplete || refreshed.OutputUnavailableReason != "retention_expired" {
		t.Fatalf("later remote snapshot revived expired output = %+v err=%v", refreshed, err)
	}
	clock.Advance(6 * 24 * time.Hour)
	report, err = authority.CollectGarbage(ctx, GarbageCollectionOptions{OutputRetention: 24 * time.Hour})
	if err != nil || report.RemoteCommandEventsDeleted != 4 {
		t.Fatalf("post-deadline mirrored GC = %+v err=%v", report, err)
	}
}

func p092PublishEventResponse(t *testing.T, authority *AuthorityStore, requestID string, commandID domain.CommandID, available int64) {
	t.Helper()
	ctx := context.Background()
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"operation":"get_command","command_id":"` + string(commandID) + `"}`)
	digest := sha256.Sum256(payload)
	hash, err := domain.NewCanonicalHash(domain.CanonicalizationVersionV1, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := authority.AcceptMailboxExchange(ctx, MailboxExchangeCreate{
		RequestID: requestID, Operation: "get_command", Controller: controller,
		RequestHash: hash, CanonicalPayload: payload, ResourceID: string(commandID),
	}); err != nil {
		t.Fatal(err)
	}
	response := []byte(`{"request_id":"` + requestID + `","operation":"get_command","request_state":"complete","response_revision":1,"command_id":"` + string(commandID) + `","available_event_sequence":` + strconv.FormatInt(available, 10) + `,"events_file":"events/` + string(commandID) + `.ndjson"}`)
	if _, err := authority.PublishMailboxResponse(ctx, requestID, MailboxResponsePublication{
		State: MailboxExchangeComplete, Bytes: response, AvailableEventSequence: &available,
	}); err != nil {
		t.Fatal(err)
	}
}
