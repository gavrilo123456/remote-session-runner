package releaseflow

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitResolverRequiresMatchingPublishedHistoryAndPreservesCheckout(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "work")
	primary := filepath.Join(root, "primary.git")
	mirror := filepath.Join(root, "mirror.git")
	initGitTestBranch(t, work, "dev")
	runGitTest(t, "-C", work, "config", "user.name", "Fixture")
	runGitTest(t, "-C", work, "config", "user.email", "fixture@example.invalid")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, "-C", work, "add", "README.md")
	runGitTest(t, "-C", work, "commit", "-m", "first")
	first := strings.TrimSpace(runGitTest(t, "-C", work, "rev-parse", "HEAD"))
	runGitTest(t, "init", "--bare", primary)
	runGitTest(t, "init", "--bare", mirror)
	runGitTest(t, "-C", work, "remote", "add", "primary", primary)
	runGitTest(t, "-C", work, "remote", "add", "mirror", mirror)
	runGitTest(t, "-C", work, "push", "primary", "dev")
	runGitTest(t, "-C", work, "push", "mirror", "dev")
	runGitTest(t, "-C", work, "remote", "add", "origin", primary)
	runGitTest(t, "-C", work, "remote", "add", "github", mirror)
	resolver := GitResolver{Repository: work, TempRoot: root}
	resolved, err := resolver.Resolve(context.Background(), "dev", first)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Commit != first || resolved.CapturedTip != first || resolved.PrimaryRef != resolved.MirrorRef {
		t.Fatalf("%+v", resolved)
	}
	commitOnly, err := resolver.Resolve(context.Background(), "", first)
	if err != nil || commitOnly.Branch != "dev" || commitOnly.Commit != first {
		t.Fatalf("commit-only source did not retain its verified branch witness: %+v %v", commitOnly, err)
	}
	runGitTest(t, "-C", work, "push", "primary", first+":refs/heads/prd")
	runGitTest(t, "-C", work, "push", "mirror", first+":refs/heads/prd")
	fromPRD, err := resolver.Resolve(context.Background(), "prd", first)
	if err != nil || fromPRD.Branch != "prd" || fromPRD.CapturedTip != first {
		t.Fatalf("published prd source selection failed: %+v %v", fromPRD, err)
	}
	if err := os.WriteFile(filepath.Join(work, "untracked.txt"), []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(context.Background(), "dev", first); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(filepath.Join(work, "untracked.txt")); err != nil || string(body) != "keep me" {
		t.Fatalf("dirty checkout changed: %q %v", body, err)
	}
	if _, err := resolver.Resolve(context.Background(), "dev", strings.Repeat("f", 40)); err == nil {
		t.Fatal("unpublished commit was accepted")
	}
	if _, err := resolver.Resolve(context.Background(), "../dev", ""); err == nil {
		t.Fatal("unsafe branch was accepted")
	}
}

func TestGitResolverRejectsBranchMovementDuringSourceFreeze(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "work")
	primary := filepath.Join(root, "primary.git")
	mirror := filepath.Join(root, "mirror.git")
	initGitTestBranch(t, work, "dev")
	runGitTest(t, "-C", work, "config", "user.name", "Fixture")
	runGitTest(t, "-C", work, "config", "user.email", "fixture@example.invalid")
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, "-C", work, "add", "a.txt")
	runGitTest(t, "-C", work, "commit", "-m", "base")
	base := strings.TrimSpace(runGitTest(t, "-C", work, "rev-parse", "HEAD"))
	runGitTest(t, "init", "--bare", primary)
	runGitTest(t, "init", "--bare", mirror)
	runGitTest(t, "-C", work, "remote", "add", "primary", primary)
	runGitTest(t, "-C", work, "remote", "add", "mirror", mirror)
	runGitTest(t, "-C", work, "push", "primary", "dev")
	runGitTest(t, "-C", work, "push", "mirror", "dev")
	runGitTest(t, "-C", work, "remote", "add", "origin", primary)
	runGitTest(t, "-C", work, "remote", "add", "github", mirror)
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("advanced\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, "-C", work, "commit", "-am", "branch moves during freeze")
	resolver := GitResolver{Repository: work, TempRoot: root}
	resolver.afterProof = func() {
		runGitTest(t, "-C", work, "push", "primary", "dev")
		runGitTest(t, "-C", work, "push", "mirror", "dev")
	}
	if _, err := resolver.Resolve(context.Background(), "dev", base); err == nil || !errors.Is(err, ErrReleaseMismatch) {
		t.Fatalf("source freeze accepted a moving primary/mirror branch: %v", err)
	}
}

func TestGitResolverRejectsMirrorLagAndCommitOutsideBranch(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "work")
	primary := filepath.Join(root, "primary.git")
	mirror := filepath.Join(root, "mirror.git")
	initGitTestBranch(t, work, "dev")
	runGitTest(t, "-C", work, "config", "user.name", "Fixture")
	runGitTest(t, "-C", work, "config", "user.email", "fixture@example.invalid")
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, "-C", work, "add", "a.txt")
	runGitTest(t, "-C", work, "commit", "-m", "base")
	base := strings.TrimSpace(runGitTest(t, "-C", work, "rev-parse", "HEAD"))
	runGitTest(t, "init", "--bare", primary)
	runGitTest(t, "init", "--bare", mirror)
	runGitTest(t, "-C", work, "remote", "add", "primary", primary)
	runGitTest(t, "-C", work, "remote", "add", "mirror", mirror)
	runGitTest(t, "-C", work, "push", "primary", "dev")
	runGitTest(t, "-C", work, "push", "mirror", "dev")
	runGitTest(t, "-C", work, "remote", "add", "origin", primary)
	runGitTest(t, "-C", work, "remote", "add", "github", mirror)
	resolver := GitResolver{Repository: work, TempRoot: root}
	if _, err := resolver.Resolve(context.Background(), "dev", base); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "b.txt"), []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, "-C", work, "add", "b.txt")
	runGitTest(t, "-C", work, "commit", "-m", "primary only")
	primaryOnly := strings.TrimSpace(runGitTest(t, "-C", work, "rev-parse", "HEAD"))
	runGitTest(t, "-C", work, "push", "primary", "dev")
	if _, err := resolver.Resolve(context.Background(), "dev", ""); err == nil {
		t.Fatal("mirror lag accepted")
	}
	runGitTest(t, "-C", work, "push", "mirror", "dev")
	if _, err := resolver.Resolve(context.Background(), "dev", base); err != nil {
		t.Fatal(err)
	}
	// A detached commit from another branch is not a member of dev's history.
	runGitTest(t, "-C", work, "checkout", "--orphan", "other")
	runGitTest(t, "-C", work, "rm", "-rf", ".")
	if err := os.WriteFile(filepath.Join(work, "other.txt"), []byte("other"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, "-C", work, "add", "other.txt")
	runGitTest(t, "-C", work, "commit", "-m", "unrelated")
	unrelated := strings.TrimSpace(runGitTest(t, "-C", work, "rev-parse", "HEAD"))
	if _, err := resolver.Resolve(context.Background(), "dev", unrelated); err == nil {
		t.Fatalf("unrelated commit %s accepted (primary tip %s)", unrelated, primaryOnly)
	}
}

func runGitTest(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// Keep disposable Git fixtures compatible with the oldest supported Git on
// the Ubuntu workers: `git init -b` is not available there.
func initGitTestBranch(t *testing.T, path, branch string) {
	t.Helper()
	runGitTest(t, "init", path)
	runGitTest(t, "-C", path, "checkout", "-b", branch)
}
