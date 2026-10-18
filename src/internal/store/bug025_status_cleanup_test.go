package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/testfixture"
)

func dropBUG025CleanupSchemaForMigrationFixture(t *testing.T, database *sql.DB) {
	t.Helper()
	for _, statement := range []string{
		`DROP TRIGGER IF EXISTS mailbox_event_cleanup_work_exchange_update`,
		`DROP TABLE IF EXISTS mailbox_event_cleanup_work_cursor`,
		`DROP TABLE IF EXISTS mailbox_event_cleanup_work`,
		`DELETE FROM runner_schema_migrations WHERE version = 40`,
	} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatalf("remove BUG-025 schema from historical migration fixture (%s): %v", statement, err)
		}
	}
}

func TestBUG025StatusReadUsesSnapshotWhileIndependentWriterIsHeld(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	path := filepath.Join(root.Path(), "state", "bug025-status-read.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	intent := p057CreateSessionIntent(t, "intent-bug025-status", "session-bug025-status", "key-bug025-status")
	if _, err := store.CreateLocalIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}

	blockerDB, err := sql.Open("sqlite", dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = blockerDB.Close() })
	blockerDB.SetMaxOpenConns(1)
	blocker, err := blockerDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = blocker.Close() })
	if _, err := blocker.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = blocker.ExecContext(context.Background(), "ROLLBACK") }()

	controller := intent.Controller
	read, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	got, err := store.GetLocalIntentByResource(read, "create_session", string(intent.SessionID), controller)
	if err != nil || got.IntentID != intent.IntentID {
		t.Fatalf("status read=%+v err=%v, want the validated intent while another connection holds BEGIN IMMEDIATE", got, err)
	}
	if store.storageErrors.Load() != 0 {
		t.Fatalf("successful read incremented storage errors to %d", store.storageErrors.Load())
	}
}

func TestBUG025ReadSideSQLiteFailureRemainsCounted(t *testing.T) {
	ctx := context.Background()
	store := newP019Store(t, &p019Clock{value: time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)})
	intent := p057CreateSessionIntent(t, "intent-bug025-read-error", "session-bug025-read-error", "key-bug025-read-error")
	if _, err := store.CreateLocalIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}
	store.db.SetMaxOpenConns(1)
	connection, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		_ = connection.Close()
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(ctx, "DROP TABLE local_intents"); err != nil {
		_ = connection.Close()
		t.Fatal(err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = store.GetLocalIntentByResource(ctx, "create_session", string(intent.SessionID), intent.Controller)
	if err == nil || !IsSQLiteError(err) {
		t.Fatalf("read error=%v, want a wrapped SQLite error after fixture table removal", err)
	}
	if got := store.storageErrors.Load(); got != 1 {
		t.Fatalf("read-side SQLite error count=%d, want 1", got)
	}
}

func TestBUG025EventCleanupScansLargeReferenceGroupInBoundedRestartablePages(t *testing.T) {
	ctx := context.Background()
	clock := &p019Clock{value: time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)}
	root := testfixture.New(t)
	path := filepath.Join(root.Path(), "state", "bug025-large.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewAuthorityStoreWithClock(db, clock.Now)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	commandID := bug025TerminalLocalCommand(t, store, "command-bug025-large")
	const referenceCount = maxEventCleanupRefsPerWorkItem + 8
	for index := 0; index < referenceCount; index++ {
		p092PublishEventResponse(t, store, fmt.Sprintf("req-bug025-large-%03d", index), commandID, 4)
	}
	clock.Advance(MailboxUnackedResponseLifetime + time.Hour)

	first, err := store.ClaimMailboxEventFilesForCleanupInMailbox(ctx, DefaultMailboxID)
	if err != nil || len(first) != 0 {
		t.Fatalf("first bounded cleanup page=%+v err=%v, want progress without a premature claim", first, err)
	}
	var scanned, complete int
	if err := store.db.QueryRowContext(ctx, `SELECT scanned_refs, scan_complete FROM mailbox_event_cleanup_work WHERE command_id = ? AND is_remote = 0`, string(commandID)).Scan(&scanned, &complete); err != nil {
		t.Fatal(err)
	}
	if scanned != maxEventCleanupRefsPerWorkItem || complete != 0 {
		t.Fatalf("first cleanup page scanned=%d complete=%d, want bounded %d and incomplete", scanned, complete, maxEventCleanupRefsPerWorkItem)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	store, err = NewAuthorityStoreWithClock(db, clock.Now)
	if err != nil {
		t.Fatal(err)
	}

	second, err := store.ClaimMailboxEventFilesForCleanupInMailbox(ctx, DefaultMailboxID)
	if err != nil || len(second) != 1 || second[0].CommandID != commandID {
		t.Fatalf("second bounded cleanup page=%+v err=%v, want the now fully verified command", second, err)
	}
	retry, err := store.ClaimMailboxEventFilesForCleanupInMailbox(ctx, DefaultMailboxID)
	if err != nil || len(retry) != 1 || retry[0] != second[0] {
		t.Fatalf("interrupted unlink retry=%+v err=%v, want the same durable cleanup claim", retry, err)
	}
	if err := store.MarkMailboxEventFileRemovedInMailbox(ctx, retry[0]); err != nil {
		t.Fatal(err)
	}
	var references, work, removed int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM mailbox_event_file_references WHERE command_id = ?`, string(commandID)).Scan(&references); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM mailbox_event_cleanup_work WHERE command_id = ?`, string(commandID)).Scan(&work); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM mailbox_event_file_cleanup WHERE command_id = ? AND file_removed_at IS NOT NULL`, string(commandID)).Scan(&removed); err != nil {
		t.Fatal(err)
	}
	if references != referenceCount || work != 0 || removed != 1 {
		t.Fatalf("completed event cleanup references=%d work=%d removed=%d, want history retained=%d, no queued work, one durable removal marker", references, work, removed, referenceCount)
	}
	if afterHistory, err := store.ClaimMailboxEventFilesForCleanupInMailbox(ctx, DefaultMailboxID); err != nil || len(afterHistory) != 0 {
		t.Fatalf("repeat pass revisited removed history: claims=%+v err=%v", afterHistory, err)
	}
}

func TestBUG025Schema39MigrationBackfillsOnlyOutstandingEventFiles(t *testing.T) {
	ctx := context.Background()
	clock := &p019Clock{value: time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)}
	root := testfixture.New(t)
	path := filepath.Join(root.Path(), "state", "bug025-schema39.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewAuthorityStoreWithClock(db, clock.Now)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	removedCommand := bug025TerminalLocalCommand(t, store, "command-bug025-migration-removed")
	pendingCommand := bug025TerminalLocalCommand(t, store, "command-bug025-migration-pending")
	p092PublishEventResponse(t, store, "req-bug025-migration-removed", removedCommand, 4)
	p092PublishEventResponse(t, store, "req-bug025-migration-pending", pendingCommand, 4)
	clock.Advance(MailboxUnackedResponseLifetime + time.Hour)
	claims, err := store.ClaimMailboxEventFilesForCleanupInMailbox(ctx, DefaultMailboxID)
	if err != nil || len(claims) != 2 {
		t.Fatalf("pre-migration event claims=%+v err=%v, want both fixture files", claims, err)
	}
	for _, ref := range claims {
		if ref.CommandID == removedCommand {
			if err := store.MarkMailboxEventFileRemovedInMailbox(ctx, ref); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	downgrade, err := sql.Open("sqlite", dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	dropBUG025CleanupSchemaForMigrationFixture(t, downgrade)
	if _, err := downgrade.ExecContext(ctx, `PRAGMA user_version = 39`); err != nil {
		_ = downgrade.Close()
		t.Fatal(err)
	}
	if err := downgrade.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = migrated.Close() })
	var work, references, historicalRemoved int
	if err := migrated.QueryRowContext(ctx, `SELECT count(*) FROM mailbox_event_cleanup_work`).Scan(&work); err != nil {
		t.Fatal(err)
	}
	if err := migrated.QueryRowContext(ctx, `SELECT count(*) FROM mailbox_event_file_references`).Scan(&references); err != nil {
		t.Fatal(err)
	}
	if err := migrated.QueryRowContext(ctx, `SELECT count(*) FROM mailbox_event_file_cleanup WHERE command_id = ? AND file_removed_at IS NOT NULL`, string(removedCommand)).Scan(&historicalRemoved); err != nil {
		t.Fatal(err)
	}
	if work != 1 || references != 2 || historicalRemoved != 1 {
		t.Fatalf("schema39 migration work=%d refs=%d removed_history=%d, want one outstanding item and both retained references", work, references, historicalRemoved)
	}
	var queuedCommand string
	if err := migrated.QueryRowContext(ctx, `SELECT command_id FROM mailbox_event_cleanup_work`).Scan(&queuedCommand); err != nil || queuedCommand != string(pendingCommand) {
		t.Fatalf("schema39 migration queued=%q err=%v, want only pending command %q", queuedCommand, err, pendingCommand)
	}
}

func TestBUG025AckDeadlineChangeInvalidatesCompletedCleanupScan(t *testing.T) {
	ctx := context.Background()
	clock := &p019Clock{value: time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)}
	store := newP019Store(t, clock)
	commandID := bug025TerminalLocalCommand(t, store, "command-bug025-ack-reset")
	p092PublishEventResponse(t, store, "req-bug025-ack-reset", commandID, 4)

	if claims, err := store.ClaimMailboxEventFilesForCleanupInMailbox(ctx, DefaultMailboxID); err != nil || len(claims) != 0 {
		t.Fatalf("pre-deadline cleanup claims=%+v err=%v, want no claim", claims, err)
	}
	var scanned, complete int
	if err := store.db.QueryRowContext(ctx, `SELECT scanned_refs, scan_complete FROM mailbox_event_cleanup_work WHERE command_id = ?`, string(commandID)).Scan(&scanned, &complete); err != nil {
		t.Fatal(err)
	}
	if scanned != 1 || complete != 1 {
		t.Fatalf("pre-ACK progress scanned=%d complete=%d, want one completed reference walk", scanned, complete)
	}
	clock.Advance(time.Hour)
	cursor := int64(4)
	if _, err := store.AcknowledgeMailboxExchange(ctx, MailboxAcknowledgement{
		RequestID: "req-bug025-ack-reset", ResponseRevision: 1, AvailableEventSequence: &cursor,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT scanned_refs, scan_complete FROM mailbox_event_cleanup_work WHERE command_id = ?`, string(commandID)).Scan(&scanned, &complete); err != nil {
		t.Fatal(err)
	}
	if scanned != 0 || complete != 0 {
		t.Fatalf("ACK left stale cleanup progress scanned=%d complete=%d, want invalidated scan", scanned, complete)
	}
	clock.Advance(MailboxAckedResponseLifetime + time.Hour)
	claims, err := store.ClaimMailboxEventFilesForCleanupInMailbox(ctx, DefaultMailboxID)
	if err != nil || len(claims) != 1 || claims[0].CommandID != commandID {
		t.Fatalf("post-ACK cleanup claims=%+v err=%v, want cleanup after the ACK retention deadline", claims, err)
	}
}

func TestBUG025EventCleanupRotatesFairlyAndRespectsRemoteOwnership(t *testing.T) {
	ctx := context.Background()
	clock := &p019Clock{value: time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)}
	store := newP019Store(t, clock)
	var localIDs []domain.CommandID
	for index := 0; index < maxEventCleanupWorkItemsPerBatch+2; index++ {
		commandID := bug025TerminalLocalCommand(t, store, domain.CommandID(fmt.Sprintf("command-bug025-fair-%02d", index)))
		localIDs = append(localIDs, commandID)
		p092PublishEventResponse(t, store, fmt.Sprintf("req-bug025-fair-%02d", index), commandID, 4)
	}
	remoteID := domain.CommandID("command-bug025-remote")
	finalSequence, exitCode := int64(3), 0
	target, err := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeQueuedMac, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	base := clock.Now()
	if _, err := store.UpsertRemoteCommandProjection(ctx, RemoteCommandProjection{
		CommandID: remoteID, SessionID: "session-bug025-remote", Ordinal: 1,
		State: domain.CommandStateSucceeded, ExitCode: &exitCode, FinalEventSequence: &finalSequence,
		OutputComplete: true, Target: target, Controller: controller, Environment: "linux-dev",
		Source: domain.NewEmptySource(), Capabilities: p076Capabilities(), ObservedAt: base,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MirrorRemoteEvents(ctx, []RemoteEventRecord{
		{CommandID: remoteID, Sequence: 1, Type: "command_queued", OccurredAt: base},
		{CommandID: remoteID, Sequence: 2, Type: "command_started", OccurredAt: base.Add(time.Second)},
		{CommandID: remoteID, Sequence: 3, Type: "command_succeeded", OccurredAt: base.Add(2 * time.Second)},
	}); err != nil {
		t.Fatal(err)
	}
	p092PublishEventResponse(t, store, "req-bug025-remote", remoteID, 3)
	clock.Advance(MailboxUnackedResponseLifetime + time.Hour)

	first, err := store.ClaimMailboxEventFilesForCleanupInMailbox(ctx, DefaultMailboxID)
	if err != nil || len(first) != maxEventCleanupWorkItemsPerBatch {
		t.Fatalf("first fair cleanup batch=%+v err=%v, want %d bounded claims", first, err, maxEventCleanupWorkItemsPerBatch)
	}
	for _, ref := range first {
		if err := store.MarkMailboxEventFileRemovedInMailbox(ctx, ref); err != nil {
			t.Fatal(err)
		}
	}
	second, err := store.ClaimMailboxEventFilesForCleanupInMailbox(ctx, DefaultMailboxID)
	if err != nil || len(second) != 3 {
		t.Fatalf("next fair cleanup batch=%+v err=%v, want remaining local and remote work", second, err)
	}
	seen := map[domain.CommandID]bool{}
	for _, ref := range append(first, second...) {
		if seen[ref.CommandID] {
			t.Fatalf("cleanup returned duplicate command %s across consecutive batches", ref.CommandID)
		}
		seen[ref.CommandID] = true
	}
	for _, commandID := range append(localIDs, remoteID) {
		if !seen[commandID] {
			t.Fatalf("bounded fair batches did not reach command %s", commandID)
		}
	}
	for _, ref := range second {
		if err := store.MarkMailboxEventFileRemovedInMailbox(ctx, ref); err != nil {
			t.Fatal(err)
		}
	}
	var localClaim, remoteClaim int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM mailbox_event_file_cleanup WHERE command_id = ?`, string(remoteID)).Scan(&localClaim); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM mailbox_remote_event_file_cleanup WHERE command_id = ?`, string(remoteID)).Scan(&remoteClaim); err != nil {
		t.Fatal(err)
	}
	if localClaim != 0 || remoteClaim != 1 {
		t.Fatalf("remote cleanup ownership local=%d remote=%d, want only the remote marker", localClaim, remoteClaim)
	}
}

func TestBUG025EventCleanupFailsClosedOnMalformedDeadline(t *testing.T) {
	ctx := context.Background()
	clock := &p019Clock{value: time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)}
	store := newP019Store(t, clock)
	commandID := bug025TerminalLocalCommand(t, store, "command-bug025-malformed")
	p092PublishEventResponse(t, store, "req-bug025-malformed", commandID, 4)
	if _, err := store.db.ExecContext(ctx, `UPDATE mailbox_exchanges SET response_cleanup_at = 'not-a-time' WHERE client_request_id = ?`, "req-bug025-malformed"); err != nil {
		t.Fatal(err)
	}
	clock.Advance(10 * 24 * time.Hour)
	if claims, err := store.ClaimMailboxEventFilesForCleanupInMailbox(ctx, DefaultMailboxID); err == nil || !errors.Is(err, ErrMailboxCleanupState) || len(claims) != 0 {
		t.Fatalf("malformed deadline cleanup claims=%+v err=%v, want fail-closed cleanup state error", claims, err)
	}
	var markers int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM mailbox_event_file_cleanup WHERE command_id = ?`, string(commandID)).Scan(&markers); err != nil {
		t.Fatal(err)
	}
	if markers != 0 {
		t.Fatalf("malformed metadata created %d destructive cleanup markers", markers)
	}
}

func bug025TerminalLocalCommand(t *testing.T, store *AuthorityStore, commandID domain.CommandID) domain.CommandID {
	t.Helper()
	ctx := context.Background()
	sessionID := domain.SessionID("session-" + string(commandID))
	p019ReadySession(t, store, sessionID, "key-"+string(sessionID))
	command := p019Command(t, store, sessionID, commandID, "key-"+string(commandID))
	if _, err := store.StartNextEligibleCommand(ctx, DefaultRunningCommandLimit); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendCommandEvent(ctx, CommandEventAppend{CommandID: commandID, Type: "stdout", Payload: []byte("fixture\n"), ByteCount: 8}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteRunningCommand(ctx, CommandTransition{CommandID: commandID, NextState: domain.CommandStateSucceeded, ExitCode: intPointer(0), OutputComplete: true}, domain.SessionStateReady, "command_complete", true); err != nil {
		t.Fatal(err)
	}
	if command.State != domain.CommandStateQueued {
		t.Fatalf("fixture command initial state=%s, want queued", command.State)
	}
	return commandID
}
