package runtime

import (
	"context"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"remote-session-runner/src/internal/domain"
)

func p045Git(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output))
}

func p045Commit(t *testing.T, repository, contents string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repository, "tracked.txt"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	p045Git(t, repository, "add", "tracked.txt")
	p045Git(t, repository, "-c", "user.name=P045 Fixture", "-c", "user.email=p045@example.invalid", "commit", "-m", contents)
	return p045Git(t, repository, "rev-parse", "HEAD")
}

func TestP045LinuxRepositoryAliasAndCredentialValidation(t *testing.T) {
	if _, _, err := validateLinuxRepositoryLocation("https://example.invalid/fixture.git"); err != nil {
		t.Fatalf("HTTPS alias validation = %v", err)
	}
	if _, _, err := validateLinuxRepositoryLocation("ssh://example.invalid/fixture.git"); err == nil {
		t.Fatal("SSH alias unexpectedly accepted")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	credential := filepath.Join(root, "credential")
	if err := os.WriteFile(credential, []byte("username=test\npassword=test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateLinuxCredentialReference(credential); err != nil {
		t.Fatalf("owner-only credential validation = %v", err)
	}
	if err := os.Chmod(credential, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateLinuxCredentialReference(credential); err == nil {
		t.Fatal("group-readable credential unexpectedly accepted")
	}
}

func TestP045LinuxExactGitRevisionAndCredentialBoundary(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("P045 is a Linux host-process source gate")
	}
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	repository := filepath.Join(root, "fixture-repository")
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	p045Git(t, repository, "init")
	p045Git(t, repository, "checkout", "-b", "main")
	commitA := p045Commit(t, repository, "revision-a")
	commitB := ""
	hookDir := filepath.Join(root, "outside-hooks")
	if err := os.Mkdir(hookDir, 0o700); err != nil {
		t.Fatal(err)
	}
	hookMarker := filepath.Join(root, "hook-fired-outside-workspace")
	hook := filepath.Join(hookDir, "post-checkout")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf hook > "+hookMarker+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	p045Git(t, repository, "config", "core.hooksPath", hookDir)
	credential := filepath.Join(root, "git-credential")
	if err := os.WriteFile(credential, []byte("username=p045-user\npassword=p045-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspaceRoot := filepath.Join(root, "workspaces")
	if err := os.Mkdir(workspaceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	adapter, err := NewLinuxProcessAdapter(LinuxRuntimeOptions{
		Account:       current.Username,
		WorkspaceRoot: workspaceRoot,
		ShellPath:     "/usr/bin/bash",
		RepositoryAliases: map[string]LinuxRepositoryAlias{
			"fixture": {Location: repository, CredentialReference: credential},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := adapter.PrepareSource(context.Background(), "session-p045", "generation-p045", LinuxSourceRequest{
		Mode:              domain.SourceModeGitRevision,
		RepositoryAlias:   "fixture",
		RequestedRevision: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Source.Mode != domain.SourceModeGitRevision || prepared.Source.ResolvedRevision != commitA || !prepared.Source.Portable || !prepared.OwnedWorkspace {
		t.Fatalf("prepared source = %+v, want detached revision %s", prepared.Source, commitA)
	}
	if got := p045Git(t, prepared.Workspace, "rev-parse", "HEAD"); got != commitA {
		t.Fatalf("prepared HEAD = %s, want %s", got, commitA)
	}
	if _, err := os.Stat(hookMarker); !os.IsNotExist(err) {
		t.Fatalf("outside hook marker = %v, hook policy did not hold", err)
	}
	if got := p045Git(t, prepared.Workspace, "config", "--get", "core.hooksPath"); got != "/dev/null" {
		t.Fatalf("prepared core.hooksPath = %q, want /dev/null", got)
	}
	commitB = p045Commit(t, repository, "revision-b")
	if commitB == commitA {
		t.Fatal("fixture commits unexpectedly match")
	}
	if err := adapter.StartAgent(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	shell, err := adapter.Shell(prepared.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := shell.RunScript(context.Background(), "command-p045-source", []byte("printf '%s|' \"$(git rev-parse HEAD)\"\ncat tracked.txt\nprintf '\\n'\nif env | grep -F 'p045-secret' >/dev/null; then exit 41; fi\nprintf ready\n"))
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Stdout) != commitA+"|revision-a\nready" {
		t.Fatalf("pinned source result = %q, want commit A and original content", result.Stdout)
	}
	if err := adapter.Cleanup(prepared.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(prepared.Workspace); !os.IsNotExist(err) {
		t.Fatalf("prepared workspace remains after cleanup: %v", err)
	}
}
