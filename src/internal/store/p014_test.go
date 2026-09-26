package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/testfixture"
)

func TestP014AcceptCommandCommitsExactScriptAndQueuedEvent(t *testing.T) {
	root := testfixture.New(t)
	databasePath := root.Path() + "/state/p014.db"
	db, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 9, 26, 17, 0, 0, 0, time.UTC)
	store, err := NewAuthorityStoreWithClock(db, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	create := p013Acceptance(t, "session-p014-1", "key-p014-create", "linux-dev")
	created, _, err := store.AcceptSessionCreate(context.Background(), create)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionSession(context.Background(), created.SessionID, domain.SessionStateReady, "agent_ready"); err != nil {
		t.Fatal(err)
	}
	script := "printf 'é\\n'; printf '\\000\\377'"
	hash := p014Hash(t, `{"operation":"submit_command","session_id":"session-p014-1","script":"printf 'é\\n'; printf '\\000\\377'"}`)
	accepted, err := store.AcceptCommand(context.Background(), CommandAcceptance{
		CommandID:     domain.CommandID("command-p014-1"),
		SessionID:     created.SessionID,
		RequestHash:   hash,
		Script:        script,
		Timeout:       30 * time.Second,
		IntentOrdinal: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantScript := []byte(script)
	if accepted.Ordinal != 1 || accepted.State != domain.CommandStateQueued || accepted.OutputComplete || accepted.OutputTruncated || string(accepted.ScriptBytes) != script || accepted.IntentOrdinal == nil || *accepted.IntentOrdinal != 7 {
		t.Fatalf("accepted command = %+v", accepted)
	}
	wantHash := sha256.Sum256(wantScript)
	if string(accepted.ScriptSHA256) != string(wantHash[:]) {
		t.Fatalf("script hash = %x, want %x", accepted.ScriptSHA256, wantHash)
	}
	events, err := store.ListCommandEvents(context.Background(), accepted.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Sequence != 1 || events[0].Type != "command_queued" || len(events[0].Payload) != 0 || events[0].ByteCount != 0 {
		t.Fatalf("queued events = %+v", events)
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedDB, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedDB.Close()
	reopened, err := NewAuthorityStoreWithClock(reopenedDB, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	reopenedCommand, err := reopened.GetCommand(context.Background(), accepted.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if string(reopenedCommand.ScriptBytes) != script || reopenedCommand.RequestHash.String() != hash.String() || reopenedCommand.Ordinal != 1 {
		t.Fatalf("reopened command = %+v", reopenedCommand)
	}
}

func TestP014RejectsInvalidOrOversizedScriptBeforeInsert(t *testing.T) {
	root := testfixture.New(t)
	db, err := Open(context.Background(), root.Path()+"/state/reject.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	create := p013Acceptance(t, "session-p014-reject", "key-p014-reject", "linux-dev")
	created, _, err := store.AcceptSessionCreate(context.Background(), create)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionSession(context.Background(), created.SessionID, domain.SessionStateReady, "agent_ready"); err != nil {
		t.Fatal(err)
	}
	base := CommandAcceptance{SessionID: created.SessionID, RequestHash: p014Hash(t, `{"operation":"submit_command","session_id":"session-p014-reject","script":"x"}`), Timeout: time.Second}
	invalid := base
	invalid.CommandID = "command-p014-invalid"
	invalid.Script = string([]byte{0xff})
	if _, err := store.AcceptCommand(context.Background(), invalid); !errors.Is(err, domain.ErrScriptInvalidUTF8) {
		t.Fatalf("invalid UTF-8 error = %v", err)
	}
	oversized := base
	oversized.CommandID = "command-p014-large"
	oversized.Script = string(make([]byte, domain.MaxScriptUTF8Bytes+1))
	if _, err := store.AcceptCommand(context.Background(), oversized); !errors.Is(err, domain.ErrScriptTooLarge) {
		t.Fatalf("oversized script error = %v", err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM exec_commands").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rejected scripts inserted %d commands", count)
	}
}

func TestP014CorruptScriptIsRejectedAndDuplicateInsertRollsBack(t *testing.T) {
	root := testfixture.New(t)
	db, err := Open(context.Background(), root.Path()+"/state/corrupt.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	create := p013Acceptance(t, "session-p014-corrupt", "key-p014-corrupt", "linux-dev")
	created, _, err := store.AcceptSessionCreate(context.Background(), create)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionSession(context.Background(), created.SessionID, domain.SessionStateReady, "agent_ready"); err != nil {
		t.Fatal(err)
	}
	input := CommandAcceptance{CommandID: "command-p014-corrupt", SessionID: created.SessionID, RequestHash: p014Hash(t, `{"operation":"submit_command","session_id":"session-p014-corrupt","script":"echo ok"}`), Script: "echo ok", Timeout: time.Second}
	accepted, err := store.AcceptCommand(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcceptCommand(context.Background(), input); err == nil {
		t.Fatal("duplicate command ID unexpectedly succeeded")
	}
	var eventCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM exec_command_events WHERE command_id = ?", string(accepted.CommandID)).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 {
		t.Fatalf("duplicate insert changed event count to %d", eventCount)
	}
	if _, err := db.Exec("UPDATE exec_commands SET script_bytes = ? WHERE command_id = ?", []byte("tampered"), string(accepted.CommandID)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetCommand(context.Background(), accepted.CommandID); !errors.Is(err, ErrCommandPayloadCorrupt) {
		t.Fatalf("corrupt script read error = %v", err)
	}

}

func p014Hash(t *testing.T, request string) domain.CanonicalHash {
	t.Helper()
	hash, err := domain.HashMutationRequestJSON("submit_command", []byte(request), domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return hash
}
