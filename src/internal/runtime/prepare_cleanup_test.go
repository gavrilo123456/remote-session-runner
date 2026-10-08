package runtime

import (
	"errors"
	"strings"
	"testing"
)

func TestDiscardPartialWorkspaceSurfacesCleanupDebt(t *testing.T) {
	cause := errors.New("materialize source failed")
	cleanup := errors.New("fixture remove failure")
	err := discardPartialWorkspaceWithRemover("/fixture/workspace", cause, func(path string) error {
		if path != "/fixture/workspace" {
			t.Fatalf("remove path=%q", path)
		}
		return cleanup
	})
	if !errors.Is(err, cause) || !errors.Is(err, cleanup) || !strings.Contains(err.Error(), "remove partial workspace") {
		t.Fatalf("partial workspace error=%v, want both materialization and cleanup failures", err)
	}
}
