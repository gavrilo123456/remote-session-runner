package releaseflow

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestHostBuildSettingsMatchNativeBundleCompilation(t *testing.T) {
	goTool := filepath.Join(t.TempDir(), "go")
	if err := os.WriteFile(goTool, []byte("test toolchain identity\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	revision := strings.Repeat("a", 40)
	wantFlags := []string{
		"-trimpath",
		"-buildvcs=false",
		"-ldflags",
		"-X remote-session-runner/src/internal/buildinfo.SourceRevision=" + revision,
	}
	cases := []struct {
		host    string
		goos    string
		goarch  string
		wantCGO bool
	}{
		{host: "linux-host", goos: "linux", goarch: "amd64"},
		{host: "sandbox-host", goos: "linux", goarch: "arm64"},
		{host: "factory-sandbox", goos: "linux", goarch: "amd64"},
		{host: "mac-control-plane", goos: "darwin", goarch: "arm64", wantCGO: true},
	}
	for _, tc := range cases {
		t.Run(tc.host, func(t *testing.T) {
			settings, err := hostBuildSettings(tc.host, goTool, tc.goos, tc.goarch, revision)
			if err != nil {
				t.Fatal(err)
			}
			if settings.CGOEnabled != tc.wantCGO {
				t.Fatalf("cgo_enabled=%t, want %t", settings.CGOEnabled, tc.wantCGO)
			}
			if !reflect.DeepEqual(settings.Flags, wantFlags) {
				t.Fatalf("flags=%q, want exact builder flags %q", settings.Flags, wantFlags)
			}
			bundle := Bundle{HostID: tc.host, GOOS: tc.goos, GOARCH: tc.goarch, SourceRevision: revision}
			if !validHostBuildSettings(settings, tc.host, bundle) {
				t.Fatal("recorded settings do not validate against the bundle")
			}

			wrongCGO := settings
			wrongCGO.CGOEnabled = !tc.wantCGO
			if validHostBuildSettings(wrongCGO, tc.host, bundle) {
				t.Fatal("settings with a mismatched CGO value were accepted")
			}

			missingLinkerArgument := settings
			missingLinkerArgument.Flags = []string{"-trimpath", "-buildvcs=false"}
			if validHostBuildSettings(missingLinkerArgument, tc.host, bundle) {
				t.Fatal("settings that omit the source-revision linker argument were accepted")
			}

			wrongRevision := settings
			wrongRevision.Flags = hostBuildFlags(strings.Repeat("b", 40))
			if validHostBuildSettings(wrongRevision, tc.host, bundle) {
				t.Fatal("settings that link a different source revision were accepted")
			}

			wrongPlatform := bundle
			wrongPlatform.GOOS = "darwin"
			wrongPlatform.GOARCH = "arm64"
			if tc.goos == "darwin" {
				wrongPlatform.GOOS = "linux"
				wrongPlatform.GOARCH = "amd64"
			}
			linuxSettings := settings
			linuxSettings.GOOS = wrongPlatform.GOOS
			linuxSettings.GOARCH = wrongPlatform.GOARCH
			linuxSettings.CGOEnabled = false
			if validHostBuildSettings(linuxSettings, tc.host, wrongPlatform) {
				t.Fatal("settings for a different host platform were accepted")
			}
		})
	}
}

func TestHostBuildSettingsRejectsInvalidSourceRevision(t *testing.T) {
	goTool := filepath.Join(t.TempDir(), "go")
	if err := os.WriteFile(goTool, []byte("test toolchain identity\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := hostBuildSettings("linux-host", goTool, "linux", "amd64", "not-a-full-commit"); err != ErrInvalidInput {
		t.Fatalf("invalid source revision error=%v, want %v", err, ErrInvalidInput)
	}
}
