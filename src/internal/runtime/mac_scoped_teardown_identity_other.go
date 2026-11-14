//go:build !darwin || !cgo

package runtime

func inspectMacScopedRootAbsent(pid int) (bool, error) {
	return false, ErrMacRuntimePlatform
}
