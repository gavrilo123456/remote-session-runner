package runnerd

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/audit"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestRequireRunnerdFailedUnitRecoveryRechecksUnitAndPendingJobs(t *testing.T) {
	base := failedRunnerdUnitValues()
	processChecks := 0
	jobChecks := 0
	listJobs := func() ([]string, error) {
		jobChecks++
		return nil, nil
	}
	if err := requireRunnerdServiceStoppedForRecoveryWith(systemdValues(base), listJobs, func() error {
		processChecks++
		return nil
	}); err != nil {
		t.Fatalf("valid independently inspected failed unit: %v", err)
	}
	if processChecks != 1 || jobChecks != 2 {
		t.Fatalf("process checks=%d systemd job checks=%d, want 1/2", processChecks, jobChecks)
	}

	jobChecks = 0
	if err := requireRunnerdServiceStoppedForRecoveryWith(systemdValues(base), func() ([]string, error) {
		jobChecks++
		if jobChecks == 1 {
			return nil, nil
		}
		return []string{"42 runnerd.service start waiting"}, nil
	}, func() error { return nil }); err == nil || !strings.Contains(err.Error(), "pending systemd work") {
		t.Fatalf("concurrent start job error=%v, want fail-closed result", err)
	}

	calls := 0
	changing := func(property string) (string, error) {
		calls++
		if calls > 8 && property == "ActiveState" {
			return "activating", nil
		}
		return base[property], nil
	}
	if err := requireRunnerdServiceStoppedForRecoveryWith(changing, listJobs, func() error { return nil }); err == nil ||
		!strings.Contains(err.Error(), "state changed") {
		t.Fatalf("unit start during process scan error=%v, want refusal", err)
	}
}

func TestRequireRunnerdFailedUnitRecoveryRejectsUnverifiableUnit(t *testing.T) {
	tests := []struct {
		name   string
		change func(map[string]string)
	}{
		{"wrong unit", func(v map[string]string) { v["Id"] = "runnerd-dev.service" }},
		{"wrong account", func(v map[string]string) { v["User"] = "other" }},
		{"main process remains", func(v map[string]string) { v["MainPID"] = "123" }},
		{"control process remains", func(v map[string]string) { v["ControlPID"] = "123" }},
		{"exit time missing", func(v map[string]string) { v["ExecMainExitTimestamp"] = "" }},
		{"exit time malformed", func(v map[string]string) { v["ExecMainExitTimestamp"] = "n/a" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := failedRunnerdUnitValues()
			test.change(values)
			processChecks := 0
			err := requireRunnerdServiceStoppedForRecoveryWith(systemdValues(values), func() ([]string, error) { return nil, nil }, func() error {
				processChecks++
				return nil
			})
			if err == nil {
				t.Fatal("unverifiable failed unit was accepted")
			}
			if processChecks != 0 {
				t.Fatalf("process check ran %d times before unit guard passed", processChecks)
			}
		})
	}
}

func TestRunnerdFailedUnitFallbackRequiresExactProbeSelection(t *testing.T) {
	want := []string{linuxPRDProbeSession}
	if !isExactLinuxPRDProbeRecoverySelection(nil, 0, 0, want) {
		t.Fatal("exact one-session recovery selection was rejected")
	}
	for _, test := range []struct {
		jobs     []string
		pairs    int
		teardown int
		sessions []string
	}{
		{jobs: []string{"job-extra"}, sessions: want},
		{pairs: 1, sessions: want},
		{teardown: 1, sessions: want},
		{sessions: []string{linuxPRDProbeSession, "sess-other"}},
		{sessions: []string{"sess-other"}},
	} {
		if isExactLinuxPRDProbeRecoverySelection(test.jobs, test.pairs, test.teardown, test.sessions) {
			t.Fatalf("unsafe failed-unit recovery input was accepted: %+v", test)
		}
	}
}

func TestExactLinuxPRDProbeRecoveryRecordsBindOnlyTheAuthorizedCommandlessReservation(t *testing.T) {
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeDirectMTLS, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	target, err := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	reservedAt := time.Date(2026, time.October, 25, 20, 8, 0, 0, time.UTC)
	job := store.JobRecord{
		JobID: domain.JobID(linuxPRDProbeJob), SessionID: domain.SessionID(linuxPRDProbeSession),
		CommandID: domain.CommandID(linuxPRDProbeCommand), Controller: controller, Ingress: audit.IngressDirectMTLS,
		IdempotencyKey: "catalog-probe-63471d41af07b7afaf577d6a0989cc03-linux-host", Environment: "linux-dev",
		Target: target, Source: domain.NewEmptySource(), ScriptBytes: []byte(linuxPRDProbeScript),
		Phase: store.JobPhaseLost, TeardownState: store.JobTeardownPending,
	}
	session := store.SessionRecord{
		SessionID: job.SessionID, Target: target, Environment: job.Environment, Controller: controller,
		Source: domain.NewEmptySource(), State: domain.SessionStateLost,
	}
	reservation := store.SessionReservation{SessionID: session.SessionID, HostKey: "authority", ReservedAt: reservedAt}
	recovery := store.CommandlessLostRuntimeRecovery{
		JobID: job.JobID, SessionID: session.SessionID, CommandID: job.CommandID,
		Session: session, SessionReservation: reservation,
	}
	if !exactLinuxPRDProbeRecoveryRecordsMatch(recovery, job, session, reservation) {
		t.Fatal("exact recorded probe recovery tuple was rejected")
	}

	cleanupAt := reservedAt.Add(time.Minute)
	releasedAt := cleanupAt.Add(time.Nanosecond)
	reservation.CleanupConfirmedAt, reservation.ReleasedAt = &cleanupAt, &releasedAt
	recovery.SessionReservation = reservation
	recovery.AlreadyRecovered = true
	if !exactLinuxPRDProbeRecoveryRecordsMatch(recovery, job, session, reservation) {
		t.Fatal("supported recovered or interrupted-finalization retry state was rejected")
	}
	recovery.FinalizationPending = true
	if !exactLinuxPRDProbeRecoveryRecordsMatch(recovery, job, session, reservation) {
		t.Fatal("supported pending-finalization retry state was rejected")
	}

	mutations := []struct {
		name string
		edit func(*store.CommandlessLostRuntimeRecovery, *store.JobRecord, *store.SessionRecord, *store.SessionReservation)
	}{
		{"different job", func(r *store.CommandlessLostRuntimeRecovery, _ *store.JobRecord, _ *store.SessionRecord, _ *store.SessionReservation) {
			r.JobID = "job-other"
		}},
		{"different session", func(r *store.CommandlessLostRuntimeRecovery, _ *store.JobRecord, _ *store.SessionRecord, _ *store.SessionReservation) {
			r.SessionID = "sess-other"
		}},
		{"different planned command", func(r *store.CommandlessLostRuntimeRecovery, _ *store.JobRecord, _ *store.SessionRecord, _ *store.SessionReservation) {
			r.CommandID = "cmd-other"
		}},
		{"different idempotency key", func(_ *store.CommandlessLostRuntimeRecovery, j *store.JobRecord, _ *store.SessionRecord, _ *store.SessionReservation) {
			j.IdempotencyKey = "other"
		}},
		{"different ingress", func(_ *store.CommandlessLostRuntimeRecovery, j *store.JobRecord, _ *store.SessionRecord, _ *store.SessionReservation) {
			j.Ingress = audit.IngressMailbox
		}},
		{"different environment", func(_ *store.CommandlessLostRuntimeRecovery, j *store.JobRecord, _ *store.SessionRecord, _ *store.SessionReservation) {
			j.Environment = "linux-qas"
		}},
		{"different script", func(_ *store.CommandlessLostRuntimeRecovery, j *store.JobRecord, _ *store.SessionRecord, _ *store.SessionReservation) {
			j.ScriptBytes = []byte("other")
		}},
		{"successful job phase", func(_ *store.CommandlessLostRuntimeRecovery, j *store.JobRecord, _ *store.SessionRecord, _ *store.SessionReservation) {
			j.Phase = store.JobPhaseComplete
		}},
		{"runtime generation", func(_ *store.CommandlessLostRuntimeRecovery, _ *store.JobRecord, s *store.SessionRecord, _ *store.SessionReservation) {
			s.RuntimeGeneration = "unexpected"
		}},
		{"different reservation host", func(_ *store.CommandlessLostRuntimeRecovery, _ *store.JobRecord, _ *store.SessionRecord, r *store.SessionReservation) {
			r.HostKey = "other"
		}},
		{"partial release timestamps", func(_ *store.CommandlessLostRuntimeRecovery, _ *store.JobRecord, _ *store.SessionRecord, r *store.SessionReservation) {
			r.ReleasedAt = nil
		}},
		{"finalization without release", func(r *store.CommandlessLostRuntimeRecovery, _ *store.JobRecord, _ *store.SessionRecord, _ *store.SessionReservation) {
			r.AlreadyRecovered = false
		}},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			candidateRecovery, candidateJob, candidateSession, candidateReservation := recovery, job, session, reservation
			mutation.edit(&candidateRecovery, &candidateJob, &candidateSession, &candidateReservation)
			if exactLinuxPRDProbeRecoveryRecordsMatch(candidateRecovery, candidateJob, candidateSession, candidateReservation) {
				t.Fatal("mismatched recovery tuple was accepted")
			}
		})
	}
}

func TestRunnerdProcStatBindsPIDAndKernelStartTime(t *testing.T) {
	_, start, err := parseRunnerdProcStat(1234, fixtureProcStat(1234, 1, 456))
	if err != nil || start != 456 {
		t.Fatalf("parsed PID/start=%d/%d err=%v", 1234, start, err)
	}
	if _, _, err := parseRunnerdProcStat(1235, fixtureProcStat(1234, 1, 456)); err == nil {
		t.Fatal("reused directory with a different /proc PID was accepted")
	}
	fs := realRunnerdProcFS()
	root := t.TempDir()
	processFixture(t, root, 1234, 1001, "unrelated")
	if gone, err := runnerdProcIncarnationGone(fs, root, runnerdProcIdentity{PID: 1234, StartTime: 99}); err != nil || !gone {
		t.Fatalf("PID-reused incarnation gone=%v err=%v, want true/nil", gone, err)
	}
	if gone, err := runnerdProcIncarnationGone(fs, root, runnerdProcIdentity{PID: 1234, StartTime: 1234}); err != nil || gone {
		t.Fatalf("same PID incarnation gone=%v err=%v, want false/nil", gone, err)
	}
}

func TestRunnerdProcScanChecksPeerLaneAndResolvedPathReferences(t *testing.T) {
	procRoot := t.TempDir()
	serviceRoot := "/home/ubuntu/.local/share/remote-session-runner"
	processFixture(t, procRoot, 1234, 1001, "runnerd", "--config", serviceRoot+"-dev/config/linux.yaml")
	if err := runnerdProcessesReferencePathsWith(realRunnerdProcFS(), procRoot, 1001, []string{serviceRoot}, nil); err != nil {
		t.Fatalf("peer lane process was mistaken for PRD: %v", err)
	}
	processFixture(t, procRoot, 2234, 1001, "runnerd", "--config", serviceRoot+"/config/linux.yaml")
	if err := runnerdProcessesReferencePathsWith(realRunnerdProcFS(), procRoot, 1001, []string{serviceRoot}, nil); err == nil ||
		!strings.Contains(err.Error(), "references the selected PRD paths") {
		t.Fatalf("PRD process evidence error=%v, want refusal", err)
	}
}

func TestRunnerdProcScanChecksCwdDescriptorsAndNestedUnitCgroup(t *testing.T) {
	serviceRoot := "/home/ubuntu/.local/share/remote-session-runner"
	tests := []struct {
		name  string
		setup func(*testing.T, string)
		want  string
	}{
		{
			name: "working directory",
			setup: func(t *testing.T, root string) {
				cwd := filepath.Join(root, "cwd")
				if err := os.Remove(cwd); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(serviceRoot+"/workspaces/session", cwd); err != nil {
					t.Fatal(err)
				}
			},
			want: "working directory under the selected PRD paths",
		},
		{
			name: "open database descriptor",
			setup: func(t *testing.T, root string) {
				if err := os.Symlink(serviceRoot+"/state/remote.db", filepath.Join(root, "fd", "7")); err != nil {
					t.Fatal(err)
				}
			},
			want: "holds a descriptor under the selected PRD paths",
		},
		{
			name: "nested runnerd service cgroup",
			setup: func(t *testing.T, root string) {
				if err := os.WriteFile(filepath.Join(root, "cgroup"), []byte("0::/system.slice/runnerd.service/child/worker\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			want: "runnerd.service cgroup or descendant",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			procRoot := t.TempDir()
			processRoot := processFixture(t, procRoot, 4321, 1001, "unrelated")
			test.setup(t, processRoot)
			err := runnerdProcessesReferencePathsWith(realRunnerdProcFS(), procRoot, 1001, []string{serviceRoot}, nil)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("process evidence error=%v, want %q", err, test.want)
			}
		})
	}
}

func TestRunnerdProcScanDoesNotTreatMissingLiveEvidenceAsProcessExit(t *testing.T) {
	procRoot := t.TempDir()
	processRoot := processFixture(t, procRoot, 987, 1001, "idle")
	if err := os.Remove(filepath.Join(processRoot, "cmdline")); err != nil {
		t.Fatal(err)
	}
	err := runnerdProcessesReferencePathsWith(realRunnerdProcFS(), procRoot, 1001, []string{"/home/ubuntu/prd"}, nil)
	if err == nil || !strings.Contains(err.Error(), "command line disappeared while its process incarnation remains live") {
		t.Fatalf("missing-but-live process error=%v, want fail-closed result", err)
	}
}

func TestRunnerdProcScanAcceptsConfirmedProcessDisappearanceAndRejectsChurn(t *testing.T) {
	procRoot := t.TempDir()
	processRoot := processFixture(t, procRoot, 777, 1001, "idle")
	base := realRunnerdProcFS()
	removed := false
	fs := base
	fs.readFile = func(path string) ([]byte, error) {
		if filepath.Base(path) == "cmdline" && !removed {
			removed = true
			if err := os.RemoveAll(processRoot); err != nil {
				t.Fatal(err)
			}
			return nil, os.ErrNotExist
		}
		return base.readFile(path)
	}
	if err := runnerdProcessesReferencePathsWith(fs, procRoot, 1001, []string{"/home/ubuntu/prd"}, nil); err != nil {
		t.Fatalf("confirmed disappearance of original PID incarnation: %v", err)
	}

	churnRoot := t.TempDir()
	processFixture(t, churnRoot, 1001, 1001, "idle")
	processFixture(t, churnRoot, 1002, 1001, "idle")
	readCount := 0
	churningFS := realRunnerdProcFS()
	churningFS.readDir = func(path string) ([]os.DirEntry, error) {
		entries, err := os.ReadDir(path)
		if err != nil || path != churnRoot {
			return entries, err
		}
		readCount++
		filtered := make([]os.DirEntry, 0, len(entries))
		for _, entry := range entries {
			if entry.Name() == "1002" && readCount%2 == 1 {
				continue
			}
			filtered = append(filtered, entry)
		}
		return filtered, nil
	}
	if err := runnerdProcessesReferencePathsWith(churningFS, churnRoot, 1001, []string{"/home/ubuntu/prd"}, nil); err == nil ||
		!strings.Contains(err.Error(), "kept changing") {
		t.Fatalf("unresolved PID churn error=%v, want refusal", err)
	}
}

func TestRunnerdProcScanFailsClosedOnDeniedInspection(t *testing.T) {
	for _, denied := range []string{"proc-root", "stat", "status", "cgroup", "cmdline", "cwd", "exe", "fd-root", "fd-link"} {
		t.Run(denied, func(t *testing.T) {
			procRoot := t.TempDir()
			processRoot := processFixture(t, procRoot, 987, 1001, "idle")
			if err := os.Symlink("/tmp/descriptor", filepath.Join(processRoot, "fd", "7")); err != nil {
				t.Fatal(err)
			}
			base := realRunnerdProcFS()
			fs := base
			fs.readDir = func(path string) ([]os.DirEntry, error) {
				if denied == "proc-root" && path == procRoot || denied == "fd-root" && filepath.Base(path) == "fd" {
					return nil, os.ErrPermission
				}
				return base.readDir(path)
			}
			fs.readFile = func(path string) ([]byte, error) {
				if filepath.Base(path) == denied {
					return nil, os.ErrPermission
				}
				return base.readFile(path)
			}
			fs.readlink = func(path string) (string, error) {
				if filepath.Base(path) == denied || denied == "fd-link" && filepath.Base(path) == "7" {
					return "", os.ErrPermission
				}
				return base.readlink(path)
			}
			err := runnerdProcessesReferencePathsWith(fs, procRoot, 1001, []string{"/home/ubuntu/prd"}, nil)
			if err == nil || !strings.Contains(err.Error(), "permission denied") {
				t.Fatalf("denied proc inspection error=%v, want fail-closed permission result", err)
			}
		})
	}
}

func TestRunnerdHasPRDServiceCgroupUsesExactPathAndDescendants(t *testing.T) {
	for _, test := range []struct {
		path string
		want bool
	}{
		{"0::/system.slice/runnerd.service", true},
		{"0::/system.slice/runnerd.service/child/worker", true},
		{"0::/system.slice/runnerd-dev.service", false},
		{"0::/system.slice/runnerd.service-peer", false},
	} {
		if got := runnerdHasPRDServiceCgroup([]byte(test.path)); got != test.want {
			t.Errorf("runnerdHasPRDServiceCgroup(%q)=%v, want %v", test.path, got, test.want)
		}
	}
}

func TestRunnerdPathReferencesIncludesSQLiteSidecarsWithoutMatchingPeerLanes(t *testing.T) {
	database := "/home/ubuntu/.local/share/remote-session-runner/state/runner.db"
	for _, test := range []struct {
		path string
		want bool
	}{
		{database, true},
		{database + "-wal", true},
		{database + "-shm", true},
		{database + "-journal", true},
		{database + "-wal.extra", false},
		{database + "-dev", false},
		{"/home/ubuntu/.local/share/remote-session-runner-dev/state/runner.db", false},
	} {
		if got := runnerdPathReferencesPaths(test.path, []string{database}); got != test.want {
			t.Errorf("runnerdPathReferencesPaths(%q)=%v, want %v", test.path, got, test.want)
		}
	}
}

func TestRecoveryConfigurationFingerprintRejectsDrift(t *testing.T) {
	path := filepath.Join(t.TempDir(), "linux.yaml")
	initial := []byte("account: ubuntu\n")
	if err := os.WriteFile(path, initial, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := requireConfigFingerprint(path, sha256.Sum256(initial)); err != nil {
		t.Fatalf("unchanged config fingerprint: %v", err)
	}
	if err := os.WriteFile(path, []byte("account: changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := requireConfigFingerprint(path, sha256.Sum256(initial)); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("changed config fingerprint error=%v, want rejection", err)
	}
}

func processFixture(t *testing.T, procRoot string, pid, uid int, argv ...string) string {
	t.Helper()
	processRoot := filepath.Join(procRoot, strconv.Itoa(pid))
	if err := os.Mkdir(processRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(processRoot, "stat"), fixtureProcStat(pid, 1, uint64(pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(processRoot, "status"), []byte(fmt.Sprintf("Name:\ttest\nUid:\t%d\t%d\t%d\t%d\n", uid, uid, uid, uid)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(processRoot, "cmdline"), []byte(strings.Join(argv, "\x00")+"\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(processRoot, "cgroup"), []byte("0::/user.slice\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/tmp", filepath.Join(processRoot, "cwd")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/usr/bin/bash", filepath.Join(processRoot, "exe")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(processRoot, "fd"), 0o700); err != nil {
		t.Fatal(err)
	}
	return processRoot
}

func fixtureProcStat(pid, parentPID int, startTime uint64) []byte {
	fields := []string{"S", strconv.Itoa(parentPID)}
	for index := 2; index < 20; index++ {
		fields = append(fields, "0")
	}
	fields[19] = strconv.FormatUint(startTime, 10)
	return []byte(fmt.Sprintf("%d (test process) %s\n", pid, strings.Join(fields, " ")))
}

func failedRunnerdUnitValues() map[string]string {
	return map[string]string{
		"Id": "runnerd.service", "User": linuxPRDServiceAccount, "ActiveState": "failed", "SubState": "failed",
		"MainPID": "0", "ControlPID": "0", "ControlGroup": "", "ExecMainExitTimestamp": "Sun 2026-10-25 20:08:29 UTC",
	}
}

func systemdValues(values map[string]string) func(string) (string, error) {
	return func(property string) (string, error) {
		value, ok := values[property]
		if !ok {
			return "", fmt.Errorf("unexpected systemd property %q", property)
		}
		return value, nil
	}
}
