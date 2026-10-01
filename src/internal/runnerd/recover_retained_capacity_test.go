package runnerd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"remote-session-runner/src/internal/execution"
	"remote-session-runner/src/internal/store"
)

func TestRunRecoverRetainedCapacityRequiresExplicitOnlineApplyAndPairs(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{
			name: "configuration path",
			args: []string{"recover-retained-capacity", "--online", "--apply", "--lost-pair", "sess-one:cmd-one"},
		},
		{
			name: "online acknowledgement",
			args: []string{"recover-retained-capacity", "--config", "/fixture/linux.yaml", "--apply", "--lost-pair", "sess-one:cmd-one"},
		},
		{
			name: "apply acknowledgement",
			args: []string{"recover-retained-capacity", "--config", "/fixture/linux.yaml", "--online", "--lost-pair", "sess-one:cmd-one"},
		},
		{
			name: "explicit lost pair",
			args: []string{"recover-retained-capacity", "--config", "/fixture/linux.yaml", "--online", "--apply"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if exit := Run(test.args, &stdout, &stderr); exit != 2 {
				t.Fatalf("recover-retained-capacity exit=%d stdout=%q stderr=%q, want usage error", exit, stdout.String(), stderr.String())
			}
			if !strings.Contains(stderr.String(), "--config, --online, --apply, and at least one --lost-pair are required") {
				t.Fatalf("recover-retained-capacity usage=%q", stderr.String())
			}
		})
	}
}

func TestRunRecoverRetainedCapacityRejectsInvalidPairBeforeConfigLoad(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exit := Run([]string{
		"recover-retained-capacity", "--config", "/fixture/linux.yaml", "--online", "--apply", "--lost-pair", "sess-one:cmd-one:extra",
	}, &stdout, &stderr)
	if exit != 2 || !strings.Contains(stderr.String(), "invalid --lost-pair") {
		t.Fatalf("recover-retained-capacity exit=%d stdout=%q stderr=%q, want invalid-pair usage error", exit, stdout.String(), stderr.String())
	}
}

func TestRequireRunnerdServiceActiveAcceptsActiveServiceWithMainPID(t *testing.T) {
	values := map[string]string{
		"ActiveState": "active",
		"MainPID":     "1234",
	}
	if err := requireRunnerdServiceActiveWith(func(property string) (string, error) {
		value, ok := values[property]
		if !ok {
			return "", fmt.Errorf("unexpected property %q", property)
		}
		return value, nil
	}); err != nil {
		t.Fatalf("require active runnerd.service: %v", err)
	}
}

// The online helper's liveness preflight must not contend with the daemon's
// lifecycle lock. The P1 authority transaction, not this systemd observation,
// supplies the concurrent safety boundary.
func TestRequireRunnerdServiceActiveDoesNotRequireLifecycleLock(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	release, err := acquireRunnerdLifecycleLock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	err = requireRunnerdServiceActiveWith(func(property string) (string, error) {
		switch property {
		case "ActiveState":
			return "active", nil
		case "MainPID":
			return "1234", nil
		default:
			return "", fmt.Errorf("unexpected property %q", property)
		}
	})
	if err != nil {
		t.Fatalf("active-service preflight was blocked by a held lifecycle lock: %v", err)
	}
}

func TestRequireRunnerdServiceActiveRejectsInactiveOrMissingMainPID(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]string
		want   string
	}{
		{
			name:   "inactive service",
			values: map[string]string{"ActiveState": "inactive", "MainPID": "0"},
			want:   "requires it to remain active",
		},
		{
			name:   "zero PID",
			values: map[string]string{"ActiveState": "active", "MainPID": "0"},
			want:   "requires a live service process",
		},
		{
			name:   "invalid PID",
			values: map[string]string{"ActiveState": "active", "MainPID": "not-a-pid"},
			want:   "requires a live service process",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := requireRunnerdServiceActiveWith(func(property string) (string, error) {
				value, ok := test.values[property]
				if !ok {
					return "", fmt.Errorf("unexpected property %q", property)
				}
				return value, nil
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("require active runnerd.service error=%v, want %q", err, test.want)
			}
		})
	}
}

func TestRetainedCapacityRecoveryFailureReasonIsSanitized(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{err: fmt.Errorf("sensitive runtime detail: %w", execution.ErrLostRuntimeRecoveryUnconfirmed), want: "cleanup_unconfirmed"},
		{err: fmt.Errorf("sensitive runtime detail: %w", execution.ErrLostRuntimeRecoveryFinalization), want: "finalization_unconfirmed"},
		{err: fmt.Errorf("sensitive runtime detail: %w", store.ErrLostRuntimeRecoveryNotReleasable), want: "inventory_not_eligible"},
		{err: errors.Join(fmt.Errorf("sensitive runtime detail"), context.DeadlineExceeded), want: "deadline_exceeded"},
		{err: fmt.Errorf("unexpected private detail"), want: "operation_failed"},
	}
	for _, test := range tests {
		if got := retainedCapacityRecoveryFailureReason(test.err); got != test.want {
			t.Fatalf("recovery failure reason=%q, want %q", got, test.want)
		}
	}
}
