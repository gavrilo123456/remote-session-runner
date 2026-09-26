package runtime

import (
	"bytes"
	"context"
	"testing"
)

func TestP034O02OutputCapTruncatesOnceContinuesDrainingAndKeepsShellUsable(t *testing.T) {
	const capBytes = 64 * 1024
	shell, err := StartPersistentShell(context.Background(), PersistentShellOptions{
		SessionID:      "session-p034-cap",
		Generation:     "generation-p034-cap",
		MaxOutputBytes: capBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := shell.Close(); err != nil {
			t.Errorf("close shell: %v", err)
		}
	}()

	result, err := shell.RunScript(context.Background(), "command-p034-cap", []byte("head -c 200000 /dev/zero\nprintf 'tail'\n"))
	if err != nil {
		t.Fatal(err)
	}
	if result.CommandComplete.ExitCode == nil || *result.CommandComplete.ExitCode != 0 {
		t.Fatalf("completion = %+v", result.CommandComplete)
	}
	if !result.OutputTruncated || result.OutputTruncationEvents != 1 {
		t.Fatalf("truncation = %t events=%d, want true and exactly one event", result.OutputTruncated, result.OutputTruncationEvents)
	}
	if len(result.Stdout) != capBytes {
		t.Fatalf("retained stdout bytes = %d, want cap %d", len(result.Stdout), capBytes)
	}
	if !bytes.Equal(result.Stdout, make([]byte, capBytes)) {
		t.Fatal("retained output was not the original raw prefix")
	}
	for index, chunk := range result.Chunks {
		if len(chunk.Data) > MaxOutputChunkBytes {
			t.Fatalf("chunk %d length = %d, exceeds bound", index, len(chunk.Data))
		}
	}

	continued, err := shell.RunScript(context.Background(), "command-p034-after-cap", []byte("printf 'after-cap'\n"))
	if err != nil {
		t.Fatal(err)
	}
	if continued.OutputTruncated || continued.OutputTruncationEvents != 0 || string(continued.Stdout) != "after-cap" {
		t.Fatalf("post-cap result = %+v, want untruncated after-cap output", continued)
	}
}
