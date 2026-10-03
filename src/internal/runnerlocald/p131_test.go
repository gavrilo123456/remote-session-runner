package runnerlocald

import (
	"context"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
)

func TestP131PrivateServerDrainFinishesAcceptedWorkAndClosesLocalRuntime(t *testing.T) {
	authority, service := newP060Service(t)
	server, worker := startBUG011P4PrivateServer(t, authority, service, p060SocketPath(t))

	createIntent := p060CreateIntent(t, authority, "intent-p131-create", "session-p131-create", "key-p131-create")
	if _, err := server.acceptIntent(context.Background(), createIntent); err != nil {
		t.Fatalf("accept local session: %v", err)
	}
	submitInput := p060SubmitIntent(t, "intent-p131-submit", string(createIntent.SessionID), "command-p131-submit", "key-p131-submit", "printf p131-drained")
	submitIntent, err := authority.CreateLocalIntent(context.Background(), submitInput)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := server.acceptIntent(context.Background(), submitIntent)
	if err != nil {
		t.Fatalf("accept queued command: %v", err)
	}
	if accepted.CommandState != string(domain.CommandStateQueued) {
		t.Fatalf("command acceptance state = %q, want queued", accepted.CommandState)
	}
	server.wakeQueueAfterAcceptedIntent(accepted)
	command := waitBUG011P4Command(t, authority, submitIntent.CommandID, domain.CommandStateSucceeded)
	if !command.OutputComplete {
		t.Fatalf("worker command output_complete=%v, want true", command.OutputComplete)
	}

	shutdown := &privateServerShutdown{server: server, worker: worker}
	shutdown.StopAccepting()
	shutdown.StopDispatch()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := shutdown.Drain(ctx); err != nil {
		t.Fatalf("drain local executor: %v", err)
	}
	command, err = authority.GetCommand(context.Background(), submitIntent.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if command.State != domain.CommandStateSucceeded || !command.OutputComplete {
		t.Fatalf("drained command state=%s output_complete=%v", command.State, command.OutputComplete)
	}
	session, err := authority.GetSession(context.Background(), createIntent.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if session.State != domain.SessionStateClosed {
		t.Fatalf("session state after confirmed runtime cleanup = %s, want closed", session.State)
	}
	events, err := authority.ListCommandEvents(context.Background(), submitIntent.CommandID)
	if err != nil || len(events) < 2 {
		t.Fatalf("drained command event tail=%v err=%v", events, err)
	}
	if err := server.Flush(context.Background()); err != nil {
		t.Fatalf("flush committed audit tail: %v", err)
	}
	rows, err := authority.ListAuditRecords(context.Background(), 32)
	if err != nil {
		t.Fatal(err)
	}
	foundClose := false
	for _, row := range rows {
		if row.SessionID == createIntent.SessionID && row.Action == "close" {
			foundClose = true
			break
		}
	}
	if !foundClose {
		t.Fatal("normal session close did not commit its audit row")
	}
	if err := server.CloseStreams(); err != nil {
		t.Fatalf("close event streams: %v", err)
	}
}
