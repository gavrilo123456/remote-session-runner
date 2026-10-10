package runnerlocald

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	bug011MacLaunchctlHelperEnv = "RSR_B011_MAC_LAUNCHCTL_HELPER"
	bug011MacLaunchctlRootEnv   = "RSR_B011_MAC_LAUNCHCTL_ROOT"
	bug011MacLaunchctlModeEnv   = "RSR_B011_MAC_LAUNCHCTL_MODE"

	bug011MacLaunchctlInitialMode   = "initial"
	bug011MacLaunchctlCandidateMode = "candidate"

	// Match the production runner-locald source plist. The isolated fixture
	// must hold for at least this full interval *after enable* before candidate
	// bootstrap, otherwise a restart race can hide behind launchd throttling.
	bug011MacLaunchctlThrottleInterval = 10 * time.Second
)

type bug011MacLaunchctlEvent struct {
	Kind      string    `json:"kind"`
	Mode      string    `json:"mode"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	Signal    string    `json:"signal,omitempty"`
}

type bug011MacLaunchctlFixture struct {
	root           string
	eventsRoot     string
	label          string
	launchDomain   string
	initialPlist   string
	candidatePlist string
	initialPID     int
	candidatePID   int
	disabled       bool
}

// TestBUG011MacLaunchctlKeepAliveHandoff proves the only launchd property on
// which the B011 controlled-restart installer relies.  It uses a generated
// unique-label LaunchAgent entirely below a fresh 0700 /tmp root.  It never
// loads, stops, disables, signals, or reads either installed Runner agent.
//
// The test deliberately uses the host-proven handoff order: disable
// KeepAlive, SIGSTOP the old helper, boot it out while frozen, prove the label
// unloaded, SIGKILL the still-frozen helper only if bootout did not already
// terminate it, enable the label, then bootstrap a candidate.  The helper's
// append-only event ledger proves that its SIGTERM handler never ran and that
// the old helper did not restart before candidate bootstrap.
func TestBUG011MacLaunchctlKeepAliveHandoff(t *testing.T) {
	if os.Getenv(bug011MacLaunchctlHelperEnv) == "1" {
		bug011MacLaunchctlKeepAliveHelper(t)
		return
	}
	if os.Getenv(bug011MacHostGate) != "1" {
		t.Skip("set RSR_B011_MAC_HOST_GATE=1 to run the isolated launchd KeepAlive handoff gate")
	}
	if runtime.GOOS != "darwin" {
		t.Fatalf("BUG-011 launchd handoff gate must run on macOS, got %s", runtime.GOOS)
	}
	current, err := user.Current()
	if err != nil || current.Username != "tomasz.walczuk" || current.Uid != "502" {
		t.Fatalf("BUG-011 launchd handoff account=%v err=%v, want tomasz.walczuk uid 502", current, err)
	}
	hostname, err := os.Hostname()
	if err != nil || hostname != "AMAK2KJ6X9JJJ" {
		t.Fatalf("BUG-011 launchd handoff host=%q err=%v, want AMAK2KJ6X9JJJ", hostname, err)
	}

	fixture := newBUG011MacLaunchctlFixture(t)
	t.Cleanup(func() { fixture.cleanup(t) })

	bug011MacLaunchctl(t, "bootstrap", fixture.launchDomain, fixture.initialPlist)
	bug011MacLaunchctl(t, "kickstart", "-k", fixture.serviceTarget())
	initial := fixture.waitForSingleModeEvent(t, bug011MacLaunchctlInitialMode, 10*time.Second)
	fixture.initialPID = initial.PID
	fixture.assertOnlyInitialEvent(t)

	// launchctl disable alone does not prevent a loaded KeepAlive job from
	// respawning after SIGKILL.  Freeze it first, then remove the label from
	// launchd before any direct signal can end the old helper.
	fixture.disabled = true
	bug011MacLaunchctl(t, "disable", fixture.serviceTarget())
	bug011MacLaunchctl(t, "kill", "SIGSTOP", fixture.serviceTarget())
	fixture.waitForInitialHelperStopped(t, 10*time.Second)
	bug011MacLaunchctl(t, "bootout", fixture.launchDomain, fixture.initialPlist)
	fixture.waitForServiceUnloaded(t, 10*time.Second)
	directKill := fixture.killFrozenInitialHelperAfterUnload(t)
	fixture.waitForPIDGone(t, fixture.initialPID, 10*time.Second)
	fixture.assertOnlyInitialEventFor(t, 300*time.Millisecond)
	// This is intentionally before candidate bootstrap, matching the live
	// installer.  A disabled state is stored by label rather than by the plist
	// file that was just booted out.
	bug011MacLaunchctl(t, "enable", fixture.serviceTarget())
	fixture.disabled = false
	// Hold a full production ThrottleInterval after clearing the override.
	// killFrozenInitialHelperAfterUnload is only a test-fixture fallback; the
	// production installer never sends a direct SIGKILL and instead refuses to
	// start a candidate unless its staged stale-socket boundary is clear.
	fixture.assertOnlyInitialEventFor(t, bug011MacLaunchctlThrottleInterval)

	bug011MacLaunchctl(t, "bootstrap", fixture.launchDomain, fixture.candidatePlist)
	bug011MacLaunchctl(t, "kickstart", "-k", fixture.serviceTarget())
	candidate := fixture.waitForSingleModeEvent(t, bug011MacLaunchctlCandidateMode, 10*time.Second)
	fixture.candidatePID = candidate.PID
	if candidate.PID == initial.PID {
		t.Fatalf("candidate helper PID=%d reused the old helper PID; cannot prove a distinct candidate", candidate.PID)
	}
	fixture.assertEvents(t, map[string]int{
		bug011MacLaunchctlInitialMode:   1,
		bug011MacLaunchctlCandidateMode: 1,
	})
	t.Logf("B011-P7 launchd handoff PASS: label=%s old_pid=%d was frozen and booted out before termination, fixture_direct_kill_after_unload=%t, did not run SIGTERM cleanup or restart during the full %s post-enable ThrottleInterval; candidate_pid=%d started only after candidate bootstrap", fixture.label, initial.PID, directKill, bug011MacLaunchctlThrottleInterval, candidate.PID)
}

// The helper is invoked only by the disposable LaunchAgent in the host gate.
// It publishes one atomic event before blocking forever.  launchctl owns its
// lifecycle, and the parent fixture always bootouts the test-owned label.
func bug011MacLaunchctlKeepAliveHelper(t *testing.T) {
	t.Helper()
	root := os.Getenv(bug011MacLaunchctlRootEnv)
	mode := os.Getenv(bug011MacLaunchctlModeEnv)
	if root == "" || (mode != bug011MacLaunchctlInitialMode && mode != bug011MacLaunchctlCandidateMode) {
		t.Fatalf("BUG-011 launchd helper requires root and an initial or candidate mode")
	}
	eventsRoot := filepath.Join(root, "events")
	info, err := os.Stat(eventsRoot)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("BUG-011 launchd helper events root=%q info=%v err=%v, want 0700 directory", eventsRoot, info, err)
	}
	event := bug011MacLaunchctlEvent{Kind: "started", Mode: mode, PID: os.Getpid(), StartedAt: time.Now().UTC()}
	if err := bug011MacPublishLaunchctlEvent(eventsRoot, event); err != nil {
		t.Fatalf("publish BUG-011 launchd helper event: %v", err)
	}
	termSignals := make(chan os.Signal, 1)
	signal.Notify(termSignals, syscall.SIGTERM)
	defer signal.Stop(termSignals)
	go func() {
		for received := range termSignals {
			_ = bug011MacPublishLaunchctlEvent(eventsRoot, bug011MacLaunchctlEvent{
				Kind: "signal", Mode: mode, PID: os.Getpid(), StartedAt: time.Now().UTC(), Signal: received.String(),
			})
		}
	}()
	select {}
}

func newBUG011MacLaunchctlFixture(t *testing.T) *bug011MacLaunchctlFixture {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "rsr-bug011-launchctl-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		_ = os.RemoveAll(root)
		t.Fatal(err)
	}
	eventsRoot := filepath.Join(root, "events")
	if err := os.Mkdir(eventsRoot, 0o700); err != nil {
		_ = os.RemoveAll(root)
		t.Fatal(err)
	}
	label := fmt.Sprintf("com.remote-session-runner.b011-handoff.%d.%d", os.Getpid(), time.Now().UnixNano())
	fixture := &bug011MacLaunchctlFixture{
		root:           root,
		eventsRoot:     eventsRoot,
		label:          label,
		launchDomain:   fmt.Sprintf("gui/%d", os.Getuid()),
		initialPlist:   filepath.Join(root, "initial.plist"),
		candidatePlist: filepath.Join(root, "candidate.plist"),
	}
	fixture.writePlist(t, fixture.initialPlist, bug011MacLaunchctlInitialMode)
	fixture.writePlist(t, fixture.candidatePlist, bug011MacLaunchctlCandidateMode)
	return fixture
}

func (fixture *bug011MacLaunchctlFixture) serviceTarget() string {
	return fixture.launchDomain + "/" + fixture.label
}

func (fixture *bug011MacLaunchctlFixture) writePlist(t *testing.T, path, mode string) {
	t.Helper()
	plist, err := bug011MacLaunchctlPlist(fixture.label, os.Args[0], fixture.root, mode)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(plist); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("test-owned B011 launchd plist=%q info=%v err=%v, want regular 0600", path, info, err)
	}
}

func (fixture *bug011MacLaunchctlFixture) waitForSingleModeEvent(t *testing.T, mode string, timeout time.Duration) bug011MacLaunchctlEvent {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		events := fixture.events(t)
		var matches []bug011MacLaunchctlEvent
		for _, event := range events {
			if event.Kind == "started" && event.Mode == mode {
				matches = append(matches, event)
			}
		}
		if len(matches) == 1 {
			return matches[0]
		}
		if len(matches) > 1 {
			t.Fatalf("launchd helper mode %q started %d times before deadline: %+v", mode, len(matches), matches)
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for one launchd helper event mode=%q; events=%+v", mode, fixture.events(t))
	return bug011MacLaunchctlEvent{}
}

func (fixture *bug011MacLaunchctlFixture) assertOnlyInitialEvent(t *testing.T) {
	t.Helper()
	fixture.assertEvents(t, map[string]int{bug011MacLaunchctlInitialMode: 1})
}

func (fixture *bug011MacLaunchctlFixture) assertOnlyInitialEventFor(t *testing.T, duration time.Duration) {
	t.Helper()
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		fixture.assertOnlyInitialEvent(t)
		time.Sleep(25 * time.Millisecond)
	}
}

func (fixture *bug011MacLaunchctlFixture) assertEvents(t *testing.T, want map[string]int) {
	t.Helper()
	events := fixture.events(t)
	got := make(map[string]int)
	for _, event := range events {
		if event.Kind == "signal" {
			t.Fatalf("old launchd helper executed signal handler during protected handoff: %+v", event)
		}
		if event.Kind != "started" {
			t.Fatalf("unexpected launchd helper event kind: %+v", event)
		}
		got[event.Mode]++
	}
	if len(got) != len(want) {
		t.Fatalf("launchd helper events=%+v counts=%v, want counts=%v", events, got, want)
	}
	for mode, count := range want {
		if got[mode] != count {
			t.Fatalf("launchd helper events=%+v counts=%v, want counts=%v", events, got, want)
		}
	}
}

func (fixture *bug011MacLaunchctlFixture) events(t *testing.T) []bug011MacLaunchctlEvent {
	t.Helper()
	entries, err := os.ReadDir(fixture.eventsRoot)
	if err != nil {
		t.Fatal(err)
	}
	events := make([]bug011MacLaunchctlEvent, 0, len(entries))
	for _, entry := range entries {
		// A helper publishes each event via same-directory temporary file then
		// atomic rename.  The parent may observe that temporary file while the
		// helper is still syncing it; only a final .json file is evidence of a
		// started instance.
		if strings.HasSuffix(entry.Name(), ".tmp") {
			continue
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			t.Fatalf("unexpected test-owned launchd event entry %q", entry.Name())
		}
		bytes, err := os.ReadFile(filepath.Join(fixture.eventsRoot, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var event bug011MacLaunchctlEvent
		if err := json.Unmarshal(bytes, &event); err != nil || (event.Kind != "started" && event.Kind != "signal") || event.Mode == "" || event.PID <= 0 || event.StartedAt.IsZero() {
			t.Fatalf("invalid test-owned launchd event %q: event=%+v err=%v", entry.Name(), event, err)
		}
		if event.Kind == "signal" && event.Signal == "" {
			t.Fatalf("test-owned launchd signal event %q omitted its signal: %+v", entry.Name(), event)
		}
		events = append(events, event)
	}
	sort.Slice(events, func(left, right int) bool {
		if events[left].StartedAt.Equal(events[right].StartedAt) {
			return events[left].PID < events[right].PID
		}
		return events[left].StartedAt.Before(events[right].StartedAt)
	})
	return events
}

func (fixture *bug011MacLaunchctlFixture) waitForPIDGone(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil {
			t.Fatalf("check test-owned launchd helper PID %d: %v", pid, err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("test-owned launchd helper PID %d survived past deadline", pid)
}

func (fixture *bug011MacLaunchctlFixture) waitForInitialHelperStopped(t *testing.T, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		state, command, live, err := fixture.initialHelperProcessState()
		if err != nil {
			t.Fatalf("inspect frozen test-owned launchd helper: %v", err)
		}
		if !live {
			t.Fatalf("test-owned launchd helper PID %d exited before bootout", fixture.initialPID)
		}
		if !strings.Contains(command, filepath.Base(os.Args[0])) {
			t.Fatalf("test-owned launchd helper PID %d command=%q is not the fixture test binary", fixture.initialPID, command)
		}
		if strings.Contains(state, "T") {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("test-owned launchd helper PID %d did not enter stopped state before bootout", fixture.initialPID)
}

// killFrozenInitialHelperAfterUnload is deliberately direct only after the
// job no longer exists in launchd.  It refuses to signal a PID unless ps
// still identifies the stopped B011 helper test binary.  Production code uses
// the same ordering but obtains its old process identity before bootout.
func (fixture *bug011MacLaunchctlFixture) killFrozenInitialHelperAfterUnload(t *testing.T) bool {
	t.Helper()
	loaded, err := fixture.serviceLoaded()
	if err != nil {
		t.Fatalf("recheck launchd label before direct fixture SIGKILL: %v", err)
	}
	if loaded {
		t.Fatalf("refuse direct fixture SIGKILL while launchd label is still loaded: %s", fixture.label)
	}
	state, command, live, err := fixture.initialHelperProcessState()
	if err != nil {
		t.Fatalf("inspect detached frozen helper before direct SIGKILL: %v", err)
	}
	if !live {
		return false
	}
	if !strings.Contains(state, "T") || !strings.Contains(command, filepath.Base(os.Args[0])) || !strings.Contains(command, "TestBUG011MacLaunchctlKeepAliveHandoff") {
		t.Fatalf("refuse direct fixture SIGKILL without exact stopped test-helper identity: pid=%d state=%q command=%q", fixture.initialPID, state, command)
	}
	if err := syscall.Kill(fixture.initialPID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("SIGKILL detached frozen test-owned helper PID %d: %v", fixture.initialPID, err)
	}
	return true
}

func (fixture *bug011MacLaunchctlFixture) initialHelperProcessState() (state, command string, live bool, err error) {
	if fixture.initialPID <= 0 {
		return "", "", false, errors.New("missing initial helper PID")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	process := exec.CommandContext(ctx, "ps", "-o", "state=", "-o", "command=", "-p", fmt.Sprintf("%d", fixture.initialPID))
	output, runErr := process.CombinedOutput()
	if ctx.Err() != nil {
		return "", "", false, ctx.Err()
	}
	if runErr != nil {
		if errors.Is(syscall.Kill(fixture.initialPID, 0), syscall.ESRCH) {
			return "", "", false, nil
		}
		return "", "", false, fmt.Errorf("ps for PID %d: %w: %s", fixture.initialPID, runErr, strings.TrimSpace(string(output)))
	}
	line := strings.TrimSpace(string(output))
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return "", "", false, fmt.Errorf("ps for PID %d returned %q", fixture.initialPID, line)
	}
	return fields[0], strings.Join(fields[1:], " "), true, nil
}

func (fixture *bug011MacLaunchctlFixture) waitForServiceUnloaded(t *testing.T, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		loaded, err := fixture.serviceLoaded()
		if err != nil {
			t.Fatalf("inspect test-owned launchd label after bootout: %v", err)
		}
		if !loaded {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("test-owned launchd label remained loaded after bootout: %s", fixture.label)
}

func (fixture *bug011MacLaunchctlFixture) cleanup(t *testing.T) {
	t.Helper()
	// The test can fail after disable but before the normal enable.  Always
	// tear down by this fixture's unique label, then clear its disabled override
	// before removing anything on disk.  No installed Runner label is ever
	// constructed or passed to launchctl here.
	for _, plist := range []string{fixture.candidatePlist, fixture.initialPlist} {
		if plist == "" {
			continue
		}
		_ = fixture.cleanupLaunchctl("bootout", fixture.launchDomain, plist)
	}
	loaded, err := fixture.serviceLoaded()
	if err != nil {
		t.Errorf("inspect disposable B011 launchd label during cleanup: %v", err)
		return
	}
	if loaded {
		// Fallback only for this randomly generated fixture label.  Disabling
		// first prevents KeepAlive from replacing a helper while cleanup is
		// taking place; launchctl kill is label-scoped, never a PID signal.
		fixture.disabled = true
		_ = fixture.cleanupLaunchctl("disable", fixture.serviceTarget())
		_ = fixture.cleanupLaunchctl("kill", "SIGKILL", fixture.serviceTarget())
		for _, plist := range []string{fixture.candidatePlist, fixture.initialPlist} {
			if plist != "" {
				_ = fixture.cleanupLaunchctl("bootout", fixture.launchDomain, plist)
			}
		}
		loaded, err = fixture.serviceLoaded()
		if err != nil {
			t.Errorf("reinspect disposable B011 launchd label during cleanup: %v", err)
			return
		}
	}
	if loaded {
		t.Errorf("test-owned launchd label remained loaded during cleanup: %s", fixture.label)
		return
	}
	if fixture.disabled {
		if err := fixture.cleanupLaunchctl("enable", fixture.serviceTarget()); err != nil {
			t.Errorf("clear disabled override for test-owned launchd label %s: %v", fixture.label, err)
			return
		}
		fixture.disabled = false
	}
	if !fixture.waitForHelperPIDsGone(5 * time.Second) {
		t.Errorf("test-owned launchd helper remained live during cleanup; preserving fixture root %s", fixture.root)
		return
	}
	if fixture.root != "" {
		if err := os.RemoveAll(fixture.root); err != nil {
			t.Errorf("remove B011 launchd fixture root: %v", err)
		}
	}
}

func (fixture *bug011MacLaunchctlFixture) cleanupLaunchctl(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "launchctl", args...)
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("launchctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (fixture *bug011MacLaunchctlFixture) serviceLoaded() (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "launchctl", "print", fixture.serviceTarget())
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, nil
	}
	return true, nil
}

func (fixture *bug011MacLaunchctlFixture) waitForHelperPIDsGone(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		allGone := true
		for _, pid := range []int{fixture.candidatePID, fixture.initialPID} {
			if pid > 0 && !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
				allGone = false
				break
			}
		}
		if allGone {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return false
}

func bug011MacLaunchctl(t *testing.T, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "launchctl", args...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("launchctl %s failed: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
}

func bug011MacPublishLaunchctlEvent(eventsRoot string, event bug011MacLaunchctlEvent) error {
	if event.Kind != "started" && event.Kind != "signal" {
		return fmt.Errorf("invalid helper event kind %q", event.Kind)
	}
	if event.Mode != bug011MacLaunchctlInitialMode && event.Mode != bug011MacLaunchctlCandidateMode {
		return fmt.Errorf("invalid helper mode %q", event.Mode)
	}
	if event.PID <= 0 || event.StartedAt.IsZero() {
		return errors.New("missing helper identity")
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		return err
	}
	name := fmt.Sprintf("%s-%s-%d-%d.json", event.Mode, event.Kind, event.PID, event.StartedAt.UnixNano())
	finalPath := filepath.Join(eventsRoot, name)
	temporaryPath := finalPath + ".tmp"
	file, err := os.OpenFile(temporaryPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(encoded, '\n')); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		return err
	}
	directory, err := os.Open(eventsRoot)
	if err != nil {
		return err
	}
	err = directory.Sync()
	closeErr := directory.Close()
	return errors.Join(err, closeErr)
}

func bug011MacLaunchctlPlist(label, testBinary, root, mode string) ([]byte, error) {
	if label == "" || testBinary == "" || root == "" || (mode != bug011MacLaunchctlInitialMode && mode != bug011MacLaunchctlCandidateMode) {
		return nil, errors.New("launchd fixture plist requires label, test binary, root, and valid mode")
	}
	var body bytes.Buffer
	body.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	body.WriteString("<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n")
	body.WriteString("<plist version=\"1.0\"><dict>\n")
	bug011MacLaunchctlPlistString(&body, "Label", label)
	body.WriteString("<key>ProgramArguments</key><array>")
	bug011MacLaunchctlPlistValue(&body, testBinary)
	bug011MacLaunchctlPlistValue(&body, "-test.run=^TestBUG011MacLaunchctlKeepAliveHandoff$")
	body.WriteString("</array>\n")
	body.WriteString("<key>EnvironmentVariables</key><dict>")
	bug011MacLaunchctlPlistString(&body, bug011MacLaunchctlHelperEnv, "1")
	bug011MacLaunchctlPlistString(&body, bug011MacLaunchctlRootEnv, root)
	bug011MacLaunchctlPlistString(&body, bug011MacLaunchctlModeEnv, mode)
	body.WriteString("</dict>\n")
	body.WriteString("<key>KeepAlive</key><true/>\n")
	body.WriteString("<key>ThrottleInterval</key><integer>10</integer>\n")
	body.WriteString("<key>ProcessType</key><string>Background</string>\n")
	bug011MacLaunchctlPlistString(&body, "StandardOutPath", filepath.Join(root, mode+".stdout"))
	bug011MacLaunchctlPlistString(&body, "StandardErrorPath", filepath.Join(root, mode+".stderr"))
	body.WriteString("</dict></plist>\n")
	return body.Bytes(), nil
}

func bug011MacLaunchctlPlistString(body *bytes.Buffer, key, value string) {
	body.WriteString("<key>")
	bug011MacLaunchctlPlistEscaped(body, key)
	body.WriteString("</key>")
	bug011MacLaunchctlPlistValue(body, value)
}

func bug011MacLaunchctlPlistValue(body *bytes.Buffer, value string) {
	body.WriteString("<string>")
	bug011MacLaunchctlPlistEscaped(body, value)
	body.WriteString("</string>")
}

func bug011MacLaunchctlPlistEscaped(body *bytes.Buffer, value string) {
	_ = xml.EscapeText(body, []byte(value))
}

func TestBUG011LaunchctlKeepAliveFixturePlistContract(t *testing.T) {
	plist, err := bug011MacLaunchctlPlist("com.remote-session-runner.b011-handoff.fixture", "/tmp/runner-locald.test", "/tmp/rsr-b011", bug011MacLaunchctlInitialMode)
	if err != nil {
		t.Fatal(err)
	}
	text := string(plist)
	for _, required := range []string{
		"<key>Label</key><string>com.remote-session-runner.b011-handoff.fixture</string>",
		"<key>KeepAlive</key><true/>",
		"<key>ThrottleInterval</key><integer>10</integer>",
		"<key>ProgramArguments</key><array>",
		"-test.run=^TestBUG011MacLaunchctlKeepAliveHandoff$",
		"<key>RSR_B011_MAC_LAUNCHCTL_HELPER</key><string>1</string>",
		"<key>RSR_B011_MAC_LAUNCHCTL_MODE</key><string>initial</string>",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("disposable B011 launchd plist omitted %q: %s", required, text)
		}
	}
	if strings.Contains(text, "com.remote-session-runner.locald") || strings.Contains(text, "Library/Application Support/RemoteSessionRunner") {
		t.Fatalf("disposable B011 launchd plist references an installed Runner service: %s", text)
	}
}
