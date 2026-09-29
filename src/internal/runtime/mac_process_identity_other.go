//go:build !darwin || !cgo

package runtime

import "fmt"

func inspectMacProcessStartIdentity(pid int) (string, error) {
	return "", fmt.Errorf("Mac process start identity requires Darwin with cgo (pid %d)", pid)
}
