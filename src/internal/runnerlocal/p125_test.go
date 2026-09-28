package runnerlocal

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"remote-session-runner/src/internal/config"
)

func TestP125OwnerOnlyServiceDirectories(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "state", "nested")
	if err := ensureOwnedDirectoryUnder(root, path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("service directory mode/type = %s/%v, want directory/0700", info.Mode().Perm(), info.IsDir())
	}
}

func TestP125ServiceDirectoriesRejectSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "state")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := ensureOwnedDirectoryUnder(root, filepath.Join(link, "nested")); err == nil {
		t.Fatal("service path accepted a symlinked directory")
	}
}

func TestP125MacConfigTemplateLoads(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate P125 config fixture")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
	contents, err := os.ReadFile(filepath.Join(repositoryRoot, "deploy", "macos", "mac.yaml.example"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "mac.yaml")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadFile(path)
	if err != nil {
		t.Fatalf("selected Mac config template did not validate: %v", err)
	}
	if loaded.Kind() != config.HostKindMac {
		t.Fatalf("config host kind = %q, want mac", loaded.Kind())
	}
	settings, ok := loaded.MacSettings()
	if !ok || settings.Account != config.MacAccount || settings.APISocket != filepath.Join(config.MacServiceRoot, "run", "local-api.sock") {
		t.Fatalf("selected Mac settings are incomplete: %+v", settings)
	}
}

func TestP125LaunchAgentDefinitionsUseSelectedPathsAndOwnerUmask(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate P125 launch-agent definitions")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
	for _, fixture := range []struct {
		name       string
		label      string
		binaryName string
	}{
		{name: "local", label: "com.remote-session-runner.local", binaryName: "runner-local"},
		{name: "locald", label: "com.remote-session-runner.locald", binaryName: "runner-locald"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			path := filepath.Join(repositoryRoot, "deploy", "macos", "launchagents", fixture.label+".plist")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			text := string(data)
			for _, required := range []string{
				"<string>" + fixture.label + "</string>",
				"/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/bin/" + fixture.binaryName,
				"/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/config/mac.yaml",
				"<key>KeepAlive</key>", "<key>RunAtLoad</key>", "<key>Umask</key>", "<integer>63</integer>",
			} {
				if !strings.Contains(text, required) {
					t.Errorf("launch-agent definition %s lacks %q", path, required)
				}
			}
		})
	}
}
