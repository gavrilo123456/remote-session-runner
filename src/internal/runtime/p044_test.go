package runtime

import (
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"testing"
)

func runP044SharedShellSuite(t *testing.T, shell *PersistentShell, prefix string) {
	t.Helper()
	first, err := shell.RunScript(context.Background(), prefix+"-state-1", []byte("cd /tmp\nexport P044_EXPORTED=one\nP044_LOCAL=two\nshopt -s expand_aliases\nalias p044_alias='printf alias'\np044_function() { printf function; }\numask 077\nprintf '%s|%s|%s|%s|%s' \"$PWD\" \"$P044_EXPORTED\" \"$P044_LOCAL\" \"$(p044_alias)\" \"$(p044_function)\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if string(first.Stdout) != "/tmp|one|two|alias|function" {
		t.Fatalf("first state output = %q", first.Stdout)
	}
	second, err := shell.RunScript(context.Background(), prefix+"-state-2", []byte("printf '%s|%s|%s|' \"$PWD\" \"$P044_EXPORTED\" \"$P044_LOCAL\"\numask\n"))
	if err != nil {
		t.Fatal(err)
	}
	if string(second.Stdout) != "/tmp|one|two|0077\n" {
		t.Fatalf("second state output = %q", second.Stdout)
	}
	nonzero, err := shell.RunScript(context.Background(), prefix+"-nonzero", []byte("printf 'out'\nprintf 'err' >&2\nreturn 7\n"))
	if err != nil {
		t.Fatal(err)
	}
	if nonzero.CommandComplete.ExitCode == nil || *nonzero.CommandComplete.ExitCode != 7 || string(nonzero.Stdout) != "out" || string(nonzero.Stderr) != "err" {
		t.Fatalf("nonzero result = %+v", nonzero)
	}
}

func runP044UnsafeBoundary(t *testing.T, shell *PersistentShell, prefix, script string) {
	t.Helper()
	if _, err := shell.RunScript(context.Background(), prefix+"-unsafe", []byte(script)); !errors.Is(err, ErrPersistentShellExited) {
		t.Fatalf("unsafe boundary error = %v, want ErrPersistentShellExited", err)
	}
	if _, err := shell.RunScript(context.Background(), prefix+"-after", []byte("printf must-not-run\n")); !errors.Is(err, ErrPersistentShellLost) {
		t.Fatalf("post-unsafe error = %v, want ErrPersistentShellLost", err)
	}
}

func runP044DelayedReaderBoundary(t *testing.T, shell *PersistentShell, prefix string) {
	t.Helper()
	if _, err := shell.RunScript(context.Background(), prefix+"-delayed-reader", []byte("printf prefix\n(sleep 2) &\n")); !errors.Is(err, ErrOutputBoundary) {
		t.Fatalf("delayed-reader boundary error = %v, want ErrOutputBoundary", err)
	}
	if _, err := shell.RunScript(context.Background(), prefix+"-after-delayed-reader", []byte("printf must-not-run\n")); !errors.Is(err, ErrPersistentShellLost) {
		t.Fatalf("post-delayed-reader error = %v, want ErrPersistentShellLost", err)
	}
}

func TestP044MacSharedPersistentShellAndUnsafeBoundaries(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("P044 Mac gate runs on the configured Mac account")
	}
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := NewMacProcessAdapter(MacRuntimeOptions{Account: current.Username, WorkspaceRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := adapter.Prepare(context.Background(), "session-p044-mac", "generation-p044-mac")
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.StartAgent(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	shell, err := adapter.Shell(prepared.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	runP044SharedShellSuite(t, shell, "command-p044-mac")
	if err := adapter.Cleanup(prepared.SessionID); err != nil {
		t.Fatal(err)
	}
	for index, script := range []string{"exit 7\n", "exec /bin/true\n", "exec 4>&-\nprintf marker\n", "printf prefix\n(sleep 2) &\n"} {
		prepared, err := adapter.Prepare(context.Background(), "session-p044-mac-unsafe-"+string(rune('a'+index)), "generation-p044-mac-unsafe-"+string(rune('a'+index)))
		if err != nil {
			t.Fatal(err)
		}
		if err := adapter.StartAgent(context.Background(), prepared); err != nil {
			t.Fatal(err)
		}
		shell, err := adapter.Shell(prepared.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		if index == 3 {
			runP044DelayedReaderBoundary(t, shell, "command-p044-mac-unsafe")
		} else {
			runP044UnsafeBoundary(t, shell, "command-p044-mac-unsafe", script)
		}
		_ = adapter.Cleanup(prepared.SessionID)
	}
}

func TestP044LinuxSharedPersistentShellAndUnsafeBoundaries(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("P044 Linux gate runs on the designated Ubuntu host")
	}
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	workspaceRoot := filepath.Join(root, "workspaces")
	if err := os.Mkdir(workspaceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	adapter, err := NewLinuxProcessAdapter(LinuxRuntimeOptions{Account: current.Username, WorkspaceRoot: workspaceRoot, ShellPath: "/usr/bin/bash"})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := adapter.Prepare(context.Background(), "session-p044-linux", "generation-p044-linux")
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.StartAgent(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	shell, err := adapter.Shell(prepared.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	runP044SharedShellSuite(t, shell, "command-p044-linux")
	if err := adapter.Cleanup(prepared.SessionID); err != nil {
		t.Fatal(err)
	}
	for index, script := range []string{"exit 7\n", "exec /bin/true\n", "exec 4>&-\nprintf marker\n", "printf prefix\n(sleep 2) &\n"} {
		suffix := string(rune('a' + index))
		prepared, err := adapter.Prepare(context.Background(), "session-p044-linux-unsafe-"+suffix, "generation-p044-linux-unsafe-"+suffix)
		if err != nil {
			t.Fatal(err)
		}
		if err := adapter.StartAgent(context.Background(), prepared); err != nil {
			t.Fatal(err)
		}
		shell, err := adapter.Shell(prepared.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		if index == 3 {
			runP044DelayedReaderBoundary(t, shell, "command-p044-linux-unsafe")
		} else {
			runP044UnsafeBoundary(t, shell, "command-p044-linux-unsafe", script)
		}
		_ = adapter.Cleanup(prepared.SessionID)
	}
}
