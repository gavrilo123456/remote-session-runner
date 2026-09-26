package runtime

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestP032O01ConcurrentRawDrainPreservesBinaryBytesAndChunkBounds(t *testing.T) {
	shell, err := StartPersistentShell(context.Background(), PersistentShellOptions{SessionID: "session-p032-bytes", Generation: "generation-p032-bytes"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := shell.Close(); err != nil {
			t.Errorf("close shell: %v", err)
		}
	}()

	result, err := shell.RunScript(context.Background(), "command-p032-bytes", []byte("head -c 40000 /dev/zero\nprintf 'out\\000\\377\\n'\nprintf 'err\\001\\376' >&2\n"))
	if err != nil {
		t.Fatal(err)
	}
	if result.CommandComplete.ExitCode == nil || *result.CommandComplete.ExitCode != 0 {
		t.Fatalf("completion = %+v", result.CommandComplete)
	}
	if len(result.Stdout) != 40006 || !bytes.Equal(result.Stdout[40000:], []byte("out\x00\xff\n")) {
		t.Fatalf("stdout length/tail = %d/%v", len(result.Stdout), result.Stdout[max(0, len(result.Stdout)-6):])
	}
	if !bytes.Equal(result.Stderr, []byte("err\x01\xfe")) {
		t.Fatalf("stderr = %v", result.Stderr)
	}
	if len(result.Chunks) < 4 {
		t.Fatalf("chunks = %d, want separate bounded stdout/stderr chunks", len(result.Chunks))
	}
	var stdout, stderr []byte
	for index, chunk := range result.Chunks {
		if len(chunk.Data) == 0 || len(chunk.Data) > MaxOutputChunkBytes {
			t.Fatalf("chunk %d length = %d", index, len(chunk.Data))
		}
		if index > 0 && result.Chunks[index-1].Sequence >= chunk.Sequence {
			t.Fatalf("chunk sequence %d then %d is not increasing", result.Chunks[index-1].Sequence, chunk.Sequence)
		}
		switch chunk.Stream {
		case OutputStreamStdout:
			stdout = append(stdout, chunk.Data...)
		case OutputStreamStderr:
			stderr = append(stderr, chunk.Data...)
		default:
			t.Fatalf("unknown output stream %q", chunk.Stream)
		}
	}
	if !bytes.Equal(stdout, result.Stdout) || !bytes.Equal(stderr, result.Stderr) {
		t.Fatalf("chunk reconstruction differs: stdout=%v/%v stderr=%v/%v", len(stdout), len(result.Stdout), stderr, result.Stderr)
	}
}

func TestP032O01PartialOutputFlushesNearFiftyMilliseconds(t *testing.T) {
	shell, err := StartPersistentShell(context.Background(), PersistentShellOptions{SessionID: "session-p032-flush", Generation: "generation-p032-flush"})
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()

	started := time.Now()
	result, err := shell.RunScript(context.Background(), "command-p032-flush", []byte("printf 'a'\nsleep 0.12\nprintf 'b'\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(result.Stdout); got != "ab" {
		t.Fatalf("stdout = %q", got)
	}
	if len(result.Chunks) < 2 {
		t.Fatalf("chunks = %+v, want the first partial chunk flushed before delayed byte", result.Chunks)
	}
	if got := string(result.Chunks[0].Data); got != "a" {
		t.Fatalf("first chunk = %q, want a", got)
	}
	if elapsed := time.Since(started); elapsed < 100*time.Millisecond {
		t.Fatalf("delayed script elapsed %s, want real pipe delay", elapsed)
	}
}
