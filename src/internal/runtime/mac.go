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
)

// MacRuntimeOptions describes the configured local account and owner-only
// workspace root. No sudo, credential switching, or confinement is performed.
type MacRuntimeOptions struct {
	Account       string
	WorkspaceRoot string
	ShellPath     string
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
	SessionID  string
	Generation string
	Workspace  string
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

// Prepare creates an owner-only per-session workspace without starting Bash.
func (a *MacProcessAdapter) Prepare(_ context.Context, sessionID, generation string) (MacPrepared, error) {
	if a == nil {
		return MacPrepared{}, ErrMacRuntimeSession
	}
	if sessionID == "" || generation == "" {
		return MacPrepared{}, fmt.Errorf("%w: empty session or generation", ErrMacRuntimeSession)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	workspace, err := os.MkdirTemp(a.options.WorkspaceRoot, "session-")
	if err != nil {
		return MacPrepared{}, err
	}
	_ = os.Chmod(workspace, 0o700)
	prepared := MacPrepared{SessionID: sessionID, Generation: generation, Workspace: workspace}
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
		return ErrMacRuntimeSession
	}
	if err := shell.Close(); err != nil {
		return err
	}
	return os.RemoveAll(prepared.Workspace)
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
