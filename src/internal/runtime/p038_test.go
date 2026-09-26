package runtime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestP038PMac02SourceProvenanceAndUncommittedLocalWorktree(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("P038 is a macOS host gate")
	}
	rootBytes, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatal(err)
	}
	root := strings.TrimSpace(string(rootBytes))
	adapter, err := NewMacProcessAdapter(MacRuntimeOptions{Account: "tomasz.walczuk", WorkspaceRoot: t.TempDir(), RepositoryAliases: map[string]string{"runner": root}})
	if err != nil {
		t.Fatal(err)
	}
	empty, err := adapter.PrepareSource(context.Background(), "session-p038-empty", "generation-p038-empty", MacSourceRequest{Mode: MacSourceEmpty})
	if err != nil {
		t.Fatal(err)
	}
	if empty.Source.Mode != MacSourceEmpty || !empty.Source.Portable || !empty.OwnedWorkspace {
		t.Fatalf("empty provenance = %+v", empty)
	}
	if err := adapter.Cleanup(empty.SessionID); err != nil {
		t.Fatal(err)
	}

	headBytes, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	head := strings.TrimSpace(string(headBytes))
	prepared, err := adapter.PrepareSource(context.Background(), "session-p038-git", "generation-p038-git", MacSourceRequest{Mode: MacSourceGitRevision, RepositoryAlias: "runner", RequestedRevision: head})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Source.ResolvedRevision != head || !prepared.Source.Portable || !prepared.OwnedWorkspace {
		t.Fatalf("git provenance = %+v", prepared)
	}
	if err := adapter.Cleanup(prepared.SessionID); err != nil {
		t.Fatal(err)
	}

	local := t.TempDir()
	marker := filepath.Join(local, "uncommitted.txt")
	if err := os.WriteFile(marker, []byte("visible"), 0o600); err != nil {
		t.Fatal(err)
	}
	localPrepared, err := adapter.PrepareSource(context.Background(), "session-p038-local", "generation-p038-local", MacSourceRequest{Mode: MacSourceLocalWorktree, Path: local})
	if err != nil {
		t.Fatal(err)
	}
	canonicalLocal, err := filepath.EvalSymlinks(local)
	if err != nil {
		t.Fatal(err)
	}
	if localPrepared.Source.Mode != MacSourceLocalWorktree || localPrepared.Source.Portable || localPrepared.OwnedWorkspace || localPrepared.Workspace != canonicalLocal {
		t.Fatalf("local provenance = %+v", localPrepared)
	}
	if err := adapter.StartAgent(context.Background(), localPrepared); err != nil {
		t.Fatal(err)
	}
	shell, err := adapter.Shell(localPrepared.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := shell.RunScript(context.Background(), "command-p038-local", []byte("cat uncommitted.txt\n"))
	if err != nil || string(result.Stdout) != "visible" {
		t.Fatalf("local worktree result=%q err=%v", result.Stdout, err)
	}
	if err := adapter.Cleanup(localPrepared.SessionID); err != nil {
		t.Fatal(err)
	}
}
