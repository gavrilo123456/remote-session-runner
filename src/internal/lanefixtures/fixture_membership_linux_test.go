//go:build linux

package lanefixtures

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if role := os.Getenv("LANES_FIXTURE_WORKER_ROLE"); role != "" {
		os.Exit(runFixtureWorker(role))
	}
	os.Exit(m.Run())
}

func verifyFixtureWorkloadMembership(t *testing.T, fixture Manifest) error {
	t.Helper()
	if err := verifyLinuxScope(fixture); err != nil {
		return err
	}
	expected := "/system.slice/" + fixture.TestSystemdUnit
	roles := []string{"listener", "session", "wrapper", "bridge"}
	workers := make([]fixtureWorker, 0, len(roles))
	defer func() {
		for index := len(workers) - 1; index >= 0; index-- {
			if err := workers[index].stop(); err != nil {
				t.Errorf("stop fixture %s process: %v", workers[index].role, err)
			}
		}
	}()

	listenerAddress := ""
	for _, role := range roles {
		worker, ready, err := startFixtureWorker(role, fixture.TestIdentity)
		if worker.process != nil {
			workers = append(workers, worker)
		}
		if err != nil {
			return err
		}
		if got, err := processCgroupPath(worker.process.Pid); err != nil {
			return fmt.Errorf("read fixture %s cgroup: %w", role, err)
		} else if got != expected {
			return fmt.Errorf("fixture %s process cgroup %q, want %q", role, got, expected)
		}
		if role == "listener" {
			listenerAddress = ready
		}
	}
	if !strings.HasPrefix(listenerAddress, "127.0.0.1:") {
		return fmt.Errorf("fixture listener did not bind loopback: %q", listenerAddress)
	}
	connection, err := net.DialTimeout("tcp4", listenerAddress, 2*time.Second)
	if err != nil {
		return fmt.Errorf("connect to bounded loopback fixture listener: %w", err)
	}
	_ = connection.Close()
	return nil
}

type fixtureWorker struct {
	role    string
	process *os.Process
	stdout  io.ReadCloser
	done    chan error
}

func startFixtureWorker(role, testIdentity string) (fixtureWorker, string, error) {
	if role != "listener" && role != "session" && role != "wrapper" && role != "bridge" {
		return fixtureWorker{}, "", fmt.Errorf("unsupported fixture worker role %q", role)
	}
	if !validFixtureTestIdentity(testIdentity) {
		return fixtureWorker{}, "", fmt.Errorf("invalid fixture test identity %q", testIdentity)
	}
	executable, err := os.Executable()
	if err != nil {
		return fixtureWorker{}, "", err
	}
	command := exec.Command(executable, "-test.run=^TestLanesL207ControlsFixture$")
	command.Env = fixtureWorkerEnvironment(role, testIdentity)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return fixtureWorker{}, "", err
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		_ = stdout.Close()
		return fixtureWorker{}, "", fmt.Errorf("start fixture %s process: %w", role, err)
	}
	worker := fixtureWorker{role: role, process: command.Process, stdout: stdout, done: make(chan error, 1)}
	go func() { worker.done <- command.Wait() }()
	type readiness struct {
		line string
		err  error
	}
	ready := make(chan readiness, 1)
	go func() {
		line, err := bufio.NewReader(stdout).ReadString('\n')
		if err != nil {
			err = fmt.Errorf("read fixture %s readiness: %w", role, err)
		}
		ready <- readiness{line: strings.TrimSpace(line), err: err}
	}()
	select {
	case <-time.After(5 * time.Second):
		return worker, "", fmt.Errorf("fixture %s process did not become ready", role)
	case result := <-ready:
		if result.err != nil {
			return worker, "", result.err
		}
		if role == "listener" {
			address := strings.TrimPrefix(result.line, "READY ")
			return worker, address, nil
		}
		if result.line != "READY" {
			return worker, "", fmt.Errorf("fixture %s returned unexpected readiness %q", role, result.line)
		}
		return worker, "", nil
	}
}

func (worker fixtureWorker) stop() error {
	if worker.process == nil {
		return nil
	}
	_ = worker.process.Signal(syscall.SIGTERM)
	select {
	case err := <-worker.done:
		_ = worker.stdout.Close()
		return err
	case <-time.After(5 * time.Second):
		_ = worker.process.Kill()
		err := <-worker.done
		_ = worker.stdout.Close()
		return fmt.Errorf("worker exceeded five-second stop bound: %v", err)
	}
}

func fixtureWorkerEnvironment(role, testIdentity string) []string {
	return []string{"LANES_FIXTURE_WORKER_ROLE=" + role, "LANES_FIXTURE_TEST_IDENTITY=" + testIdentity}
}

func processCgroupPath(pid int) (string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "0::") {
			return strings.TrimPrefix(line, "0::"), nil
		}
	}
	return "", fmt.Errorf("unified cgroup-v2 membership not present")
}

func runFixtureWorker(role string) int {
	if !validFixtureTestIdentity(os.Getenv("LANES_FIXTURE_TEST_IDENTITY")) {
		return 2
	}
	switch role {
	case "listener":
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			return 2
		}
		defer listener.Close()
		fmt.Fprintf(os.Stdout, "READY %s\n", listener.Addr())
		go func() {
			for {
				connection, err := listener.Accept()
				if err != nil {
					return
				}
				_ = connection.Close()
			}
		}()
	case "session", "wrapper", "bridge":
		fmt.Fprintln(os.Stdout, "READY")
	default:
		return 2
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, os.Interrupt)
	<-stop
	return 0
}

func TestFixtureWorkerEnvironmentDoesNotInheritOperatorEnvironment(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "test-parent-value")
	if got := fixtureWorkerEnvironment("session", "rsr-lanes-test-lanes-l2-07-fixture1234"); len(got) != 2 || got[0] != "LANES_FIXTURE_WORKER_ROLE=session" || got[1] != "LANES_FIXTURE_TEST_IDENTITY=rsr-lanes-test-lanes-l2-07-fixture1234" {
		t.Fatalf("fixture worker environment = %#v; operator/provider variables must not be inherited", got)
	}
}
