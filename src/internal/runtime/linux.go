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
	ErrLinuxRuntimePlatform = errors.New("linux process runtime requires Linux")
	ErrLinuxRuntimeAccount  = errors.New("linux process runtime account mismatch")
	ErrLinuxRuntimePath     = errors.New("linux process runtime service path is not owner-only")
	ErrLinuxProfileNotReady = errors.New("linux process profile is not ready")
)

const (
	LinuxHostProfile = "linux-host"
	LinuxHostAccount = "ubuntu"
)

// LinuxRuntimeOptions describes the account and owner-only paths used by the
// Linux host-process profile. A path is a start/service directory, never a
// filesystem boundary for commands running as the same account.
type LinuxRuntimeOptions struct {
	Account       string
	ServiceRoot   string
	WorkspaceRoot string
	ShellPath     string
}

// LinuxProfileCapabilities contains only controls that this host-process
// profile can truthfully report. UnsupportedIsolationControls is deliberately
// explicit so callers cannot infer a sandbox from a successful doctor run.
type LinuxProfileCapabilities struct {
	HostClass                    string
	EffectiveAccount             string
	Isolation                    string
	ServiceControls              []string
	UnsupportedIsolationControls []string
}

// LinuxPathCheck records the non-secret result of one owner-only service path
// check. The doctor never includes file contents in its report.
type LinuxPathCheck struct {
	Path        string
	Exists      bool
	Directory   bool
	OwnerOnly   bool
	OwnerUID    int
	ExpectedUID int
	Writable    bool
	Mode        os.FileMode
	Error       string
}

// LinuxProcessCheck records a short-lived probe process used to verify the
// host's process inspection and signalling primitives.
type LinuxProcessCheck struct {
	PID       int
	UID       int
	Username  string
	Command   string
	Inspected bool
	Signaled  bool
	Exited    bool
	Error     string
}

// LinuxDoctorReport is safe to serialize as operator-facing health evidence.
// It contains identity, paths, process observations, and truthful
// capabilities, but no credentials or command output beyond the Bash version.
type LinuxDoctorReport struct {
	Host          string
	Account       string
	UID           int
	ShellPath     string
	ShellVersion  string
	ServiceRoot   string
	WorkspaceRoot string
	Paths         []LinuxPathCheck
	Process       LinuxProcessCheck
	Capabilities  LinuxProfileCapabilities
	Ready         bool
	Failures      []string
}

type linuxProfileHooks struct {
	currentUser func() (*user.User, error)
	currentUID  func() int
	hostname    func() (string, error)
	lookPath    func(string) (string, error)
	inspectPID  func(int) (LinuxProcessRecord, error)
	signalPID   func(int, syscall.Signal) error
	startProbe  func(context.Context, string) (*exec.Cmd, error)
}

// LinuxProcessProfile is the preflight/doctor boundary for the named
// linux-host process adapter. P040 adds workspace/process lifecycle operations;
// this type intentionally performs no session creation.
type LinuxProcessProfile struct {
	options LinuxRuntimeOptions
	hooks   linuxProfileHooks
}

// LinuxProfile is a concise alias for callers that use the profile name.
type LinuxProfile = LinuxProcessProfile

// LinuxPrepared is the immutable session/generation ownership record created
// before a Bash process starts. OwnedWorkspace is always true for this P040
// adapter; later source modes must add an explicit non-owned record instead of
// reusing this cleanup path.
type LinuxPrepared struct {
	SessionID      string
	Generation     string
	Workspace      string
	OwnedWorkspace bool
	OwnerUID       int
	OwnerAccount   string
}

// LinuxProcessAdapter owns Linux host-process sessions for the configured
// account. It uses the shared PersistentShell implementation and does not
// add a second command engine or claim workspace confinement.
type LinuxProcessAdapter struct {
	mu       sync.Mutex
	options  LinuxRuntimeOptions
	account  *user.User
	sessions map[string]*PersistentShell
	prepared map[string]LinuxPrepared
}

// NewLinuxProcessAdapter constructs the real Linux process adapter after
// checking that the current host account is the selected ubuntu account. The
// owner-only service/workspace root is checked when Prepare is called.
func NewLinuxProcessAdapter(options LinuxRuntimeOptions) (*LinuxProcessAdapter, error) {
	if runtime.GOOS != "linux" {
		return nil, ErrLinuxRuntimePlatform
	}
	if options.Account == "" {
		options.Account = LinuxHostAccount
	}
	current, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("%w: current user: %v", ErrLinuxRuntimeAccount, err)
	}
	if current.Username != options.Account || options.Account != LinuxHostAccount {
		return nil, fmt.Errorf("%w: configured=%q current=%q", ErrLinuxRuntimeAccount, options.Account, current.Username)
	}
	profile := newLinuxProcessProfile(options, defaultLinuxProfileHooks())
	return &LinuxProcessAdapter{options: profile.options, account: current, sessions: make(map[string]*PersistentShell), prepared: make(map[string]LinuxPrepared)}, nil
}

// Prepare creates a private owner-only workspace without starting Bash.
func (a *LinuxProcessAdapter) Prepare(_ context.Context, sessionID, generation string) (LinuxPrepared, error) {
	if a == nil {
		return LinuxPrepared{}, ErrLinuxRuntimeAccount
	}
	if sessionID == "" || generation == "" {
		return LinuxPrepared{}, fmt.Errorf("%w: empty session or generation", ErrLinuxRuntimeAccount)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, exists := a.prepared[sessionID]; exists {
		return LinuxPrepared{}, fmt.Errorf("%w: session already prepared", ErrLinuxRuntimeAccount)
	}
	root := checkLinuxServicePath(a.options.WorkspaceRoot, a.accountUID())
	if root.Error != "" {
		return LinuxPrepared{}, fmt.Errorf("%w: %s", ErrLinuxRuntimePath, root.Error)
	}
	workspace, err := os.MkdirTemp(a.options.WorkspaceRoot, "session-")
	if err != nil {
		return LinuxPrepared{}, fmt.Errorf("%w: create workspace: %v", ErrLinuxRuntimePath, err)
	}
	if err := os.Chmod(workspace, 0o700); err != nil {
		_ = os.RemoveAll(workspace)
		return LinuxPrepared{}, fmt.Errorf("%w: workspace mode: %v", ErrLinuxRuntimePath, err)
	}
	prepared := LinuxPrepared{SessionID: sessionID, Generation: generation, Workspace: workspace, OwnedWorkspace: true, OwnerUID: a.accountUID(), OwnerAccount: a.account.Username}
	a.prepared[sessionID] = prepared
	return prepared, nil
}

// StartAgent starts one shared persistent Bash for a prepared session.
func (a *LinuxProcessAdapter) StartAgent(ctx context.Context, prepared LinuxPrepared) error {
	if a == nil {
		return ErrLinuxRuntimeAccount
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, exists := a.sessions[prepared.SessionID]; exists {
		return fmt.Errorf("%w: session already started", ErrLinuxRuntimeAccount)
	}
	if a.prepared[prepared.SessionID] != prepared {
		return fmt.Errorf("%w: preparation mismatch", ErrLinuxRuntimeAccount)
	}
	shell, err := StartPersistentShell(ctx, PersistentShellOptions{SessionID: prepared.SessionID, Generation: prepared.Generation, ShellPath: a.options.ShellPath, Workspace: prepared.Workspace})
	if err != nil {
		return err
	}
	a.sessions[prepared.SessionID] = shell
	return nil
}

// Shell returns the shared persistent Bash for a started session.
func (a *LinuxProcessAdapter) Shell(sessionID string) (*PersistentShell, error) {
	if a == nil {
		return nil, ErrLinuxRuntimeAccount
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	shell := a.sessions[sessionID]
	if shell == nil {
		return nil, ErrLinuxRuntimeAccount
	}
	return shell, nil
}

// Inspect reports the session/generation-owned Bash process identity.
func (a *LinuxProcessAdapter) Inspect(sessionID string) (LinuxProcessRecord, error) {
	if a == nil {
		return LinuxProcessRecord{}, ErrLinuxRuntimeAccount
	}
	a.mu.Lock()
	shell := a.sessions[sessionID]
	prepared := a.prepared[sessionID]
	a.mu.Unlock()
	if shell == nil || prepared.SessionID == "" || shell.cmd == nil || shell.cmd.Process == nil {
		return LinuxProcessRecord{}, ErrLinuxRuntimeAccount
	}
	record, err := inspectLinuxPID(shell.cmd.Process.Pid)
	if err != nil {
		return LinuxProcessRecord{}, err
	}
	record.SessionID = prepared.SessionID
	record.Generation = prepared.Generation
	record.Workspace = prepared.Workspace
	if record.UID != prepared.OwnerUID || record.Username != prepared.OwnerAccount {
		return LinuxProcessRecord{}, fmt.Errorf("%w: process uid=%d user=%q expected uid=%d user=%q", ErrLinuxRuntimeAccount, record.UID, record.Username, prepared.OwnerUID, prepared.OwnerAccount)
	}
	return record, nil
}

// Cleanup closes the shell and removes only this adapter's owned workspace.
func (a *LinuxProcessAdapter) Cleanup(sessionID string) error {
	if a == nil {
		return ErrLinuxRuntimeAccount
	}
	a.mu.Lock()
	shell := a.sessions[sessionID]
	prepared := a.prepared[sessionID]
	delete(a.sessions, sessionID)
	delete(a.prepared, sessionID)
	a.mu.Unlock()
	if shell == nil {
		if prepared.SessionID == "" {
			return ErrLinuxRuntimeAccount
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

func (a *LinuxProcessAdapter) accountUID() int {
	uid, _ := strconv.Atoi(a.account.Uid)
	return uid
}

// NewLinuxProcessProfile validates the selected account and constructs the
// real Linux profile. The doctor performs the host checks; construction never
// creates service directories or starts a process.
func NewLinuxProcessProfile(options LinuxRuntimeOptions) (*LinuxProcessProfile, error) {
	if runtime.GOOS != "linux" {
		return nil, ErrLinuxRuntimePlatform
	}
	return newLinuxProcessProfile(options, defaultLinuxProfileHooks()), nil
}

// NewLinuxProfile is an alternate constructor spelling matching the profile
// name used by the environment registry.
func NewLinuxProfile(options LinuxRuntimeOptions) (*LinuxProfile, error) {
	return NewLinuxProcessProfile(options)
}

func newLinuxProcessProfile(options LinuxRuntimeOptions, hooks linuxProfileHooks) *LinuxProcessProfile {
	defaults := defaultLinuxProfileHooks()
	if hooks.currentUser == nil {
		hooks.currentUser = defaults.currentUser
	}
	if hooks.currentUID == nil {
		hooks.currentUID = defaults.currentUID
	}
	if hooks.hostname == nil {
		hooks.hostname = defaults.hostname
	}
	if hooks.lookPath == nil {
		hooks.lookPath = defaults.lookPath
	}
	if hooks.inspectPID == nil {
		hooks.inspectPID = defaults.inspectPID
	}
	if hooks.signalPID == nil {
		hooks.signalPID = defaults.signalPID
	}
	if hooks.startProbe == nil {
		hooks.startProbe = defaults.startProbe
	}
	if options.Account == "" {
		options.Account = LinuxHostAccount
	}
	if options.ServiceRoot == "" {
		if home, err := os.UserHomeDir(); err == nil {
			options.ServiceRoot = filepath.Join(home, ".local", "share", "remote-session-runner")
		}
	}
	if options.WorkspaceRoot == "" && options.ServiceRoot != "" {
		options.WorkspaceRoot = filepath.Join(options.ServiceRoot, "workspaces")
	}
	if options.ShellPath == "" {
		options.ShellPath = "bash"
	}
	return &LinuxProcessProfile{options: options, hooks: hooks}
}

// Doctor verifies the actual Linux account, Bash, owner-only writable service
// paths, and a real inspect/signal/wait cycle. It does not inspect Podman or
// cgroups because those are not capabilities of this PoC profile.
func (p *LinuxProcessProfile) Doctor(ctx context.Context) (LinuxDoctorReport, error) {
	if p == nil {
		return LinuxDoctorReport{}, fmt.Errorf("%w: nil profile", ErrLinuxProfileNotReady)
	}
	report := LinuxDoctorReport{
		Account:       p.options.Account,
		ServiceRoot:   p.options.ServiceRoot,
		WorkspaceRoot: p.options.WorkspaceRoot,
		Capabilities:  linuxHostCapabilities(p.options.Account),
	}
	if ctx == nil {
		return report, fmt.Errorf("%w: nil context", ErrLinuxProfileNotReady)
	}
	if p.options.Account != LinuxHostAccount {
		report.Failures = append(report.Failures, fmt.Sprintf("configured account %q is not %q", p.options.Account, LinuxHostAccount))
	}

	current, err := p.hooks.currentUser()
	if err != nil {
		report.Failures = append(report.Failures, "current user: "+err.Error())
	} else {
		report.UID, _ = strconv.Atoi(current.Uid)
		if current.Username != p.options.Account {
			report.Failures = append(report.Failures, fmt.Sprintf("current account is %q, want %q", current.Username, p.options.Account))
		}
		if report.UID != p.hooks.currentUID() {
			report.Failures = append(report.Failures, fmt.Sprintf("user database UID %d differs from process UID %d", report.UID, p.hooks.currentUID()))
		}
	}
	if report.Host, err = p.hooks.hostname(); err != nil {
		report.Failures = append(report.Failures, "hostname: "+err.Error())
	}

	shellPath, shellErr := p.hooks.lookPath(p.options.ShellPath)
	if shellErr != nil {
		report.Failures = append(report.Failures, fmt.Sprintf("Bash %q: %v", p.options.ShellPath, shellErr))
	} else {
		report.ShellPath = shellPath
		versionOutput, versionErr := exec.CommandContext(ctx, shellPath, "--version").Output()
		if versionErr != nil {
			report.Failures = append(report.Failures, "Bash version: "+versionErr.Error())
		} else {
			report.ShellVersion = strings.SplitN(strings.TrimSpace(string(versionOutput)), "\n", 2)[0]
		}
	}

	for _, path := range uniqueLinuxPaths(p.options.ServiceRoot, p.options.WorkspaceRoot) {
		check := checkLinuxServicePath(path, report.UID)
		report.Paths = append(report.Paths, check)
		if check.Error != "" {
			report.Failures = append(report.Failures, check.Error)
		}
	}
	if shellErr == nil {
		if err := p.probeProcess(ctx, shellPath, &report.Process); err != nil {
			report.Failures = append(report.Failures, err.Error())
		}
	}
	report.Ready = len(report.Failures) == 0 && report.Process.Inspected && report.Process.Signaled && report.Process.Exited && report.ShellVersion != ""
	if !report.Ready && len(report.Failures) == 0 {
		report.Failures = append(report.Failures, "one or more Linux profile checks did not complete")
	}
	if !report.Ready {
		return report, fmt.Errorf("%w: %s", ErrLinuxProfileNotReady, strings.Join(report.Failures, "; "))
	}
	return report, nil
}

func (p *LinuxProcessProfile) probeProcess(ctx context.Context, shellPath string, report *LinuxProcessCheck) error {
	cmd, err := p.hooks.startProbe(ctx, shellPath)
	if err != nil {
		return fmt.Errorf("process probe start: %w", err)
	}
	if cmd == nil || cmd.Process == nil {
		return errors.New("process probe start returned no process")
	}
	report.PID = cmd.Process.Pid
	observed, err := p.hooks.inspectPID(report.PID)
	if err != nil {
		return fmt.Errorf("process inspection: %w", err)
	}
	report.UID, report.Username, report.Command = observed.UID, observed.Username, observed.Command
	report.Inspected = true
	if err := p.hooks.signalPID(report.PID, syscall.SIGTERM); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("process signalling: %w", err)
	}
	report.Signaled = true
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	select {
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		<-waitCh
		return fmt.Errorf("process probe wait: %w", ctx.Err())
	case <-time.After(2 * time.Second):
		_ = cmd.Process.Kill()
		<-waitCh
		return errors.New("process probe did not exit after SIGTERM")
	case <-waitCh:
		report.Exited = true
		return nil
	}
}

func defaultLinuxProfileHooks() linuxProfileHooks {
	return linuxProfileHooks{
		currentUser: user.Current,
		currentUID:  os.Getuid,
		hostname:    os.Hostname,
		lookPath:    exec.LookPath,
		inspectPID:  inspectLinuxPID,
		signalPID:   syscall.Kill,
		startProbe: func(ctx context.Context, shellPath string) (*exec.Cmd, error) {
			cmd := exec.CommandContext(ctx, shellPath, "-c", "sleep 30")
			if err := cmd.Start(); err != nil {
				return nil, err
			}
			return cmd, nil
		},
	}
}

func inspectLinuxPID(pid int) (LinuxProcessRecord, error) {
	uid, command, err := inspectPID(pid)
	if err != nil {
		return LinuxProcessRecord{}, err
	}
	identity, lookupErr := user.LookupId(strconv.Itoa(uid))
	if lookupErr != nil {
		return LinuxProcessRecord{}, lookupErr
	}
	return LinuxProcessRecord{PID: pid, UID: uid, Username: identity.Username, Command: command}, nil
}

// LinuxProcessRecord is the public form of one host process-table observation.
type LinuxProcessRecord struct {
	SessionID  string
	Generation string
	Workspace  string
	PID        int
	UID        int
	Username   string
	Command    string
}

func linuxHostCapabilities(account string) LinuxProfileCapabilities {
	return LinuxProfileCapabilities{
		HostClass:        LinuxHostProfile,
		EffectiveAccount: account,
		Isolation:        "os-user",
		// Service ceilings are supplied later by the environment registry. An
		// empty list here prevents a preflight from inventing limits.
		ServiceControls: nil,
		UnsupportedIsolationControls: []string{
			"filesystem", "cpu", "memory", "pid", "disk", "network", "mounts", "volumes", "privilege",
		},
	}
}

func uniqueLinuxPaths(paths ...string) []string {
	seen := make(map[string]struct{}, len(paths))
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		result = append(result, path)
	}
	return result
}

func checkLinuxServicePath(path string, expectedUID int) LinuxPathCheck {
	check := LinuxPathCheck{Path: path, ExpectedUID: expectedUID}
	info, err := os.Lstat(path)
	if err != nil {
		check.Error = fmt.Sprintf("service path %q: %v", path, err)
		return check
	}
	check.Exists = true
	check.Mode = info.Mode()
	if info.Mode()&os.ModeSymlink != 0 {
		check.Error = fmt.Sprintf("service path %q is a symlink", path)
		return check
	}
	check.Directory = info.IsDir()
	if !check.Directory {
		check.Error = fmt.Sprintf("service path %q is not a directory", path)
		return check
	}
	check.OwnerUID = linuxFileOwnerUID(info)
	check.OwnerOnly = info.Mode().Perm()&0o077 == 0
	if check.OwnerUID != expectedUID {
		check.Error = fmt.Sprintf("service path %q owner UID %d, want %d", path, check.OwnerUID, expectedUID)
		return check
	}
	if !check.OwnerOnly {
		check.Error = fmt.Sprintf("service path %q mode %o is not owner-only", path, info.Mode().Perm())
		return check
	}
	probe, err := os.OpenFile(filepath.Join(path, fmt.Sprintf(".runner-doctor-%d", time.Now().UnixNano())), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		check.Error = fmt.Sprintf("service path %q is not writable: %v", path, err)
		return check
	}
	probePath := probe.Name()
	check.Writable = true
	_ = probe.Close()
	_ = os.Remove(probePath)
	return check
}

func linuxFileOwnerUID(info os.FileInfo) int {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return -1
	}
	return int(stat.Uid)
}
