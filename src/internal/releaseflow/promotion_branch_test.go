package releaseflow

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeBranchMarkerFastForwardAndMirrorDelivery(t *testing.T) {
	fixture := newBranchMarkerFixture(t, true)
	backend := &NativePromotionBackend{Repository: fixture.work}
	if err := backend.UpdateSourceBranch(context.Background(), "qas", fixture.candidate, fixture.base); err != nil {
		t.Fatal(err)
	}
	if got := bareRef(t, fixture.primary, "refs/heads/qas"); got != fixture.candidate {
		t.Fatalf("primary qas=%s want %s", got, fixture.candidate)
	}
	if got := bareRef(t, fixture.mirror, "refs/heads/qas"); got != fixture.candidate {
		t.Fatalf("mirror qas=%s want %s", got, fixture.candidate)
	}
}

func TestNativeBranchMarkerRejectsDivergentConcurrentFastForwardRace(t *testing.T) {
	fixture := newBranchMarkerFixture(t, false)
	if err := os.WriteFile(filepath.Join(fixture.work, "racer.txt"), []byte("concurrent marker update\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, "-C", fixture.work, "checkout", "-b", "racer", fixture.base)
	runGitTest(t, "-C", fixture.work, "add", "racer.txt")
	runGitTest(t, "-C", fixture.work, "commit", "-m", "concurrent source marker")
	racer := strings.TrimSpace(runGitTest(t, "-C", fixture.work, "rev-parse", "HEAD"))
	runGitTest(t, "-C", fixture.work, "push", "origin", racer+":refs/heads/racer")
	runGitTest(t, "-C", fixture.work, "checkout", "dev")
	hook := filepath.Join(fixture.work, ".git", "hooks", "pre-push")
	body := fmt.Sprintf("#!/bin/sh\nexec git --git-dir=%q update-ref refs/heads/qas %q %q\n", fixture.primary, racer, fixture.base)
	if err := os.WriteFile(hook, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	backend := &NativePromotionBackend{Repository: fixture.work}
	err := backend.UpdateSourceBranch(context.Background(), "qas", fixture.candidate, fixture.base)
	if err == nil || !strings.Contains(err.Error(), "fast-forward") {
		t.Fatalf("divergent primary update was not rejected by Git receive-pack: %v", err)
	}
	if got := bareRef(t, fixture.primary, "refs/heads/qas"); got != racer {
		t.Fatalf("racing primary marker was overwritten: got=%s want=%s", got, racer)
	}
	if got := bareRef(t, fixture.mirror, "refs/heads/qas"); got != fixture.base {
		t.Fatalf("mirror should remain on pre-race marker: got=%s want=%s", got, fixture.base)
	}
}

func TestNativeBranchMarkerRetriesAfterPrimaryAdvanceAndMirrorLag(t *testing.T) {
	fixture := newBranchMarkerFixture(t, false)
	backend := &NativePromotionBackend{Repository: fixture.work}
	err := backend.UpdateSourceBranch(context.Background(), "qas", fixture.candidate, fixture.base)
	if err == nil || !strings.Contains(err.Error(), "mirror synchronization is pending") {
		t.Fatalf("expected primary success with mirror lag, got %v", err)
	}
	if got := bareRef(t, fixture.primary, "refs/heads/qas"); got != fixture.candidate {
		t.Fatalf("primary did not retain accepted marker: %s", got)
	}
	if got := bareRef(t, fixture.mirror, "refs/heads/qas"); got != fixture.base {
		t.Fatalf("mirror unexpectedly moved before simulated catch-up: %s", got)
	}
	runGitTest(t, "--git-dir", fixture.mirror, "update-ref", "refs/heads/qas", fixture.candidate, fixture.base)
	if err := backend.UpdateSourceBranch(context.Background(), "qas", fixture.candidate, fixture.base); err != nil {
		t.Fatalf("Git-only idempotent retry after mirror catch-up failed: %v", err)
	}
	if got := bareRef(t, fixture.primary, "refs/heads/qas"); got != fixture.candidate {
		t.Fatalf("retry changed primary marker: %s", got)
	}
	if got := bareRef(t, fixture.mirror, "refs/heads/qas"); got != fixture.candidate {
		t.Fatalf("mirror marker=%s want %s", got, fixture.candidate)
	}
}

type branchMarkerFixture struct {
	work, primary, mirror, base, candidate string
}

func newBranchMarkerFixture(t *testing.T, mirrorHook bool) branchMarkerFixture {
	t.Helper()
	root := t.TempDir()
	fixture := branchMarkerFixture{work: filepath.Join(root, "work"), primary: filepath.Join(root, "primary.git"), mirror: filepath.Join(root, "mirror.git")}
	initGitTestBranch(t, fixture.work, "dev")
	runGitTest(t, "-C", fixture.work, "config", "user.name", "Promotion fixture")
	runGitTest(t, "-C", fixture.work, "config", "user.email", "promotion@example.invalid")
	if err := os.WriteFile(filepath.Join(fixture.work, "base.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, "-C", fixture.work, "add", "base.txt")
	runGitTest(t, "-C", fixture.work, "commit", "-m", "base")
	fixture.base = strings.TrimSpace(runGitTest(t, "-C", fixture.work, "rev-parse", "HEAD"))
	runGitTest(t, "init", "--bare", fixture.primary)
	runGitTest(t, "init", "--bare", fixture.mirror)
	runGitTest(t, "-C", fixture.work, "remote", "add", "origin", fixture.primary)
	runGitTest(t, "-C", fixture.work, "remote", "add", "github", fixture.mirror)
	runGitTest(t, "-C", fixture.work, "push", "origin", "dev")
	runGitTest(t, "-C", fixture.work, "push", "github", "dev")
	runGitTest(t, "-C", fixture.work, "push", "origin", fixture.base+":refs/heads/qas")
	runGitTest(t, "-C", fixture.work, "push", "github", fixture.base+":refs/heads/qas")
	if err := os.WriteFile(filepath.Join(fixture.work, "candidate.txt"), []byte("candidate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, "-C", fixture.work, "add", "candidate.txt")
	runGitTest(t, "-C", fixture.work, "commit", "-m", "candidate")
	fixture.candidate = strings.TrimSpace(runGitTest(t, "-C", fixture.work, "rev-parse", "HEAD"))
	runGitTest(t, "-C", fixture.work, "push", "origin", "dev")
	runGitTest(t, "-C", fixture.work, "push", "github", "dev")
	if mirrorHook {
		runGitTest(t, "--git-dir", fixture.mirror, "remote", "add", "origin", fixture.primary)
		hook := filepath.Join(fixture.primary, "hooks", "post-receive")
		body := fmt.Sprintf("#!/bin/sh\nwhile read old new ref; do\n  git --git-dir=%q fetch --quiet origin \"$ref:$ref\" || exit 1\ndone\n", fixture.mirror)
		if err := os.WriteFile(hook, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return fixture
}

func bareRef(t *testing.T, repo, ref string) string {
	t.Helper()
	cmd := exec.Command("git", "--git-dir", repo, "rev-parse", ref)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git --git-dir %s rev-parse %s: %v: %s", repo, ref, err, out)
	}
	return strings.TrimSpace(string(out))
}
