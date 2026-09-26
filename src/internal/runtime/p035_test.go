package runtime

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestP035R03InterruptWithoutProvenBoundaryReturnsLost(t *testing.T) {
	shell, err := StartPersistentShell(context.Background(), PersistentShellOptions{SessionID: "session-p035-stop", Generation: "generation-p035-stop"})
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	resultCh := make(chan struct {
		result PersistentShellResult
		err    error
	}, 1)
	go func() {
		result, err := shell.RunScript(context.Background(), "command-p035-stop", []byte("printf 'before-stop'\nsleep 0.15\nprintf 'after-stop'\n"))
		resultCh <- struct {
			result PersistentShellResult
			err    error
		}{result: result, err: err}
	}()
	time.Sleep(20 * time.Millisecond)
	stop, err := shell.CancelCurrentCommand(context.Background(), 750*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if stop.CommandID != "command-p035-stop" {
		t.Fatalf("stop result = %+v err=%v, want the active command ID", stop, err)
	}
	run := <-resultCh
	if stop.Confirmed {
		if err != nil || run.err != nil {
			t.Fatalf("confirmed stop err=%v run=%v", err, run.err)
		}
	} else {
		if run.err == nil {
			t.Fatalf("unconfirmed stop = %+v err=%v run=%v", stop, err, run.err)
		}
		if _, err := shell.RunScript(context.Background(), "command-p035-after-stop", []byte("printf 'must-not-run'\n")); !errors.Is(err, ErrPersistentShellLost) {
			t.Fatalf("post-stop error = %v, want ErrPersistentShellLost", err)
		}
	}
}

func TestP035R03UnconfirmedStopReturnsLostAndBlocksReplacement(t *testing.T) {
	shell, err := StartPersistentShell(context.Background(), PersistentShellOptions{SessionID: "session-p035-lost", Generation: "generation-p035-lost"})
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	resultCh := make(chan error, 1)
	go func() {
		_, err := shell.RunScript(context.Background(), "command-p035-lost", []byte("(sleep 0.8) &\nsleep 5\n"))
		resultCh <- err
	}()
	time.Sleep(60 * time.Millisecond)
	stop, err := shell.CancelCurrentCommand(context.Background(), 60*time.Millisecond)
	if stop.Confirmed {
		if err != nil {
			t.Fatalf("confirmed stop error = %v", err)
		}
		// A host may close the shell immediately after the confirmed boundary;
		// the next phase owns the stronger reusable-shell process-tree proof.
	} else if !errors.Is(err, ErrPersistentShellLost) {
		t.Fatalf("unconfirmed stop = %+v err=%v, want lost/unconfirmed", stop, err)
	}
	runErr := <-resultCh
	if stop.Confirmed {
		if runErr != nil {
			t.Fatalf("confirmed stop run error = %v", runErr)
		}
	} else {
		if runErr == nil {
			t.Fatal("run unexpectedly succeeded after unconfirmed stop")
		}
		if _, err := shell.RunScript(context.Background(), "command-p035-after-lost", []byte("printf 'must-not-run'\n")); !errors.Is(err, ErrPersistentShellLost) {
			t.Fatalf("post-lost run error = %v, want ErrPersistentShellLost", err)
		}
	}
}
