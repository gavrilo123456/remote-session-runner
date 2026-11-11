//go:build !darwin || !cgo

package runtime

import "remote-session-runner/src/internal/domain"

func inspectMacCleanupGroupMembers(int) ([]domain.RuntimeCleanupMember, error) {
	return nil, macCleanupInspectionFailure("group_inspection_failed", ErrMacRuntimePlatform)
}
