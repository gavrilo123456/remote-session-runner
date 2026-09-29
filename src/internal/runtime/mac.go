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
	"syscall"
	"time"
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
	PID                  int
	ProcessGroupID       int
	UID                  int
	Username             string
	Command              string
	ProcessStartIdentity string
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
	mu         sync.Mutex
	options    MacRuntimeOptions
	account    *user.User
	sessions   map[string]*PersistentShell
	prepared   map[string]MacPrepared
	identities map[string]MacProcessRecord
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
	return &MacProcessAdapter{
		options: options, account: current, sessions: make(map[string]*PersistentShell),
		prepared: make(map[string]MacPrepared), identities: make(map[string]MacProcessRecord),
	}, nil
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
	shellPath := a.options.ShellPath
	if shellPath == "" {
		shellPath = "/bin/bash"
	}
	shell, err := StartPersistentShell(ctx, PersistentShellOptions{SessionID: prepared.SessionID, Generation: prepared.Generation, ShellPath: shellPath, Workspace: prepared.Workspace})
	if err != nil {
		return err
	}
	a.sessions[prepared.SessionID] = shell
	pid := shell.cmd.Process.Pid
	processGroupID, err := syscall.Getpgid(pid)
	startIdentity, identityErr := inspectMacProcessStartIdentity(pid)
	if err == nil {
		err = identityErr
	}
	if err == nil {
		record := MacProcessRecord{
			PID: pid, ProcessGroupID: processGroupID, UID: os.Getuid(), Username: a.account.Username,
			Command: shellPath, ProcessStartIdentity: startIdentity,
		}
		err = writeRuntimeOwnership(a.options.WorkspaceRoot, RuntimeOwnershipRecord{
			Version: runtimeOwnershipVersion, HostOS: runtime.GOOS, SessionID: prepared.SessionID,
			Generation: prepared.Generation, Workspace: prepared.Workspace, OwnedWorkspace: prepared.OwnedWorkspace,
			PID: record.PID, ProcessGroupID: record.ProcessGroupID, UID: record.UID, Username: record.Username,
			Command: record.Command, ProcessStartIdentity: record.ProcessStartIdentity,
		})
		if err == nil {
			a.identities[prepared.SessionID] = record
		}
	}
	if err != nil {
		delete(a.sessions, prepared.SessionID)
		_ = shell.Close()
		return fmt.Errorf("record Mac runtime ownership: %w", err)
	}
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
	a.mu.Lock()
	record, ok := a.identities[sessionID]
	a.mu.Unlock()
	if !ok || shell.cmd == nil || shell.cmd.Process == nil || record.PID != shell.cmd.Process.Pid {
		return MacProcessRecord{}, ErrMacRuntimeSession
	}
	if err := syscall.Kill(record.PID, 0); err != nil {
		return MacProcessRecord{}, fmt.Errorf("inspect Mac Bash process: %w", err)
	}
	processGroupID, err := syscall.Getpgid(record.PID)
	if err != nil || processGroupID != record.ProcessGroupID {
		return MacProcessRecord{}, fmt.Errorf("inspect Mac Bash process group: %w", ErrMacRuntimeSession)
	}
	startIdentity, err := inspectMacProcessStartIdentity(record.PID)
	if err != nil || startIdentity != record.ProcessStartIdentity {
		return MacProcessRecord{}, fmt.Errorf("inspect Mac Bash process start identity: %w", ErrRuntimeOwnershipRecord)
	}
	return record, nil
}

// Cleanup closes the shell and removes its owner-only workspace.
func (a *MacProcessAdapter) Cleanup(sessionID string) error {
	if a == nil {
		return ErrMacRuntimeSession
	}
	a.mu.Lock()
	shell := a.sessions[sessionID]
	prepared := a.prepared[sessionID]
	identity, hasIdentity := a.identities[sessionID]
	a.mu.Unlock()
	if prepared.SessionID == "" {
		return ErrMacRuntimeSession
	}
	owner, ownerErr := readRuntimeOwnership(a.options.WorkspaceRoot, sessionID)
	if ownerErr != nil && !errors.Is(ownerErr, os.ErrNotExist) {
		return ownerErr
	}
	if shell == nil && ownerErr == nil {
		result, err := a.ReconcileSession(context.Background(), sessionID, owner.Generation, 500*time.Millisecond)
		if err != nil {
			return err
		}
		if !result.CleanupConfirmed {
			return fmt.Errorf("%w: runtime cleanup remains unconfirmed", ErrMacRuntimeSession)
		}
		return nil
	}

	processGroupID := 0
	if ownerErr == nil {
		if owner.Generation != prepared.Generation || owner.Workspace != prepared.Workspace || owner.OwnedWorkspace != prepared.OwnedWorkspace || owner.Username != a.account.Username || owner.UID != os.Getuid() {
			return fmt.Errorf("%w: persisted ownership does not match prepared Mac session", ErrRuntimeOwnershipRecord)
		}
		processGroupID = owner.ProcessGroupID
		if hasIdentity && (identity.PID != owner.PID || identity.ProcessGroupID != owner.ProcessGroupID || identity.ProcessStartIdentity != owner.ProcessStartIdentity) {
			return fmt.Errorf("%w: in-memory and persisted Mac process identities differ", ErrRuntimeOwnershipRecord)
		}
	}
	if shell != nil {
		if shell.cmd == nil || shell.cmd.Process == nil {
			return ErrMacRuntimeSession
		}
		pid := shell.cmd.Process.Pid
		if processGroupID == 0 {
			var err error
			processGroupID, err = syscall.Getpgid(pid)
			if err != nil {
				return fmt.Errorf("inspect Mac process group before cleanup: %w", err)
			}
		}
		if processGroupID != pid {
			return fmt.Errorf("%w: Bash process group does not match its PID", ErrRuntimeOwnershipRecord)
		}
		waitErr := shell.Close()
		groupExists, err := processGroupExists(processGroupID)
		if err != nil {
			return fmt.Errorf("inspect Mac session process group after shell exit: %w", err)
		}
		if groupExists {
			return fmt.Errorf("%w: residual process group %d remains after shell exit", ErrRuntimeOwnershipRecord, processGroupID)
		}
		if waitErr != nil {
			var exitErr *exec.ExitError
			if !errors.As(waitErr, &exitErr) {
				return waitErr
			}
		}
	}
	if prepared.OwnedWorkspace {
		if err := os.RemoveAll(prepared.Workspace); err != nil {
			return err
		}
	}
	if err := removeRuntimeOwnership(a.options.WorkspaceRoot, sessionID); err != nil {
		return err
	}
	a.mu.Lock()
	delete(a.sessions, sessionID)
	delete(a.prepared, sessionID)
	delete(a.identities, sessionID)
	a.mu.Unlock()
	return nil
}

// ReconcileSession stops a prior executor's recorded process group. It never
// adopts the old Bash shell; missing or mismatched ownership remains
// unconfirmed so the authority keeps its capacity reservation.
func (a *MacProcessAdapter) ReconcileSession(ctx context.Context, sessionID, expectedGeneration string, grace time.Duration) (MacReconciliationResult, error) {
	result := MacReconciliationResult{SessionID: sessionID, Reattached: false}
	if a == nil {
		return result, ErrMacRuntimeSession
	}
	record, err := readRuntimeOwnership(a.options.WorkspaceRoot, sessionID)
	if errors.Is(err, os.ErrNotExist) {
		result.Reason = "runtime ownership record is missing; shell reattachment is forbidden"
		return result, nil
	}
	if err != nil {
		result.Reason = "runtime ownership record could not be validated"
		result.CapacityRetained = true
		return result, err
	}
	result.Generation, result.PID = record.Generation, record.PID
	if expectedGeneration != "" && record.Generation != expectedGeneration {
		result.Reason = "runtime generation differs from durable session; shell reattachment is forbidden"
	}
	if record.UID != os.Getuid() || record.Username != a.account.Username || record.ProcessGroupID <= 0 {
		result.CapacityRetained = true
		result.Reason = "recorded process identity does not belong to the selected Mac account"
		return result, fmt.Errorf("%w: recorded uid=%d user=%q", ErrMacRuntimeAccount, record.UID, record.Username)
	}
	if _, err := os.Stat(record.Workspace); err != nil && !errors.Is(err, os.ErrNotExist) {
		result.CapacityRetained = true
		return result, err
	}
	if _, err := os.Stat(record.Workspace); err == nil {
		if filepath.IsAbs(record.Workspace) == false {
			result.CapacityRetained = true
			return result, fmt.Errorf("%w: recorded workspace is not absolute", ErrRuntimeOwnershipRecord)
		}
	}
	if err := verifyMacProcessIdentity(record); err != nil {
		if !errors.Is(err, os.ErrProcessDone) {
			result.CapacityRetained = true
			result.Reason = "recorded process identity does not match the current process"
			return result, err
		}
	}
	result.Quarantined = true
	if err := stopOwnedProcessGroup(ctx, record.ProcessGroupID, grace); err != nil {
		result.CapacityRetained = true
		result.Reason = "prior Mac process group remains after bounded cleanup"
		return result, err
	}
	if record.OwnedWorkspace {
		if err := removeOwnedRuntimeWorkspace(a.options.WorkspaceRoot, record.Workspace); err != nil {
			result.CapacityRetained = true
			return result, err
		}
	}
	if err := removeRuntimeOwnership(a.options.WorkspaceRoot, sessionID); err != nil {
		result.CapacityRetained = true
		return result, err
	}
	result.CleanupConfirmed = true
	result.Reason = "prior process group stopped; old shell was not reattached"
	a.mu.Lock()
	delete(a.sessions, sessionID)
	delete(a.prepared, sessionID)
	delete(a.identities, sessionID)
	a.mu.Unlock()
	return result, nil
}

// AuditOwnership verifies that all host owner markers can be matched to a
// durable live session reservation before runner-locald advertises readiness.
func (a *MacProcessAdapter) AuditOwnership(ctx context.Context, attributable map[string]struct{}) error {
	if a == nil {
		return ErrMacRuntimeAccount
	}
	return auditRuntimeOwnership(ctx, a.options.WorkspaceRoot, attributable)
}

type MacReconciliationResult struct {
	SessionID        string
	Generation       string
	PID              int
	Quarantined      bool
	Reattached       bool
	CleanupConfirmed bool
	CapacityRetained bool
	Reason           string
}

func verifyMacProcessIdentity(record RuntimeOwnershipRecord) error {
	if record.UID != os.Getuid() || record.ProcessGroupID != record.PID {
		return fmt.Errorf("%w: persisted process identity is not a current-user session leader", ErrRuntimeOwnershipRecord)
	}
	err := syscall.Kill(record.PID, 0)
	if errors.Is(err, syscall.ESRCH) {
		if !processGroupAlive(record.ProcessGroupID) {
			return os.ErrProcessDone
		}
		return nil
	}
	if err != nil && !errors.Is(err, syscall.EPERM) {
		return fmt.Errorf("inspect recorded Mac process: %w", err)
	}
	group, err := syscall.Getpgid(record.PID)
	if err != nil || group != record.ProcessGroupID {
		return fmt.Errorf("%w: PID %d process group changed", ErrRuntimeOwnershipRecord, record.PID)
	}
	startIdentity, err := inspectMacProcessStartIdentity(record.PID)
	if err != nil || startIdentity != record.ProcessStartIdentity {
		return fmt.Errorf("%w: PID %d process start identity changed", ErrRuntimeOwnershipRecord, record.PID)
	}
	return nil
}

func processGroupAlive(processGroupID int) bool {
	exists, err := processGroupExists(processGroupID)
	return err == nil && exists
}

func removeOwnedRuntimeWorkspace(workspaceRoot, workspace string) error {
	root, err := filepath.EvalSymlinks(workspaceRoot)
	if err != nil {
		return fmt.Errorf("resolve Mac workspace root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(workspace)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("resolve prior Mac workspace: %w", err)
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: prior workspace is outside the configured root", ErrRuntimeOwnershipRecord)
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		return err
	}
	if !info.IsDir() || !ownedByCurrentUser(info) {
		return fmt.Errorf("%w: prior workspace is not an owned directory", ErrRuntimeOwnershipRecord)
	}
	return os.RemoveAll(resolved)
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
