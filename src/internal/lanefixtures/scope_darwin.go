//go:build darwin

package lanefixtures

// RunLinuxControlsInBoundedScope is a no-op on macOS; Darwin F procedures run
// directly as the manifest owner inside isolated APFS fixture volumes.
func RunLinuxControlsInBoundedScope(Manifest, string, []string) (bool, error) {
	return false, nil
}
