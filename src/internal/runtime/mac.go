package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

var (
	ErrMacRuntimePlatform = errors.New("mac process runtime requires macOS")
	ErrMacRuntimeAccount  = errors.New("mac process runtime account mismatch")
	ErrMacRuntimeSession  = errors.New("mac process runtime session not found")
	ErrMacRuntimeSource   = errors.New("mac process runtime source is invalid")
)

// MacRuntimeOptions describes the configured local account and owner-only
// workspace root. No sudo, credential switching, or confinement is performed.
type MacRuntimeOptions struct {
	Account           string
	WorkspaceRoot     string
	ShellPath         string
	RepositoryAliases map[string]string
}

type MacSourceMode string

const (
	MacSourceEmpty         MacSourceMode = "empty"
	MacSourceGitRevision   MacSourceMode = "git_revision"
	MacSourceLocalWorktree MacSourceMode = "local_worktree"
)

type MacSourceRequest struct {
	Mode              MacSourceMode
	RepositoryAlias   string
	RequestedRevision string
	Path              string
}

type MacResolvedSource struct {
	Mode             MacSourceMode
	CanonicalPath    string
	ResolvedRevision string
	Portable         bool
}

// MacProcessRecord is an observed process identity for lifecycle evidence.
type MacProcessRecord struct {
	PID      int
	UID      int
	Username string
	Command  string
}

// MacPrepared is the private workspace and immutable generation selected for
// one local session before its Bash process is started.
type MacPrepared struct {
	SessionID      string
	Generation     string
	Workspace      string
	Source         MacResolvedSource
	OwnedWorkspace bool
	RepositoryPath string
}

// MacProcessAdapter starts the persistent shell under the current configured
// macOS account. A worktree remains a starting directory, never a boundary.
type MacProcessAdapter struct {
	mu       sync.Mutex
	options  MacRuntimeOptions
	account  *user.User
	sessions map[string]*PersistentShell
	prepared map[string]MacPrepared
}

func NewMacProcessAdapter(options MacRuntimeOptions) (*MacProcessAdapter, error) {
	if runtime.GOOS != "darwin" {
		return nil, ErrMacRuntimePlatform
	}
	if options.Account == "" {
		return nil, fmt.Errorf("%w: account is empty", ErrMacRuntimeAccount)
	}
	current, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("%w: current user: %v", ErrMacRuntimeAccount, err)
	}
	if current.Username != options.Account {
		return nil, fmt.Errorf("%w: configured=%q current=%q", ErrMacRuntimeAccount, options.Account, current.Username)
	}
	if options.WorkspaceRoot == "" {
		options.WorkspaceRoot = filepath.Join(os.TempDir(), "remote-session-runner-local")
	}
	if err := os.MkdirAll(options.WorkspaceRoot, 0o700); err != nil {
		return nil, fmt.Errorf("%w: workspace root: %v", ErrMacRuntimeAccount, err)
	}
	if err := os.Chmod(options.WorkspaceRoot, 0o700); err != nil {
		return nil, fmt.Errorf("%w: workspace root mode: %v", ErrMacRuntimeAccount, err)
	}
	return &MacProcessAdapter{options: options, account: current, sessions: make(map[string]*PersistentShell), prepared: make(map[string]MacPrepared)}, nil
}

// Prepare creates an empty owner-only per-session workspace without starting Bash.
func (a *MacProcessAdapter) Prepare(ctx context.Context, sessionID, generation string) (MacPrepared, error) {
	return a.PrepareSource(ctx, sessionID, generation, MacSourceRequest{Mode: MacSourceEmpty})
}

// PrepareSource resolves source provenance on the Mac. Git revisions are
// pinned to an exact commit and materialized in a detached worktree; a local
// worktree is used in place and is explicitly non-portable.
func (a *MacProcessAdapter) PrepareSource(_ context.Context, sessionID, generation string, source MacSourceRequest) (MacPrepared, error) {
	if a == nil {
		return MacPrepared{}, ErrMacRuntimeSession
	}
	if sessionID == "" || generation == "" {
		return MacPrepared{}, fmt.Errorf("%w: empty session or generation", ErrMacRuntimeSession)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	prepared := MacPrepared{SessionID: sessionID, Generation: generation}
	switch source.Mode {
	case MacSourceEmpty:
		workspace, err := os.MkdirTemp(a.options.WorkspaceRoot, "session-")
		if err != nil {
			return MacPrepared{}, err
		}
		_ = os.Chmod(workspace, 0o700)
		prepared.Workspace = workspace
		prepared.OwnedWorkspace = true
		prepared.Source = MacResolvedSource{Mode: MacSourceEmpty, CanonicalPath: workspace, Portable: true}
	case MacSourceLocalWorktree:
		if !filepath.IsAbs(source.Path) {
			return MacPrepared{}, fmt.Errorf("%w: local worktree path must be absolute", ErrMacRuntimeSource)
		}
		canonical, err := filepath.EvalSymlinks(source.Path)
		if err != nil {
			return MacPrepared{}, fmt.Errorf("%w: local worktree path: %v", ErrMacRuntimeSource, err)
		}
		info, err := os.Stat(canonical)
		if err != nil || !info.IsDir() {
			return MacPrepared{}, fmt.Errorf("%w: local worktree is not a directory", ErrMacRuntimeSource)
		}
		prepared.Workspace = canonical
		prepared.Source = MacResolvedSource{Mode: MacSourceLocalWorktree, CanonicalPath: canonical, Portable: false}
	case MacSourceGitRevision:
		repository, ok := a.options.RepositoryAliases[source.RepositoryAlias]
		if !ok || !filepath.IsAbs(repository) || source.RequestedRevision == "" {
			return MacPrepared{}, fmt.Errorf("%w: unknown repository alias or revision", ErrMacRuntimeSource)
		}
		canonicalRepo, err := filepath.EvalSymlinks(repository)
		if err != nil {
			return MacPrepared{}, fmt.Errorf("%w: repository path: %v", ErrMacRuntimeSource, err)
		}
		resolvedBytes, err := exec.Command("git", "-C", canonicalRepo, "rev-parse", "--verify", "--end-of-options", source.RequestedRevision+"^{commit}").Output()
		if err != nil {
			return MacPrepared{}, fmt.Errorf("%w: resolve revision: %v", ErrMacRuntimeSource, err)
		}
		resolved := strings.TrimSpace(string(resolvedBytes))
		if resolved == "" {
			return MacPrepared{}, fmt.Errorf("%w: empty resolved revision", ErrMacRuntimeSource)
		}
		workspace, err := os.MkdirTemp(a.options.WorkspaceRoot, "session-")
		if err != nil {
			return MacPrepared{}, err
		}
		if output, err := exec.Command("git", "clone", "--shared", "--no-checkout", canonicalRepo, workspace).CombinedOutput(); err != nil {
			_ = os.RemoveAll(workspace)
			return MacPrepared{}, fmt.Errorf("%w: clone repository: %v: %s", ErrMacRuntimeSource, err, strings.TrimSpace(string(output)))
		}
		if output, err := exec.Command("git", "-C", workspace, "checkout", "--detach", resolved).CombinedOutput(); err != nil {
			_ = os.RemoveAll(workspace)
			return MacPrepared{}, fmt.Errorf("%w: checkout resolved revision: %v: %s", ErrMacRuntimeSource, err, strings.TrimSpace(string(output)))
		}
		_ = os.Chmod(workspace, 0o700)
		prepared.Workspace = workspace
		prepared.OwnedWorkspace = true
		prepared.RepositoryPath = canonicalRepo
		prepared.Source = MacResolvedSource{Mode: MacSourceGitRevision, CanonicalPath: workspace, ResolvedRevision: resolved, Portable: true}
	default:
		return MacPrepared{}, fmt.Errorf("%w: mode %q", ErrMacRuntimeSource, source.Mode)
	}
	a.prepared[sessionID] = prepared
	return prepared, nil
}

// StartAgent starts the one persistent Bash for a prepared session.
func (a *MacProcessAdapter) StartAgent(ctx context.Context, prepared MacPrepared) error {
	if a == nil {
		return ErrMacRuntimeSession
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if existing := a.sessions[prepared.SessionID]; existing != nil {
		return fmt.Errorf("%w: session already started", ErrMacRuntimeSession)
	}
	if a.prepared[prepared.SessionID] != prepared {
		return fmt.Errorf("%w: preparation mismatch", ErrMacRuntimeSession)
	}
	shell, err := StartPersistentShell(ctx, PersistentShellOptions{SessionID: prepared.SessionID, Generation: prepared.Generation, ShellPath: a.options.ShellPath, Workspace: prepared.Workspace})
	if err != nil {
		return err
	}
	a.sessions[prepared.SessionID] = shell
	return nil
}

// Shell returns the command-capable persistent shell for a started session.
func (a *MacProcessAdapter) Shell(sessionID string) (*PersistentShell, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	shell := a.sessions[sessionID]
	if shell == nil {
		return nil, ErrMacRuntimeSession
	}
	return shell, nil
}

// Inspect reports the Bash PID and configured account identity.
func (a *MacProcessAdapter) Inspect(sessionID string) (MacProcessRecord, error) {
	shell, err := a.Shell(sessionID)
	if err != nil {
		return MacProcessRecord{}, err
	}
	pid := shell.cmd.Process.Pid
	uid, command, err := inspectPID(pid)
	if err != nil {
		return MacProcessRecord{}, err
	}
	return MacProcessRecord{PID: pid, UID: uid, Username: a.account.Username, Command: command}, nil
}

// Cleanup closes the shell and removes its owner-only workspace.
func (a *MacProcessAdapter) Cleanup(sessionID string) error {
	a.mu.Lock()
	shell := a.sessions[sessionID]
	prepared := a.prepared[sessionID]
	delete(a.sessions, sessionID)
	delete(a.prepared, sessionID)
	a.mu.Unlock()
	if shell == nil {
		if prepared.SessionID == "" {
			return ErrMacRuntimeSession
		}
		if prepared.OwnedWorkspace {
			return os.RemoveAll(prepared.Workspace)
		}
		return nil
	}
	if err := shell.Close(); err != nil {
		return err
	}
	if prepared.OwnedWorkspace {
		return os.RemoveAll(prepared.Workspace)
	}
	return nil
}

func inspectPID(pid int) (int, string, error) {
	output, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "uid=,command=").Output()
	if err != nil {
		return 0, "", err
	}
	fields := strings.Fields(string(output))
	if len(fields) == 0 {
		return 0, "", ErrMacRuntimeSession
	}
	uid, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, "", err
	}
	return uid, strings.Join(fields[1:], " "), nil
}
