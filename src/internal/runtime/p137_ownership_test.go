package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"
)

func TestP137RuntimeOwnershipAuditAllowsKnownResidualAndBlocksUnattributed(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "session")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeRuntimeOwnership(root, RuntimeOwnershipRecord{
		Version: runtimeOwnershipVersion, HostOS: goruntime.GOOS,
		SessionID: "session-p137-known", Generation: "generation-p137",
		Workspace: workspace, PID: os.Getpid(), ProcessGroupID: os.Getpid(),
		UID: os.Getuid(), Username: "test-account", Command: "/bin/bash",
		ProcessStartIdentity: "test-start-identity",
	}); err != nil {
		t.Fatal(err)
	}

	if err := auditRuntimeOwnership(context.Background(), root, map[string]struct{}{"session-p137-known": {}}); err != nil {
		t.Fatalf("known live reservation was not attributed: %v", err)
	}
	if err := auditRuntimeOwnership(context.Background(), root, map[string]struct{}{}); !errors.Is(err, ErrRuntimeOwnershipRecord) {
		t.Fatalf("unattributed owner error = %v, want ErrRuntimeOwnershipRecord", err)
	}
}

func TestP137RuntimeOwnershipAuditBlocksIncompleteOwnerRecord(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, runtimeOwnershipDirectory)
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, ".ownership-p137.tmp"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := auditRuntimeOwnership(context.Background(), root, map[string]struct{}{}); !errors.Is(err, ErrRuntimeOwnershipRecord) {
		t.Fatalf("incomplete owner entry error = %v, want ErrRuntimeOwnershipRecord", err)
	}
}
