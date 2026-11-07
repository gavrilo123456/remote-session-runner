package sshclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"remote-session-runner/src/internal/sshbridge"
)

// TestBUG029TransportHelper is a disposable fake SSH process. Its holder is
// explicitly released by test cleanup; no installed Runner or shell is used.
func TestBUG029TransportHelper(t *testing.T) {
	mode := os.Getenv("RSR_BUG029_TRANSPORT_MODE")
	if mode == "" {
		return
	}
	root := os.Getenv("RSR_BUG029_TRANSPORT_ROOT")
	write := func(name, value string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(value), 0o600); err != nil {
			os.Exit(91)
		}
	}
	wait := func() {
		until := time.Now().Add(10 * time.Second)
		for time.Now().Before(until) {
			if _, err := os.Stat(filepath.Join(root, "release")); err == nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if strings.HasPrefix(mode, "holder_") {
		write("holder.pid", strconv.Itoa(os.Getpid()))
		write("ready", mode)
		wait()
		write("holder.exited", "done")
		os.Exit(0)
	}
	write("main.pid", strconv.Itoa(os.Getpid()))
	if mode == "blocked_stdin" || mode == "inherited_output" {
		if mode == "inherited_output" {
			if _, err := io.ReadAll(os.Stdin); err != nil {
				os.Exit(92)
			}
		}
		executable, err := os.Executable()
		if err != nil {
			os.Exit(93)
		}
		holder := exec.Command(executable, "-test.run=^TestBUG029TransportHelper$", "--")
		holder.Env = append(os.Environ(), "RSR_BUG029_TRANSPORT_MODE=holder_"+mode)
		holder.Stdout, holder.Stderr = os.Stdout, os.Stderr
		if mode == "blocked_stdin" {
			holder.Stdin = os.Stdin
		}
		if err := holder.Start(); err != nil {
			os.Exit(94)
		}
		for i := 0; i < 500; i++ {
			if _, err := os.Stat(filepath.Join(root, "ready")); err == nil {
				if mode == "inherited_output" {
					os.Exit(0)
				}
				wait()
				os.Exit(0)
			}
			time.Sleep(10 * time.Millisecond)
		}
		os.Exit(95)
	}
	if _, err := io.ReadAll(os.Stdin); err != nil {
		os.Exit(96)
	}
	if mode != "no_reply" {
		if _, err := io.WriteString(os.Stdout, `{"protocol_version":1,"request_id":"p056-request-1","response_type":"result","payload":{}}`+"\n"); err != nil {
			os.Exit(97)
		}
	}
	if mode == "wait_after_eof" {
		_ = os.Stdout.Close()
	}
	write("ready", mode)
	wait()
	os.Exit(0)
}

func bug029WaitFile(t *testing.T, path string) {
	t.Helper()
	until := time.Now().Add(5 * time.Second)
	for time.Now().Before(until) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("fixture did not reach %s", filepath.Base(path))
}

func bug029Client(t *testing.T, mode string) (*Client, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("RSR_BUG029_TRANSPORT_MODE", mode)
	t.Setenv("RSR_BUG029_TRANSPORT_ROOT", root)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	quoted := "'" + strings.ReplaceAll(executable, "'", "'\\''") + "'"
	script := p056Script(t, "exec "+quoted+" -test.run='^TestBUG029TransportHelper$' -- \"$@\"")
	client, err := New(p056Config(t, script))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(root, "release"), nil, 0o600)
		if _, err := os.Stat(filepath.Join(root, "holder.pid")); err == nil {
			bug029WaitFile(t, filepath.Join(root, "holder.exited"))
		}
	})
	return client, root
}

func TestBUG029CallerCancellationBoundsEveryTransportWait(t *testing.T) {
	for _, mode := range []string{"no_reply", "reply_without_eof", "inherited_output", "wait_after_eof", "blocked_stdin"} {
		t.Run(mode, func(t *testing.T) {
			client, root := bug029Client(t, mode)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			request := p056PingRequest()
			if mode == "blocked_stdin" {
				request.Operation = sshbridge.OperationSubmitOrResumeCommand
				request.ResourceID = "command-stable-1"
				request.IdempotencyKey = "idem-stable-1"
				request.Payload, _ = json.Marshal(map[string]any{
					"session_id": "session-stable-1", "intent_ordinal": 1,
					"script": strings.Repeat("x", 128<<10),
				})
			}
			done := make(chan error, 1)
			go func() { _, err := client.Call(ctx, request); done <- err }()
			readyBy := time.After(5 * time.Second)
			for {
				if _, err := os.Stat(filepath.Join(root, "ready")); err == nil {
					break
				}
				select {
				case err := <-done:
					t.Fatalf("transport ended before fixture became ready: %v", err)
				case <-readyBy:
					t.Fatal("fixture did not reach ready")
				case <-time.After(10 * time.Millisecond):
				}
			}
			cancel()
			select {
			case err := <-done:
				var transport *TransportError
				if !errors.Is(err, context.Canceled) || !errors.As(err, &transport) || transport.Phase != PhaseAfterSend {
					t.Fatalf("canceled %s error = %v; want after-send cancellation", mode, err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("caller cancellation did not release the transport")
			}
			pidBytes, err := os.ReadFile(filepath.Join(root, "main.pid"))
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(string(pidBytes))
			if err != nil {
				t.Fatal(err)
			}
			if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
				t.Fatalf("direct SSH child %d was not reaped: %v", pid, err)
			}
		})
	}
}

func TestBUG029DeadlineCauseAndStrictReplyExitBoundary(t *testing.T) {
	client, root := bug029Client(t, "reply_without_eof")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := client.Call(ctx, p056PingRequest()); done <- err }()
	bug029WaitFile(t, filepath.Join(root, "ready"))
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrTransport) {
			t.Fatalf("deadline error = %v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("reply without EOF outlived caller deadline")
	}
}

func TestBUG029ReplyBeforeNonzeroSSHExitIsNotSuccess(t *testing.T) {
	script := p056Script(t, "cat >/dev/null; printf '%s\\n' '{\"protocol_version\":1,\"request_id\":\"p056-request-1\",\"response_type\":\"result\",\"payload\":{}}'; exit 23")
	client, err := New(p056Config(t, script))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Call(context.Background(), p056PingRequest()); !errors.Is(err, ErrTransport) {
		t.Fatalf("reply followed by failed SSH exit was accepted: %v", err)
	}
}
