package logcapture

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSimpleLaneCaptureForwardsSIGTERMAndWaitsForGracefulChildExit(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("POSIX shell is unavailable")
	}
	root := t.TempDir()
	readyPath := filepath.Join(root, "ready")
	handledPath := filepath.Join(root, "term-handled")
	logPath := filepath.Join(root, "runner.log")
	script := `trap 'printf handled > "$1"; exit 0' TERM; printf ready > "$2"; while :; do :; done`
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		done <- RunChildWithLimits(ctx, logPath,
			[]string{"/bin/sh", "-c", script, "runner-stop-probe", handledPath, readyPath},
			SimpleLaneLimits())
	}()

	deadline := time.NewTimer(3 * time.Second)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		select {
		case <-deadline.C:
			cancel()
			t.Fatal("child did not start before the shutdown probe deadline")
		case <-ticker.C:
		}
	}
	cancel()

	select {
	case exit := <-done:
		if exit != 0 {
			t.Fatalf("gracefully terminated child exit=%d, want 0", exit)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("capture did not finish after forwarding SIGTERM")
	}
	contents, err := os.ReadFile(handledPath)
	if err != nil || string(contents) != "handled" {
		t.Fatalf("child TERM handler contents=%q err=%v", contents, err)
	}
}
