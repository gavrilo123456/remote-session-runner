package laneinstall

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
)

func TestSimpleMacServiceControllerStartsAndStopsOnlySelectedLaneAgents(t *testing.T) {
	plan, err := BuildSimplePlan("mac-control-plane", config.ReleaseLaneDev)
	if err != nil {
		t.Fatal(err)
	}
	runner := &simpleServiceTestRunner{macActive: map[string]bool{}}
	controller := simpleTestServiceController(runner, "501", config.MacAccount, "darwin", "arm64")
	if err := controller.Start(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if !runner.macActive[plan.Namespace.LocalAgentLabel] || !runner.macActive[plan.Namespace.LocalDAgentLabel] {
		t.Fatalf("selected Mac agents not active: %v", runner.macActive)
	}
	if err := controller.Stop(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if runner.macActive[plan.Namespace.LocalAgentLabel] || runner.macActive[plan.Namespace.LocalDAgentLabel] {
		t.Fatalf("selected Mac agents remain active: %v", runner.macActive)
	}
	for _, call := range runner.calls {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "com.remote-session-runner.local ") || strings.Contains(joined, "com.remote-session-runner.locald ") {
			t.Fatalf("unsuffixed PRD LaunchAgent was addressed: %v", call)
		}
	}
}

func TestSimpleMacServiceControllerWaitsForAsynchronousAgentUnload(t *testing.T) {
	plan, err := BuildSimplePlan("mac-control-plane", config.ReleaseLaneDev)
	if err != nil {
		t.Fatal(err)
	}
	runner := &simpleServiceTestRunner{
		macActive: map[string]bool{
			plan.Namespace.LocalAgentLabel:  true,
			plan.Namespace.LocalDAgentLabel: true,
		},
		macStopPollsRemaining: map[string]int{
			plan.Namespace.LocalAgentLabel:  2,
			plan.Namespace.LocalDAgentLabel: 1,
		},
	}
	controller := simpleTestServiceController(runner, "501", config.MacAccount, "darwin", "arm64")
	if err := controller.Stop(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if runner.macActive[plan.Namespace.LocalAgentLabel] || runner.macActive[plan.Namespace.LocalDAgentLabel] {
		t.Fatalf("selected Mac agents remain active after asynchronous unload: %v", runner.macActive)
	}
	if runner.macStatusPolls[plan.Namespace.LocalAgentLabel] < 3 || runner.macStatusPolls[plan.Namespace.LocalDAgentLabel] < 3 {
		t.Fatalf("Stop did not poll launchd until both agents unloaded: polls=%v", runner.macStatusPolls)
	}
}

func TestSimpleLinuxServiceControllerStartsStopsAndReportsOnlySelectedUnit(t *testing.T) {
	plan, err := BuildSimplePlan("linux-host", config.ReleaseLaneQAS)
	if err != nil {
		t.Fatal(err)
	}
	runner := &simpleServiceTestRunner{}
	controller := simpleTestServiceController(runner, "1000", config.LinuxAccount, "linux", "amd64")
	active, err := controller.Status(context.Background(), plan)
	if err != nil || active {
		t.Fatalf("initial selected unit state active=%t err=%v", active, err)
	}
	if err := controller.Start(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	active, err = controller.Status(context.Background(), plan)
	if err != nil || !active {
		t.Fatalf("post-start selected unit state active=%t err=%v", active, err)
	}
	if err := controller.Stop(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	active, err = controller.Status(context.Background(), plan)
	if err != nil || active {
		t.Fatalf("post-stop selected unit state active=%t err=%v", active, err)
	}
	for _, call := range runner.calls {
		if strings.Contains(strings.Join(call, " "), "runnerd.service") {
			t.Fatalf("unsuffixed PRD systemd unit was addressed: %v", call)
		}
	}
}

func TestSimpleLinuxStartCompletesAnAlreadyActiveInitialInstall(t *testing.T) {
	plan, err := BuildSimplePlan("linux-host", config.ReleaseLaneDev)
	if err != nil {
		t.Fatal(err)
	}
	runner := &simpleServiceTestRunner{linuxOn: true}
	controller := simpleTestServiceController(runner, "1000", config.LinuxAccount, "linux", "amd64")
	finalized := 0
	controller.initialInstall = func(_ context.Context, got Plan) error {
		finalized++
		if got.HostID != plan.HostID || got.Lane != plan.Lane {
			t.Fatalf("initial install finalizer target=%s/%s, want %s/%s", got.HostID, got.Lane, plan.HostID, plan.Lane)
		}
		return nil
	}
	if err := controller.Start(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if finalized != 1 {
		t.Fatalf("already-active first install finalizations=%d, want 1", finalized)
	}
}

func TestSimpleLinuxBootEnableTargetsOnlySelectedLaneWithoutStartingIt(t *testing.T) {
	plan, err := BuildSimplePlan("linux-host", config.ReleaseLaneDev)
	if err != nil {
		t.Fatal(err)
	}
	runner := &simpleServiceTestRunner{}
	controller := simpleTestServiceController(runner, "1000", config.LinuxAccount, "linux", "amd64")
	if err := controller.enableSystemd(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if runner.linuxOn {
		t.Fatal("boot enable unexpectedly started the selected service")
	}
	if len(runner.calls) != 1 || strings.Join(runner.calls[0], " ") != "/usr/bin/sudo /usr/bin/systemctl enable --quiet runnerd-dev.service" {
		t.Fatalf("boot enable calls=%v", runner.calls)
	}
}

func TestSimpleServiceControllerRejectsForeignRuntimeBeforeRunningCommands(t *testing.T) {
	plan, err := BuildSimplePlan("linux-host", config.ReleaseLaneDev)
	if err != nil {
		t.Fatal(err)
	}
	runner := &simpleServiceTestRunner{}
	controller := simpleTestServiceController(runner, "501", config.MacAccount, "darwin", "arm64")
	if _, err := controller.Status(context.Background(), plan); !errors.Is(err, ErrSimpleServiceTarget) {
		t.Fatalf("foreign platform status error=%v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("foreign platform reached service manager: %v", runner.calls)
	}
}

func TestSimpleServiceControllerRefusesStartWhenReadinessGuardFails(t *testing.T) {
	plan, err := BuildSimplePlan("linux-host", config.ReleaseLaneDev)
	if err != nil {
		t.Fatal(err)
	}
	runner := &simpleServiceTestRunner{}
	controller := simpleTestServiceController(runner, "1000", config.LinuxAccount, "linux", "amd64")
	controller.ready = func(Plan) error { return ErrSimpleLanePreflight }
	if err := controller.Start(context.Background(), plan); !errors.Is(err, ErrSimpleLanePreflight) {
		t.Fatalf("start when readiness guard fails error=%v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("unready lane reached systemd: %v", runner.calls)
	}
}

func TestSimpleServiceStopLeavesLaneStoppedWhenTerminalInventoryCannotBeProven(t *testing.T) {
	plan, err := BuildSimplePlan("linux-host", config.ReleaseLaneDev)
	if err != nil {
		t.Fatal(err)
	}
	retained := errors.New("retained fixture work remains")
	runner := &simpleServiceTestRunner{linuxOn: true}
	controller := simpleTestServiceController(runner, "1000", config.LinuxAccount, "linux", "amd64")
	controller.stoppedProof = func(Plan) error { return retained }
	if err := controller.Stop(context.Background(), plan); !errors.Is(err, retained) {
		t.Fatalf("stop postflight error=%v, want retained-work blocker", err)
	}
	if runner.linuxOn {
		t.Fatal("selected lane service restarted after its terminal inventory failed")
	}
}

func TestSimpleLinuxStopRejectsFailedSystemdResult(t *testing.T) {
	plan, err := BuildSimplePlan("linux-host", config.ReleaseLaneDev)
	if err != nil {
		t.Fatal(err)
	}
	runner := &simpleServiceTestRunner{linuxOn: true, systemdResult: "exit-code"}
	controller := simpleTestServiceController(runner, "1000", config.LinuxAccount, "linux", "amd64")
	provedStopped := false
	controller.stoppedProof = func(Plan) error {
		provedStopped = true
		return nil
	}
	if err := controller.Stop(context.Background(), plan); err == nil || !strings.Contains(err.Error(), "did not complete cleanly") {
		t.Fatalf("stop with failed systemd result error=%v", err)
	}
	if runner.linuxOn {
		t.Fatal("failed systemd stop left the selected service active")
	}
	if provedStopped {
		t.Fatal("failed systemd stop was reported as a completed quiescence proof")
	}
}

func TestWaitForSimpleInitialDatabaseRetriesOnlyTransientPreflight(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	attempts := 0
	err := waitForSimpleInitialDatabase(ctx, func() error {
		attempts++
		if attempts < 3 {
			return ErrSimpleLanePreflight
		}
		return nil
	})
	if err != nil || attempts != 3 {
		t.Fatalf("initial database retry attempts=%d err=%v, want 3 attempts and success", attempts, err)
	}
	permanent := errors.New("configuration mismatch")
	if err := waitForSimpleInitialDatabase(context.Background(), func() error { return permanent }); !errors.Is(err, permanent) {
		t.Fatalf("non-preflight failure=%v, want immediate original error", err)
	}
}

func TestSimpleMacFirstStartRejectsRetainedMailboxInputWithoutRemovingIt(t *testing.T) {
	plan, err := BuildSimplePlan("mac-control-plane", config.ReleaseLaneDev)
	if err != nil {
		t.Fatal(err)
	}
	root := simpleTestTempDir(t)
	plan.Namespace.Database = filepath.Join(root, "state", "local.db")
	plan.Namespace.DatabaseWAL = plan.Namespace.Database + "-wal"
	plan.Namespace.DatabaseSHM = plan.Namespace.Database + "-shm"
	plan.Namespace.MailboxRoot = filepath.Join(root, "mailbox")
	if err := os.MkdirAll(plan.Namespace.MailboxRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(plan.Namespace.MailboxRoot, "pending.request")
	if err := os.WriteFile(pending, []byte("retained test request"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateSimpleInstalledDatabase(context.Background(), plan, 39); !errors.Is(err, ErrSimpleLaneNotQuiescent) {
		t.Fatalf("first start with retained mailbox input error=%v", err)
	}
	if contents, err := os.ReadFile(pending); err != nil || string(contents) != "retained test request" {
		t.Fatalf("retained mailbox input changed: contents=%q err=%v", contents, err)
	}
}

func simpleTestServiceController(runner simpleCommandRunner, uid, account, goos, goarch string) *SimpleServiceController {
	return &SimpleServiceController{
		runner: runner, ready: func(Plan) error { return nil }, stoppedProof: func(Plan) error { return nil },
		uid: uid, user: account, goos: goos, goarch: goarch,
	}
}

type simpleServiceTestRunner struct {
	calls                 [][]string
	macActive             map[string]bool
	macStopPollsRemaining map[string]int
	macBootoutRequested   map[string]bool
	macStatusPolls        map[string]int
	linuxOn               bool
	systemdResult         string
}

func (r *simpleServiceTestRunner) Run(_ context.Context, name string, args ...string) (simpleCommandResult, error) {
	call := append([]string{name}, args...)
	r.calls = append(r.calls, call)
	if name == "/usr/sbin/lsof" {
		return simpleCommandResult{}, nil
	}
	if name == "/bin/launchctl" {
		if len(args) >= 2 && args[0] == "print" {
			label := args[1][strings.LastIndex(args[1], "/")+1:]
			if r.macStatusPolls == nil {
				r.macStatusPolls = make(map[string]int)
			}
			r.macStatusPolls[label]++
			if r.macBootoutRequested[label] {
				if r.macStopPollsRemaining[label] > 0 {
					r.macStopPollsRemaining[label]--
					return simpleCommandResult{ExitCode: 0}, nil
				}
				delete(r.macActive, label)
				delete(r.macBootoutRequested, label)
				return simpleCommandResult{ExitCode: 113}, nil
			}
			if r.macActive[label] {
				return simpleCommandResult{ExitCode: 0}, nil
			}
			return simpleCommandResult{ExitCode: 113}, nil
		}
		if len(args) >= 3 && args[0] == "bootstrap" {
			label := strings.TrimSuffix(args[2][strings.LastIndex(args[2], "/")+1:], ".plist")
			r.macActive[label] = true
			return simpleCommandResult{}, nil
		}
		if len(args) >= 3 && args[0] == "kickstart" {
			return simpleCommandResult{}, nil
		}
		if len(args) >= 2 && args[0] == "bootout" {
			label := args[1][strings.LastIndex(args[1], "/")+1:]
			if r.macStopPollsRemaining[label] > 0 {
				if r.macBootoutRequested == nil {
					r.macBootoutRequested = make(map[string]bool)
				}
				r.macBootoutRequested[label] = true
			} else {
				delete(r.macActive, label)
			}
			return simpleCommandResult{}, nil
		}
	}
	if name == "/usr/bin/systemctl" && len(args) == 2 && args[0] == "is-active" {
		if r.linuxOn {
			return simpleCommandResult{ExitCode: 0, Output: "active\n"}, nil
		}
		return simpleCommandResult{ExitCode: 3, Output: "inactive\n"}, nil
	}
	if name == "/usr/bin/systemctl" && len(args) == 4 && args[0] == "show" && args[1] == "--property=Result" && args[2] == "--value" {
		result := r.systemdResult
		if result == "" {
			result = "success"
		}
		return simpleCommandResult{Output: result + "\n"}, nil
	}
	if name == "/usr/bin/sudo" && len(args) >= 2 {
		offset := 0
		if args[0] == "-n" {
			offset = 1
		}
		if offset+1 >= len(args) || args[offset] != "/usr/bin/systemctl" {
			return simpleCommandResult{}, errors.New("unexpected test sudo command")
		}
		switch args[offset+1] {
		case "start":
			r.linuxOn = true
		case "stop":
			r.linuxOn = false
		case "enable":
			if len(args)-offset != 4 || args[offset+2] != "--quiet" || (args[offset+3] != "runnerd-dev.service" && args[offset+3] != "runnerd-qas.service") {
				return simpleCommandResult{ExitCode: 2, Output: "unexpected enable target"}, nil
			}
		default:
			return simpleCommandResult{ExitCode: 2, Output: "unexpected action"}, nil
		}
		return simpleCommandResult{}, nil
	}
	return simpleCommandResult{}, errors.New("unexpected test service command")
}
