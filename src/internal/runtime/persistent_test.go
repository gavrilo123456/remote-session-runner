package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP031R01PersistentBashPreservesStateAcrossSourcedScripts(t *testing.T) {
	workspace := t.TempDir()
	shell, err := StartPersistentShell(context.Background(), PersistentShellOptions{
		SessionID:  "session-p031",
		Generation: "generation-p031",
		Workspace:  workspace,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := shell.Close(); err != nil {
			t.Errorf("close shell: %v", err)
		}
	}()

	first, err := shell.RunScript(context.Background(), "command-p031-1", []byte("cd /tmp\nexport P031_STATE=preserved\nrunner_fn() { printf 'function-state'; }\numask 027\nprintf 'first-command\\n'\n"))
	if err != nil {
		t.Fatal(err)
	}
	if first.CommandComplete.ExitCode == nil || *first.CommandComplete.ExitCode != 0 {
		t.Fatalf("first completion = %+v", first.CommandComplete)
	}
	if string(first.Stdout) != "first-command\n" || string(first.Stderr) != "" {
		t.Fatalf("first output stdout=%q stderr=%q", first.Stdout, first.Stderr)
	}

	second, err := shell.RunScript(context.Background(), "command-p031-2", []byte("printf '%s|%s|%s|%s\\n' \"$P031_STATE\" \"$PWD\" \"$(runner_fn)\" \"$(umask)\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if second.CommandComplete.ExitCode == nil || *second.CommandComplete.ExitCode != 0 {
		t.Fatalf("second completion = %+v", second.CommandComplete)
	}
	if got, want := string(second.Stdout), "preserved|/tmp|function-state|0027\n"; got != want {
		t.Fatalf("second output = %q, want %q", got, want)
	}

	entries, err := os.ReadDir(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("private per-command files remain: %v", entries)
	}
}

func TestP031PrivateWorkspaceAndScriptFilesUseOwnerMode(t *testing.T) {
	shell, err := StartPersistentShell(context.Background(), PersistentShellOptions{SessionID: "session-p031-mode", Generation: "generation-p031-mode"})
	if err != nil {
		t.Fatal(err)
	}
	workspace := shell.workspace
	info, err := os.Stat(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("workspace mode = %o, want 700", got)
	}
	if _, err := shell.RunScript(context.Background(), "command-p031-mode", []byte("printf 'mode-check'\n")); err != nil {
		t.Fatal(err)
	}
	if err := shell.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(workspace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned workspace stat error = %v, want removed", err)
	}
}

func TestP031NonzeroResultKeepsShellUsableAndCloseStopsReuse(t *testing.T) {
	shell, err := StartPersistentShell(context.Background(), PersistentShellOptions{SessionID: "session-p031-result", Generation: "generation-p031-result"})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := shell.RunScript(context.Background(), "command-p031-nonzero", []byte("printf 'before-failure'; return 23\n"))
	if err != nil {
		t.Fatal(err)
	}
	if failed.CommandComplete.ExitCode == nil || *failed.CommandComplete.ExitCode != 23 {
		t.Fatalf("nonzero completion = %+v", failed.CommandComplete)
	}
	if string(failed.Stdout) != "before-failure" {
		t.Fatalf("nonzero stdout = %q", failed.Stdout)
	}
	continued, err := shell.RunScript(context.Background(), "command-p031-after-failure", []byte("printf 'still-alive'\n"))
	if err != nil {
		t.Fatal(err)
	}
	if string(continued.Stdout) != "still-alive" {
		t.Fatalf("continued stdout = %q", continued.Stdout)
	}
	if err := shell.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := shell.RunScript(context.Background(), "command-p031-closed", nil); !errors.Is(err, ErrPersistentShellClosed) {
		t.Fatalf("closed shell error = %v, want ErrPersistentShellClosed", err)
	}
}

func TestP031ShellQuoteProtectsWorkspaceAndCommandValues(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "space ' quote")
	shell, err := StartPersistentShell(context.Background(), PersistentShellOptions{SessionID: "session-p031-quote", Generation: "generation-p031-quote", Workspace: workspace})
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	result, err := shell.RunScript(context.Background(), "command-'-p031", []byte("printf 'quoted-path'\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result.Stdout), "quoted-path") {
		t.Fatalf("quoted workspace output = %q", result.Stdout)
	}
}
