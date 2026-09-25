// Package testfixture provides isolated temporary filesystem fixtures for
// hermetic tests.
package testfixture

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// Root owns a test-scoped temporary directory.
type Root struct {
	path string
}

// New creates an isolated temporary root that is removed by the test runner.
func New(t testing.TB) *Root {
	t.Helper()
	return &Root{path: t.TempDir()}
}

// Path returns the temporary root's absolute path.
func (r *Root) Path() string {
	return r.path
}

// WriteFile writes a fixture below the root with owner-only file permissions.
func (r *Root) WriteFile(t testing.TB, name string, data []byte) string {
	t.Helper()
	if !fs.ValidPath(name) || name == "." {
		t.Fatalf("fixture path must be a clean relative path: %q", name)
	}

	path := filepath.Join(r.path, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create fixture directory: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
	return path
}
