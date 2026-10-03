package runnerlocald

import (
	"bytes"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/unixsocket"
)

func TestControlledRestartSocketBoundaryRemovesOnlyAStaleOwnedSocket(t *testing.T) {
	root := controlledRestartSocketBoundaryRoot(t)
	socketPath := filepath.Join(root, "run", "locald.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	unixListener := listener.(*net.UnixListener)
	unixListener.SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	settings := controlledRestartSocketBoundaryFixtureSettings(root)
	if err := clearControlledRestartSocketBoundaryAtRoot(settings, root, os.Geteuid(), unixsocket.RemoveStaleOwned); err != nil {
		t.Fatalf("clear stale controlled-restart socket: %v", err)
	}
	if _, err := os.Lstat(socketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale locald socket remains: %v", err)
	}
}

func TestControlledRestartSocketBoundaryFailsClosedForLiveAndUnsafePaths(t *testing.T) {
	for _, fixture := range []struct {
		name  string
		setup func(t *testing.T, socketPath string) func()
	}{
		{
			name: "live listener",
			setup: func(t *testing.T, socketPath string) func() {
				t.Helper()
				listener, err := net.Listen("unix", socketPath)
				if err != nil {
					t.Fatal(err)
				}
				return func() { _ = listener.Close() }
			},
		},
		{
			name: "regular file",
			setup: func(t *testing.T, socketPath string) func() {
				t.Helper()
				if err := os.WriteFile(socketPath, []byte("do-not-remove"), 0o600); err != nil {
					t.Fatal(err)
				}
				return func() {}
			},
		},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			root := controlledRestartSocketBoundaryRoot(t)
			socketPath := filepath.Join(root, "run", "locald.sock")
			cleanup := fixture.setup(t, socketPath)
			defer cleanup()
			if err := clearControlledRestartSocketBoundaryAtRoot(controlledRestartSocketBoundaryFixtureSettings(root), root, os.Geteuid(), unixsocket.RemoveStaleOwned); err == nil {
				t.Fatal("unsafe locald socket path was accepted")
			}
			if _, err := os.Lstat(socketPath); err != nil {
				t.Fatalf("unsafe locald socket path was removed: %v", err)
			}
		})
	}
}

func TestControlledRestartSocketBoundaryRechecksThePathAfterRemoval(t *testing.T) {
	root := controlledRestartSocketBoundaryRoot(t)
	socketPath := filepath.Join(root, "run", "locald.sock")
	settings := controlledRestartSocketBoundaryFixtureSettings(root)
	called := false
	err := clearControlledRestartSocketBoundaryAtRoot(settings, root, os.Geteuid(), func(path string) error {
		called = true
		if path != socketPath {
			t.Fatalf("clear path=%q, want %q", path, socketPath)
		}
		// Model a path appearing after an otherwise successful stale-socket
		// helper. The command must reject it instead of enabling a candidate.
		return os.WriteFile(path, []byte("changed"), 0o600)
	})
	if !called || err == nil {
		t.Fatalf("boundary result called=%t err=%v, want fail closed", called, err)
	}
	contents, readErr := os.ReadFile(socketPath)
	if readErr != nil || string(contents) != "changed" {
		t.Fatalf("post-removal replacement path contents=%q err=%v", contents, readErr)
	}
}

func TestControlledRestartSocketBoundaryRejectsUnsafeServiceRootBeforeCleanup(t *testing.T) {
	root := controlledRestartSocketBoundaryRoot(t)
	if err := os.Chmod(root, 0o750); err != nil {
		t.Fatal(err)
	}
	called := false
	err := clearControlledRestartSocketBoundaryAtRoot(controlledRestartSocketBoundaryFixtureSettings(root), root, os.Geteuid(), func(string) error {
		called = true
		return nil
	})
	if err == nil || called {
		t.Fatalf("unsafe root boundary err=%v clearer_called=%t, want fail closed before clearer", err, called)
	}
}

func TestControlledRestartSocketBoundaryCommandUsesOnlyValidatedMacSelection(t *testing.T) {
	settings := controlledRestartSocketBoundarySettings{
		Kind: config.HostKindMac, Account: config.MacAccount,
		ServiceRoot:  config.MacServiceRoot,
		LocalDSocket: filepath.Join(config.MacServiceRoot, "run", "locald.sock"),
	}
	var stdout, stderr bytes.Buffer
	cleared := false
	code := runControlledRestartSocketBoundaryWith(
		[]string{"--config", "/private/tmp/controlled-restart-socket.yaml"},
		&stdout,
		&stderr,
		func(path string) (controlledRestartSocketBoundarySettings, error) {
			if path != "/private/tmp/controlled-restart-socket.yaml" {
				t.Fatalf("selected config=%q", path)
			}
			return settings, nil
		},
		func() (string, error) { return config.MacAccount, nil },
		func(actual controlledRestartSocketBoundarySettings) error {
			cleared = actual == settings
			return nil
		},
	)
	if code != 0 || !cleared || stdout.String() != "runner-locald controlled-restart-socket-boundary: old locald socket is absent\n" || stderr.Len() != 0 {
		t.Fatalf("socket boundary command exit=%d cleared=%t stdout=%q stderr=%q", code, cleared, stdout.String(), stderr.String())
	}
}

func TestControlledRestartSocketBoundaryCommandFailsClosedWithoutOutput(t *testing.T) {
	valid := controlledRestartSocketBoundarySettings{
		Kind: config.HostKindMac, Account: config.MacAccount,
		ServiceRoot:  config.MacServiceRoot,
		LocalDSocket: filepath.Join(config.MacServiceRoot, "run", "locald.sock"),
	}
	for _, fixture := range []struct {
		name       string
		settings   controlledRestartSocketBoundarySettings
		account    string
		loadErr    error
		accountErr error
		clearErr   error
	}{
		{name: "invalid host", settings: controlledRestartSocketBoundarySettings{Kind: config.HostKindLinux, Account: config.LinuxAccount}},
		{name: "wrong current account", settings: valid, account: "other"},
		{name: "configuration unreadable", loadErr: errors.New("do not print configuration details")},
		{name: "unsafe socket", settings: valid, account: config.MacAccount, clearErr: errors.New("do not print socket details")},
		{name: "current account unavailable", settings: valid, accountErr: errors.New("do not print user details")},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runControlledRestartSocketBoundaryWith(
				[]string{"--config", "/private/tmp/controlled-restart-socket.yaml"},
				&stdout,
				&stderr,
				func(string) (controlledRestartSocketBoundarySettings, error) {
					return fixture.settings, fixture.loadErr
				},
				func() (string, error) { return fixture.account, fixture.accountErr },
				func(controlledRestartSocketBoundarySettings) error { return fixture.clearErr },
			)
			if code != 1 || stdout.Len() != 0 || stderr.String() != "runner-locald controlled-restart-socket-boundary: could not prove old locald socket boundary\n" {
				t.Fatalf("socket boundary command exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestRunDispatchesControlledRestartSocketBoundary(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"controlled-restart-socket-boundary", "--config", filepath.Join(t.TempDir(), "missing.yaml")}, &stdout, &stderr)
	if code != 1 || stdout.Len() != 0 || stderr.String() != "runner-locald controlled-restart-socket-boundary: could not prove old locald socket boundary\n" {
		t.Fatalf("Run(controlled-restart-socket-boundary) exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func controlledRestartSocketBoundaryRoot(t *testing.T) string {
	t.Helper()
	// macOS has a short Unix-domain socket pathname limit; t.TempDir's full
	// test name can exceed it, so keep this fixture directly below /tmp.
	root, err := os.MkdirTemp("/tmp", "rsr-crsb-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		_ = os.RemoveAll(root)
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "run"), 0o700); err != nil {
		_ = os.RemoveAll(root)
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

func controlledRestartSocketBoundaryFixtureSettings(root string) controlledRestartSocketBoundarySettings {
	return controlledRestartSocketBoundarySettings{
		Kind: config.HostKindMac, Account: config.MacAccount,
		ServiceRoot: root, LocalDSocket: filepath.Join(root, "run", "locald.sock"),
	}
}
