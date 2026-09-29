// Package testfixture provides isolated temporary filesystem fixtures for
// hermetic tests.
package testfixture

import (
	"fmt"
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
	path, err := r.writeFile(name, data)
	if err != nil {
		t.Fatalf("write fixture %q: %v", name, err)
	}
	return path
}

func (r *Root) writeFile(name string, data []byte) (string, error) {
	if !fs.ValidPath(name) || name == "." {
		return "", fmt.Errorf("fixture path must be a clean relative path: %q", name)
	}

	path := filepath.Join(r.path, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create fixture directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("write fixture file: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", fmt.Errorf("restrict fixture file permissions: %w", err)
	}
	return path, nil
}
