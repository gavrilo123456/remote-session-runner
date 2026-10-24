package laneinstall

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"remote-session-runner/src/internal/config"
)

func TestSimpleMacStopGracefullyStopsOnlyVerifiedOrphanSocketListener(t *testing.T) {
	plan, err := BuildSimplePlan("mac-control-plane", config.ReleaseLaneDev)
	if err != nil {
		t.Fatal(err)
	}
	resolvedBinary := "/Users/tomasz.walczuk/projects-runtime/runner-releases/test/mac-control-plane/bin/runner-local"
	runner := &macOrphanServiceTestRunner{plan: plan, uid: "502", pid: 53881, wrapperPID: 53850, resolvedBinary: resolvedBinary}
	controller := simpleTestServiceController(runner, "502", config.MacAccount, "darwin", "arm64")
	controller.resolvePath = func(path string) (string, error) {
		want := filepath.Join(plan.Namespace.ServiceRoot, "current", "bin", "runner-local")
		if path != want {
			t.Fatalf("resolved binary path=%q, want %q", path, want)
		}
		return resolvedBinary, nil
	}
	proofs := 0
	controller.stoppedProof = func(Plan) error {
		proofs++
		return nil
	}

	if err := controller.Stop(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if !runner.terminated || runner.signals != 1 || runner.signalledPID != runner.wrapperPID {
		t.Fatalf("verified orphan termination=%t signals=%d", runner.terminated, runner.signals)
	}
	if proofs != 2 {
		t.Fatalf("stopped work proof count=%d, want before and after orphan termination", proofs)
	}
	for _, call := range runner.calls {
		if strings.Contains(strings.Join(call, " "), "com.remote-session-runner.local ") || strings.Contains(strings.Join(call, " "), "runnerd.service") {
			t.Fatalf("stop addressed a peer lane or PRD service: %v", call)
		}
	}
}

func TestMacLaneCaptureCommandUsesLaneSpecificLogFile(t *testing.T) {
	plan, err := BuildSimplePlan("mac-control-plane", config.ReleaseLaneDev)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := macLaneCaptureCommand(plan, "runner-local"), filepath.Join(plan.Namespace.Logs, "local.log"); !strings.Contains(got, "--log-path "+want+" ") {
		t.Fatalf("local wrapper command=%q, want log path %q", got, want)
	}
	if got, want := macLaneCaptureCommand(plan, "runner-locald"), filepath.Join(plan.Namespace.Logs, "locald.log"); !strings.Contains(got, "--log-path "+want+" ") {
		t.Fatalf("locald wrapper command=%q, want log path %q", got, want)
	}
}

func TestSimpleMacStopRefusesUnverifiedSocketOwnerAndRetainsIt(t *testing.T) {
	plan, err := BuildSimplePlan("mac-control-plane", config.ReleaseLaneDev)
	if err != nil {
		t.Fatal(err)
	}
	runner := &macOrphanServiceTestRunner{
		plan: plan, uid: "502", pid: 53881,
		resolvedBinary: "/unexpected/runner-local",
	}
	controller := simpleTestServiceController(runner, "502", config.MacAccount, "darwin", "arm64")
	controller.resolvePath = func(string) (string, error) { return "/expected/runner-local", nil }
	if err := controller.Stop(context.Background(), plan); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("unverified listener stop error=%v, want identity refusal", err)
	}
	if runner.terminated || runner.signals != 0 {
		t.Fatalf("unverified process was signalled: terminated=%t signals=%d", runner.terminated, runner.signals)
	}
}

func TestSimpleMacStopRefusesUnexpectedLogCaptureParent(t *testing.T) {
	plan, err := BuildSimplePlan("mac-control-plane", config.ReleaseLaneDev)
	if err != nil {
		t.Fatal(err)
	}
	runner := &macOrphanServiceTestRunner{
		plan: plan, uid: "502", pid: 53881, wrapperPID: 53850,
		resolvedBinary:         "/expected/runner-local",
		wrapperCommandOverride: "/unexpected/runner-log-capture --config another-lane",
	}
	controller := simpleTestServiceController(runner, "502", config.MacAccount, "darwin", "arm64")
	controller.resolvePath = func(string) (string, error) { return "/expected/runner-local", nil }
	if err := controller.Stop(context.Background(), plan); err == nil || !strings.Contains(err.Error(), "not supervised") {
		t.Fatalf("unexpected wrapper stop error=%v, want wrapper identity refusal", err)
	}
	if runner.terminated || runner.signals != 0 {
		t.Fatalf("unexpected wrapper/process was signalled: terminated=%t signals=%d", runner.terminated, runner.signals)
	}
}

func TestSimpleMacStopDoesNotSignalOrphanWhenWorkProofFails(t *testing.T) {
	plan, err := BuildSimplePlan("mac-control-plane", config.ReleaseLaneDev)
	if err != nil {
		t.Fatal(err)
	}
	runner := &macOrphanServiceTestRunner{plan: plan, uid: "502", pid: 53881, resolvedBinary: "/expected/runner-local"}
	controller := simpleTestServiceController(runner, "502", config.MacAccount, "darwin", "arm64")
	controller.resolvePath = func(string) (string, error) { return "/expected/runner-local", nil }
	retained := errors.New("retained work remains")
	controller.stoppedProof = func(Plan) error { return retained }
	if err := controller.Stop(context.Background(), plan); !errors.Is(err, retained) {
		t.Fatalf("work-proof error=%v, want retained-work blocker", err)
	}
	if runner.terminated || runner.signals != 0 || runner.lsofCalls != 0 {
		t.Fatalf("orphan cleanup passed failed work proof: terminated=%t signals=%d lsof=%d", runner.terminated, runner.signals, runner.lsofCalls)
	}
}

func TestParseMacLsofProcessesPreservesSocketPathsAndRejectsMalformedPID(t *testing.T) {
	parsed, err := parseMacLsofProcesses("p123\ncrunner-local\nu502\nf9u\nn/Users/a b/run/local-api.sock\nftxt\nn/catalog/bin/runner-local\n")
	if err != nil || len(parsed) != 1 || parsed[0].pid != 123 || parsed[0].uid != "502" ||
		!macLsofHasFile(parsed[0].files, "9u", "/Users/a b/run/local-api.sock") ||
		!macLsofHasFile(parsed[0].files, "txt", "/catalog/bin/runner-local") {
		t.Fatalf("parsed lsof records=%+v err=%v", parsed, err)
	}
	if _, err := parseMacLsofProcesses("pnot-a-pid\ncrunner-local\n"); err == nil {
		t.Fatal("malformed lsof PID was accepted")
	}
}

type macOrphanServiceTestRunner struct {
	plan                   Plan
	uid                    string
	pid                    int
	wrapperPID             int
	resolvedBinary         string
	wrapperCommandOverride string
	terminated             bool
	signals                int
	signalledPID           int
	lsofCalls              int
	calls                  [][]string
}

func (r *macOrphanServiceTestRunner) Run(ctx context.Context, name string, args ...string) (simpleCommandResult, error) {
	call := append([]string{name}, args...)
	r.calls = append(r.calls, call)
	if name == "/bin/launchctl" {
		return (&simpleServiceTestRunner{macActive: map[string]bool{}}).Run(ctx, name, args...)
	}
	if name == "/usr/sbin/lsof" {
		r.lsofCalls++
		if strings.Contains(strings.Join(args, " "), "-U") {
			if r.terminated {
				return simpleCommandResult{}, nil
			}
			return simpleCommandResult{Output: fmt.Sprintf("p%d\ncrunner-local\nu%s\nf9u\nn%s\n", r.pid, r.uid, r.plan.Namespace.APISocket)}, nil
		}
		return simpleCommandResult{Output: fmt.Sprintf("p%d\ncrunner-local\nu%s\nfcwd\nn%s\nftxt\nn%s\n", r.pid, r.uid, r.plan.Namespace.ServiceRoot, r.resolvedBinary)}, nil
	}
	if name == "/bin/ps" && len(args) == 5 && args[0] == "-ww" && args[1] == "-p" && args[3] == "-o" {
		pid, _ := strconv.Atoi(args[2])
		switch pid {
		case r.pid:
			return simpleCommandResult{Output: fmt.Sprintf(" %d %d %s %s\n", r.pid, r.wrapperPID, "tomasz.walczuk", macLaneRunnerCommand(r.plan, "runner-local"))}, nil
		case r.wrapperPID:
			command := r.wrapperCommandOverride
			if command == "" {
				command = macLaneCaptureCommand(r.plan, "runner-local")
			}
			return simpleCommandResult{Output: fmt.Sprintf(" %d 1 %s %s\n", r.wrapperPID, "tomasz.walczuk", command)}, nil
		default:
			return simpleCommandResult{ExitCode: 1}, nil
		}
	}
	if name == "/bin/kill" && len(args) == 2 && args[1] == fmt.Sprint(r.wrapperPID) {
		switch args[0] {
		case "-TERM":
			r.signals++
			r.signalledPID = r.wrapperPID
			r.terminated = true
			return simpleCommandResult{}, nil
		case "-0":
			if r.terminated {
				return simpleCommandResult{ExitCode: 1}, nil
			}
			return simpleCommandResult{}, nil
		}
	}
	if name == "/bin/kill" && len(args) == 2 && args[1] == fmt.Sprint(r.pid) && args[0] == "-0" {
		if r.terminated {
			return simpleCommandResult{ExitCode: 1}, nil
		}
		return simpleCommandResult{}, nil
	}
	return simpleCommandResult{}, fmt.Errorf("unexpected Mac orphan-test command: %v", call)
}
