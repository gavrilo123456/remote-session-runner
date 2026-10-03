//go:build !darwin || !cgo

package runtime

import "fmt"

func inspectMacProcessStartIdentity(pid int) (string, error) {
	return "", fmt.Errorf("Mac process start identity requires Darwin with cgo (pid %d)", pid)
}

func inspectMacProcessGroupMembers(processGroupID int) ([]macProcessGroupMember, error) {
	return nil, fmt.Errorf("Mac process-group inspection requires Darwin with cgo (process group %d)", processGroupID)
}
