package testfixture

import (
	"os"
	"path/filepath"
	"testing"
)

func TestP001RootWritesPrivateFixtureInsideTempDirectory(t *testing.T) {
	root := New(t)
	path := root.WriteFile(t, "workspace/input.txt", []byte("fixture"))

	relative, err := filepath.Rel(root.Path(), path)
	if err != nil || relative != filepath.Join("workspace", "input.txt") {
		t.Fatalf("fixture path %q is not beneath root %q (relative %q, error %v)", path, root.Path(), relative, err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if string(data) != "fixture" {
		t.Fatalf("fixture contents = %q, want %q", data, "fixture")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat fixture: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("fixture mode = %04o, want 0600", got)
	}
}
