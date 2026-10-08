package runtime

import (
	"errors"
	"fmt"
	"os"
)

// discardPartialWorkspace preserves a preparation failure while surfacing a
// storage-cleanup debt for a workspace created before any runtime ownership
// record or process could exist. The caller can still take the documented
// pre-start path because no executable runtime was established.
func discardPartialWorkspace(workspace string, cause error) error {
	return discardPartialWorkspaceWithRemover(workspace, cause, os.RemoveAll)
}

func discardPartialWorkspaceWithRemover(workspace string, cause error, remove func(string) error) error {
	if cleanupErr := remove(workspace); cleanupErr != nil {
		return errors.Join(cause, fmt.Errorf("remove partial workspace: %w", cleanupErr))
	}
	return cause
}
