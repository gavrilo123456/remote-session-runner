package runnerd

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
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

var errRunnerdProcChurn = errors.New("process identity changed during /proc inspection")

type runnerdProcIdentity struct {
	PID       int
	StartTime uint64
}

type runnerdProcFS struct {
	readDir  func(string) ([]os.DirEntry, error)
	readFile func(string) ([]byte, error)
	readlink func(string) (string, error)
}

func realRunnerdProcFS() runnerdProcFS {
	return runnerdProcFS{readDir: os.ReadDir, readFile: os.ReadFile, readlink: os.Readlink}
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
	if before.ID != "runnerd.service" || before.User != linuxPRDServiceAccount || before.MainPID != "0" || before.ControlPID != "0" || !runnerdHasRecordedExitTimestamp(before.ExitTimestamp) {
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

func runnerdHasRecordedExitTimestamp(value string) bool {
	_, err := time.Parse("Mon 2006-01-02 15:04:05 MST", strings.TrimSpace(value))
	return err == nil
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
		ControlPID: values[5], ControlGroup: values[6], ExitTimestamp: values[7],
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
	for attempt := 0; attempt < maxStableProcScans; attempt++ {
		before, err := runnerdInspectProcessTable(procFS, procRoot, serviceUID, paths, exempt)
		if errors.Is(err, errRunnerdProcChurn) {
			continue
		}
		if err != nil {
			return err
		}
		after, err := runnerdInspectProcessTable(procFS, procRoot, serviceUID, paths, exempt)
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

func runnerdInspectProcessTable(procFS runnerdProcFS, procRoot string, serviceUID int, paths []string, exempt map[runnerdProcIdentity]struct{}) (map[runnerdProcIdentity]struct{}, error) {
	entries, err := procFS.readDir(procRoot)
	if err != nil {
		return nil, fmt.Errorf("read process inventory: %w", err)
	}
	relevant := make(map[runnerdProcIdentity]struct{})
	for _, entry := range entries {
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
			target, readErr := procFS.readlink(filepath.Join(processRoot, item.name))
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
		fds, err := procFS.readDir(fdRoot)
		if err != nil {
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
			target, readErr := procFS.readlink(filepath.Join(fdRoot, fd.Name()))
			if readErr != nil {
				gone, verifyErr := runnerdHandleMissingProcEvidence(procFS, procRoot, identity, readErr, "descriptor")
				if verifyErr != nil || gone {
					if gone {
						return nil, errRunnerdProcChurn
					}
					return nil, verifyErr
				}
				return nil, fmt.Errorf("PID %d descriptor %s is not inspectable: %w", pid, fd.Name(), readErr)
			}
			if runnerdPathReferencesPaths(runnerdCleanProcLink(target), paths) {
				return nil, fmt.Errorf("same-account process PID %d holds a descriptor under the selected PRD paths", pid)
			}
		}
		if err := runnerdVerifyProcessIncarnation(procFS, procRoot, identity); err != nil {
			return nil, err
		}
	}
	return relevant, nil
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
