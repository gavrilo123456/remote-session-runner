package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/testfixture"
)

func TestP019D17SchedulerFairnessAndStartedEventTransaction(t *testing.T) {
	clock := &p019Clock{value: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)}
	store := newP019Store(t, clock)

	sessionA := p019ReadySession(t, store, "session-p019-a", "key-p019-a")
	clock.Advance(time.Second)
	commandA1 := p019Command(t, store, sessionA, "command-p019-a1", "key-p019-a1")
	clock.Advance(time.Second)
	sessionB := p019ReadySession(t, store, "session-p019-b", "key-p019-b")
	clock.Advance(time.Second)
	commandB1 := p019Command(t, store, sessionB, "command-p019-b1", "key-p019-b1")
	clock.Advance(time.Second)
	commandA2 := p019Command(t, store, sessionA, "command-p019-a2", "key-p019-a2")

	subscription, err := store.SubscribeCommandEvents(context.Background(), commandA1.CommandID, 1, 4)
	if err != nil {
		t.Fatal(err)
	}
	started, err := store.StartNextEligibleCommand(context.Background(), 4)
	if err != nil {
		t.Fatal(err)
	}
	if started.CommandID != commandA1.CommandID || started.State != domain.CommandStateRunning {
		t.Fatalf("first scheduled command = %+v, want %s running", started, commandA1.CommandID)
	}
	select {
	case event := <-subscription.Events():
		if event.Sequence != 2 || event.Type != "command_started" {
			t.Fatalf("started event = %+v, want sequence 2 command_started", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for command_started event")
	}
	subscription.Close()

	if got, err := store.GetSession(context.Background(), sessionA); err != nil || got.State != domain.SessionStateBusy {
		t.Fatalf("session A after start = %+v err=%v, want busy", got, err)
	}
	if _, err := store.TransitionCommand(context.Background(), CommandTransition{CommandID: commandA1.CommandID, NextState: domain.CommandStateSucceeded, ExitCode: intPointer(0), OutputComplete: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionSession(context.Background(), sessionA, domain.SessionStateReady, "command_complete"); err != nil {
		t.Fatal(err)
	}
	if err := store.ConfirmCommandSlotRelease(context.Background(), commandA1.CommandID); err != nil {
		t.Fatal(err)
	}

	started, err = store.StartNextEligibleCommand(context.Background(), 4)
	if err != nil {
		t.Fatal(err)
	}
	if started.CommandID != commandB1.CommandID {
		t.Fatalf("second scheduled command = %s, want oldest eligible B1 %s before A2 %s", started.CommandID, commandB1.CommandID, commandA2.CommandID)
	}
	if got, err := store.GetSession(context.Background(), sessionB); err != nil || got.State != domain.SessionStateBusy {
		t.Fatalf("session B after start = %+v err=%v, want busy", got, err)
	}
}

// TestP019D17SchedulerOrdersVariableWidthTimestampTextChronologically
// protects the authoritative fairness rule for variable-width RFC3339Nano
// durable text. SQLite TEXT order places the
// later .100000001Z before the earlier .1Z, so the scheduler must parse the
// stored timestamp before it claims the next command.
func TestP019D17SchedulerOrdersVariableWidthTimestampTextChronologically(t *testing.T) {
	clock := &p019Clock{value: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)}
	authority := newP019Store(t, clock)

	firstSession := p019ReadySession(t, authority, "session-p019-variable-time-first", "key-p019-variable-time-first")
	secondSession := p019ReadySession(t, authority, "session-p019-variable-time-second", "key-p019-variable-time-second")
	first := p019Command(t, authority, firstSession, "command-p019-variable-time-first", "key-p019-variable-time-first")
	second := p019Command(t, authority, secondSession, "command-p019-variable-time-second", "key-p019-variable-time-second")

	base := time.Date(2026, 10, 3, 12, 1, 0, 0, time.UTC)
	firstCreatedAt := base.Add(100 * time.Millisecond)
	secondCreatedAt := firstCreatedAt.Add(time.Nanosecond)
	firstText := firstCreatedAt.Format(time.RFC3339Nano)
	secondText := secondCreatedAt.Format(time.RFC3339Nano)
	if firstText <= secondText {
		t.Fatalf("fixture timestamps unexpectedly preserve lexical chronology: first=%q second=%q", firstText, secondText)
	}
	if _, err := authority.db.ExecContext(context.Background(), `UPDATE exec_commands SET created_at = ? WHERE command_id = ?`, firstText, string(first.CommandID)); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.db.ExecContext(context.Background(), `UPDATE exec_commands SET created_at = ? WHERE command_id = ?`, secondText, string(second.CommandID)); err != nil {
		t.Fatal(err)
	}

	started, err := authority.StartNextEligibleCommand(context.Background(), DefaultRunningCommandLimit)
	if err != nil {
		t.Fatal(err)
	}
	if started.CommandID != first.CommandID {
		t.Fatalf("scheduled command=%s, want chronological first %s before %s", started.CommandID, first.CommandID, second.CommandID)
	}
}

func TestP019D17SchedulerRejectsCorruptCandidateTimestampBeforeClaim(t *testing.T) {
	clock := &p019Clock{value: time.Date(2026, 10, 3, 12, 2, 0, 0, time.UTC)}
	authority := newP019Store(t, clock)
	session := p019ReadySession(t, authority, "session-p019-corrupt-time", "key-p019-corrupt-time")
	queued := p019Command(t, authority, session, "command-p019-corrupt-time", "key-p019-corrupt-time")
	if _, err := authority.db.ExecContext(context.Background(), `UPDATE exec_commands SET created_at = 'not-a-timestamp' WHERE command_id = ?`, string(queued.CommandID)); err != nil {
		t.Fatal(err)
	}

	if _, err := authority.StartNextEligibleCommand(context.Background(), DefaultRunningCommandLimit); !errors.Is(err, ErrCommandOrderCorrupt) {
		t.Fatalf("start with corrupt scheduler timestamp error=%v, want ErrCommandOrderCorrupt", err)
	}
	var state string
	if err := authority.db.QueryRowContext(context.Background(), `SELECT state FROM exec_commands WHERE command_id = ?`, string(queued.CommandID)).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if domain.CommandState(state) != domain.CommandStateQueued {
		t.Fatalf("corrupt scheduler timestamp command state=%s, want queued", state)
	}
	if live, err := authority.CountLiveCommandSlots(context.Background()); err != nil || live != 0 {
		t.Fatalf("corrupt scheduler timestamp live slots=%d err=%v, want 0", live, err)
	}
}

func TestP019D17FourLiveSlotsRetainLostReservationUntilConfirmedStop(t *testing.T) {
	store := newP019Store(t, &p019Clock{value: time.Date(2026, 9, 26, 13, 0, 0, 0, time.UTC)})
	commands := make([]CommandRecord, 0, 5)
	sessions := make([]domain.SessionID, 0, 5)
	for i := 0; i < 5; i++ {
		sessionID := domain.SessionID(fmt.Sprintf("session-p019-slot-%d", i))
		session := p019ReadySession(t, store, sessionID, fmt.Sprintf("key-p019-slot-session-%d", i))
		command := p019Command(t, store, session, domain.CommandID(fmt.Sprintf("command-p019-slot-%d", i)), fmt.Sprintf("key-p019-slot-command-%d", i))
		sessions = append(sessions, session)
		commands = append(commands, command)
	}
	startedIDs := make([]domain.CommandID, 0, 4)
	for i := 0; i < 4; i++ {
		started, err := store.StartNextEligibleCommand(context.Background(), 4)
		if err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
		startedIDs = append(startedIDs, started.CommandID)
	}
	if count, err := store.CountLiveCommandSlots(context.Background()); err != nil || count != 4 {
		t.Fatalf("live slots = %d err=%v, want 4", count, err)
	}
	if _, err := store.StartNextEligibleCommand(context.Background(), 4); !errors.Is(err, ErrCommandSlotsFull) {
		t.Fatalf("fifth start error = %v, want ErrCommandSlotsFull", err)
	}

	lostID := startedIDs[0]
	if err := store.ConfirmCommandSlotRelease(context.Background(), lostID); !errors.Is(err, ErrCommandSlotNotReleasable) {
		t.Fatalf("release running slot error = %v, want ErrCommandSlotNotReleasable", err)
	}
	if _, err := store.TransitionCommand(context.Background(), CommandTransition{CommandID: lostID, NextState: domain.CommandStateLost, OutputComplete: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionSession(context.Background(), sessions[0], domain.SessionStateReady, "lost_command"); err != nil {
		t.Fatal(err)
	}
	if count, err := store.CountLiveCommandSlots(context.Background()); err != nil || count != 4 {
		t.Fatalf("live slots after loss = %d err=%v, want 4 until stop confirmation", count, err)
	}
	if _, err := store.StartNextEligibleCommand(context.Background(), 4); !errors.Is(err, ErrCommandSlotsFull) {
		t.Fatalf("start after unconfirmed loss error = %v, want ErrCommandSlotsFull", err)
	}
	if err := store.ConfirmCommandSlotRelease(context.Background(), lostID); err != nil {
		t.Fatal(err)
	}
	if err := store.ConfirmCommandSlotRelease(context.Background(), lostID); err != nil {
		t.Fatalf("duplicate release error = %v", err)
	}
	if count, err := store.CountLiveCommandSlots(context.Background()); err != nil || count != 3 {
		t.Fatalf("live slots after confirmed loss = %d err=%v, want 3", count, err)
	}
	started, err := store.StartNextEligibleCommand(context.Background(), 4)
	if err != nil || started.CommandID != commands[4].CommandID {
		t.Fatalf("fifth command after release = %+v err=%v, want %s", started, err, commands[4].CommandID)
	}
	slot, err := store.GetCommandSlot(context.Background(), lostID)
	if err != nil || slot.StopConfirmedAt == nil || slot.ReleasedAt == nil {
		t.Fatalf("released slot = %+v err=%v", slot, err)
	}
}

func TestP019D17ConcurrentSchedulerClaimsAtMostFourSlots(t *testing.T) {
	store := newP019Store(t, &p019Clock{value: time.Date(2026, 9, 26, 14, 0, 0, 0, time.UTC)})
	const candidates = 8
	for i := 0; i < candidates; i++ {
		session := p019ReadySession(t, store, domain.SessionID(fmt.Sprintf("session-p019-race-%d", i)), fmt.Sprintf("key-p019-race-session-%d", i))
		p019Command(t, store, session, domain.CommandID(fmt.Sprintf("command-p019-race-%d", i)), fmt.Sprintf("key-p019-race-command-%d", i))
	}
	start := make(chan struct{})
	claimed := make(chan domain.CommandID, candidates)
	errorsCh := make(chan error, candidates)
	var wait sync.WaitGroup
	for i := 0; i < candidates; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			command, err := store.StartNextEligibleCommand(context.Background(), 4)
			if err != nil {
				errorsCh <- err
				return
			}
			claimed <- command.CommandID
		}()
	}
	close(start)
	wait.Wait()
	close(claimed)
	close(errorsCh)
	var claimedIDs []domain.CommandID
	for id := range claimed {
		claimedIDs = append(claimedIDs, id)
	}
	if len(claimedIDs) != 4 {
		t.Fatalf("claimed %d commands = %v, want 4", len(claimedIDs), claimedIDs)
	}
	sort.Slice(claimedIDs, func(i, j int) bool { return claimedIDs[i] < claimedIDs[j] })
	for err := range errorsCh {
		if !errors.Is(err, ErrCommandSlotsFull) && !errors.Is(err, ErrCommandNotEligible) {
			t.Fatalf("unexpected concurrent scheduler error: %v", err)
		}
	}
	if count, err := store.CountLiveCommandSlots(context.Background()); err != nil || count != 4 {
		t.Fatalf("concurrent live slots = %d err=%v, want 4", count, err)
	}
}

func newP019Store(t *testing.T, clock *p019Clock) *AuthorityStore {
	t.Helper()
	root := testfixture.New(t)
	db, err := Open(context.Background(), root.Path()+"/state/p019.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewAuthorityStoreWithClock(db, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func p019ReadySession(t *testing.T, store *AuthorityStore, sessionID domain.SessionID, key string) domain.SessionID {
	t.Helper()
	created, _, err := store.AcceptSessionCreate(context.Background(), p013Acceptance(t, string(sessionID), key, "linux-dev"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionSession(context.Background(), sessionID, domain.SessionStateReady, "agent_ready"); err != nil {
		t.Fatal(err)
	}
	return created.SessionID
}

// p019RuntimeReadySession models the normal successful runtime handshake. Use
// it for fixtures representing a command that could have reached an executor.
func p019RuntimeReadySession(t *testing.T, store *AuthorityStore, sessionID domain.SessionID, key string) domain.SessionID {
	t.Helper()
	created, _, err := store.AcceptSessionCreate(context.Background(), p013Acceptance(t, string(sessionID), key, "linux-dev"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteSessionCreation(context.Background(), created.SessionID, domain.SessionStateReady, "generation-"+string(created.SessionID), "", "runtime_ready"); err != nil {
		t.Fatal(err)
	}
	return created.SessionID
}

func p019Command(t *testing.T, store *AuthorityStore, sessionID domain.SessionID, commandID domain.CommandID, key string) CommandRecord {
	t.Helper()
	script := "echo " + string(commandID)
	command, duplicate, err := store.AcceptCommand(context.Background(), CommandAcceptance{
		CommandID:      commandID,
		SessionID:      sessionID,
		IdempotencyKey: key,
		RequestHash:    p014Hash(t, fmt.Sprintf(`{"operation":"submit_command","session_id":"%s","script":%q}`, sessionID, script)),
		Script:         script,
		Timeout:        time.Minute,
	})
	if err != nil || duplicate {
		t.Fatalf("accept %s = %+v duplicate=%v err=%v", commandID, command, duplicate, err)
	}
	return command
}

type p019Clock struct {
	mu    sync.Mutex
	value time.Time
}

func (c *p019Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.value
}

func (c *p019Clock) Advance(delta time.Duration) {
	c.mu.Lock()
	c.value = c.value.Add(delta)
	c.mu.Unlock()
}
