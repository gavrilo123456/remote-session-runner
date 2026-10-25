package runnerd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"remote-session-runner/src/internal/config"
)

const (
	linuxPRDServiceAccount = "ubuntu"
	linuxPRDServiceRoot    = "/home/ubuntu/.local/share/remote-session-runner"
	linuxPRDConfigPath     = "/home/ubuntu/.local/share/remote-session-runner/config/linux.yaml"
	linuxPRDProbeSession   = "sess-548ea3986a2b736f9643b2b402188830"
	linuxPRDProbeJob       = "job-7fae7172e801f2e22e7d6fce6014403d"
	linuxPRDProbeCommand   = "cmd-7dc98e864e7abd3a9c2596c3c4fe9399"
)

const maxStableProcScans = 4

const (
	maxPrivilegedProcLinkReads = 256
	maxPrivilegedProcLinkBytes = 4096
	maxPrivilegedProcFDBytes   = 4096
	maxPrivilegedProcFDEntries = 256
	maxPrivilegedProcErrBytes  = 1024
	privilegedProcLinkTimeout  = 2 * time.Second
	processProofTimeout        = 30 * time.Second
)

var errRunnerdProcChurn = errors.New("process identity changed during /proc inspection")

type runnerdProcIdentity struct {
	PID       int
	StartTime uint64
}

type runnerdProcFS struct {
	readDir            func(string) ([]os.DirEntry, error)
	readFile           func(string) ([]byte, error)
	readlink           func(string) (string, error)
	privilegedReadlink func(context.Context, string, runnerdProcIdentity, string) (string, error)
	privilegedReadFDs  func(context.Context, string, runnerdProcIdentity, string) ([]string, error)
}

func realRunnerdProcFS() runnerdProcFS {
	return runnerdProcFS{
		readDir: os.ReadDir, readFile: os.ReadFile, readlink: os.Readlink,
		privilegedReadlink: runnerdPrivilegedProcReadlink, privilegedReadFDs: runnerdPrivilegedProcFDNames,
	}
}

// requireRunnerdServiceStoppedForRecovery handles only the one authorized
// Linux PRD probe when systemd has already failed and removed its cgroup. The
// ordinary inactive/inspectable-cgroup guard remains authoritative for every
// other recovery shape.
func requireRunnerdServiceStoppedForRecovery(settings config.LinuxSettings, configPath string, configFingerprint [32]byte, jobIDs []string, pairCount, teardownCount int, sessionIDs []string) error {
	paths := []string{settings.ServiceRoot, settings.Workspaces, settings.Database, configPath}
	return requireRunnerdServiceStoppedForRecoveryWith(runnerdSystemdValue, runnerdSystemdJobs,
		func() error {
			if settings.Account != linuxPRDServiceAccount || filepath.Clean(settings.ServiceRoot) != linuxPRDServiceRoot || filepath.Clean(configPath) != linuxPRDConfigPath {
				return errors.New("failed-unit recovery is restricted to the configured unsuffixed Linux PRD service and config")
			}
			account, err := user.Lookup(settings.Account)
			if err != nil {
				return fmt.Errorf("resolve runnerd recovery service account: %w", err)
			}
			serviceUID, err := strconv.Atoi(account.Uid)
			if err != nil || serviceUID != os.Getuid() || serviceUID != os.Geteuid() {
				return errors.New("runnerd recovery process account does not match the configured service account")
			}
			if err := validateLinuxPRDRecoveryPaths(settings, configPath); err != nil {
				return err
			}
			if !isExactLinuxPRDProbeRecoverySelection(jobIDs, pairCount, teardownCount, sessionIDs) {
				return errors.New("failed-unit/no-cgroup recovery accepts only the single explicitly selected Linux PRD probe session")
			}
			self, err := verifiedCurrentRecoveryProcessIdentity("/proc", serviceUID, configPath, linuxPRDProbeSession, settings.ServiceRoot)
			if err != nil {
				return err
			}
			canonicalPaths, err := runnerdCanonicalGuardPaths(paths)
			if err != nil {
				return err
			}
			if err := requireConfigFingerprint(configPath, configFingerprint); err != nil {
				return err
			}
			if err := runnerdProcessesReferencePathsWith(realRunnerdProcFS(), "/proc", serviceUID, canonicalPaths, map[runnerdProcIdentity]struct{}{self: {}}); err != nil {
				return err
			}
			return requireConfigFingerprint(configPath, configFingerprint)
		})
}

func requireConfigFingerprint(path string, expected [32]byte) error {
	contents, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read Linux PRD configuration fingerprint: %w", err)
	}
	if sha256.Sum256(contents) != expected {
		return errors.New("Linux PRD configuration changed during offline recovery")
	}
	return nil
}

func isExactLinuxPRDProbeRecoverySelection(jobIDs []string, pairCount, teardownCount int, sessionIDs []string) bool {
	return len(jobIDs) == 0 && pairCount == 0 && teardownCount == 0 && len(sessionIDs) == 1 && sessionIDs[0] == linuxPRDProbeSession
}

func validateLinuxPRDRecoveryPaths(settings config.LinuxSettings, configPath string) error {
	if settings.Account != linuxPRDServiceAccount || filepath.Clean(settings.ServiceRoot) != linuxPRDServiceRoot || filepath.Clean(configPath) != linuxPRDConfigPath {
		return errors.New("recovery configuration is not the exact unsuffixed Linux PRD installation")
	}
	peerRoots := []string{linuxPRDServiceRoot + "-dev", linuxPRDServiceRoot + "-qas"}
	resolvedConfig, err := filepath.EvalSymlinks(configPath)
	if err != nil {
		return fmt.Errorf("resolve Linux PRD configuration path: %w", err)
	}
	if !runnerdPathReferencesPaths(filepath.Clean(resolvedConfig), []string{linuxPRDServiceRoot}) {
		return errors.New("Linux PRD configuration resolves outside the unsuffixed service tree")
	}
	for _, value := range []string{settings.Workspaces, settings.Database} {
		if !filepath.IsAbs(value) {
			return errors.New("Linux PRD recovery path is not absolute")
		}
		resolved, err := filepath.EvalSymlinks(value)
		if err != nil {
			return fmt.Errorf("resolve Linux PRD recovery path: %w", err)
		}
		for _, peerRoot := range peerRoots {
			if runnerdPathReferencesPaths(resolved, []string{peerRoot}) {
				return errors.New("Linux PRD recovery path resolves into a peer lane")
			}
		}
	}
	return nil
}

func requireRunnerdServiceStoppedForRecoveryWith(systemdValue func(string) (string, error), listJobs func() ([]string, error), processCheck func() error) error {
	before, err := readRunnerdRecoveryUnitSnapshot(systemdValue)
	if err != nil {
		return err
	}
	if before.ActiveState != "failed" || before.SubState != "failed" || before.ControlGroup != "" {
		return requireRunnerdServiceStoppedWith(systemdValue, os.ReadFile)
	}
	if before.ID != "runnerd.service" || before.User != linuxPRDServiceAccount || before.MainPID != "0" || before.ControlPID != "0" ||
		!runnerdHasRecordedExit(before.ExitTimestamp, before.ExitCode, before.ExitStatus) {
		return errors.New("failed runnerd.service lacks the exact stopped-unit evidence required for the PRD recovery fallback")
	}
	if err := requireNoRunnerdStartJobs(listJobs); err != nil {
		return err
	}
	if processCheck == nil {
		return errors.New("runnerd failed-unit process proof is unavailable")
	}
	if err := processCheck(); err != nil {
		return fmt.Errorf("runnerd failed-unit PRD process proof failed: %w", err)
	}
	after, err := readRunnerdRecoveryUnitSnapshot(systemdValue)
	if err != nil {
		return err
	}
	if before != after {
		return errors.New("runnerd.service state changed during failed-unit process inspection")
	}
	if err := requireNoRunnerdStartJobs(listJobs); err != nil {
		return err
	}
	return nil
}

func runnerdHasRecordedExit(timestamp, code, status string) bool {
	if _, err := time.Parse("Mon 2006-01-02 15:04:05 MST", strings.TrimSpace(timestamp)); err != nil {
		return false
	}
	// systemd encodes CLD_EXITED as 1. Require a recorded nonzero process exit
	// status so a timestamp alone cannot stand in for proof of the failed unit.
	if strings.TrimSpace(code) != "1" {
		return false
	}
	exitStatus, err := strconv.Atoi(strings.TrimSpace(status))
	return err == nil && exitStatus > 0 && exitStatus <= 255
}

type runnerdRecoveryUnitSnapshot struct {
	ID            string
	User          string
	ActiveState   string
	SubState      string
	MainPID       string
	ControlPID    string
	ControlGroup  string
	ExitTimestamp string
	ExitCode      string
	ExitStatus    string
}

func readRunnerdRecoveryUnitSnapshot(systemdValue func(string) (string, error)) (runnerdRecoveryUnitSnapshot, error) {
	properties := []struct {
		name string
	}{
		{"Id"},
		{"User"},
		{"ActiveState"},
		{"SubState"},
		{"MainPID"},
		{"ControlPID"},
		{"ControlGroup"},
		{"ExecMainExitTimestamp"},
		{"ExecMainCode"},
		{"ExecMainStatus"},
	}
	values := make([]string, len(properties))
	for index, property := range properties {
		value, err := systemdValue(property.name)
		if err != nil {
			return runnerdRecoveryUnitSnapshot{}, fmt.Errorf("read runnerd.service %s: %w", property.name, err)
		}
		values[index] = strings.TrimSpace(value)
	}
	return runnerdRecoveryUnitSnapshot{
		ID: values[0], User: values[1], ActiveState: values[2], SubState: values[3], MainPID: values[4],
		ControlPID: values[5], ControlGroup: values[6], ExitTimestamp: values[7], ExitCode: values[8], ExitStatus: values[9],
	}, nil
}

func requireNoRunnerdStartJobs(listJobs func() ([]string, error)) error {
	if listJobs == nil {
		return errors.New("systemd pending-job inspection is unavailable")
	}
	jobs, err := listJobs()
	if err != nil {
		return fmt.Errorf("inspect pending systemd jobs: %w", err)
	}
	if len(jobs) != 0 {
		return fmt.Errorf("runnerd.service has pending systemd work: %s", strings.Join(jobs, ","))
	}
	return nil
}

func runnerdSystemdJobs() ([]string, error) {
	output, err := exec.Command("systemctl", "list-jobs", "--no-legend", "--no-pager").Output()
	if err != nil {
		return nil, err
	}
	var matches []string
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == "runnerd.service" {
			matches = append(matches, strings.Join(fields, " "))
		}
	}
	return matches, nil
}

func verifiedCurrentRecoveryProcessIdentity(procRoot string, serviceUID int, configPath, sessionID, serviceRoot string) (runnerdProcIdentity, error) {
	pid := os.Getpid()
	identity, uids, _, err := readRunnerdProcIdentityWith(realRunnerdProcFS(), procRoot, pid)
	if err != nil {
		return runnerdProcIdentity{}, fmt.Errorf("inspect recovery process identity: %w", err)
	}
	if !containsInt(uids, serviceUID) {
		return runnerdProcIdentity{}, errors.New("recovery process does not run as the configured service account")
	}
	expected := []string{"recover-stalled", "--config", configPath, "--apply", "--lost-session", sessionID}
	if !sameStringSlice(os.Args[1:], expected) {
		return runnerdProcIdentity{}, errors.New("recovery process arguments do not match the one-time Linux PRD probe recovery")
	}
	cmdline, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return runnerdProcIdentity{}, fmt.Errorf("read recovery process command line: %w", err)
	}
	if !bytesEqual(cmdline, []byte(strings.Join(os.Args, "\x00")+"\x00")) {
		return runnerdProcIdentity{}, errors.New("recovery process command line changed during identity verification")
	}
	executable, err := os.Executable()
	if err != nil {
		return runnerdProcIdentity{}, fmt.Errorf("resolve recovery executable: %w", err)
	}
	actualExecutable, err := os.Readlink(filepath.Join(procRoot, strconv.Itoa(pid), "exe"))
	if err != nil {
		return runnerdProcIdentity{}, fmt.Errorf("inspect recovery executable: %w", err)
	}
	resolvedExecutable, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return runnerdProcIdentity{}, fmt.Errorf("resolve recovery executable path: %w", err)
	}
	if runnerdCleanProcLink(actualExecutable) != filepath.Clean(resolvedExecutable) {
		return runnerdProcIdentity{}, errors.New("recovery executable identity changed during verification")
	}
	if runnerdPathReferencesPaths(resolvedExecutable, []string{filepath.Clean(serviceRoot)}) {
		return runnerdProcIdentity{}, errors.New("offline recovery executable must be outside the installed PRD service tree")
	}
	if err := runnerdVerifyProcessIncarnation(realRunnerdProcFS(), procRoot, identity); err != nil {
		return runnerdProcIdentity{}, err
	}
	return identity, nil
}

func sameStringSlice(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func bytesEqual(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func runnerdCanonicalGuardPaths(paths []string) ([]string, error) {
	canonical := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			return nil, errors.New("process guard path is not absolute")
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil, fmt.Errorf("resolve process guard path: %w", err)
		}
		resolved = filepath.Clean(resolved)
		if _, exists := seen[resolved]; !exists {
			seen[resolved] = struct{}{}
			canonical = append(canonical, resolved)
		}
	}
	if len(canonical) == 0 {
		return nil, errors.New("no PRD process guard paths are configured")
	}
	return canonical, nil
}

func runnerdProcessesReferencePathsWith(procFS runnerdProcFS, procRoot string, serviceUID int, paths []string, exempt map[runnerdProcIdentity]struct{}) error {
	if procFS.readDir == nil || procFS.readFile == nil || procFS.readlink == nil {
		return errors.New("process inspection interface is incomplete")
	}
	ctx, cancel := context.WithTimeout(context.Background(), processProofTimeout)
	defer cancel()
	linkReader := runnerdProcLinkReader{procFS: procFS, procRoot: procRoot, ctx: ctx, maxReads: maxPrivilegedProcLinkReads}
	for attempt := 0; attempt < maxStableProcScans; attempt++ {
		if err := ctx.Err(); err != nil {
			return errors.New("process inventory exceeded its bounded inspection time")
		}
		before, err := runnerdInspectProcessTable(procFS, &linkReader, procRoot, serviceUID, paths, exempt)
		if errors.Is(err, errRunnerdProcChurn) {
			continue
		}
		if err != nil {
			return err
		}
		after, err := runnerdInspectProcessTable(procFS, &linkReader, procRoot, serviceUID, paths, exempt)
		if errors.Is(err, errRunnerdProcChurn) {
			continue
		}
		if err != nil {
			return err
		}
		if sameRunnerdProcessSet(before, after) {
			return nil
		}
	}
	return errors.New("same-account process inventory kept changing during bounded inspection")
}

type runnerdProcLinkReader struct {
	procFS   runnerdProcFS
	procRoot string
	ctx      context.Context
	reads    int
	maxReads int
}

func (reader *runnerdProcLinkReader) readlink(identity runnerdProcIdentity, path string) (string, error) {
	target, err := reader.procFS.readlink(path)
	if err == nil || !runnerdPermissionDenied(err) {
		return target, err
	}
	if reader.procFS.privilegedReadlink == nil {
		return "", errors.New("privileged proc link inspection is unavailable")
	}
	if reader.reads >= reader.maxReads {
		return "", errors.New("privileged proc link inspection exceeded its bounded read count")
	}
	if err := validateRunnerdProcLinkPath(reader.procRoot, identity, path); err != nil {
		return "", err
	}
	if err := reader.ctx.Err(); err != nil {
		return "", errors.New("process inventory exceeded its bounded inspection time")
	}
	if err := runnerdVerifyProcessIncarnation(reader.procFS, reader.procRoot, identity); err != nil {
		return "", err
	}
	reader.reads++
	target, err = reader.procFS.privilegedReadlink(reader.ctx, reader.procRoot, identity, path)
	if err != nil {
		return "", fmt.Errorf("privileged proc link inspection failed for PID %d: %w", identity.PID, err)
	}
	if err := runnerdVerifyProcessIncarnation(reader.procFS, reader.procRoot, identity); err != nil {
		return "", err
	}
	if target == "" || len(target) > maxPrivilegedProcLinkBytes || strings.ContainsAny(target, "\x00\r\n") {
		return "", errors.New("privileged proc link inspection returned malformed or truncated metadata")
	}
	return target, nil
}

func (reader *runnerdProcLinkReader) readFDNames(identity runnerdProcIdentity, path string) ([]string, error) {
	entries, err := reader.procFS.readDir(path)
	if err == nil {
		return validateRunnerdProcFDNames(entryNames(entries))
	}
	if !runnerdPermissionDenied(err) {
		return nil, err
	}
	if reader.procFS.privilegedReadFDs == nil {
		return nil, errors.New("privileged proc descriptor enumeration is unavailable")
	}
	if reader.reads >= reader.maxReads {
		return nil, errors.New("privileged proc inspection exceeded its bounded read count")
	}
	if err := validateRunnerdProcFDDirectoryPath(reader.procRoot, identity, path); err != nil {
		return nil, err
	}
	if err := reader.ctx.Err(); err != nil {
		return nil, errors.New("process inventory exceeded its bounded inspection time")
	}
	if err := runnerdVerifyProcessIncarnation(reader.procFS, reader.procRoot, identity); err != nil {
		return nil, err
	}
	reader.reads++
	names, err := reader.procFS.privilegedReadFDs(reader.ctx, reader.procRoot, identity, path)
	if err != nil {
		return nil, fmt.Errorf("privileged proc descriptor enumeration failed for PID %d: %w", identity.PID, err)
	}
	if err := runnerdVerifyProcessIncarnation(reader.procFS, reader.procRoot, identity); err != nil {
		return nil, err
	}
	return validateRunnerdProcFDNames(names)
}

func entryNames(entries []os.DirEntry) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func validateRunnerdProcFDDirectoryPath(procRoot string, identity runnerdProcIdentity, path string) error {
	if identity.PID <= 0 || identity.StartTime == 0 {
		return errors.New("privileged proc descriptor inspection requires a complete PID/start-time identity")
	}
	processRoot := filepath.Join(procRoot, strconv.Itoa(identity.PID))
	relative, err := filepath.Rel(processRoot, filepath.Clean(path))
	if err != nil || relative != "fd" {
		return errors.New("privileged proc descriptor path is outside the inspected process")
	}
	return nil
}

func validateRunnerdProcFDNames(names []string) ([]string, error) {
	if len(names) > maxPrivilegedProcFDEntries {
		return nil, errors.New("proc descriptor inventory exceeded its bounded entry count")
	}
	validated := make([]string, len(names))
	seen := make(map[string]struct{}, len(names))
	for index, name := range names {
		if name == "" {
			return nil, errors.New("proc descriptor inventory contains an empty entry")
		}
		if _, err := strconv.ParseUint(name, 10, 64); err != nil {
			return nil, errors.New("proc descriptor inventory contains a malformed entry")
		}
		if _, exists := seen[name]; exists {
			return nil, errors.New("proc descriptor inventory contains a duplicate entry")
		}
		seen[name] = struct{}{}
		validated[index] = name
	}
	// find(1) and ReadDir have different ordering guarantees. Sorting makes the
	// before/after comparison a stable check for descriptor churn.
	sort.Strings(validated)
	return validated, nil
}

func runnerdPermissionDenied(err error) bool {
	return errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM)
}

func validateRunnerdProcLinkPath(procRoot string, identity runnerdProcIdentity, path string) error {
	if identity.PID <= 0 || identity.StartTime == 0 {
		return errors.New("privileged proc link inspection requires a complete PID/start-time identity")
	}
	processRoot := filepath.Join(procRoot, strconv.Itoa(identity.PID))
	relative, err := filepath.Rel(processRoot, filepath.Clean(path))
	if err != nil {
		return errors.New("privileged proc link path is outside the inspected process")
	}
	if relative == "cwd" || relative == "exe" {
		return nil
	}
	parts := strings.Split(relative, string(os.PathSeparator))
	if len(parts) == 2 && parts[0] == "fd" {
		if _, err := strconv.ParseUint(parts[1], 10, 64); err == nil {
			return nil
		}
	}
	return errors.New("privileged proc link path is outside the allowed cwd, executable, or descriptor metadata")
}

func runnerdPrivilegedProcReadlink(ctx context.Context, procRoot string, identity runnerdProcIdentity, path string) (string, error) {
	if filepath.Clean(procRoot) != "/proc" {
		return "", errors.New("privileged proc metadata is restricted to the live /proc filesystem")
	}
	if err := validateRunnerdProcLinkPath(procRoot, identity, path); err != nil {
		return "", err
	}
	if err := verifyRunnerdReadOnlyUtility("/usr/bin/sudo", true); err != nil {
		return "", err
	}
	if err := verifyRunnerdReadOnlyUtility("/usr/bin/readlink", false); err != nil {
		return "", err
	}
	commandCtx, cancel := context.WithTimeout(ctx, privilegedProcLinkTimeout)
	defer cancel()
	command := exec.CommandContext(commandCtx, "/usr/bin/sudo", "-n", "--", "/usr/bin/readlink", "--", path)
	var stdout, stderr runnerdBoundedBuffer
	stdout.limit = maxPrivilegedProcLinkBytes + 1
	stderr.limit = maxPrivilegedProcErrBytes
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		if commandCtx.Err() != nil {
			return "", errors.New("privileged proc metadata command timed out")
		}
		return "", errors.New("sudo -n readlink could not inspect proc metadata")
	}
	if stdout.overflow || stderr.overflow {
		return "", errors.New("privileged proc metadata command output exceeded its bound")
	}
	target, err := parseRunnerdReadlinkOutput(stdout.Bytes())
	if err != nil {
		return "", err
	}
	return target, nil
}

func runnerdPrivilegedProcFDNames(ctx context.Context, procRoot string, identity runnerdProcIdentity, path string) ([]string, error) {
	if filepath.Clean(procRoot) != "/proc" {
		return nil, errors.New("privileged proc metadata is restricted to the live /proc filesystem")
	}
	if err := validateRunnerdProcFDDirectoryPath(procRoot, identity, path); err != nil {
		return nil, err
	}
	if err := verifyRunnerdReadOnlyUtility("/usr/bin/sudo", true); err != nil {
		return nil, err
	}
	if err := verifyRunnerdReadOnlyUtility("/usr/bin/find", false); err != nil {
		return nil, err
	}
	commandCtx, cancel := context.WithTimeout(ctx, privilegedProcLinkTimeout)
	defer cancel()
	// The path was constructed from a validated numeric PID and is exactly its
	// /proc/<pid>/fd directory. find lists symlink names only; it does not follow
	// links or read descriptor contents, environment, or memory.
	command := exec.CommandContext(commandCtx, "/usr/bin/sudo", "-n", "--", "/usr/bin/find", path,
		"-mindepth", "1", "-maxdepth", "1", "-type", "l", "-printf", "%f\\n")
	var stdout, stderr runnerdBoundedBuffer
	stdout.limit = maxPrivilegedProcFDBytes + 1
	stderr.limit = maxPrivilegedProcErrBytes
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		if commandCtx.Err() != nil {
			return nil, errors.New("privileged proc descriptor enumeration timed out")
		}
		return nil, errors.New("sudo -n find could not enumerate proc descriptor metadata")
	}
	if stdout.overflow || stderr.overflow {
		return nil, errors.New("privileged proc descriptor output exceeded its bound")
	}
	return parseRunnerdProcFDOutput(stdout.Bytes())
}

type runnerdBoundedBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (buffer *runnerdBoundedBuffer) Write(value []byte) (int, error) {
	if buffer.Len()+len(value) > buffer.limit {
		buffer.overflow = true
		return 0, errors.New("bounded process metadata output exceeded")
	}
	return buffer.Buffer.Write(value)
}

func parseRunnerdReadlinkOutput(output []byte) (string, error) {
	if len(output) < 2 || len(output) > maxPrivilegedProcLinkBytes+1 || output[len(output)-1] != '\n' {
		return "", errors.New("privileged proc link inspection returned malformed or truncated metadata")
	}
	target := output[:len(output)-1]
	if len(target) == 0 || bytes.IndexAny(target, "\x00\r\n") >= 0 {
		return "", errors.New("privileged proc link inspection returned malformed or truncated metadata")
	}
	return string(target), nil
}

func parseRunnerdProcFDOutput(output []byte) ([]string, error) {
	if len(output) > maxPrivilegedProcFDBytes {
		return nil, errors.New("privileged proc descriptor output exceeded its bound")
	}
	if len(output) == 0 {
		return []string{}, nil
	}
	if output[len(output)-1] != '\n' || bytes.IndexByte(output, '\x00') >= 0 || bytes.IndexByte(output, '\r') >= 0 {
		return nil, errors.New("privileged proc descriptor output is malformed or truncated")
	}
	lines := strings.Split(string(output[:len(output)-1]), "\n")
	return validateRunnerdProcFDNames(lines)
}

func verifyRunnerdReadOnlyUtility(path string, requireSetuid bool) error {
	info, err := os.Stat(path)
	if err != nil {
		return errors.New("required fixed privileged metadata utility is unavailable")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 ||
		(requireSetuid && info.Mode()&os.ModeSetuid == 0) {
		return errors.New("fixed privileged metadata utility ownership or mode is unsafe")
	}
	return nil
}

func runnerdInspectProcessTable(procFS runnerdProcFS, linkReader *runnerdProcLinkReader, procRoot string, serviceUID int, paths []string, exempt map[runnerdProcIdentity]struct{}) (map[runnerdProcIdentity]struct{}, error) {
	entries, err := procFS.readDir(procRoot)
	if err != nil {
		return nil, fmt.Errorf("read process inventory: %w", err)
	}
	relevant := make(map[runnerdProcIdentity]struct{})
	for _, entry := range entries {
		if err := linkReader.ctx.Err(); err != nil {
			return nil, errors.New("process inventory exceeded its bounded inspection time")
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 || !entry.IsDir() {
			continue
		}
		identity, uids, _, err := readRunnerdProcIdentityWith(procFS, procRoot, pid)
		if errors.Is(err, errRunnerdProcChurn) {
			return nil, errRunnerdProcChurn
		}
		if err != nil {
			return nil, fmt.Errorf("read process identity for PID %d: %w", pid, err)
		}
		cgroupPath, err := procFS.readFile(filepath.Join(procRoot, strconv.Itoa(pid), "cgroup"))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				gone, verifyErr := runnerdProcIncarnationGone(procFS, procRoot, identity)
				if verifyErr != nil {
					return nil, verifyErr
				}
				if gone {
					return nil, errRunnerdProcChurn
				}
				return nil, fmt.Errorf("PID %d remains live but its cgroup is not inspectable", pid)
			}
			return nil, fmt.Errorf("read cgroup for PID %d: %w", pid, err)
		}
		inUnitCgroup := runnerdHasPRDServiceCgroup(cgroupPath)
		serviceAccountProcess := containsInt(uids, serviceUID)
		if !inUnitCgroup && !serviceAccountProcess {
			if err := runnerdVerifyProcessIncarnation(procFS, procRoot, identity); err != nil {
				return nil, err
			}
			continue
		}
		relevant[identity] = struct{}{}
		if inUnitCgroup {
			return nil, fmt.Errorf("process PID %d remains in runnerd.service cgroup or descendant", pid)
		}
		if _, allowed := exempt[identity]; allowed {
			if err := runnerdVerifyProcessIncarnation(procFS, procRoot, identity); err != nil {
				return nil, err
			}
			continue
		}
		processRoot := filepath.Join(procRoot, strconv.Itoa(pid))
		cmdline, err := procFS.readFile(filepath.Join(processRoot, "cmdline"))
		if err != nil {
			gone, verifyErr := runnerdHandleMissingProcEvidence(procFS, procRoot, identity, err, "command line")
			if verifyErr != nil || gone {
				if gone {
					return nil, errRunnerdProcChurn
				}
				return nil, verifyErr
			}
			return nil, fmt.Errorf("PID %d command line is not inspectable: %w", pid, err)
		}
		if len(cmdline) == 0 {
			return nil, fmt.Errorf("PID %d has an empty command line while its incarnation remains live", pid)
		}
		if runnerdTextReferencesPaths(string(cmdline), paths) {
			return nil, fmt.Errorf("same-account process PID %d references the selected PRD paths", pid)
		}
		for _, item := range []struct {
			name string
			want string
		}{
			{"cwd", "has a working directory under the selected PRD paths"},
			{"exe", "executes from the selected PRD paths"},
		} {
			target, readErr := linkReader.readlink(identity, filepath.Join(processRoot, item.name))
			if readErr != nil {
				gone, verifyErr := runnerdHandleMissingProcEvidence(procFS, procRoot, identity, readErr, item.name)
				if verifyErr != nil || gone {
					if gone {
						return nil, errRunnerdProcChurn
					}
					return nil, verifyErr
				}
				return nil, fmt.Errorf("PID %d %s is not inspectable: %w", pid, item.name, readErr)
			}
			if runnerdPathReferencesPaths(runnerdCleanProcLink(target), paths) {
				return nil, fmt.Errorf("same-account process PID %d %s", pid, item.want)
			}
		}
		fdRoot := filepath.Join(processRoot, "fd")
		fds, err := linkReader.readFDNames(identity, fdRoot)
		if err != nil {
			if errors.Is(err, errRunnerdProcChurn) {
				return nil, errRunnerdProcChurn
			}
			gone, verifyErr := runnerdHandleMissingProcEvidence(procFS, procRoot, identity, err, "descriptor directory")
			if verifyErr != nil || gone {
				if gone {
					return nil, errRunnerdProcChurn
				}
				return nil, verifyErr
			}
			return nil, fmt.Errorf("PID %d descriptors are not inspectable: %w", pid, err)
		}
		for _, fd := range fds {
			if err := linkReader.ctx.Err(); err != nil {
				return nil, errors.New("process inventory exceeded its bounded inspection time")
			}
			target, readErr := linkReader.readlink(identity, filepath.Join(fdRoot, fd))
			if readErr != nil {
				if errors.Is(readErr, errRunnerdProcChurn) {
					return nil, errRunnerdProcChurn
				}
				gone, verifyErr := runnerdHandleMissingProcEvidence(procFS, procRoot, identity, readErr, "descriptor")
				if verifyErr != nil || gone {
					if gone {
						return nil, errRunnerdProcChurn
					}
					return nil, verifyErr
				}
				return nil, fmt.Errorf("PID %d descriptor %s is not inspectable: %w", pid, fd, readErr)
			}
			if runnerdPathReferencesPaths(runnerdCleanProcLink(target), paths) {
				return nil, fmt.Errorf("same-account process PID %d holds a descriptor under the selected PRD paths", pid)
			}
		}
		fdsAfter, err := linkReader.readFDNames(identity, fdRoot)
		if err != nil {
			if errors.Is(err, errRunnerdProcChurn) {
				return nil, errRunnerdProcChurn
			}
			gone, verifyErr := runnerdHandleMissingProcEvidence(procFS, procRoot, identity, err, "descriptor directory")
			if verifyErr != nil || gone {
				if gone {
					return nil, errRunnerdProcChurn
				}
				return nil, verifyErr
			}
			return nil, fmt.Errorf("PID %d descriptors are not inspectable: %w", pid, err)
		}
		if !sameRunnerdProcFDNames(fds, fdsAfter) {
			return nil, errRunnerdProcChurn
		}
		if err := runnerdVerifyProcessIncarnation(procFS, procRoot, identity); err != nil {
			return nil, err
		}
	}
	return relevant, nil
}

func sameRunnerdProcFDNames(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func readRunnerdProcIdentityWith(procFS runnerdProcFS, procRoot string, pid int) (runnerdProcIdentity, []int, int, error) {
	processRoot := filepath.Join(procRoot, strconv.Itoa(pid))
	statPath := filepath.Join(processRoot, "stat")
	stat, err := procFS.readFile(statPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return runnerdProcIdentity{}, nil, 0, errRunnerdProcChurn
		}
		return runnerdProcIdentity{}, nil, 0, err
	}
	parentPID, startTime, err := parseRunnerdProcStat(pid, stat)
	if err != nil {
		return runnerdProcIdentity{}, nil, 0, err
	}
	status, err := procFS.readFile(filepath.Join(processRoot, "status"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			gone, verifyErr := runnerdProcIncarnationGone(procFS, procRoot, runnerdProcIdentity{PID: pid, StartTime: startTime})
			if verifyErr != nil {
				return runnerdProcIdentity{}, nil, 0, verifyErr
			}
			if gone {
				return runnerdProcIdentity{}, nil, 0, errRunnerdProcChurn
			}
			return runnerdProcIdentity{}, nil, 0, fmt.Errorf("PID %d status is missing while its incarnation remains live", pid)
		}
		return runnerdProcIdentity{}, nil, 0, fmt.Errorf("read process status: %w", err)
	}
	uids, err := parseRunnerdProcUIDs(status)
	if err != nil {
		return runnerdProcIdentity{}, nil, 0, err
	}
	identity := runnerdProcIdentity{PID: pid, StartTime: startTime}
	if err := runnerdVerifyProcessIncarnation(procFS, procRoot, identity); err != nil {
		return runnerdProcIdentity{}, nil, 0, err
	}
	return identity, uids, parentPID, nil
}

func parseRunnerdProcStat(pid int, stat []byte) (parentPID int, startTime uint64, err error) {
	text := string(stat)
	open := strings.IndexByte(text, '(')
	close := strings.LastIndexByte(text, ')')
	if open < 1 || close < open || close+1 >= len(text) {
		return 0, 0, fmt.Errorf("malformed /proc/%d/stat", pid)
	}
	actualPID, parsePIDErr := strconv.Atoi(strings.TrimSpace(text[:open]))
	if parsePIDErr != nil || actualPID != pid {
		return 0, 0, fmt.Errorf("PID identity mismatch in /proc/%d/stat", pid)
	}
	fields := strings.Fields(text[close+1:])
	if len(fields) <= 19 {
		return 0, 0, fmt.Errorf("incomplete /proc/%d/stat", pid)
	}
	parentPID, err = strconv.Atoi(fields[1])
	if err != nil || parentPID < 0 {
		return 0, 0, fmt.Errorf("invalid parent PID in /proc/%d/stat", pid)
	}
	startTime, err = strconv.ParseUint(fields[19], 10, 64)
	if err != nil || startTime == 0 {
		return 0, 0, fmt.Errorf("invalid start time in /proc/%d/stat", pid)
	}
	return parentPID, startTime, nil
}

func parseRunnerdProcUIDs(status []byte) ([]int, error) {
	for _, line := range strings.Split(string(status), "\n") {
		if !strings.HasPrefix(line, "Uid:") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "Uid:"))
		if len(fields) < 1 {
			return nil, errors.New("invalid process UID identity")
		}
		uids := make([]int, 0, len(fields))
		for _, field := range fields {
			uid, err := strconv.Atoi(field)
			if err != nil || uid < 0 {
				return nil, errors.New("invalid process UID identity")
			}
			uids = append(uids, uid)
		}
		return uids, nil
	}
	return nil, errors.New("process UID identity is unavailable")
}

func containsInt(values []int, wanted int) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func runnerdVerifyProcessIncarnation(procFS runnerdProcFS, procRoot string, expected runnerdProcIdentity) error {
	data, err := procFS.readFile(filepath.Join(procRoot, strconv.Itoa(expected.PID), "stat"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errRunnerdProcChurn
		}
		return fmt.Errorf("recheck process PID %d identity: %w", expected.PID, err)
	}
	_, startTime, err := parseRunnerdProcStat(expected.PID, data)
	if err != nil {
		return err
	}
	if startTime != expected.StartTime {
		return errRunnerdProcChurn
	}
	return nil
}

func runnerdProcIncarnationGone(procFS runnerdProcFS, procRoot string, expected runnerdProcIdentity) (bool, error) {
	data, err := procFS.readFile(filepath.Join(procRoot, strconv.Itoa(expected.PID), "stat"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
		return false, fmt.Errorf("confirm original PID %d incarnation disappearance: %w", expected.PID, err)
	}
	_, startTime, err := parseRunnerdProcStat(expected.PID, data)
	if err != nil {
		return false, err
	}
	return startTime != expected.StartTime, nil
}

func runnerdHandleMissingProcEvidence(procFS runnerdProcFS, procRoot string, identity runnerdProcIdentity, missing error, label string) (gone bool, err error) {
	if !errors.Is(missing, os.ErrNotExist) {
		return false, nil
	}
	gone, err = runnerdProcIncarnationGone(procFS, procRoot, identity)
	if err != nil {
		return false, err
	}
	if !gone {
		return false, fmt.Errorf("PID %d %s disappeared while its process incarnation remains live", identity.PID, label)
	}
	return true, nil
}

func runnerdPathReferencesPaths(value string, paths []string) bool {
	value = runnerdCleanProcLink(value)
	for _, path := range paths {
		if runnerdPathHasPathPrefix(value, path) {
			return true
		}
	}
	return false
}

func runnerdPathHasPathPrefix(value, path string) bool {
	if value == path || strings.HasPrefix(value, path+string(os.PathSeparator)) {
		return true
	}
	for _, sidecar := range []string{"-wal", "-shm", "-journal"} {
		if value == path+sidecar {
			return true
		}
	}
	return false
}

func runnerdCleanProcLink(value string) string {
	value = strings.TrimSuffix(value, " (deleted)")
	return filepath.Clean(value)
}

func runnerdTextReferencesPaths(value string, paths []string) bool {
	for _, path := range paths {
		for offset := 0; offset < len(value); {
			index := strings.Index(value[offset:], path)
			if index < 0 {
				break
			}
			index += offset
			end := index + len(path)
			if end == len(value) || isPathArgumentBoundary(value[end]) || isSQLiteSidecarBoundary(value, end) {
				return true
			}
			offset = end
		}
	}
	return false
}

func isSQLiteSidecarBoundary(value string, end int) bool {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if strings.HasPrefix(value[end:], suffix) {
			next := end + len(suffix)
			return next == len(value) || isPathArgumentBoundary(value[next])
		}
	}
	return false
}

func isPathArgumentBoundary(value byte) bool {
	switch value {
	case '/', '=', '\x00', ' ', '\t', '\n', '\'', '"', ':':
		return true
	default:
		return false
	}
}

func runnerdHasPRDServiceCgroup(cgroup []byte) bool {
	const unitPath = "/system.slice/runnerd.service"
	for _, line := range strings.Split(string(cgroup), "\n") {
		fields := strings.SplitN(line, ":", 3)
		if len(fields) != 3 {
			continue
		}
		path := filepath.Clean(fields[2])
		if path == unitPath || strings.HasPrefix(path, unitPath+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

func sameRunnerdProcessSet(left, right map[runnerdProcIdentity]struct{}) bool {
	if len(left) != len(right) {
		return false
	}
	for identity := range left {
		if _, ok := right[identity]; !ok {
			return false
		}
	}
	return true
}
