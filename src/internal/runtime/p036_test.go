package runtime

import (
	"context"
	"errors"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestP036R03ResidualDescendantRetainsCapacityUntilConfirmedCleanup(t *testing.T) {
	var treeMu sync.Mutex
	residual := true
	inspect := func(int) ([]DescendantProcess, error) {
		treeMu.Lock()
		defer treeMu.Unlock()
		if !residual {
			return nil, nil
		}
		return []DescendantProcess{{PID: 99, Parent: 98, Command: "sleep 0.4"}}, nil
	}
	kill := func(int, syscall.Signal) int {
		treeMu.Lock()
		residual = false
		treeMu.Unlock()
		return 1
	}
	shell, err := StartPersistentShell(context.Background(), PersistentShellOptions{
		SessionID:             "session-p036-residual",
		Generation:            "generation-p036-residual",
		OutputBoundaryTimeout: 60 * time.Millisecond,
		ProcessInspector:      inspect,
		DescendantKiller:      kill,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	if _, err := shell.RunScript(context.Background(), "command-p036-residual", []byte("(sleep 0.4) &\nprintf 'started'\n")); !errors.Is(err, ErrOutputBoundary) {
		t.Fatalf("residual command error = %v, want ErrOutputBoundary", err)
	}
	if !shell.CapacityRetained() {
		t.Fatal("uncertain residual did not retain capacity")
	}
	descendants, err := shell.InspectDescendants()
	if err != nil {
		t.Fatal(err)
	}
	if len(descendants) == 0 {
		t.Fatal("expected at least one residual descendant")
	}
	cleanup, err := shell.CleanupDescendants(context.Background(), 500*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !cleanup.Confirmed || len(cleanup.Remaining) != 0 {
		t.Fatalf("cleanup = %+v, want confirmed and empty", cleanup)
	}
	if shell.CapacityRetained() {
		t.Fatal("confirmed cleanup still retained capacity")
	}
	if _, err := shell.RunScript(context.Background(), "command-p036-after-loss", []byte("printf 'must-not-run'\n")); !errors.Is(err, ErrPersistentShellLost) {
		t.Fatalf("post-loss run error = %v, want ErrPersistentShellLost", err)
	}
}
