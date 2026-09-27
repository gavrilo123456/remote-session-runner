package runtime

import (
	"context"
	"testing"
	"time"
)

func TestP104OutputCallbackRunsBeforeCommandCompletes(t *testing.T) {
	shell, err := StartPersistentShell(context.Background(), PersistentShellOptions{
		SessionID: "session-p104-stream", Generation: "generation-p104-stream",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := shell.Close(); err != nil {
			t.Errorf("close shell: %v", err)
		}
	}()

	type runResult struct {
		result PersistentShellResult
		err    error
	}
	completed := make(chan runResult, 1)
	output := make(chan OutputChunk, 4)
	go func() {
		result, err := shell.RunScriptWithOutput(context.Background(), "command-p104-stream", []byte("printf 'prefix\\n'\nsleep 4\nprintf 'tail\\n'\n"), func(chunk OutputChunk) error {
			output <- chunk
			return nil
		})
		completed <- runResult{result: result, err: err}
	}()

	select {
	case chunk := <-output:
		if chunk.Stream != OutputStreamStdout || string(chunk.Data) != "prefix\n" {
			t.Fatalf("early output chunk = %+v, want stdout prefix", chunk)
		}
	case <-time.After(3500 * time.Millisecond):
		t.Fatal("first output callback did not run while command was active")
	}
	select {
	case result := <-completed:
		t.Fatalf("command completed before its delayed tail: err=%v result=%+v", result.err, result.result)
	default:
	}

	select {
	case result := <-completed:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.result.CommandComplete.ExitCode == nil || *result.result.CommandComplete.ExitCode != 0 {
			t.Fatalf("completion = %+v", result.result.CommandComplete)
		}
		if got := string(result.result.Stdout); got != "prefix\ntail\n" {
			t.Fatalf("captured stdout = %q", got)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("command did not reach completion")
	}
}
