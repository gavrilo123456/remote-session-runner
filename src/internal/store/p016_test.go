package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/testfixture"
)

func TestP016EligibilitySkipsTerminalCommandsAndBlocksActivePredecessors(t *testing.T) {
	root := testfixture.New(t)
	db, err := Open(context.Background(), root.Path()+"/state/eligibility.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	created, _, err := store.AcceptSessionCreate(context.Background(), p013Acceptance(t, "session-p016-eligibility", "key-p016-session", "linux-dev"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionSession(context.Background(), created.SessionID, domain.SessionStateReady, "agent_ready"); err != nil {
		t.Fatal(err)
	}
	commands := make([]CommandRecord, 0, 3)
	for i := 1; i <= 3; i++ {
		command, duplicate, err := store.AcceptCommand(context.Background(), CommandAcceptance{
			CommandID:      domain.CommandID(fmt.Sprintf("command-p016-%d", i)),
			SessionID:      created.SessionID,
			IdempotencyKey: fmt.Sprintf("key-p016-command-%d", i),
			RequestHash:    p014Hash(t, fmt.Sprintf(`{"operation":"submit_command","session_id":"session-p016-eligibility","script":"echo %d"}`, i)),
			Script:         fmt.Sprintf("echo %d", i),
			Timeout:        time.Second,
		})
		if err != nil || duplicate {
			t.Fatalf("accept command %d = %+v duplicate=%v err=%v", i, command, duplicate, err)
		}
		commands = append(commands, command)
	}
	eligible, err := store.NextEligibleCommand(context.Background(), created.SessionID)
	if err != nil || eligible.CommandID != commands[0].CommandID || eligible.Ordinal != 1 {
		t.Fatalf("first eligible = %+v err=%v", eligible, err)
	}
	if _, err := db.Exec("UPDATE exec_commands SET state = ? WHERE command_id = ?", string(domain.CommandStateSucceeded), string(commands[0].CommandID)); err != nil {
		t.Fatal(err)
	}
	eligible, err = store.NextEligibleCommand(context.Background(), created.SessionID)
	if err != nil || eligible.CommandID != commands[1].CommandID || eligible.Ordinal != 2 {
		t.Fatalf("eligible after terminal predecessor = %+v err=%v", eligible, err)
	}
	if _, err := db.Exec("UPDATE exec_commands SET state = ? WHERE command_id = ?", string(domain.CommandStateRunning), string(commands[1].CommandID)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.NextEligibleCommand(context.Background(), created.SessionID); !errors.Is(err, ErrCommandNotEligible) {
		t.Fatalf("running predecessor eligibility error = %v", err)
	}
	if _, err := db.Exec("UPDATE exec_commands SET state = ? WHERE command_id = ?", string(domain.CommandStateCancelled), string(commands[1].CommandID)); err != nil {
		t.Fatal(err)
	}
	eligible, err = store.NextEligibleCommand(context.Background(), created.SessionID)
	if err != nil || eligible.CommandID != commands[2].CommandID || eligible.Ordinal != 3 {
		t.Fatalf("eligible after cancelled predecessor = %+v err=%v", eligible, err)
	}
}

func TestP016RejectsOrdinalGapsAndDoesNotAllocatePastCorruption(t *testing.T) {
	root := testfixture.New(t)
	db, err := Open(context.Background(), root.Path()+"/state/gap.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	created, _, err := store.AcceptSessionCreate(context.Background(), p013Acceptance(t, "session-p016-gap", "key-p016-gap-session", "linux-dev"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionSession(context.Background(), created.SessionID, domain.SessionStateReady, "agent_ready"); err != nil {
		t.Fatal(err)
	}
	command, _, err := store.AcceptCommand(context.Background(), CommandAcceptance{CommandID: "command-p016-gap", SessionID: created.SessionID, IdempotencyKey: "key-p016-gap-command", RequestHash: p014Hash(t, `{"operation":"submit_command","session_id":"session-p016-gap","script":"echo gap"}`), Script: "echo gap", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE exec_commands SET ordinal = 2 WHERE command_id = ?", string(command.CommandID)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.NextEligibleCommand(context.Background(), created.SessionID); !errors.Is(err, ErrCommandOrderCorrupt) {
		t.Fatalf("gapped eligibility error = %v", err)
	}
	if _, _, err := store.AcceptCommand(context.Background(), CommandAcceptance{CommandID: "command-p016-after-gap", SessionID: created.SessionID, IdempotencyKey: "key-p016-after-gap", RequestHash: p014Hash(t, `{"operation":"submit_command","session_id":"session-p016-gap","script":"echo after"}`), Script: "echo after", Timeout: time.Second}); !errors.Is(err, ErrCommandOrderCorrupt) {
		t.Fatalf("gapped allocation error = %v", err)
	}
}

func TestP016ConcurrentAcceptanceAllocatesContiguousOrdinals(t *testing.T) {
	root := testfixture.New(t)
	db, err := Open(context.Background(), root.Path()+"/state/race.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	created, _, err := store.AcceptSessionCreate(context.Background(), p013Acceptance(t, "session-p016-race", "key-p016-race-session", "linux-dev"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionSession(context.Background(), created.SessionID, domain.SessionStateReady, "agent_ready"); err != nil {
		t.Fatal(err)
	}
	const attempts = 8
	start := make(chan struct{})
	ordinals := make(chan int64, attempts)
	errorsCh := make(chan error, attempts)
	var wait sync.WaitGroup
	for i := 0; i < attempts; i++ {
		i := i
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			command, _, err := store.AcceptCommand(context.Background(), CommandAcceptance{CommandID: domain.CommandID(fmt.Sprintf("command-p016-race-%d", i)), SessionID: created.SessionID, IdempotencyKey: fmt.Sprintf("key-p016-race-%d", i), RequestHash: p014Hash(t, fmt.Sprintf(`{"operation":"submit_command","session_id":"session-p016-race","script":"echo %d"}`, i)), Script: fmt.Sprintf("echo %d", i), Timeout: time.Second})
			if err != nil {
				errorsCh <- err
				return
			}
			ordinals <- command.Ordinal
		}()
	}
	close(start)
	wait.Wait()
	close(ordinals)
	close(errorsCh)
	for err := range errorsCh {
		t.Fatal(err)
	}
	values := make([]int64, 0, attempts)
	for ordinal := range ordinals {
		values = append(values, ordinal)
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	for i, ordinal := range values {
		if ordinal != int64(i+1) {
			t.Fatalf("ordinals = %v, expected contiguous from one", values)
		}
	}
	var ignored sql.NullInt64
	if err := db.QueryRow("SELECT MIN(ordinal) FROM exec_commands WHERE session_id = ?", string(created.SessionID)).Scan(&ignored); err != nil {
		t.Fatal(err)
	}
}
