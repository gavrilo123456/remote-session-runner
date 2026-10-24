package releaseflow

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"remote-session-runner/src/internal/laneinstall"
)

func TestWriteOrVerifyBuildBundleCreatesMissingAndProtectsExisting(t *testing.T) {
	root := resolvedBuildTestTempDir(t)
	path := filepath.Join(root, "build", "linux-host", "bundle")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	want := buildBundleFixture(t, "linux-host", "first")
	if err := writeOrVerifyBuildBundle(path, want); err != nil {
		t.Fatalf("write missing build bundle: %v", err)
	}
	loaded, err := laneinstall.LoadSimpleBundleDirectory(path)
	if err != nil || loaded.Manifest.BundleSHA256 != want.Manifest.BundleSHA256 {
		t.Fatalf("load created bundle: sha=%q err=%v", loaded.Manifest.BundleSHA256, err)
	}
	if err := writeOrVerifyBuildBundle(path, want); err != nil {
		t.Fatalf("verify matching retained bundle: %v", err)
	}
	if err := writeOrVerifyBuildBundle(path, buildBundleFixture(t, "linux-host", "different")); !errors.Is(err, ErrReleaseMismatch) {
		t.Fatalf("mismatched retained bundle error=%v, want %v", err, ErrReleaseMismatch)
	}
}

func TestWriteOrVerifyBuildBundlePreservesCorruptExistingPath(t *testing.T) {
	root := resolvedBuildTestTempDir(t)
	path := filepath.Join(root, "bundle")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(path, "sentinel")
	if err := os.WriteFile(sentinel, []byte("preserve corrupt evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := writeOrVerifyBuildBundle(path, buildBundleFixture(t, "linux-host", "candidate"))
	if err == nil {
		t.Fatal("corrupt existing bundle path was accepted")
	}
	got, readErr := os.ReadFile(sentinel)
	if readErr != nil || string(got) != "preserve corrupt evidence" {
		t.Fatalf("corrupt existing path was changed: content=%q err=%v", got, readErr)
	}
}

func resolvedBuildTestTempDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func buildBundleFixture(t *testing.T, host, marker string) laneinstall.SimpleBundle {
	t.Helper()
	components := make(map[string][]byte)
	for _, component := range testComponents(host) {
		components[component.Name] = []byte(strings.Join([]string{host, component.Name, marker}, ":"))
	}
	bundle, err := laneinstall.NewSimpleBundle(host, strings.Repeat("a", 40), components)
	if err != nil {
		t.Fatal(err)
	}
	return bundle
}
