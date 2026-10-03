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
	mu           sync.Mutex
	options      MacRuntimeOptions
	account      *user.User
	sessions     map[string]*PersistentShell
	prepared     map[string]MacPrepared
	identities   map[string]MacProcessRecord
	replacements map[string]RuntimeOwnershipRecord
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
		replacements: make(map[string]RuntimeOwnershipRecord),
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
	return a.startAgentLocked(ctx, prepared, nil)
}

// StartExactReplacementAgent starts a replacement only after
// ReconcileExactSession has retained and staged the exact prior owner record.
// Its atomic owner-record replacement ensures that an interrupted handoff
// leaves either the old durable record or the new one, never no record.
func (a *MacProcessAdapter) StartExactReplacementAgent(ctx context.Context, prepared MacPrepared) error {
	if a == nil {
		return ErrMacRuntimeSession
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	prior, ok := a.replacements[prepared.SessionID]
	if !ok || prior.SessionID != prepared.SessionID || prior.Generation != prepared.Generation ||
		prior.LostRecoveryCleanupConfirmedAt != "" || prepared.Source.Mode != MacSourceEmpty || !prepared.OwnedWorkspace {
		return fmt.Errorf("%w: exact controlled-restart owner handoff is not staged", ErrRuntimeOwnershipRecord)
	}
	return a.startAgentLocked(ctx, prepared, &prior)
}

func (a *MacProcessAdapter) startAgentLocked(ctx context.Context, prepared MacPrepared, prior *RuntimeOwnershipRecord) error {
	if existing := a.sessions[prepared.SessionID]; existing != nil {
		return fmt.Errorf("%w: session already started", ErrMacRuntimeSession)
	}
	if a.prepared[prepared.SessionID] != prepared {
		return fmt.Errorf("%w: preparation mismatch", ErrMacRuntimeSession)
	}
	if prior != nil {
		if err := a.validateExactReplacementLocked(*prior, prepared); err != nil {
			return err
		}
	}
	shellPath := a.options.ShellPath
	if shellPath == "" {
		shellPath = "/bin/bash"
	}
	shellOptions := PersistentShellOptions{SessionID: prepared.SessionID, Generation: prepared.Generation, ShellPath: shellPath, Workspace: prepared.Workspace}
	var shell *PersistentShell
	var err error
	if prior == nil {
		shell, err = StartPersistentShell(ctx, shellOptions)
	} else {
		// A controlled-restart replacement cannot enter its ordinary Bash loop
		// until the old ownership marker has been atomically replaced below.
		shell, err = startPersistentShellAwaitingCommit(ctx, shellOptions)
	}
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
		owner := RuntimeOwnershipRecord{
			Version: runtimeOwnershipVersion, HostOS: runtime.GOOS, SessionID: prepared.SessionID,
			Generation: prepared.Generation, Workspace: prepared.Workspace, OwnedWorkspace: prepared.OwnedWorkspace,
			PID: record.PID, ProcessGroupID: record.ProcessGroupID, UID: record.UID, Username: record.Username,
			Command: record.Command, ProcessStartIdentity: record.ProcessStartIdentity,
		}
		if prior == nil {
			err = writeRuntimeOwnership(a.options.WorkspaceRoot, owner)
		} else {
			err = replaceRuntimeOwnership(a.options.WorkspaceRoot, *prior, owner)
		}
		if err == nil {
			a.identities[prepared.SessionID] = record
		}
	}
	if err != nil {
		delete(a.sessions, prepared.SessionID)
		_ = shell.Close()
		return fmt.Errorf("record Mac runtime ownership: %w", err)
	}
	if prior != nil {
		if err := shell.commitStartup(); err != nil {
			// The new record is already durable. Closing the still-gated shell
			// leaves that record available for a fresh exact reconciliation if
			// the commit pipe itself failed; do not restore or erase it here.
			delete(a.sessions, prepared.SessionID)
			delete(a.identities, prepared.SessionID)
			_ = shell.Close()
			return fmt.Errorf("commit replacement Mac runtime ownership: %w", err)
		}
	}
	if prior != nil {
		delete(a.replacements, prepared.SessionID)
		// The old owner record was replaced atomically above. Its now-unreferenced
		// private empty-source workspace may be removed only after that durable
		// handoff; a cleanup failure cannot turn a successful replacement into a
		// failed one and cause the new marker to be removed.
		if prior.OwnedWorkspace && prior.Workspace != prepared.Workspace {
			_ = removeOwnedRuntimeWorkspace(a.options.WorkspaceRoot, prior.Workspace)
		}
	}
	return nil
}

func (a *MacProcessAdapter) validateExactReplacementLocked(prior RuntimeOwnershipRecord, prepared MacPrepared) error {
	current, err := readRuntimeOwnership(a.options.WorkspaceRoot, prepared.SessionID)
	if err != nil {
		return fmt.Errorf("read staged Mac replacement owner: %w", err)
	}
	if current != prior {
		return fmt.Errorf("%w: staged Mac replacement owner changed before replacement start", ErrRuntimeOwnershipRecord)
	}
	state, err := exactMacControlledRestartGroupState(prior)
	if err != nil {
		return err
	}
	if state != macControlledRestartRootAbsentGroupEmpty {
		return fmt.Errorf("%w: prior Mac runtime is not absent before replacement start", ErrRuntimeOwnershipRecord)
	}
	return nil
}

// DiscardExactReplacementPreparation removes only a failed replacement's
// newly-created workspace. It intentionally retains the staged old owner
// record, so the durable queued identity remains available to a later fresh
// controlled-restart attempt.
func (a *MacProcessAdapter) DiscardExactReplacementPreparation(sessionID, expectedGeneration string) error {
	if a == nil {
		return ErrMacRuntimeSession
	}
	a.mu.Lock()
	prior, staged := a.replacements[sessionID]
	prepared, preparedOK := a.prepared[sessionID]
	if a.sessions[sessionID] != nil || !staged || !preparedOK || expectedGeneration == "" ||
		prior.Generation != expectedGeneration || prepared.Generation != expectedGeneration ||
		prepared.SessionID != sessionID || prepared.Workspace == prior.Workspace {
		a.mu.Unlock()
		return fmt.Errorf("%w: failed replacement preparation is not safely discardable", ErrRuntimeOwnershipRecord)
	}
	a.mu.Unlock()

	current, err := readRuntimeOwnership(a.options.WorkspaceRoot, sessionID)
	if err != nil {
		return fmt.Errorf("read staged Mac replacement owner before discard: %w", err)
	}
	if current != prior {
		return fmt.Errorf("%w: staged Mac replacement owner changed before discard", ErrRuntimeOwnershipRecord)
	}
	if prepared.OwnedWorkspace {
		if err := removeOwnedRuntimeWorkspace(a.options.WorkspaceRoot, prepared.Workspace); err != nil {
			return err
		}
	}
	a.mu.Lock()
	if currentPrepared, ok := a.prepared[sessionID]; ok && currentPrepared == prepared {
		delete(a.prepared, sessionID)
	}
	a.mu.Unlock()
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

// ReconcileExactSession is the controlled-restart variant of reconciliation.
// It permits replacement only after a fresh snapshot proves that the recorded
// root is absent with an empty group, or after it has safely stopped the sole
// exact recorded root. It deliberately does not share ReconcileSession's
// broad historical group-stop path: a restart must never signal a group with
// an extra member, a zombie, or an absent recorded root.
func (a *MacProcessAdapter) ReconcileExactSession(ctx context.Context, sessionID, expectedGeneration string, grace time.Duration) (MacReconciliationResult, error) {
	result := MacReconciliationResult{SessionID: sessionID, Reattached: false}
	if a == nil || a.account == nil {
		return result, ErrMacRuntimeAccount
	}
	record, err := readRuntimeOwnership(a.options.WorkspaceRoot, sessionID)
	if errors.Is(err, os.ErrNotExist) {
		result.Reason = "runtime ownership record is missing; controlled restart cannot replace a shell"
		return result, nil
	}
	if err != nil {
		result.Reason = "runtime ownership record could not be validated"
		result.CapacityRetained = true
		return result, err
	}
	result.Generation, result.PID = record.Generation, record.PID
	if expectedGeneration == "" || record.Generation != expectedGeneration {
		result.CapacityRetained = true
		result.Reason = "recorded runtime generation does not match controlled restart session"
		return result, fmt.Errorf("%w: recorded runtime generation does not match controlled restart session", ErrRuntimeOwnershipRecord)
	}
	if record.LostRecoveryCleanupConfirmedAt != "" {
		result.CapacityRetained = true
		result.Reason = "recorded runtime owner is already reserved for lost-runtime cleanup"
		return result, fmt.Errorf("%w: controlled restart cannot replace a lost-runtime cleanup owner", ErrRuntimeOwnershipRecord)
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
	if _, err := os.Stat(record.Workspace); err == nil && !filepath.IsAbs(record.Workspace) {
		result.CapacityRetained = true
		return result, fmt.Errorf("%w: recorded workspace is not absolute", ErrRuntimeOwnershipRecord)
	}

	state, err := stopExactMacControlledRestartGroup(ctx, record, grace)
	if err != nil {
		result.CapacityRetained = true
		result.Reason = "recorded Mac process group remains unproven after exact controlled-restart cleanup"
		return result, err
	}
	result.Quarantined = true
	if state != macControlledRestartRootAbsentGroupEmpty {
		result.CapacityRetained = true
		result.Reason = "recorded Mac process group did not become absent after exact controlled-restart cleanup"
		return result, fmt.Errorf("%w: recorded Mac process group %d remains before controlled restart replacement", ErrRuntimeOwnershipRecord, record.ProcessGroupID)
	}
	// Keep the prior marker and workspace until StartExactReplacementAgent has
	// successfully published its replacement owner record with an atomic rename.
	// A crash or a prepare/start failure before that point therefore leaves the
	// exact durable queued-session identity available for a future fresh adapter.
	a.mu.Lock()
	a.replacements[sessionID] = record
	delete(a.sessions, sessionID)
	delete(a.prepared, sessionID)
	delete(a.identities, sessionID)
	a.mu.Unlock()
	result.CleanupConfirmed = true
	result.Reason = "exact recorded Mac root and process group are absent; owner marker retained pending atomic replacement"
	return result, nil
}

// ConfirmLostRecoveryCleanup proves the exact Mac runtime for one terminal
// lost command can no longer execute. It retains the owner record and owned
// workspace, then durably stamps that proof before the caller may release any
// SQLite capacity. A retry that sees the stamp returns without inspecting or
// signalling the old PID, which avoids PID-reuse risk.
func (a *MacProcessAdapter) ConfirmLostRecoveryCleanup(ctx context.Context, sessionID, expectedGeneration string, grace time.Duration) (MacReconciliationResult, error) {
	result := MacReconciliationResult{SessionID: sessionID, Reattached: false, CapacityRetained: true}
	if a == nil || a.account == nil {
		return result, ErrMacRuntimeAccount
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	record, err := readRuntimeOwnership(a.options.WorkspaceRoot, sessionID)
	if errors.Is(err, os.ErrNotExist) {
		result.Reason = "runtime ownership record is missing; lost recovery cannot prove cleanup"
		return result, nil
	}
	if err != nil {
		result.Reason = "runtime ownership record could not be validated"
		return result, err
	}
	result.Generation, result.PID = record.Generation, record.PID
	if expectedGeneration == "" || record.Generation != expectedGeneration {
		result.Reason = "runtime generation does not match the selected lost session"
		return result, fmt.Errorf("%w: recorded generation does not match selected lost session", ErrRuntimeOwnershipRecord)
	}
	if record.UID != os.Getuid() || record.Username != a.account.Username {
		result.Reason = "recorded owner does not belong to the selected Mac account"
		return result, fmt.Errorf("%w: recorded owner uid=%d user=%q", ErrMacRuntimeAccount, record.UID, record.Username)
	}
	if record.LostRecoveryCleanupConfirmedAt != "" {
		result.CleanupConfirmed = true
		result.Reason = "lost runtime cleanup was durably proven; owner marker retained pending capacity release"
		return result, nil
	}

	// A fresh daemon cannot reap a zombie because it does not own the prior
	// exec.Cmd wait handle. It can, however, prove that the exact recorded
	// process is already gone when both the root PID and its process group are
	// absent. Keep the owner marker until the paired SQLite capacity release;
	// FinalizeLostRecoveryCleanup deliberately does not inspect or signal PIDs.
	if identityErr := verifyMacProcessIdentity(record); errors.Is(identityErr, os.ErrProcessDone) {
		members, inspectErr := inspectMacProcessGroupMembers(record.ProcessGroupID)
		if inspectErr != nil {
			result.Reason = "recorded Mac process group could not be inspected after root exit"
			return result, inspectErr
		}
		if len(members) != 0 {
			result.Reason = "recorded Mac process group still has members after root exit"
			return result, fmt.Errorf("%w: recorded Mac process group %d remains nonempty after root exit", ErrRuntimeOwnershipRecord, record.ProcessGroupID)
		}
		if _, err := markLostRecoveryCleanupConfirmed(a.options.WorkspaceRoot, sessionID, record, time.Now()); err != nil {
			result.Reason = "lost runtime cleanup proof could not be persisted"
			return result, err
		}
		result.CleanupConfirmed = true
		result.Quarantined = true
		result.Reason = "recorded Mac root and process group are absent; cleanup proof and owner marker retained pending capacity release"
		return result, nil
	}

	// Reaping an exited child requires the same adapter's exec.Cmd wait handle.
	// A new daemon must retain capacity rather than pretending it can reap or
	// safely prove an arbitrary prior process.
	shell, err := a.trackedLostRecoveryShell(sessionID, record)
	if err != nil {
		result.Reason = "current Mac adapter does not own the recorded lost runtime"
		return result, err
	}

	members, err := inspectMacProcessGroupMembers(record.ProcessGroupID)
	if err != nil {
		result.Reason = "recorded Mac process group could not be inspected"
		return result, err
	}
	hasRunnable, err := validateMacLostRecoveryGroup(record, members)
	if err != nil {
		result.Reason = "recorded Mac process group contains an unproven member"
		return result, err
	}
	if hasRunnable {
		if err := a.stopLostRecoveryMacGroup(ctx, record, grace); err != nil {
			result.Quarantined = true
			result.Reason = "recorded Mac process group remains unproven after bounded cleanup"
			return result, err
		}
	}
	// The known direct child must still be represented as a non-runnable
	// member before Close calls its owned cmd.Wait. An empty or unrelated
	// snapshot is unproven rather than permission to close a live shell.
	members, err = inspectMacProcessGroupMembers(record.ProcessGroupID)
	if err != nil {
		result.Reason = "recorded Mac process group could not be reinspected before reaping"
		return result, err
	}
	if hasRunnable, err = validateMacLostRecoveryGroup(record, members); err != nil {
		result.Reason = "recorded Mac process group changed before reaping"
		return result, err
	} else if hasRunnable || !containsMacLostRecoveryZombie(record, members) {
		result.Reason = "recorded Mac Bash child is not proven non-runnable before reaping"
		return result, fmt.Errorf("%w: recorded Mac Bash child is not a proven zombie", ErrRuntimeOwnershipRecord)
	}
	if err := closeLostRecoveryShell(shell); err != nil {
		result.Reason = "recorded Mac Bash child could not be reaped"
		return result, err
	}

	// Do not write proof until a new bounded snapshot confirms that no member
	// of the owned group remains runnable. Zombies are harmless, but the direct
	// child above was reaped through the adapter that owns its wait handle.
	members, err = inspectMacProcessGroupMembers(record.ProcessGroupID)
	if err != nil {
		result.Reason = "recorded Mac process group could not be verified after cleanup"
		return result, err
	}
	if hasRunnable, err = validateMacLostRecoveryGroup(record, members); err != nil {
		result.Reason = "recorded Mac process group changed during cleanup verification"
		return result, err
	} else if hasRunnable {
		result.Quarantined = true
		result.Reason = "recorded Mac process group still has runnable members"
		return result, fmt.Errorf("%w: recorded Mac process group %d remains runnable", ErrRuntimeOwnershipRecord, record.ProcessGroupID)
	}
	if _, err := markLostRecoveryCleanupConfirmed(a.options.WorkspaceRoot, sessionID, record, time.Now()); err != nil {
		result.Reason = "lost runtime cleanup proof could not be persisted"
		return result, err
	}
	result.CleanupConfirmed = true
	result.Quarantined = true
	result.Reason = "prior Mac process group stopped; cleanup proof and owner marker retained pending capacity release"
	return result, nil
}

// FinalizeLostRecoveryCleanup removes only the workspace and owner record
// retained after ConfirmLostRecoveryCleanup persisted its process proof. It is
// intentionally free of PID inspection and signalling because the original
// PID may now belong to an unrelated process.
func (a *MacProcessAdapter) FinalizeLostRecoveryCleanup(ctx context.Context, sessionID, expectedGeneration string) (MacReconciliationResult, error) {
	result := MacReconciliationResult{SessionID: sessionID, Reattached: false}
	if a == nil || a.account == nil {
		return result, ErrMacRuntimeAccount
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	record, err := readRuntimeOwnership(a.options.WorkspaceRoot, sessionID)
	if errors.Is(err, os.ErrNotExist) {
		result.CleanupConfirmed = true
		result.Reason = "lost recovery ownership record is already absent after durable capacity release"
		return result, nil
	}
	if err != nil {
		result.CapacityRetained = true
		result.Reason = "lost recovery ownership record could not be validated"
		return result, err
	}
	result.Generation, result.PID = record.Generation, record.PID
	if expectedGeneration == "" || record.Generation != expectedGeneration {
		result.CapacityRetained = true
		result.Reason = "runtime generation does not match the selected lost session"
		return result, fmt.Errorf("%w: recorded generation does not match selected lost session", ErrRuntimeOwnershipRecord)
	}
	if record.UID != os.Getuid() || record.Username != a.account.Username {
		result.CapacityRetained = true
		result.Reason = "recorded owner does not belong to the selected Mac account"
		return result, fmt.Errorf("%w: recorded owner uid=%d user=%q", ErrMacRuntimeAccount, record.UID, record.Username)
	}
	if record.LostRecoveryCleanupConfirmedAt == "" {
		result.CapacityRetained = true
		result.Reason = "lost recovery cleanup proof is absent"
		return result, fmt.Errorf("%w: lost recovery cleanup proof is absent", ErrRuntimeOwnershipRecord)
	}
	if record.OwnedWorkspace {
		if err := removeOwnedRuntimeWorkspace(a.options.WorkspaceRoot, record.Workspace); err != nil {
			result.CapacityRetained = true
			result.Reason = "owned lost-recovery workspace could not be removed"
			return result, err
		}
	}
	if err := removeRuntimeOwnership(a.options.WorkspaceRoot, sessionID); err != nil {
		result.CapacityRetained = true
		result.Reason = "lost-recovery ownership record could not be removed"
		return result, err
	}
	a.mu.Lock()
	delete(a.sessions, sessionID)
	delete(a.prepared, sessionID)
	delete(a.identities, sessionID)
	a.mu.Unlock()
	result.CleanupConfirmed = true
	result.CapacityRetained = false
	result.Reason = "lost recovery ownership finalized after durable capacity release"
	return result, nil
}

func (a *MacProcessAdapter) trackedLostRecoveryShell(sessionID string, record RuntimeOwnershipRecord) (*PersistentShell, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	shell := a.sessions[sessionID]
	prepared, preparedOK := a.prepared[sessionID]
	identity, identityOK := a.identities[sessionID]
	if shell == nil || !preparedOK || !identityOK || shell.cmd == nil || shell.cmd.Process == nil || shell.cmd.ProcessState != nil {
		return nil, fmt.Errorf("%w: no current-adapter shell handle for lost runtime", ErrMacRuntimeSession)
	}
	if prepared.SessionID != record.SessionID || prepared.Generation != record.Generation || prepared.Workspace != record.Workspace || prepared.OwnedWorkspace != record.OwnedWorkspace {
		return nil, fmt.Errorf("%w: current-adapter preparation differs from lost runtime owner", ErrRuntimeOwnershipRecord)
	}
	if shell.cmd.Process.Pid != record.PID || identity.PID != record.PID || identity.ProcessGroupID != record.ProcessGroupID || identity.UID != record.UID || identity.Username != record.Username || identity.Command != record.Command || identity.ProcessStartIdentity != record.ProcessStartIdentity {
		return nil, fmt.Errorf("%w: current-adapter process identity differs from lost runtime owner", ErrRuntimeOwnershipRecord)
	}
	return shell, nil
}

// validateMacLostRecoveryGroup permits only the exact, direct Bash root known
// to this adapter. Any extra runnable member is deliberately unknown for
// lost-runtime repair, even if it appears to descend from Bash: a group signal
// would otherwise broaden the recovery action beyond the recorded child.
func validateMacLostRecoveryGroup(record RuntimeOwnershipRecord, members []macProcessGroupMember) (bool, error) {
	foundRoot := false
	for _, member := range members {
		if !member.runnable() {
			continue
		}
		if member.PID <= 0 || member.ProcessGroupID != record.ProcessGroupID || member.UID != record.UID {
			return false, fmt.Errorf("%w: runnable Mac process-group member is not owned by the selected runtime", ErrRuntimeOwnershipRecord)
		}
		if member.PID != record.PID || foundRoot {
			return true, fmt.Errorf("%w: recorded Mac process group contains an additional runnable member", ErrRuntimeOwnershipRecord)
		}
		foundRoot = true
	}
	if !foundRoot {
		return false, nil
	}
	return true, nil
}

func containsMacLostRecoveryZombie(record RuntimeOwnershipRecord, members []macProcessGroupMember) bool {
	for _, member := range members {
		if member.PID == record.PID && member.ProcessGroupID == record.ProcessGroupID && !member.runnable() {
			return true
		}
	}
	return false
}

// macControlledRestartGroupState describes the only two states that can lead
// to a controlled-restart replacement. Anything else is deliberately an
// error, retaining the durable reservation and the owner record.
type macControlledRestartGroupState uint8

const (
	macControlledRestartRootAbsentGroupEmpty macControlledRestartGroupState = iota + 1
	macControlledRestartRootLive
)

// exactMacControlledRestartGroupState takes bounded process-table snapshots
// around an exact root identity check. A group signal is safe only when every
// fresh observation contains precisely the recorded runnable root and no
// zombie or extra member. A root that is absent while the group remains
// nonempty is intentionally not a signal target.
func exactMacControlledRestartGroupState(record RuntimeOwnershipRecord) (macControlledRestartGroupState, error) {
	members, err := inspectMacProcessGroupMembers(record.ProcessGroupID)
	if err != nil {
		return 0, err
	}
	if len(members) == 0 {
		if err := verifyMacProcessIdentity(record); errors.Is(err, os.ErrProcessDone) {
			return macControlledRestartRootAbsentGroupEmpty, nil
		} else if err == nil {
			return 0, fmt.Errorf("%w: recorded Mac root remains live while its process-group snapshot is empty", ErrRuntimeOwnershipRecord)
		} else {
			return 0, fmt.Errorf("%w: could not prove recorded Mac root absence with an empty process group: %v", ErrRuntimeOwnershipRecord, err)
		}
	}
	if err := validateExactMacControlledRestartGroup(record, members); err != nil {
		return 0, err
	}
	if err := verifyExactMacControlledRestartRootLive(record); err != nil {
		return 0, err
	}

	// A second snapshot narrows the interval between inspection and a later
	// TERM/KILL signal. Darwin has no atomic validate-and-signal operation, so
	// any change seen here fails closed before the caller can signal the group.
	members, err = inspectMacProcessGroupMembers(record.ProcessGroupID)
	if err != nil {
		return 0, err
	}
	if err := validateExactMacControlledRestartGroup(record, members); err != nil {
		return 0, err
	}
	if err := verifyExactMacControlledRestartRootLive(record); err != nil {
		return 0, err
	}
	return macControlledRestartRootLive, nil
}

func validateExactMacControlledRestartGroup(record RuntimeOwnershipRecord, members []macProcessGroupMember) error {
	if len(members) != 1 {
		return fmt.Errorf("%w: recorded Mac process group %d has %d members; controlled restart requires exactly one", ErrRuntimeOwnershipRecord, record.ProcessGroupID, len(members))
	}
	member := members[0]
	if !member.runnable() {
		return fmt.Errorf("%w: recorded Mac process group %d contains a zombie; controlled restart will not signal it", ErrRuntimeOwnershipRecord, record.ProcessGroupID)
	}
	if member.PID != record.PID || member.ProcessGroupID != record.ProcessGroupID || member.UID != record.UID {
		return fmt.Errorf("%w: recorded Mac process group does not contain the exact recorded live root", ErrRuntimeOwnershipRecord)
	}
	return nil
}

// verifyExactMacControlledRestartRootLive avoids verifyMacProcessIdentity's
// legacy "root absent but group alive" compatibility case. The controlled
// restart path needs a positive proof that the recorded PID is still live,
// remains the recorded session leader, and still has the recorded start
// identity immediately before it can signal the process group.
func verifyExactMacControlledRestartRootLive(record RuntimeOwnershipRecord) error {
	if record.UID != os.Getuid() || record.ProcessGroupID != record.PID {
		return fmt.Errorf("%w: persisted process identity is not a current-user session leader", ErrRuntimeOwnershipRecord)
	}
	if err := syscall.Kill(record.PID, 0); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("%w: recorded Mac root is absent while its process group is nonempty", ErrRuntimeOwnershipRecord)
		}
		if !errors.Is(err, syscall.EPERM) {
			return fmt.Errorf("inspect recorded Mac root: %w", err)
		}
	}
	group, err := syscall.Getpgid(record.PID)
	if err != nil || group != record.ProcessGroupID {
		return fmt.Errorf("%w: recorded Mac root PID %d process group changed", ErrRuntimeOwnershipRecord, record.PID)
	}
	startIdentity, err := inspectMacProcessStartIdentity(record.PID)
	if err != nil || startIdentity != record.ProcessStartIdentity {
		return fmt.Errorf("%w: recorded Mac root PID %d start identity changed", ErrRuntimeOwnershipRecord, record.PID)
	}
	return nil
}

// stopExactMacControlledRestartGroup rechecks the exact safe target directly
// before each signal. It returns an absent-and-empty proof only; a zombie,
// extra member, or a root that disappears while another member remains is an
// error and leaves the owner record and capacity reservation intact.
func stopExactMacControlledRestartGroup(ctx context.Context, record RuntimeOwnershipRecord, grace time.Duration) (macControlledRestartGroupState, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if grace <= 0 {
		grace = 500 * time.Millisecond
	}
	state, err := signalExactMacControlledRestartGroup(record, syscall.SIGTERM)
	if err != nil || state == macControlledRestartRootAbsentGroupEmpty {
		return state, err
	}
	state, err = waitExactMacControlledRestartGroupState(ctx, record, grace)
	if err != nil || state == macControlledRestartRootAbsentGroupEmpty {
		return state, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	state, err = signalExactMacControlledRestartGroup(record, syscall.SIGKILL)
	if err != nil || state == macControlledRestartRootAbsentGroupEmpty {
		return state, err
	}
	state, err = waitExactMacControlledRestartGroupState(context.Background(), record, 2*time.Second)
	if err != nil || state == macControlledRestartRootAbsentGroupEmpty {
		return state, err
	}
	return 0, fmt.Errorf("%w: recorded Mac process group %d remains after exact controlled-restart cleanup", ErrRuntimeOwnershipRecord, record.ProcessGroupID)
}

// signalExactMacControlledRestartGroup performs the last bounded exact check
// immediately before a group signal. The remaining kernel scheduling window
// cannot be made atomic on Darwin; all observable ambiguity fails closed.
func signalExactMacControlledRestartGroup(record RuntimeOwnershipRecord, signal syscall.Signal) (macControlledRestartGroupState, error) {
	state, err := exactMacControlledRestartGroupState(record)
	if err != nil || state == macControlledRestartRootAbsentGroupEmpty {
		return state, err
	}
	if err := signalOwnedProcessGroup(record.ProcessGroupID, signal); err != nil {
		return 0, fmt.Errorf("signal exact recorded Mac process group with %s: %w", signal, err)
	}
	return state, nil
}

func waitExactMacControlledRestartGroupState(ctx context.Context, record RuntimeOwnershipRecord, timeout time.Duration) (macControlledRestartGroupState, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		timeout = time.Second
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		state, err := exactMacControlledRestartGroupState(record)
		if err != nil || state == macControlledRestartRootAbsentGroupEmpty {
			return state, err
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-deadline.C:
			return exactMacControlledRestartGroupState(record)
		case <-ticker.C:
		}
	}
}

// stopLostRecoveryMacGroup narrows the unavoidable signal race by taking an
// exact direct-root snapshot immediately before each signal. Darwin exposes no
// atomic "validate then signal group" primitive; when either recheck sees an
// extra or mismatched member, the method returns unconfirmed without sending
// the next signal.
func (a *MacProcessAdapter) stopLostRecoveryMacGroup(ctx context.Context, record RuntimeOwnershipRecord, grace time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if grace <= 0 {
		grace = 500 * time.Millisecond
	}
	ready, err := macLostRecoverySignalTarget(record)
	if err != nil {
		return err
	}
	if !ready {
		return nil
	}
	if err := signalOwnedProcessGroup(record.ProcessGroupID, syscall.SIGTERM); err != nil {
		return fmt.Errorf("signal recorded Mac process group with TERM: %w", err)
	}
	stopped, err := waitLostRecoveryMacGroupStopped(ctx, record, grace)
	if err != nil {
		return err
	}
	if stopped {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ready, err = macLostRecoverySignalTarget(record)
	if err != nil {
		return err
	}
	if !ready {
		return nil
	}
	if err := signalOwnedProcessGroup(record.ProcessGroupID, syscall.SIGKILL); err != nil {
		return fmt.Errorf("signal recorded Mac process group with KILL: %w", err)
	}
	stopped, err = waitLostRecoveryMacGroupStopped(context.Background(), record, 2*time.Second)
	if err != nil {
		return err
	}
	if stopped {
		return nil
	}
	return fmt.Errorf("%w: recorded Mac process group %d remains after bounded cleanup", ErrRuntimeOwnershipRecord, record.ProcessGroupID)
}

func waitLostRecoveryMacGroupStopped(ctx context.Context, record RuntimeOwnershipRecord, timeout time.Duration) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		timeout = time.Second
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		stopped, err := lostRecoveryMacGroupStopped(record)
		if err != nil || stopped {
			return stopped, err
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-deadline.C:
			return lostRecoveryMacGroupStopped(record)
		case <-ticker.C:
		}
	}
}

func lostRecoveryMacGroupStopped(record RuntimeOwnershipRecord) (bool, error) {
	members, err := inspectMacProcessGroupMembers(record.ProcessGroupID)
	if err != nil {
		return false, err
	}
	hasRunnable, err := validateMacLostRecoveryGroup(record, members)
	if err != nil {
		return false, err
	}
	return !hasRunnable, nil
}

func macLostRecoverySignalTarget(record RuntimeOwnershipRecord) (bool, error) {
	members, err := inspectMacProcessGroupMembers(record.ProcessGroupID)
	if err != nil {
		return false, err
	}
	ready, err := validateMacLostRecoveryGroup(record, members)
	if err != nil || !ready {
		return ready, err
	}
	if err := verifyMacProcessIdentity(record); err != nil {
		return false, err
	}
	return true, nil
}

func closeLostRecoveryShell(shell *PersistentShell) error {
	if err := shell.Close(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return err
		}
	}
	return nil
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
