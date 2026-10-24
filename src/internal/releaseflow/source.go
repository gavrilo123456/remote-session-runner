package releaseflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
)

// GitResolver proves the same published branch tip exists on primary Gitea
// and its automatic GitHub mirror, then proves the selected commit belongs to
// that captured history. It never updates canonical checkout refs.
type GitResolver struct {
	Repository        string
	Primary           string
	Mirror            string
	PrimarySSHCommand string
	MirrorSSHCommand  string
	TempRoot          string
	afterProof        func()
}

func (g GitResolver) Resolve(ctx context.Context, branch, commit string) (Source, error) {
	if branch == "" && commit == "" {
		return Source{}, ErrInvalidInput
	}
	if (branch != "" && !validBranch(branch)) || (commit != "" && !shaPattern.MatchString(commit)) {
		return Source{}, ErrInvalidInput
	}
	primary, mirror := g.Primary, g.Mirror
	var err error
	if primary == "" || mirror == "" {
		if g.Repository == "" {
			return Source{}, ErrInvalidInput
		}
		if primary == "" {
			primary = "origin"
		}
		if mirror == "" {
			mirror = "github"
		}
	}
	if branch == "" {
		branch, err = g.findPublishedWitness(ctx, primary, mirror, commit)
		if err != nil {
			return Source{}, err
		}
	}
	primaryTip, err := g.remoteTip(ctx, primary, branch, g.PrimarySSHCommand)
	if err != nil {
		return Source{}, fmt.Errorf("resolve primary branch %s: %w", branch, err)
	}
	mirrorTip, err := g.remoteTip(ctx, mirror, branch, g.MirrorSSHCommand)
	if err != nil {
		return Source{}, fmt.Errorf("resolve mirrored branch %s: %w", branch, err)
	}
	if primaryTip != mirrorTip {
		return Source{}, fmt.Errorf("%w: primary %s=%s, mirror %s=%s", ErrInvalidInput, branch, primaryTip, branch, mirrorTip)
	}
	selected := primaryTip
	if commit != "" {
		selected = commit
	}
	if err := g.proveAncestor(ctx, primary, branch, selected, g.PrimarySSHCommand); err != nil {
		return Source{}, fmt.Errorf("selected commit is not in captured published branch history: %w", err)
	}
	if g.afterProof != nil {
		g.afterProof()
	}
	primaryAfter, err := g.remoteTip(ctx, primary, branch, g.PrimarySSHCommand)
	if err != nil {
		return Source{}, fmt.Errorf("recheck primary branch %s while freezing source: %w", branch, err)
	}
	mirrorAfter, err := g.remoteTip(ctx, mirror, branch, g.MirrorSSHCommand)
	if err != nil {
		return Source{}, fmt.Errorf("recheck mirrored branch %s while freezing source: %w", branch, err)
	}
	if primaryAfter != primaryTip || mirrorAfter != mirrorTip || primaryAfter != mirrorAfter {
		return Source{}, fmt.Errorf("%w: branch %s moved during source freeze", ErrReleaseMismatch, branch)
	}
	return Source{Branch: branch, CapturedTip: primaryTip, Commit: selected, PrimaryRef: primaryTip, MirrorRef: mirrorTip}, nil
}

// findPublishedWitness chooses the first matching branch, in lexical order,
// whose primary and mirror tips agree and contain the requested commit. A
// commit-only request is never silently interpreted as a dev request.
func (g GitResolver) findPublishedWitness(ctx context.Context, primary, mirror, commit string) (string, error) {
	primaryHeads, err := g.remoteHeads(ctx, primary, g.PrimarySSHCommand)
	if err != nil {
		return "", fmt.Errorf("list primary published branches: %w", err)
	}
	mirrorHeads, err := g.remoteHeads(ctx, mirror, g.MirrorSSHCommand)
	if err != nil {
		return "", fmt.Errorf("list mirrored published branches: %w", err)
	}
	branches := make([]string, 0, len(primaryHeads))
	for branch, tip := range primaryHeads {
		if mirrorHeads[branch] == tip && validBranch(branch) {
			branches = append(branches, branch)
		}
	}
	sort.Strings(branches)
	for _, branch := range branches {
		if err := g.proveAncestor(ctx, primary, branch, commit, g.PrimarySSHCommand); err == nil {
			return branch, nil
		}
	}
	return "", fmt.Errorf("%w: commit has no matching primary/mirror branch witness", ErrReleaseMismatch)
}

func validBranch(name string) bool {
	if !branchPattern.MatchString(name) || strings.Contains(name, "..") || strings.Contains(name, "@{") ||
		strings.Contains(name, "//") || strings.HasSuffix(name, ".") || strings.HasSuffix(name, "/") ||
		strings.HasPrefix(name, "/") || strings.HasPrefix(name, "-") {
		return false
	}
	for _, component := range strings.Split(name, "/") {
		if component == "" || component == "." || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return true
}

func (g GitResolver) remoteTip(ctx context.Context, remote, branch, sshCommand string) (string, error) {
	args := []string{"ls-remote", "--exit-code", remote, "refs/heads/" + branch}
	out, err := g.gitWithSSH(ctx, sshCommand, args...)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 || fields[1] != "refs/heads/"+branch || !shaPattern.MatchString(fields[0]) {
		return "", ErrInvalidInput
	}
	return fields[0], nil
}

func (g GitResolver) remoteHeads(ctx context.Context, remote, sshCommand string) (map[string]string, error) {
	out, err := g.gitWithSSH(ctx, sshCommand, "ls-remote", "--heads", remote)
	if err != nil {
		return nil, err
	}
	heads := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || !strings.HasPrefix(fields[1], "refs/heads/") || !shaPattern.MatchString(fields[0]) {
			return nil, ErrOperationCorrupted
		}
		branch := strings.TrimPrefix(fields[1], "refs/heads/")
		if !validBranch(branch) {
			continue
		}
		heads[branch] = fields[0]
	}
	return heads, nil
}

func (g GitResolver) proveAncestor(ctx context.Context, remote, branch, commit, sshCommand string) error {
	if g.TempRoot == "" {
		g.TempRoot = os.TempDir()
	}
	bare, err := os.MkdirTemp(g.TempRoot, "rsr-source-proof-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(bare)
	if err := os.Remove(bare); err != nil {
		return err
	}
	if _, err := gitAt(ctx, sshCommand, "", "init", "--bare", bare); err != nil {
		return err
	}
	remoteURL := remote
	if g.Repository != "" {
		resolved, e := g.git(ctx, "remote", "get-url", remote)
		if e != nil {
			return e
		}
		remoteURL = strings.TrimSpace(string(resolved))
	}
	if _, err := gitAt(ctx, sshCommand, bare, "remote", "add", "proof", remoteURL); err != nil {
		return err
	}
	if _, err := gitAt(ctx, sshCommand, bare, "fetch", "--no-tags", "proof", "refs/heads/"+branch); err != nil {
		return err
	}
	// Ensure the requested object is present without allowing arbitrary revision
	// expressions. A branch fetch must already have delivered every ancestor.
	if _, err := gitAt(ctx, sshCommand, bare, "cat-file", "-e", commit+"^{commit}"); err != nil {
		return err
	}
	if _, err := gitAt(ctx, sshCommand, bare, "merge-base", "--is-ancestor", commit, "FETCH_HEAD"); err != nil {
		return err
	}
	return nil
}

func (g GitResolver) git(ctx context.Context, args ...string) ([]byte, error) {
	return g.gitWithSSH(ctx, "", args...)
}

func (g GitResolver) gitWithSSH(ctx context.Context, sshCommand string, args ...string) ([]byte, error) {
	if g.Repository != "" {
		args = append([]string{"-C", g.Repository}, args...)
	}
	return g.runGit(ctx, sshCommand, args...)
}

func (g GitResolver) runGit(ctx context.Context, sshCommand string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if sshCommand != "" {
		cmd.Env = append(os.Environ(), "GIT_SSH_COMMAND="+sshCommand)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func gitAt(ctx context.Context, sshCommand, dir string, args ...string) ([]byte, error) {
	cmdArgs := append([]string(nil), args...)
	if dir != "" {
		cmdArgs = append([]string{"-C", dir}, cmdArgs...)
	}
	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	if sshCommand != "" {
		cmd.Env = append(os.Environ(), "GIT_SSH_COMMAND="+sshCommand)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(cmdArgs, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func (g GitResolver) ValidateRepository() error {
	if g.Repository == "" {
		return nil
	}
	info, err := os.Stat(g.Repository)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("repository path is not a directory")
	}
	return nil
}
